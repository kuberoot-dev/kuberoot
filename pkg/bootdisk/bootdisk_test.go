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
