// Package bootdisk lays out kuberoot disks: the boot media an installer runs
// from and the installed system with its A/B root slots.
//
// Installed disk (GPT):
//
//	kuberoot-efi     EFI system partition: systemd-boot, kernel and initrd per slot
//	kuberoot-root-a  squashfs root, slot A
//	kuberoot-root-b  squashfs root, slot B
//	kuberoot-state   ext4, mounted at /var: PKI, cluster state, images
package bootdisk

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
)

const (
	MiB = int64(1) << 20

	MediaLabel = "KUBEROOT-MEDIA"
	EFILabel   = "kuberoot-efi"
	StateLabel = "kuberoot-state"

	efiSize  = 512 * MiB
	rootSize = 1024 * MiB
	// The state partition gets whatever is left, but no less than this.
	minStateSize = 2048 * MiB
)

// RootLabel names the root partition of a slot ("a" or "b").
func RootLabel(slot string) string { return "kuberoot-root-" + slot }

// Artifacts are the files a kuberoot release consists of.
type Artifacts struct {
	Dir        string // holds vmlinuz.efi, initrd.cpio, rootfs.squashfs, systemd-boot.efi, maybe kernel.args
	Arch       string // arm64 or amd64
	Version    string
	ConsoleArg string // console= arguments for the kernel command line
}

func (a Artifacts) file(name string) string { return path.Join(a.Dir, name) }

// KernelArgsFile holds the kernel's own boot arguments where the kernel
// cannot add them to the boot loader's itself, as on arm64: they go into
// every boot entry of that kernel.
const KernelArgsFile = "kernel.args"

// KernelArgs are the kernel's own boot arguments; none without the file.
func (a Artifacts) KernelArgs() string {
	raw, err := os.ReadFile(a.file(KernelArgsFile))
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(raw)), " ")
}

// BootArgs are the arguments every boot entry of the artifacts carries
// after its own: the kernel's, then the console and carried settings.
func (a Artifacts) BootArgs() string {
	return strings.TrimSpace(a.KernelArgs() + " " + a.ConsoleArg)
}

// RemovableBootPath is where firmware looks for a bootloader without NVRAM entries.
func (a Artifacts) RemovableBootPath() string {
	if a.Arch == "amd64" {
		return "/EFI/BOOT/BOOTX64.EFI"
	}
	return "/EFI/BOOT/BOOTAA64.EFI"
}

// InstalledTable is the partition table of an installed system on a disk of size bytes.
func InstalledTable(size int64) (*gpt.Table, error) {
	first := MiB // keep the first MiB for the GPT and alignment
	stateStart := first + efiSize + 2*rootSize
	stateSize := size - stateStart - MiB // leave room for the backup GPT
	if stateSize < minStateSize {
		return nil, fmt.Errorf("disk too small: %d MiB, need at least %d MiB", size/MiB, (stateStart+minStateSize+MiB)/MiB)
	}
	parts := []struct {
		name  string
		typ   gpt.Type
		start int64
		size  int64
	}{
		{EFILabel, gpt.EFISystemPartition, first, efiSize},
		{RootLabel("a"), gpt.LinuxFilesystem, first + efiSize, rootSize},
		{RootLabel("b"), gpt.LinuxFilesystem, first + efiSize + rootSize, rootSize},
		{StateLabel, gpt.LinuxFilesystem, stateStart, stateSize},
	}
	return table(parts)
}

// MediaTable is the single EFI system partition of boot media holding size bytes of files.
func MediaTable(size int64) *gpt.Table {
	t, _ := table([]struct {
		name  string
		typ   gpt.Type
		start int64
		size  int64
	}{{MediaLabel, gpt.EFISystemPartition, MiB, size}})
	return t
}

func table(parts []struct {
	name  string
	typ   gpt.Type
	start int64
	size  int64
}) (*gpt.Table, error) {
	t := &gpt.Table{LogicalSectorSize: 512, PhysicalSectorSize: 512, ProtectiveMBR: true}
	for i, p := range parts {
		t.Partitions = append(t.Partitions, &gpt.Partition{
			Index: i + 1,
			Start: uint64(p.start / 512),
			End:   uint64((p.start+p.size)/512 - 1),
			Size:  uint64(p.size),
			Type:  p.typ,
			Name:  p.name,
		})
	}
	return t, nil
}

// Entry is one systemd-boot loader entry.
type Entry struct {
	ID      string // file name without .conf; may carry a boot counter, e.g. kuberoot-b+3
	Title   string
	Version string // higher versions sort first, so the newest slot is the default
	Kernel  string
	Initrd  string
	Options string
}

func (e Entry) String() string {
	return fmt.Sprintf("title %s\nsort-key kuberoot\nversion %s\nlinux %s\ninitrd %s\noptions %s\n",
		e.Title, e.Version, e.Kernel, e.Initrd, e.Options)
}

// LoaderConf picks the highest-version kuberoot entry; entries that ran out of
// boot attempts sort last, which is what makes a failed upgrade fall back.
// The editor stays off: whoever reaches the console must not be able to add
// boot arguments.
const LoaderConf = "timeout 3\ndefault kuberoot-*\nconsole-mode keep\neditor no\n"

// SlotEntry describes the boot entry of an installed slot.
func SlotEntry(a Artifacts, slot, version string) Entry {
	return Entry{
		ID:      "kuberoot-" + slot,
		Title:   fmt.Sprintf("kuberoot %s (slot %s)", a.Version, slot),
		Version: version,
		Kernel:  "/kuberoot/" + slot + "/vmlinuz.efi",
		Initrd:  "/kuberoot/" + slot + "/initrd.cpio",
		Options: strings.TrimSpace("kuberoot.root=PARTLABEL=" + RootLabel(slot) + " quiet panic=10 " + a.BootArgs()),
	}
}

// WriteFile copies src into the FAT filesystem at dst, creating parent directories.
func WriteFile(fs filesystem.FileSystem, dst string, src io.Reader) error {
	if dir := path.Dir(dst); dir != "/" {
		if err := fs.Mkdir(dir); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	f, err := fs.OpenFile(dst, os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, src); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// CopyFile writes the local file src into fs at dst.
func CopyFile(fs filesystem.FileSystem, dst, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return WriteFile(fs, dst, f)
}
