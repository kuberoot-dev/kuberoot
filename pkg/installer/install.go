package installer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"

	"github.com/tym83/kuberoot/pkg/bootdisk"
	"github.com/tym83/kuberoot/pkg/etcdstore"
)

// MediaDir is where the running live system finds its boot media.
const MediaDir = "/run/kuberoot/media"

// Phases an installation goes through, in order.
const (
	PhasePending      = "Pending"
	PhasePartitioning = "Partitioning"
	PhaseBootloader   = "InstallingBootloader"
	PhaseWritingRoot  = "WritingRoot"
	PhaseFormatting   = "FormattingState"
	PhaseCompleted    = "Completed"
	PhaseFailed       = "Failed"
)

// Progress receives phase changes and the overall percentage.
type Progress func(phase, message string, percent int)

// FromMedia describes the release on the boot media the live system runs from.
func FromMedia() (bootdisk.Artifacts, error) {
	a := bootdisk.Artifacts{Dir: filepath.Join(MediaDir, "kuberoot"), Arch: archName(), Version: osVersion()}
	if _, err := os.Stat(filepath.Join(a.Dir, "rootfs.squashfs")); err != nil {
		return a, fmt.Errorf("not running from kuberoot boot media")
	}
	a.ConsoleArg = consoleArgs()
	return a, nil
}

// Install erases the disk and writes slot A, the bootloader and the state
// partition. The state is the running node's own, or, with restore set to a
// state archive, the one the archive was taken from.
func Install(ctx context.Context, diskName string, a bootdisk.Artifacts, restore string, progress Progress) error {
	target, err := checkTarget(diskName)
	if err != nil {
		return err
	}
	progress(PhasePartitioning, "writing partition table to "+target.Name, 5)
	if err := partitionAndBoot(ctx, target, a, progress); err != nil {
		return err
	}

	progress(PhaseWritingRoot, "writing the root image to slot A", 30)
	if err := WriteRoot(ctx, a, PartitionOn(target.Name, bootdisk.RootLabel("a")), func(done, total int64) {
		progress(PhaseWritingRoot, fmt.Sprintf("writing the root image to slot A: %d of %d MiB", done>>20, total>>20), 30+int(55*done/max(total, 1)))
	}); err != nil {
		return err
	}

	progress(PhaseFormatting, "creating the state partition", 90)
	state := PartitionOn(target.Name, bootdisk.StateLabel)
	if out, err := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", "-L", bootdisk.StateLabel, state).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 %s: %v: %s", state, err, out)
	}
	if restore != "" {
		progress(PhaseFormatting, "restoring the state from the backup", 95)
		return onState(state, func(mnt string) error { return extractState(restore, mnt) })
	}
	progress(PhaseFormatting, "carrying the node identity over", 95)
	return onState(state, carryIdentity)
}

// carriedState is what an installed node keeps from the system it was
// installed from: its identity and certificates, its membership of a cluster,
// the cluster's store on a control plane, and the kubelet's client
// certificate. Without them a worker comes back as a cluster of its own and a
// control plane comes back empty.
var carriedState = []string{
	"lib/kuberoot",    // PKI, machine-id, cluster membership
	"lib/kubelet/pki", // the kubelet's client certificate
}

// notCarried are paths below the carried state that stay behind: upgrade
// bundles downloaded by the live system, as large as a root filesystem.
var notCarried = []string{"lib/kuberoot/upgrade", "lib/kuberoot/backups"}

// kineStore is the control plane's cluster store. It is written all the
// time, so it is carried as a consistent snapshot, not copied file by file.
const kineStore = "lib/kine/state.db"

// onState runs fn on the new state partition, mounted, and syncs it.
func onState(stateDev string, fn func(mnt string) error) error {
	const mnt = "/run/kuberoot/target-state"
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	if err := unix.Mount(stateDev, mnt, "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount new state: %w", err)
	}
	defer unix.Unmount(mnt, 0)
	if err := fn(mnt); err != nil {
		return err
	}
	unix.Sync()
	return nil
}

// carryIdentity copies the node's identity, membership and store onto the new
// state partition: the installed system is the same node the admin already
// talks to, and credentials issued by the live system keep working.
func carryIdentity(mnt string) error {
	for _, rel := range carriedState {
		src := filepath.Join("/var", rel)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := copyTree(src, filepath.Join(mnt, rel)); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
	}
	if etcdRunning() {
		return carryEtcd(filepath.Join(mnt, etcdRestore))
	}
	if err := snapshotStore(filepath.Join("/var", kineStore), filepath.Join(mnt, kineStore)); err != nil {
		return fmt.Errorf("carry the cluster store: %w", err)
	}
	return nil
}

// carryEtcd carries the store of a control plane that is its only member as
// a snapshot the installed system starts from. A member among others is not
// carried: install control planes before they join, or join this one again
// after its installation.
func carryEtcd(dst string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := etcdstore.Client()
	if err != nil {
		return err
	}
	defer c.Close()
	list, err := c.MemberList(ctx)
	if err != nil {
		return fmt.Errorf("cluster store: %w", err)
	}
	if len(list.Members) > 1 {
		return fmt.Errorf("this control plane is one of %d members of the cluster store, which an installation does not carry: install control planes before they join, or remove this one (etcdmembers) and join it again once installed", len(list.Members))
	}
	return etcdSnapshot(ctx, dst)
}

// snapshotStore writes a consistent copy of a live SQLite database: VACUUM
// INTO reads it under SQLite's own locking while kine keeps writing.
func snapshotStore(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil // a worker has no store
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+src+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("VACUUM INTO ?", dst)
	return err
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, strings.TrimPrefix(path, src))
		for _, skip := range notCarried {
			if strings.HasSuffix(path, "/"+skip) && info.IsDir() {
				return filepath.SkipDir
			}
		}
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			// The kubelet keeps its current certificates as symlinks and
			// rotates them by moving the link; a plain file breaks rotation.
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		case !info.Mode().IsRegular():
			return nil // sockets, fifos: runtime state
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode().Perm())
	})
}

// partitionAndBoot writes the partition table and the EFI partition. The disk is
// held open exclusively only for this step: mkfs refuses partitions of a busy disk.
func partitionAndBoot(ctx context.Context, target Disk, a bootdisk.Artifacts, progress Progress) error {
	d, err := diskfs.Open("/dev/"+target.Name, diskfs.WithOpenMode(diskfs.ReadWriteExclusive))
	if err != nil {
		return fmt.Errorf("open %s: %w", target.Name, err)
	}
	defer d.Close()
	table, err := bootdisk.InstalledTable(target.SizeBytes)
	if err != nil {
		return err
	}
	if err := d.Partition(table); err != nil {
		return fmt.Errorf("partition: %w", err)
	}
	_ = d.ReReadPartitionTable()
	if err := waitForLabels(ctx, target.Name, bootdisk.EFILabel, bootdisk.RootLabel("a"), bootdisk.StateLabel); err != nil {
		return err
	}

	progress(PhaseBootloader, "installing systemd-boot and the slot A kernel", 15)
	esp, err := d.CreateFilesystem(disk.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "KUBEROOT-EFI"})
	if err != nil {
		return fmt.Errorf("format EFI partition: %w", err)
	}
	if err := WriteSlotBoot(esp, a, "a", "1"); err != nil {
		return err
	}
	if err := bootdisk.CopyFile(esp, a.RemovableBootPath(), filepath.Join(MediaDir, a.RemovableBootPath())); err != nil {
		return err
	}
	return bootdisk.WriteFile(esp, "/loader/loader.conf", strings.NewReader(bootdisk.LoaderConf))
}

// WriteSlotBoot puts a slot's kernel, initrd and loader entry on the EFI partition.
func WriteSlotBoot(esp filesystem.FileSystem, a bootdisk.Artifacts, slot, version string) error {
	entry := bootdisk.SlotEntry(a, slot, version)
	if err := bootdisk.CopyFile(esp, entry.Kernel, filepath.Join(a.Dir, "vmlinuz.efi")); err != nil {
		return err
	}
	if err := bootdisk.CopyFile(esp, entry.Initrd, filepath.Join(a.Dir, "initrd.cpio")); err != nil {
		return err
	}
	return bootdisk.WriteFile(esp, "/loader/entries/"+entry.ID+".conf", strings.NewReader(entry.String()))
}

// WriteRoot copies the squashfs image onto a root partition device.
func WriteRoot(ctx context.Context, a bootdisk.Artifacts, device string, report func(done, total int64)) error {
	src, err := os.Open(filepath.Join(a.Dir, "rootfs.squashfs"))
	if err != nil {
		return err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(device, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer dst.Close()
	buf := make([]byte, 4<<20)
	var done int64
	for {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			report(done, st.Size())
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return dst.Sync()
}

func checkTarget(name string) (Disk, error) {
	for _, d := range Disks() {
		if d.Name != name {
			continue
		}
		if d.Role == RoleBootMedia {
			return d, fmt.Errorf("%s is the boot media", name)
		}
		return d, nil
	}
	return Disk{}, fmt.Errorf("no disk named %q", name)
}

func waitForLabels(ctx context.Context, diskName string, labels ...string) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		missing := ""
		for _, l := range labels {
			if PartitionOn(diskName, l) == "" {
				missing = l
			}
		}
		if missing == "" {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("partition %s did not appear", missing)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// PartitionOn finds the device node of the partition with the given GPT name on
// one disk; with an empty disk name, on any disk.
func PartitionOn(diskName, label string) string {
	for _, d := range Disks() {
		if diskName != "" && d.Name != diskName {
			continue
		}
		for _, p := range d.Partitions {
			if p.Label == label {
				return "/dev/" + p.Name
			}
		}
	}
	return ""
}

// CurrentBootArgs are the boot arguments a new slot inherits from the running one.
func CurrentBootArgs() string { return consoleArgs() }

// carriedArgs are the boot arguments an installed system keeps from the media
// it was installed from: the console and the node's network and cluster
// settings. Development switches, which print credentials or allow plain HTTP,
// never carry over.
var carriedArgs = []string{
	"console=",
	"kuberoot.nameserver=", "kuberoot.ip=",
	"kuberoot.pod-cidr=", "kuberoot.service-cidr=", "kuberoot.pod-network=",
	"kuberoot.distro=", "kuberoot.repo=", "kuberoot.repo-key=",
}

func consoleArgs() string { return carryArgs(readFile("/proc/cmdline")) }

func carryArgs(cmdline string) string {
	var args []string
	for _, f := range strings.Fields(cmdline) {
		for _, prefix := range carriedArgs {
			if strings.HasPrefix(f, prefix) {
				args = append(args, f)
				break
			}
		}
	}
	return strings.Join(args, " ")
}

func osVersion() string {
	for _, line := range strings.Split(readFile("/etc/os-release"), "\n") {
		if v, ok := strings.CutPrefix(line, "VERSION_ID="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return "unknown"
}
