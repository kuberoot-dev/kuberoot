package nodeapi

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
)

// Storage states of the state partition.
const (
	StorageHealthy  = "Healthy"
	StorageFull     = "Full"
	StorageReadOnly = "ReadOnly"
	StorageFailing  = "Failing"
)

const (
	stateMount = "/var"
	// probeEvery bounds how often the device is read; probeTimeout is how
	// long a read may take before the device counts as failing.
	probeEvery   = 5 * time.Second
	probeTimeout = 3 * time.Second
)

// readStateStorage reports the state partition's health. It reads the
// kernel's view (mount options, filesystem error count, free space) and
// the device itself, bypassing the page cache, and writes nothing to it:
// a node whose state disk fails must still be able to say so.
func readStateStorage() *node.StateStorage {
	m, err := findMount(stateMount)
	if err != nil || m.fstype != "ext4" {
		return nil
	}
	s := &node.StateStorage{State: StorageHealthy, Device: m.source}
	var st unix.Statfs_t
	if err := unix.Statfs(stateMount, &st); err == nil {
		// The node's services run as root: root's reserved blocks are theirs.
		s.TotalBytes, s.AvailableBytes = int64(st.Blocks)*st.Bsize, int64(st.Bfree)*st.Bsize
	}
	sysfs := filepath.Join("/sys/fs/ext4", filepath.Base(m.source))
	if raw, err := os.ReadFile(filepath.Join(sysfs, "errors_count")); err == nil {
		s.Errors, _ = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	}
	// Since Linux 6.10 an error makes ext4 refuse writes without marking the
	// mount read-only (EXT4_FLAGS_EMERGENCY_RO): an error recorded since
	// boot, on a partition mounted errors=remount-ro, is the only sign.
	errorSinceBoot := false
	if raw, err := os.ReadFile(filepath.Join(sysfs, "last_error_time")); err == nil {
		if t, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil && t > 0 {
			errorSinceBoot = time.Unix(t, 0).After(bootTime())
		}
	}
	_, err = os.Stat(supervisor.SpaceReserveReleased)
	s.ReserveReleased = err == nil
	readErr := deviceProbe.check(m.source)
	switch {
	case readErr != nil:
		s.State, s.Message = StorageFailing, readErr.Error()
	case m.readOnly || errorSinceBoot:
		s.State, s.Message = StorageReadOnly, "the kernel remounted "+stateMount+" read-only after a filesystem error; writes fail until the node is repaired"
	case s.TotalBytes > 0 && s.AvailableBytes < supervisor.SpaceReserveSize(s.TotalBytes):
		s.State, s.Message = StorageFull, fmt.Sprintf("%d of %d bytes free, less than the %d the cluster store needs to start", s.AvailableBytes, s.TotalBytes, supervisor.SpaceReserveSize(s.TotalBytes))
	}
	if s.State != StorageHealthy && s.Errors > 0 {
		s.Message += fmt.Sprintf("; the filesystem recorded %d errors", s.Errors)
	}
	return s
}

type mount struct {
	source, fstype string
	readOnly       bool
}

// findMount reads the mount at target from the kernel's mount table.
func findMount(target string) (mount, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return mount{}, err
	}
	defer f.Close()
	var found *mount
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m, ok := parseMountInfo(sc.Text(), target); ok {
			found = &m // the last one mounted over target is the one in use
		}
	}
	if found == nil {
		return mount{}, fmt.Errorf("%s is not a mount point", target)
	}
	return *found, nil
}

// parseMountInfo reads one line of /proc/self/mountinfo for the mount at
// target: "id parent major:minor root target options [optional...] -
// fstype source superoptions".
func parseMountInfo(line, target string) (mount, bool) {
	fields := strings.Fields(line)
	if len(fields) < 10 || fields[4] != target {
		return mount{}, false
	}
	sep := -1
	for i := 6; i < len(fields); i++ {
		if fields[i] == "-" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+2 >= len(fields) {
		return mount{}, false
	}
	m := mount{source: fields[sep+2], fstype: fields[sep+1]}
	// The per-mount options, and the filesystem's own: after an error the
	// kernel makes the filesystem read-only, and only its options say so.
	opts := fields[5]
	if sep+3 < len(fields) {
		opts += "," + fields[sep+3]
	}
	for _, o := range strings.Split(opts, ",") {
		m.readOnly = m.readOnly || o == "ro"
	}
	return m, true
}

// probe reads a device now and then, with at most one read in flight: a
// read stuck in a dead device must not pile up behind every request.
type probe struct {
	mu       sync.Mutex
	inFlight bool
	since    time.Time // when the read in flight began
	at       time.Time // when the last read finished
	err      error
	read     func(dev string) error
}

var deviceProbe = &probe{read: readDirect}

func (p *probe) check(dev string) error {
	p.mu.Lock()
	if !p.inFlight && time.Since(p.at) >= probeEvery {
		p.inFlight, p.since = true, time.Now()
		done := make(chan struct{})
		go func() {
			err := p.read(dev)
			p.mu.Lock()
			p.inFlight, p.at, p.err = false, time.Now(), err
			p.mu.Unlock()
			close(done)
		}()
		p.mu.Unlock()
		select {
		case <-done:
		case <-time.After(probeTimeout):
		}
		p.mu.Lock()
	}
	defer p.mu.Unlock()
	if p.inFlight && time.Since(p.since) >= probeTimeout {
		return fmt.Errorf("reading %s has not finished after %s", dev, time.Since(p.since).Round(time.Second))
	}
	return p.err
}

// readDirect reads the device's first block past the page cache, so a
// failing device answers with its own error, not with cached data.
func readDirect(dev string) error {
	fd, err := unix.Open(dev, unix.O_RDONLY|unix.O_DIRECT|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", dev, err)
	}
	defer unix.Close(fd)
	const block = 4096
	buf := make([]byte, 2*block)
	off := int(uintptr(unsafe.Pointer(&buf[0])) & (block - 1))
	if off != 0 {
		off = block - off
	}
	n, err := unix.Pread(fd, buf[off:off+block], 0)
	if err != nil {
		return fmt.Errorf("read %s: %w", dev, err)
	}
	if n != block {
		return errors.New("read " + dev + ": short read")
	}
	return nil
}

func bootTime() time.Time {
	var info unix.Sysinfo_t
	if unix.Sysinfo(&info) != nil {
		return time.Time{}
	}
	return time.Now().Add(-time.Duration(info.Uptime) * time.Second)
}
