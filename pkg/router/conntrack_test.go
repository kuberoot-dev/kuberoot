package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConntrackSize(t *testing.T) {
	for _, c := range []struct {
		mem  uint64
		want int
	}{
		{256 << 20, 65536},
		{1 << 30, 131072},
		// MemTotal is a little under the memory the machine was given.
		{2<<30 - 120<<20, 262144},
		{2 << 30, 262144},
		{4<<30 - 200<<20, 524288},
		{1280 << 20, 131072},
		{16 << 30, 2097152},
		{1 << 40, 4 << 20},
	} {
		if got := ConntrackSize(c.mem); got != c.want {
			t.Errorf("ConntrackSize(%d MiB) = %d, want %d", c.mem>>20, got, c.want)
		}
	}
}

func TestSizeConntrackGrowsAndNeverShrinks(t *testing.T) {
	root := t.TempDir()
	proc, sys := filepath.Join(root, "proc"), filepath.Join(root, "sys")
	write := func(path, s string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	max := filepath.Join(proc, "sys/net/netfilter/nf_conntrack_max")
	hash := filepath.Join(sys, "module/nf_conntrack/parameters/hashsize")
	write(filepath.Join(proc, "meminfo"), "MemTotal:        2097152 kB\nMemFree:          100000 kB\n")
	write(max, "65536\n")
	write(hash, "1048576\n")
	n, err := SizeConntrack(proc, sys, filepath.Join(root, "none.json"))
	if err != nil || n != 262144 {
		t.Fatalf("SizeConntrack = %d, %v", n, err)
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return strings.TrimSpace(string(b)) }
	if read(max) != "262144" {
		t.Errorf("nf_conntrack_max = %s, want 262144", read(max))
	}
	if read(hash) != "1048576" {
		t.Errorf("hashsize set by hand shrank to %s", read(hash))
	}
}

func TestSizeConntrackLeavesTheOperatorsChoice(t *testing.T) {
	root := t.TempDir()
	proc, sys := filepath.Join(root, "proc"), filepath.Join(root, "sys")
	max := filepath.Join(proc, "sys/net/netfilter/nf_conntrack_max")
	for path, s := range map[string]string{
		filepath.Join(proc, "meminfo"): "MemTotal: 8388608 kB\n",
		max:                            "32768\n",
		filepath.Join(sys, "module/nf_conntrack/parameters/hashsize"): "32768\n",
		filepath.Join(root, "osconfig.json"):                          `{"sysctls":{"net.netfilter.nf_conntrack_max":"32768"}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n, err := SizeConntrack(proc, sys, filepath.Join(root, "osconfig.json"))
	if err != nil || n != 32768 {
		t.Fatalf("SizeConntrack = %d, %v; want the 32768 the OSConfig sets", n, err)
	}
	if b, _ := os.ReadFile(max); strings.TrimSpace(string(b)) != "32768" {
		t.Errorf("nf_conntrack_max = %s", b)
	}
}
