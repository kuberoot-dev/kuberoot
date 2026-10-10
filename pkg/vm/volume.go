// Package vm runs virtual machines on a node: their disks, replicated with
// DRBD between nodes, and the machines themselves, run by cloud-hypervisor
// on KVM.
package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"golang.org/x/sys/unix"

	"github.com/tym83/kuberoot/pkg/apis/node"
)

// Paths where a node keeps its volumes and machines. The state survives
// reboots; the run directory holds DRBD's configuration and the monitors'
// sockets.
var (
	StateDir = "/var/lib/kuberoot/vm"
	RunDir   = "/run/kuberoot/vm"
	// Reserve is the room a volume leaves the node: its own state, and a
	// release staged for an upgrade.
	Reserve int64 = 1 << 30
)

// freeBytes is the room left on the filesystem holding dir.
func freeBytes(dir string) (int64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}

// ResourceName is a volume's DRBD resource.
func ResourceName(volume string) string { return "vm-" + volume }

// Volumes keeps the volumes of a node.
type Volumes struct {
	// Node and Address are this node's name, as its hostname, and the
	// address its peers reach it at.
	Node, Address string
}

func (v *Volumes) dir() string               { return filepath.Join(StateDir, "volumes") }
func (v *Volumes) specFile(n string) string  { return filepath.Join(v.dir(), n+".json") }
func (v *Volumes) imageFile(n string) string { return filepath.Join(v.dir(), n+".img") }
func (v *Volumes) resFile(n string) string   { return filepath.Join(RunDir, "drbd", n+".res") }
func (v *Volumes) mark(n, what string) string {
	return filepath.Join(v.dir(), n+"."+what)
}

// Save keeps a volume's spec; Apply then makes the node match it.
func (v *Volumes) Save(name string, s node.VolumeSpec) error {
	if err := os.MkdirAll(v.dir(), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeAtomic(v.specFile(name), raw)
}

// writeAtomic replaces a file whole, so a reader never sees half of it.
func writeAtomic(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a volume's spec.
func (v *Volumes) Load(name string) (node.VolumeSpec, error) {
	var s node.VolumeSpec
	raw, err := os.ReadFile(v.specFile(name))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(raw, &s)
}

// Names lists the volumes of the node.
func (v *Volumes) Names() []string {
	files, _ := filepath.Glob(filepath.Join(v.dir(), "*.json"))
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimSuffix(filepath.Base(f), ".json"))
	}
	sort.Strings(out)
	return out
}

// Device is the block device a machine uses for a volume.
func Device(s node.VolumeSpec) string { return fmt.Sprintf("/dev/drbd%d", s.Minor) }

var resTemplate = template.Must(template.New("res").Parse(`# Written by kuberoot-node for volume {{.Name}}.
resource {{.Resource}} {
	options {
		# A node cut off from the others stops writing, so a machine
		# started elsewhere never shares its disk with a stale one.
		quorum majority;
		on-no-quorum io-error;
	}
	net {
		protocol C;
		# Diverged copies settle themselves where one side has nothing to
		# lose: the side that never wrote, or the one not in use.
		after-sb-0pri discard-zero-changes;
		after-sb-1pri discard-secondary;
		after-sb-2pri disconnect;
{{- if .TwoPrimaries}}
		# A machine moves alive: for the moment it takes, both nodes hold
		# the disk writable; only one of them runs the machine.
		allow-two-primaries yes;
{{- end}}
	}
	volume 0 {
		device minor {{.Minor}};
		disk {{.Disk}};
		meta-disk internal;
	}
{{- range .Hosts}}
	on {{.Node}} {
		node-id {{.NodeID}};
		address {{.Address}}:{{$.Port}};
	}
{{- end}}
{{- if gt (len .Hosts) 1}}
	connection-mesh {
		hosts{{range .Hosts}} {{.Node}}{{end}};
	}
{{- end}}
}
`))

// ResConfig renders the DRBD resource of a volume backed by disk.
func (v *Volumes) ResConfig(name string, s node.VolumeSpec, disk string) string {
	hosts := append([]node.VolumePeer{{Node: v.Node, Address: v.Address, NodeID: s.NodeID}}, s.Peers...)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].NodeID < hosts[j].NodeID })
	var b bytes.Buffer
	_ = resTemplate.Execute(&b, map[string]any{"Name": name, "Resource": ResourceName(name), "Minor": s.Minor,
		"Port": s.Port, "Disk": disk, "Hosts": hosts, "TwoPrimaries": s.AllowTwoPrimaries})
	return b.String()
}

// Apply makes the node match a volume's spec: the backing file, its loop
// device, the DRBD resource up with these peers, metadata created once, the
// image written once by the node that has one, and the role asked for.
func (v *Volumes) Apply(ctx context.Context, name string, s node.VolumeSpec) error {
	if err := os.MkdirAll(filepath.Dir(v.resFile(name)), 0o700); err != nil {
		return err
	}
	img := v.imageFile(name)
	if _, err := os.Stat(img); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(img), 0o700); err != nil {
			return err
		}
		// The file is sparse, but its copy fills it: the node keeps room
		// for its own state and an upgrade, or the volume is not made here.
		if free, ok := freeBytes(filepath.Dir(img)); ok {
			if need := s.SizeBytes + metadataBytes(s.SizeBytes) + Reserve; free < need {
				return fmt.Errorf("no room for the volume: %d MiB free, %d MiB needed with the node's own %d MiB",
					free>>20, need>>20, Reserve>>20)
			}
		}
		f, err := os.Create(img)
		if err != nil {
			return err
		}
		// DRBD keeps its metadata at the end of the disk: room for it.
		err = f.Truncate(s.SizeBytes + metadataBytes(s.SizeBytes))
		f.Close()
		if err != nil {
			return err
		}
	}
	loop, err := loopDevice(ctx, img)
	if err != nil {
		return err
	}
	res := v.resFile(name)
	if err := os.WriteFile(res, []byte(v.ResConfig(name, s, loop)), 0o600); err != nil {
		return err
	}
	r := ResourceName(name)
	if _, err := os.Stat(v.mark(name, "md")); os.IsNotExist(err) {
		if out, err := drbdadm(ctx, res, "create-md", "--force", "--max-peers=7", r); err != nil {
			return fmt.Errorf("create metadata: %v: %s", err, out)
		}
		_ = os.WriteFile(v.mark(name, "md"), nil, 0o600)
	}
	if out, err := drbdadm(ctx, res, "adjust", r); err != nil {
		return fmt.Errorf("adjust: %v: %s", err, out)
	}
	st := v.Status(ctx, name)
	if s.Primary && s.Image != "" && !exists(v.mark(name, "image")) {
		// Never write an image over data: not over this disk's, nor over a
		// peer's, which this one would become a copy of.
		if st.DiskState == "UpToDate" || hasUpToDate(st.PeerDisks) {
			return os.WriteFile(v.mark(name, "image"), nil, 0o600)
		}
		return v.writeImage(ctx, name, s, res)
	}
	switch {
	case s.Primary && st.Role != "Primary":
		if out, err := drbdadm(ctx, res, "primary", r); err != nil {
			return fmt.Errorf("primary: %v: %s", err, out)
		}
	case !s.Primary && st.Role == "Primary":
		if out, err := drbdadm(ctx, res, "secondary", r); err != nil {
			return fmt.Errorf("secondary: %v: %s", err, out)
		}
	}
	return nil
}

// writeImage makes this node the volume's source: the image downloaded
// first, then the node primary by force, as no node has data yet, and the
// image written onto the replicated device. If writing fails, the node steps
// down again, and the next pass tries anew.
func (v *Volumes) writeImage(ctx context.Context, name string, s node.VolumeSpec, res string) error {
	download := filepath.Join(v.dir(), name+".download")
	defer os.Remove(download)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := fetch(ctx, s.Image, download); err != nil {
		return fmt.Errorf("download %s: %w", s.Image, err)
	}
	if out, err := drbdadm(ctx, res, "primary", "--force", ResourceName(name)); err != nil {
		return fmt.Errorf("primary --force: %v: %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "qemu-img", "convert", "-O", "raw", download, Device(s)).CombinedOutput(); err != nil {
		_, _ = drbdadm(context.Background(), res, "secondary", ResourceName(name))
		return fmt.Errorf("write the image: %v: %s", err, out)
	}
	return os.WriteFile(v.mark(name, "image"), nil, 0o600)
}

// ImageWritten reports whether the volume's data exists on this node: its
// image written here, or found already on a disk.
func (v *Volumes) ImageWritten(name string) bool { return exists(v.mark(name, "image")) }

func hasUpToDate(disks map[string]string) bool {
	for _, d := range disks {
		if d == "UpToDate" {
			return true
		}
	}
	return false
}

// Delete takes a volume down and removes its data from this node.
func (v *Volumes) Delete(ctx context.Context, name string) error {
	res := v.resFile(name)
	if exists(res) {
		if out, err := drbdadm(ctx, res, "down", ResourceName(name)); err != nil {
			return fmt.Errorf("down: %v: %s", err, out)
		}
	}
	if loop, err := findLoop(v.imageFile(name)); err == nil && loop != "" {
		_ = exec.CommandContext(ctx, "losetup", "-d", loop).Run()
	}
	for _, f := range []string{res, v.imageFile(name), v.specFile(name), v.mark(name, "md"), v.mark(name, "image")} {
		_ = os.Remove(f)
	}
	return nil
}

// Status reports a volume as DRBD sees it.
func (v *Volumes) Status(ctx context.Context, name string) node.VolumeStatus {
	st := node.VolumeStatus{Phase: "Creating"}
	out, err := exec.CommandContext(ctx, "drbdsetup", "status", "--json", ResourceName(name)).Output()
	if err != nil {
		return st
	}
	parsed, err := ParseStatus(out)
	if err != nil {
		st.Message = err.Error()
		return st
	}
	return parsed
}

// ParseStatus reads `drbdsetup status --json` for one resource.
func ParseStatus(raw []byte) (node.VolumeStatus, error) {
	var res []struct {
		Role    string `json:"role"`
		Devices []struct {
			Minor     int    `json:"minor"`
			DiskState string `json:"disk-state"`
			Quorum    bool   `json:"quorum"`
		} `json:"devices"`
		Connections []struct {
			Name            string `json:"name"`
			ConnectionState string `json:"connection-state"`
			PeerDevices     []struct {
				PeerDiskState string `json:"peer-disk-state"`
			} `json:"peer_devices"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return node.VolumeStatus{}, fmt.Errorf("drbdsetup status: %w", err)
	}
	if len(res) == 0 || len(res[0].Devices) == 0 {
		return node.VolumeStatus{Phase: "Creating"}, nil
	}
	r := res[0]
	st := node.VolumeStatus{Phase: "Ready", Role: r.Role, DiskState: r.Devices[0].DiskState, Quorum: r.Devices[0].Quorum,
		Device: fmt.Sprintf("/dev/drbd%d", r.Devices[0].Minor), PeerStates: map[string]string{}, PeerDisks: map[string]string{}}
	for _, c := range r.Connections {
		st.PeerStates[c.Name] = c.ConnectionState
		if len(c.PeerDevices) > 0 {
			st.PeerDisks[c.Name] = c.PeerDevices[0].PeerDiskState
		}
	}
	return st, nil
}

// metadataBytes is room for DRBD's internal metadata: its bitmap (a bit per
// 4 KiB per peer, for up to seven) and a margin.
func metadataBytes(size int64) int64 {
	return size/4096/8*7 + 4<<20
}

func drbdadm(ctx context.Context, res string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "drbdadm", append([]string{"-c", res}, args...)...).CombinedOutput()
}

// loopDevice attaches a file to a loop device, or finds the one it is on.
func loopDevice(ctx context.Context, file string) (string, error) {
	if dev, err := findLoop(file); err == nil && dev != "" {
		return dev, nil
	}
	out, err := exec.CommandContext(ctx, "losetup", "--find", "--show", "--direct-io=on", file).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("losetup %s: %v: %s", file, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func findLoop(file string) (string, error) {
	dirs, err := filepath.Glob("/sys/block/loop*/loop/backing_file")
	if err != nil {
		return "", err
	}
	for _, d := range dirs {
		raw, err := os.ReadFile(d)
		if err == nil && strings.TrimSpace(string(raw)) == file {
			return "/dev/" + filepath.Base(filepath.Dir(filepath.Dir(d))), nil
		}
	}
	return "", nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func fetch(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
