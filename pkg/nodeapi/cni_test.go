package nodeapi

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCNIConfig(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "kuberoot.conflist.tmpl")
	out := filepath.Join(dir, "net.d", "10-kuberoot.conflist")
	if err := os.WriteFile(tmpl, []byte(`{"ranges": [[{"subnet": "{{.PodCIDR}}"}]]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, subnet, _ := net.ParseCIDR("10.200.3.0/24")
	if err := writeCNIConfig(tmpl, out, subnet); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	if !strings.Contains(string(raw), `"10.200.3.0/24"`) {
		t.Fatalf("config: %s", raw)
	}
	st, _ := os.Stat(out)
	if err := writeCNIConfig(tmpl, out, subnet); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(out); !st2.ModTime().Equal(st.ModTime()) {
		t.Fatal("an unchanged configuration was written again")
	}
}
