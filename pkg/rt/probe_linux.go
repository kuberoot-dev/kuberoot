package rt

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

// ProbeCommand is the argument that turns the node's own binary into a
// deadline latency probe (ProbeMain).
const ProbeCommand = "rt-deadline-probe"

// runDeadline measures how late the node starts SCHED_DEADLINE jobs. A
// deadline task must be free to run on every CPU of its scheduling domain,
// so each CPU measured becomes a cpuset partition of its own for the test,
// with one probe process in it whose thread reserves runtime every interval
// and yields at once: the kernel wakes it at the start of each period, and
// how late it ran is recorded.
func runDeadline(ctx context.Context, s node.LatencyTestSpec) (node.LatencyTestStatus, error) {
	var st node.LatencyTestStatus
	cpus, err := ParseCPUList(CPUs(s))
	if err != nil {
		return st, err
	}
	exe, err := os.Executable()
	if err != nil {
		return st, err
	}
	if held := Intersect(cpus, KubeletAssignedCPUs()); len(held) > 0 {
		return st, fmt.Errorf("CPUs %s are given to pods: a deadline test takes its CPUs whole, so it measures only free ones", FormatCPUList(held))
	}
	d := probeDeadline(s)
	var mu sync.Mutex
	var errs []string
	var wg sync.WaitGroup
	for _, cpu := range cpus {
		cgroup := fmt.Sprintf("kuberoot-latency-cpu%d", cpu)
		dir := filepath.Join(CgroupRoot, cgroup)
		if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
			return st, err
		}
		defer os.Remove(dir)
		if err := write(filepath.Join(dir, "cpuset.cpus"), strconv.Itoa(cpu)); err != nil {
			return st, err
		}
		if _, err := Partition(cgroup); err != nil {
			return st, fmt.Errorf("CPU %d: %w (is it a pod's?)", cpu, err)
		}
		fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return st, err
		}
		defer unix.Close(fd)
		cmd := exec.CommandContext(ctx, exe, ProbeCommand, "-cpu", strconv.Itoa(cpu),
			"-runtime", d.Runtime.String(), "-period", d.Period.String(),
			"-duration", (time.Duration(s.DurationSeconds) * time.Second).String())
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
		// One thread runs Go code, and nothing interrupts it to preempt it.
		cmd.Env = append(os.Environ(), "GOMAXPROCS=1", "GODEBUG=asyncpreemptoff=1")
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := cmd.Output()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				msg := err.Error()
				if ee, ok := err.(*exec.ExitError); ok {
					msg = strings.TrimSpace(string(ee.Stderr))
				}
				errs = append(errs, fmt.Sprintf("CPU %d: %s", cpu, msg))
				return
			}
			var c node.CPULatency
			if err := json.Unmarshal(out, &c); err != nil {
				errs = append(errs, fmt.Sprintf("CPU %d: %v", cpu, err))
				return
			}
			st.CPUs = append(st.CPUs, c)
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		sort.Strings(errs)
		return st, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	for _, c := range st.CPUs {
		st.MaxMicroseconds = max(st.MaxMicroseconds, c.MaxMicroseconds)
		st.P99Microseconds = max(st.P99Microseconds, c.P99Microseconds)
		st.P9999Microseconds = max(st.P9999Microseconds, c.P9999Microseconds)
	}
	sort.Slice(st.CPUs, func(i, j int) bool { return st.CPUs[i].CPU < st.CPUs[j].CPU })
	return st, nil
}

// KubeletCPUManagerState is where the kubelet's CPU manager keeps the CPUs
// it gave containers.
var KubeletCPUManagerState = "/var/lib/kubelet/cpu_manager_state"

// KubeletAssignedCPUs are the CPUs the static CPU manager gave containers.
func KubeletAssignedCPUs() []int {
	var state struct {
		Entries map[string]map[string]string `json:"entries"`
	}
	raw, err := os.ReadFile(KubeletCPUManagerState)
	if err != nil || json.Unmarshal(raw, &state) != nil {
		return nil
	}
	var out []int
	for _, containers := range state.Entries {
		for _, list := range containers {
			cpus, _ := ParseCPUList(list)
			out = Union(out, cpus)
		}
	}
	return out
}

// ProbeMain is the probe process, started in its CPU's partition: it runs
// a deadline thread for the duration and prints what it saw as JSON.
func ProbeMain(args []string) int {
	fs := flag.NewFlagSet(ProbeCommand, flag.ContinueOnError)
	cpu := fs.Int("cpu", 0, "the CPU measured")
	rtime := fs.Duration("runtime", 100*time.Microsecond, "runtime reserved every period")
	period := fs.Duration("period", time.Millisecond, "period, and deadline")
	duration := fs.Duration("duration", time.Minute, "how long to measure")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := probe(*cpu, Deadline{Runtime: *rtime, Deadline: *period, Period: *period}, *duration)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	json.NewEncoder(os.Stdout).Encode(c)
	return 0
}

func probe(cpu int, d Deadline, duration time.Duration) (node.CPULatency, error) {
	c := node.CPULatency{CPU: int32(cpu)}
	// No page faults while measuring, and no allocation in the loop.
	if err := unix.Mlockall(unix.MCL_CURRENT | unix.MCL_FUTURE); err != nil {
		return c, fmt.Errorf("mlockall: %w", err)
	}
	runtime.LockOSThread()
	if err := SetDeadline(0, d); err != nil {
		return c, err
	}
	// Raw system calls: a blocking call through the Go scheduler lets it
	// hand the thread's P to another thread, which the woken probe then
	// waits to get back.
	yield := func() { unix.RawSyscall(unix.SYS_SCHED_YIELD, 0, 0, 0) }
	// The scheduler's clock is not slewed by time synchronisation, nor is
	// CLOCK_MONOTONIC_RAW: CLOCK_MONOTONIC drifts from the periods by up
	// to a hundred microseconds a second.
	now := func() int64 {
		var ts unix.Timespec
		unix.RawSyscall(unix.SYS_CLOCK_GETTIME, unix.CLOCK_MONOTONIC_RAW, uintptr(unsafe.Pointer(&ts)), 0)
		return ts.Nano()
	}
	// Jobs start a period apart, on the clock of the timers that wake them,
	// and each job's deadline (in the task's sched file) moves on by one
	// period. When a job overruns its budget or starts after its deadline
	// (a stalled virtual CPU does both), the kernel moves the deadline by
	// other than one period and the periods start afresh, from a point the
	// probe cannot see. So starts are measured per stretch of regular
	// periods, against a schedule anchored anywhere, and a stretch's
	// latencies are those starts less its earliest: the job that ended a
	// stretch counts in it, against its schedule.
	sched, err := unix.Open("/proc/thread-self/sched", unix.O_RDONLY, 0)
	if err != nil {
		return c, err
	}
	defer unix.Close(sched)
	buf := make([]byte, 8192)
	deadline := func() int64 {
		n, err := unix.Pread(sched, buf, 0)
		if err != nil {
			return -1
		}
		return parseSchedField(buf[:n], "dl.deadline")
	}
	period := int64(d.Period)
	st := newStretches(HistogramMicroseconds)
	yield()
	t0, dprev := now(), deadline()
	if dprev < 0 {
		return c, fmt.Errorf("no dl.deadline in /proc/thread-self/sched")
	}
	end := t0 + int64(duration)
	var k int64
	for {
		yield()
		t, dk := now(), deadline()
		if t >= end {
			break
		}
		k++
		regular := dk-dprev == period
		st.add((t-(t0+k*period))/1000, !regular)
		if !regular {
			t0, k = t, 0
		}
		dprev = dk
	}
	st.close()
	c.Samples, c.MinMicroseconds, c.MaxMicroseconds = st.samples, 0, st.max
	if c.Samples == 0 {
		return c, fmt.Errorf("no activations in %s", duration)
	}
	c.AvgMicroseconds = int64(math.Round(float64(st.sum) / float64(c.Samples)))
	m := map[string]int64{}
	for us, n := range st.hist {
		if n > 0 {
			m[strconv.Itoa(us)] = n
		}
	}
	c.P99Microseconds = percentile(m, c.Samples, c.MaxMicroseconds, 0.99)
	c.P9999Microseconds = percentile(m, c.Samples, c.MaxMicroseconds, 0.9999)
	return c, nil
}

// parseSchedField reads a number from /proc/<pid>/sched text, whose lines
// are "name   :   value"; -1 when the field is missing. No allocation: it
// runs between a deadline probe's jobs.
func parseSchedField(text []byte, name string) int64 {
	for i := 0; i < len(text); {
		end := i
		for end < len(text) && text[end] != '\n' {
			end++
		}
		line := text[i:end]
		i = end + 1
		if len(line) <= len(name) || string(line[:len(name)]) != name || (line[len(name)] != ' ' && line[len(name)] != ':') {
			continue
		}
		var v int64
		neg, seen := false, false
		for j := len(name); j < len(line); j++ {
			switch ch := line[j]; {
			case ch == '-' && !seen:
				neg = true
			case ch >= '0' && ch <= '9':
				v, seen = v*10+int64(ch-'0'), true
			case seen:
				j = len(line)
			}
		}
		if !seen {
			return -1
		}
		if neg {
			return -v
		}
		return v
	}
	return -1
}
