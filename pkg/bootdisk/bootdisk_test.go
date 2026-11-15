package bootdisk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlotEntryKernelArgs(t *testing.T) {
	dir := t.TempDir()
	a := Artifacts{Dir: dir, Arch: "arm64", Version: "1.0.0", ConsoleArg: "console=ttyAMA0"}
	want := "kuberoot.root=PARTLABEL=kuberoot-root-b quiet panic=10 console=ttyAMA0"
	if got := SlotEntry(a, "b", "2").Options; got != want {
		t.Fatalf("without kernel.args: %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(dir, KernelArgsFile), []byte("nohz_full=2-N  rcu_nocbs=2-N\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want = "kuberoot.root=PARTLABEL=kuberoot-root-b quiet panic=10 nohz_full=2-N rcu_nocbs=2-N console=ttyAMA0"
	if got := SlotEntry(a, "b", "2").Options; got != want {
		t.Fatalf("with kernel.args: %q, want %q", got, want)
	}
}

func TestRemovableBootPath(t *testing.T) {
	for arch, want := range map[string]string{"amd64": "/EFI/BOOT/BOOTX64.EFI", "arm64": "/EFI/BOOT/BOOTAA64.EFI"} {
		if got := (Artifacts{Arch: arch}).RemovableBootPath(); got != want {
			t.Errorf("%s: %s, want %s", arch, got, want)
		}
	}
}

// The initramfs is not always beside the other artifacts: on boot media it is
// at the root of the partition, the one place both systemd-boot and a
// Raspberry Pi's firmware can read it. An installer that looks only beside
// them installs the bootloader, then stops partway with a path nobody
// recognises -- which is exactly how this was found.
func TestInitrdPathFallsBackToTheMediumRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "kuberoot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := Artifacts{Dir: dir}

	// On a medium: one copy, at the root.
	atRoot := filepath.Join(root, "initrd.cpio")
	if err := os.WriteFile(atRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := a.InitrdPath(); got != atRoot {
		t.Errorf("expected the root copy %q, got %q", atRoot, got)
	}

	// Anywhere else, including an installed system: beside the rest, and that
	// one wins even when a stray copy exists further up.
	beside := filepath.Join(dir, "initrd.cpio")
	if err := os.WriteFile(beside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := a.InitrdPath(); got != beside {
		t.Errorf("expected the copy beside the artifacts %q, got %q", beside, got)
	}
}
