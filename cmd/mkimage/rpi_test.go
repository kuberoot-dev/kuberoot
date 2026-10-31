package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"

	"github.com/kuberoot-dev/kuberoot/pkg/bootdisk"
)

// The Pi firmware hands cmdline.txt to the kernel as written. A second line is
// not a continuation -- it is dropped, along with every argument on it, and
// the kernel boots with a command line that is quietly shorter than the one
// that was meant. The symptom is a root filesystem that cannot be found, on a
// board, with no clue pointing back at the file.
func TestRpiCmdlineIsOneLine(t *testing.T) {
	got := rpiCmdline("kuberoot.root=PARTLABEL=KUBEROOT:/kuberoot/rootfs.squashfs\nkuberoot.mode=install  quiet\n")
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("cmdline must be exactly one line ending in a newline, got %q", got)
	}
	for _, want := range []string{"kuberoot.root=PARTLABEL=KUBEROOT:/kuberoot/rootfs.squashfs", "kuberoot.mode=install", "quiet"} {
		if !strings.Contains(got, want) {
			t.Errorf("cmdline lost %q: %q", want, got)
		}
	}
}

// A dtparam the board does not know is a warning on a console nobody has
// connected yet, so the PCIe line that the Pi 5's NVMe drive depends on must
// be fenced into its own section rather than left for a Pi 4 to ignore.
func TestRpiConfigFencesTheBoardSpecificLines(t *testing.T) {
	cfg := rpiConfig()
	pci := strings.Index(cfg, "dtparam=pciex1")
	pi5 := strings.Index(cfg, "[pi5]")
	if pci < 0 || pi5 < 0 || pi5 > pci {
		t.Fatalf("the PCIe parameter must follow a [pi5] filter:\n%s", cfg)
	}
	// And what comes between them must not close the section again.
	if strings.Contains(cfg[pi5:pci], "[all]") {
		t.Errorf("the [pi5] section is closed before the PCIe parameter:\n%s", cfg)
	}
	for _, want := range []string{"arm_64bit=1", "kernel=Image", "initramfs initrd.cpio followkernel"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.txt is missing %q", want)
		}
	}
}

// Missing firmware has to be named as such. It is the one part of the boot
// path this tree does not build, so "no such file" sends whoever hits it
// looking through the build for something that was never there.
func TestRpiFilesNamesTheMissingFirmware(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"Image", "initrd.cpio"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "dtb"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dtb := range rpiDevicetrees {
		if err := os.WriteFile(filepath.Join(dir, "dtb", dtb), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, err := rpiFiles(dir)
	if err == nil {
		t.Fatal("expected missing firmware to be an error")
	}
	if !strings.Contains(err.Error(), "fetch-rpi-firmware.sh") {
		t.Errorf("the error should say how to get the firmware, got: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "firmware"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range rpiFirmware {
		if err := os.WriteFile(filepath.Join(dir, "firmware", f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := rpiFiles(dir)
	if err != nil {
		t.Fatalf("a complete artifacts directory should be accepted: %v", err)
	}
	// The firmware looks for the devicetrees at the root of the partition and
	// picks one by board revision, so neither may land in a subdirectory.
	for _, dtb := range rpiDevicetrees {
		if _, ok := files["/"+dtb]; !ok {
			t.Errorf("%s must sit at the root of the medium, got %v", dtb, keys(files))
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Builds a whole medium and reads it back. This is the only end-to-end check
// available without a board: that one image really does carry both ways of
// starting, each loader can reach what it needs, and neither of them pays for
// the other by carrying a second copy of the same file.
func TestMediumCarriesBothLoadersAndOneInitramfs(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, size int) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"systemd-boot.efi", "vmlinuz.efi", "Image", "rootfs.squashfs"} {
		write(f, 4096)
	}
	// Big enough that a second copy would be unmistakable in the total.
	write("initrd.cpio", 3<<20)
	for _, d := range rpiDevicetrees {
		write("dtb/"+d, 1024)
	}
	for _, f := range rpiFirmware {
		write("firmware/"+f, 1024)
	}

	out := filepath.Join(t.TempDir(), "media.img")
	a := bootdisk.Artifacts{Dir: dir, Arch: "arm64", Version: "0.0.0-test", ConsoleArg: consoleFor("arm64")}
	if err := build(a, out, nil); err != nil {
		t.Fatalf("build: %v", err)
	}

	d, err := diskfs.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fs, err := d.GetFilesystem(1)
	if err != nil {
		t.Fatal(err)
	}

	// What the Pi firmware opens, and what systemd-boot opens. Neither reads
	// the other's files, so both sets have to be there.
	for _, want := range []string{"/config.txt", "/cmdline.txt", "/Image", "/initrd.cpio", "/loader/loader.conf", "/loader/entries/kuberoot-install.conf"} {
		if _, err := fs.OpenFile(want, os.O_RDONLY); err != nil {
			t.Errorf("%s is missing from the medium: %v", want, err)
		}
	}
	for _, dtb := range rpiDevicetrees {
		if _, err := fs.OpenFile("/"+dtb, os.O_RDONLY); err != nil {
			t.Errorf("%s is missing from the root of the medium: %v", dtb, err)
		}
	}

	// One initramfs, shared. It used to be written twice -- once where the
	// firmware looks and once where the boot entry pointed -- which cost 22 MB
	// on a real medium and nothing told anyone.
	if _, err := fs.OpenFile("/kuberoot/initrd.cpio", os.O_RDONLY); err == nil {
		t.Error("the initramfs is on the medium twice: /initrd.cpio and /kuberoot/initrd.cpio")
	}
	entry, err := fs.OpenFile("/loader/entries/kuberoot-install.conf", os.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _ := entry.Read(buf)
	if !strings.Contains(string(buf[:n]), "initrd /initrd.cpio") {
		t.Errorf("the boot entry must point at the shared initramfs:\n%s", buf[:n])
	}
}
