package nodeapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeControllerProcess(t *testing.T, proc string, pid int, executable, comm string) {
	t.Helper()
	dir := filepath.Join(proc, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"comm": comm + "\n", "cmdline": executable + "\x00--secret=do-not-publish\x00", "stat": fmt.Sprintf("%d (%s) S %s 12345 0\n", pid, comm, strings.Repeat("0 ", 18))} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestControllerInventoryIncludesOrphansWithoutArguments(t *testing.T) {
	proc := t.TempDir()
	boot := filepath.Join(proc, "sys/kernel/random")
	if err := os.MkdirAll(boot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boot, "boot_id"), []byte("boot-1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fakeControllerProcess(t, proc, 10, "/usr/bin/kuberoot-vmctl", "kuberoot-vmctl")
	fakeControllerProcess(t, proc, 11, "/usr/bin/kuberoot-vmctl", "renamed")
	fakeControllerProcess(t, proc, 12, "/usr/bin/unrelated", "unrelated")
	got, message := controllerProcesses(proc, "kuberoot-vmctl")
	if message != "" || len(got) != 2 || got[0].PID != 10 || got[1].PID != 11 || got[0].StartTicks != 12345 || got[0].BootID != "boot-1" {
		t.Fatalf("inventory=%+v error=%s", got, message)
	}
	if strings.Contains(fmt.Sprint(got), "secret") {
		t.Fatal("inventory exposed arguments")
	}
	fakeControllerProcess(t, proc, 13, "/tmp/impostor", "kuberoot-vmctl")
	if _, message := controllerProcesses(proc, "kuberoot-vmctl"); message == "" {
		t.Fatal("comm alone was trusted as executable identity")
	}
}
