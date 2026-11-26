package router

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnableForwardingWritesBothFamilies(t *testing.T) {
	root := t.TempDir()
	files := []string{"sys/net/ipv4/ip_forward", "sys/net/ipv6/conf/all/forwarding"}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := enableForwarding(root); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil || string(b) != "1\n" {
			t.Errorf("%s = %q, %v; want 1", f, b, err)
		}
	}
}

func TestEnableForwardingReportsAMissingKnob(t *testing.T) {
	if err := enableForwarding(t.TempDir()); err == nil {
		t.Fatal("no error without /proc/sys entries; the controller would believe forwarding is on")
	}
}
