package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	newRoot = "/newroot"
	media   = "/media"

	// StateLabel names the partition that keeps node state across reboots.
	stateLabel = "kuberoot-state"
)

// switchRoot mounts the root filesystem image read-only, gives it writable
// /etc, /var, /run and /tmp, moves the kernel filesystems over and execs
// stage two from inside it, keeping PID 1.
//
// kuberoot.root selects the image:
//
//	(unset)                       /rootfs.squashfs inside the initramfs, for development
//	PARTLABEL=<name>              a partition holding the squashfs itself (installed system)
//	PARTLABEL=<name>:<file>       a squashfs file on a FAT partition (boot media)
func switchRoot() error {
	for _, m := range []mount{
		{"proc", "/proc", "proc", 0, ""},
		{"sysfs", "/sys", "sysfs", 0, ""},
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
	} {
		if err := unix.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
			return fmt.Errorf("mount %s: %w", m.target, err)
		}
	}
	rootDev, installed, err := rootDevice(cmdlineValue("kuberoot.root"))
	if err != nil {
		return err
	}
	if err := unix.Mount(rootDev, newRoot, "squashfs", unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("mount root image: %w", err)
	}
	for _, dir := range []string{"/run", "/var", "/tmp"} {
		if err := unix.Mount("tmpfs", newRoot+dir, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
			return fmt.Errorf("mount %s: %w", dir, err)
		}
	}
	if installed {
		state, err := findPartition(stateLabel, 10*time.Second)
		if err != nil {
			return err
		}
		// data=journal: edge nodes lose power mid-write, and delayed allocation
		// would leave freshly unpacked image layers as zero-filled files.
		if err := unix.Mount(state, newRoot+"/var", "ext4", unix.MS_NOSUID|unix.MS_NODEV, "data=journal"); err != nil {
			return fmt.Errorf("mount state: %w", err)
		}
	}
	// /var/run is /run, as the FHS has it: what looks for a socket there,
	// such as the kubelet's cAdvisor for CRI-O's, finds it, and nothing of
	// a previous boot's run time stays on the state partition.
	if err := linkVarRun(newRoot); err != nil {
		return err
	}
	// Boot media stays reachable from the running system, for the installer.
	if isMountpoint(media) {
		target := newRoot + "/run/kuberoot/media"
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		if err := unix.Mount(media, target, "", unix.MS_MOVE, ""); err != nil {
			return fmt.Errorf("move media: %w", err)
		}
	}
	// /etc stays the image's /etc underneath; node-specific files land in a tmpfs upper layer.
	upper, work := newRoot+"/run/kuberoot/etc/upper", newRoot+"/run/kuberoot/etc/work"
	for _, d := range []string{upper, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	opts := fmt.Sprintf("lowerdir=%s/etc,upperdir=%s,workdir=%s", newRoot, upper, work)
	if err := unix.Mount("overlay", newRoot+"/etc", "overlay", 0, opts); err != nil {
		return fmt.Errorf("mount /etc overlay: %w", err)
	}
	for _, dir := range []string{"/dev", "/proc", "/sys"} {
		if err := unix.Mount(dir, newRoot+dir, "", unix.MS_MOVE, ""); err != nil {
			return fmt.Errorf("move %s: %w", dir, err)
		}
	}
	if err := unix.Chdir(newRoot); err != nil {
		return err
	}
	if err := unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return err
	}
	if err := unix.Chdir("/"); err != nil {
		return err
	}
	return unix.Exec("/usr/sbin/kinit", []string{"kinit"}, os.Environ())
}

// rootDevice resolves kuberoot.root to a block device holding the squashfs root,
// and reports whether it is an installed system with a state partition.
func rootDevice(spec string) (dev string, installed bool, err error) {
	if spec == "" {
		dev, err = attachLoop(rootImage)
		return dev, false, err
	}
	label, file, onMedia := strings.Cut(strings.TrimPrefix(spec, "PARTLABEL="), ":")
	part, err := findPartition(label, 30*time.Second)
	if err != nil {
		return "", false, err
	}
	if !onMedia {
		return part, true, nil
	}
	if err := os.MkdirAll(media, 0o755); err != nil {
		return "", false, err
	}
	if err := unix.Mount(part, media, "vfat", unix.MS_RDONLY, ""); err != nil {
		return "", false, fmt.Errorf("mount boot media: %w", err)
	}
	dev, err = attachLoop(filepath.Join(media, file))
	return dev, false, err
}

// attachLoop binds a read-only loop device to the given image file.
func attachLoop(image string) (string, error) {
	ctl, err := os.OpenFile("/dev/loop-control", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer ctl.Close()
	n, err := unix.IoctlRetInt(int(ctl.Fd()), unix.LOOP_CTL_GET_FREE)
	if err != nil {
		return "", fmt.Errorf("get free loop device: %w", err)
	}
	dev := fmt.Sprintf("/dev/loop%d", n)
	loop, err := os.OpenFile(dev, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer loop.Close()
	file, err := os.Open(image)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if err := unix.IoctlSetInt(int(loop.Fd()), unix.LOOP_SET_FD, int(file.Fd())); err != nil {
		return "", fmt.Errorf("attach %s: %w", dev, err)
	}
	info := unix.LoopInfo64{Flags: unix.LO_FLAGS_READ_ONLY}
	if err := unix.IoctlLoopSetStatus64(int(loop.Fd()), &info); err != nil {
		return "", fmt.Errorf("configure %s: %w", dev, err)
	}
	return dev, nil
}

func linkVarRun(root string) error {
	p := root + "/var/run"
	if t, err := os.Readlink(p); err == nil && t == "../run" {
		return nil
	}
	if err := os.RemoveAll(p); err != nil {
		return fmt.Errorf("clear /var/run: %w", err)
	}
	if err := os.Symlink("../run", p); err != nil {
		return fmt.Errorf("link /var/run: %w", err)
	}
	return nil
}
