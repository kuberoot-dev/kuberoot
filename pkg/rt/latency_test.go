package rt

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

// report is cyclictest 2.11's JSON, as write_stats prints it.
const report2 = `{
  "file_version": 1,
  "cmdline:": "cyclictest --json=/tmp/x.json",
  "rt_test_version:": "2.11",
  "num_threads": 2,
  "resolution_in_ns": 0,
  "thread": {
    "0": {
      "histogram": {
        "2": 9000,
        "3": 900,
        "5": 99,
        "40": 1
      },
      "cycles": 10000,
      "min": 2,
      "max": 40,
      "avg": 2.19,
      "cpu": 3,
      "node": 0
    },
    "1": {
      "histogram": {
        "1": 9998
      },
      "cycles": 10000,
      "min": 1,
      "max": 12000,
      "avg": 3.50,
      "cpu": 2,
      "node": 0
    }
  }
}`

func TestParse(t *testing.T) {
	st, err := Parse([]byte(report2))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.CPUs) != 2 || st.CPUs[0].CPU != 2 || st.CPUs[1].CPU != 3 {
		t.Fatalf("cpus %+v", st.CPUs)
	}
	c3 := st.CPUs[1]
	if c3.MinMicroseconds != 2 || c3.AvgMicroseconds != 2 || c3.MaxMicroseconds != 40 || c3.P99Microseconds != 3 || c3.P9999Microseconds != 5 {
		t.Fatalf("cpu 3: %+v", c3)
	}
	// Two wake-ups past the histogram's end: the 99.99th percentile is one
	// of them, and the maximum stands for it.
	if c2 := st.CPUs[0]; c2.P99Microseconds != 1 || c2.P9999Microseconds != 12000 {
		t.Fatalf("cpu 2: %+v", c2)
	}
	if st.MaxMicroseconds != 12000 || st.P99Microseconds != 3 || st.P9999Microseconds != 12000 {
		t.Fatalf("worst: %+v", st)
	}
	if _, err := Parse([]byte(`{"thread":{}}`)); err == nil {
		t.Fatal("a report with no threads was read")
	}
}

func TestArgsAndDefaults(t *testing.T) {
	s := Defaults(node.LatencyTestSpec{})
	if s.DurationSeconds != 60 || s.Priority != 95 || s.IntervalMicroseconds != 1000 {
		t.Fatalf("defaults %+v", s)
	}
	got := strings.Join(Args(s, "2-3", "/run/x.json"), " ")
	for _, want := range []string{"--affinity=2-3", "--threads", "--priority=95", "--policy=fifo", "--duration=60",
		"--mlockall", "--mainaffinity=0", "--json=/run/x.json", "--histogram=10000"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, s := range []node.LatencyTestSpec{{DurationSeconds: 4000}, {Priority: 120}, {IntervalMicroseconds: 10}, {CPUs: "3-1"}, {CPUs: "x"}} {
		if Validate(s) == nil {
			t.Errorf("%+v passed", s)
		}
	}
	if err := Validate(node.LatencyTestSpec{CPUs: "2-3,5", DurationSeconds: 10}); err != nil {
		t.Error(err)
	}
	if got, _ := ParseCPUList("2-3,5"); !slices.Equal(got, []int{2, 3, 5}) {
		t.Errorf("cpu list %v", got)
	}
}

func TestCPUsPrefersTicklessOnes(t *testing.T) {
	dir := t.TempDir()
	NohzFull, Online = filepath.Join(dir, "nohz_full"), filepath.Join(dir, "online")
	if err := os.WriteFile(Online, []byte("0-3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CPUs(node.LatencyTestSpec{}); got != "0-3" {
		t.Errorf("no tickless CPUs: %q", got)
	}
	if err := os.WriteFile(NohzFull, []byte("2-3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CPUs(node.LatencyTestSpec{}); got != "2-3" {
		t.Errorf("tickless CPUs: %q", got)
	}
	if got := CPUs(node.LatencyTestSpec{CPUs: "1"}); got != "1" {
		t.Errorf("asked for CPU 1: %q", got)
	}
}

func TestCPUsKeepsOnlyOnlineTicklessCPUs(t *testing.T) {
	dir := t.TempDir()
	NohzFull, Online = dir+"/nohz_full", dir+"/online"
	if err := os.WriteFile(NohzFull, []byte("2-63\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Online, []byte("0-3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CPUs(node.LatencyTestSpec{}); got != "2-3" {
		t.Fatalf("got %q: CPUs the machine does not have were measured", got)
	}
	if err := os.WriteFile(NohzFull, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CPUs(node.LatencyTestSpec{}); got != "0-3" {
		t.Fatalf("got %q with no tickless CPU", got)
	}
	if got := FormatCPUList([]int{0, 2, 3, 4, 7}); got != "0,2-4,7" {
		t.Fatalf("format: %q", got)
	}
}
