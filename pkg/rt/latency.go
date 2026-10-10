// Package rt measures the scheduling latency a node delivers to real-time
// tasks, with cyclictest: a thread at a real-time priority on each CPU asked
// for wakes at a fixed interval, and how late it ran is recorded.
package rt

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

var (
	// Cyclictest is the measuring program.
	Cyclictest = "/usr/bin/cyclictest"
	// NohzFull lists the CPUs without a timer tick: those of real-time work.
	NohzFull = "/sys/devices/system/cpu/nohz_full"
	Online   = "/sys/devices/system/cpu/online"
)

// HistogramMicroseconds bounds the histogram; later wake-ups only count
// in the maximum.
const HistogramMicroseconds = 10000

// Defaults fills what a test leaves unset.
func Defaults(s node.LatencyTestSpec) node.LatencyTestSpec {
	if s.DurationSeconds == 0 {
		s.DurationSeconds = 60
	}
	if s.Priority == 0 {
		s.Priority = 95
	}
	if s.IntervalMicroseconds == 0 {
		s.IntervalMicroseconds = 1000
	}
	return s
}

// Validate refuses a test the node cannot run as asked.
func Validate(s node.LatencyTestSpec) error {
	switch {
	case s.DurationSeconds < 0 || s.DurationSeconds > 3600:
		return fmt.Errorf("durationSeconds: from 1 to 3600")
	case s.Priority < 0 || s.Priority > 99:
		return fmt.Errorf("priority: from 1 to 99")
	case s.IntervalMicroseconds < 0 || (s.IntervalMicroseconds > 0 && s.IntervalMicroseconds < 50):
		return fmt.Errorf("intervalMicroseconds: 50 or more")
	}
	if s.CPUs != "" {
		if _, err := ParseCPUList(s.CPUs); err != nil {
			return fmt.Errorf("cpus: %w", err)
		}
	}
	return nil
}

// CPUs to measure: those asked for, else those of real-time work, else all.
// The kernel lists as tickless every CPU its arguments name, those the
// machine does not have included (2-N reaches the most it could have): only
// the ones online count.
func CPUs(s node.LatencyTestSpec) string {
	if s.CPUs != "" {
		return s.CPUs
	}
	online := readList(Online)
	if rt := Intersect(readList(NohzFull), online); len(rt) > 0 {
		return FormatCPUList(rt)
	}
	if len(online) > 0 {
		return FormatCPUList(online)
	}
	return "0"
}

func readList(path string) []int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	cpus, err := ParseCPUList(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil
	}
	return cpus
}

// Intersect keeps the CPUs of a that are in b, in order.
func Intersect(a, b []int) []int {
	in := map[int]bool{}
	for _, c := range b {
		in[c] = true
	}
	var out []int
	for _, c := range a {
		if in[c] {
			out = append(out, c)
		}
	}
	return out
}

// FormatCPUList writes CPUs as the kernel does: 2-3,6.
func FormatCPUList(cpus []int) string {
	var parts []string
	for i := 0; i < len(cpus); {
		j := i
		for j+1 < len(cpus) && cpus[j+1] == cpus[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, strconv.Itoa(cpus[i])+"-"+strconv.Itoa(cpus[j]))
		} else {
			parts = append(parts, strconv.Itoa(cpus[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// Args are cyclictest's arguments for a test writing its results to out:
// one thread per CPU measured, the program's own work on CPU 0, memory
// locked so no page fault adds to what is measured.
func Args(s node.LatencyTestSpec, cpus, out string) []string {
	return []string{"--quiet", "--mlockall", "--priority=" + strconv.Itoa(int(s.Priority)), "--policy=fifo",
		"--interval=" + strconv.Itoa(int(s.IntervalMicroseconds)), "--duration=" + strconv.Itoa(int(s.DurationSeconds)),
		"--affinity=" + cpus, "--threads", "--mainaffinity=0",
		"--histogram=" + strconv.Itoa(HistogramMicroseconds), "--json=" + out}
}

// Run runs a test and returns its results; dir holds its output.
func Run(ctx context.Context, s node.LatencyTestSpec, dir string) (node.LatencyTestStatus, error) {
	var st node.LatencyTestStatus
	s = Defaults(s)
	out := filepath.Join(dir, "cyclictest.json")
	cmd := exec.CommandContext(ctx, Cyclictest, Args(s, CPUs(s), out)...)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return st, fmt.Errorf("cyclictest: %v: %s", err, strings.TrimSpace(string(msg)))
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return st, err
	}
	return Parse(raw)
}

type report struct {
	Thread map[string]struct {
		Histogram map[string]int64 `json:"histogram"`
		Cycles    int64            `json:"cycles"`
		Min       int64            `json:"min"`
		Max       int64            `json:"max"`
		Avg       float64          `json:"avg"`
		CPU       int32            `json:"cpu"`
	} `json:"thread"`
}

// Parse reads cyclictest's JSON report: per CPU, the least, mean and most
// latency and the 99th and 99.99th percentiles from the histogram; then the
// worst of each over the CPUs.
func Parse(raw []byte) (node.LatencyTestStatus, error) {
	var st node.LatencyTestStatus
	var r report
	if err := json.Unmarshal(raw, &r); err != nil {
		return st, fmt.Errorf("cyclictest report: %w", err)
	}
	if len(r.Thread) == 0 {
		return st, fmt.Errorf("cyclictest report: no threads")
	}
	for _, t := range r.Thread {
		c := node.CPULatency{CPU: t.CPU, Samples: t.Cycles, MinMicroseconds: t.Min,
			AvgMicroseconds: int64(math.Round(t.Avg)), MaxMicroseconds: t.Max}
		c.P99Microseconds = percentile(t.Histogram, t.Cycles, t.Max, 0.99)
		c.P9999Microseconds = percentile(t.Histogram, t.Cycles, t.Max, 0.9999)
		st.CPUs = append(st.CPUs, c)
		st.MaxMicroseconds = max(st.MaxMicroseconds, c.MaxMicroseconds)
		st.P99Microseconds = max(st.P99Microseconds, c.P99Microseconds)
		st.P9999Microseconds = max(st.P9999Microseconds, c.P9999Microseconds)
	}
	sort.Slice(st.CPUs, func(i, j int) bool { return st.CPUs[i].CPU < st.CPUs[j].CPU })
	return st, nil
}

// percentile is the latency q of the wake-ups did not exceed; past the
// histogram's end, the maximum stands for it.
func percentile(hist map[string]int64, samples, maxSeen int64, q float64) int64 {
	if samples == 0 {
		return 0
	}
	type bucket struct{ us, n int64 }
	var buckets []bucket
	for k, n := range hist {
		us, err := strconv.ParseInt(k, 10, 64)
		if err == nil {
			buckets = append(buckets, bucket{us, n})
		}
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].us < buckets[j].us })
	need := int64(math.Ceil(q * float64(samples)))
	var seen int64
	for _, b := range buckets {
		seen += b.n
		if seen >= need {
			return b.us
		}
	}
	return maxSeen
}

// ParseCPUList reads a CPU list such as 2-3,6.
func ParseCPUList(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		lo, hi, ranged := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 0 {
			return nil, fmt.Errorf("%q is not a CPU list", s)
		}
		b := a
		if ranged {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return nil, fmt.Errorf("%q is not a CPU list", s)
			}
		}
		for c := a; c <= b; c++ {
			out = append(out, c)
		}
	}
	return out, nil
}
