package rt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CgroupRoot is where cgroup v2 is mounted.
var CgroupRoot = "/sys/fs/cgroup"

// The kernel admits a SCHED_DEADLINE task only if it may run on every CPU of
// its root domain, so a task pinned to a pod's CPUs is refused while those
// CPUs belong to the node's one domain. A cpuset partition makes them a root
// domain of their own: the container's cgroup becomes a partition root on its
// exclusive CPUs, with deadline bandwidth of its own, which the kernel's
// admission control counts apart from the rest of the node.
//
// The container's cgroup is deep in the kubelet's tree, under cgroups that
// are not partitions themselves: a remote partition (Linux 6.7 and later),
// whose CPUs every cgroup above it lists in cpuset.cpus.exclusive.

func read(path string) string {
	raw, _ := os.ReadFile(path)
	return strings.TrimSpace(string(raw))
}

// write sets a cgroup file; with a newline, so that an empty value is a
// write too.
func write(path, value string) error {
	if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
		return fmt.Errorf("%s: %w", strings.TrimPrefix(path, CgroupRoot+"/"), err)
	}
	return nil
}

// Partition makes cgroup (relative to CgroupRoot) a partition root on the
// CPUs it has: those the kubelet gave the container. It returns the CPUs.
func Partition(cgroup string) (string, error) {
	dir := filepath.Join(CgroupRoot, cgroup)
	cpus := read(filepath.Join(dir, "cpuset.cpus"))
	if cpus == "" {
		return "", fmt.Errorf("cgroup %s has no CPUs of its own: the kubelet gave the container none", cgroup)
	}
	if state := read(filepath.Join(dir, "cpuset.cpus.partition")); state == "root" {
		return cpus, nil
	}
	want, err := ParseCPUList(cpus)
	if err != nil {
		return "", err
	}
	// Every cgroup from the top down lists the CPUs as exclusive.
	parts := strings.Split(strings.Trim(cgroup, "/"), "/")
	for i := 1; i <= len(parts); i++ {
		p := filepath.Join(append([]string{CgroupRoot}, parts[:i]...)...)
		have, _ := ParseCPUList(read(filepath.Join(p, "cpuset.cpus.exclusive")))
		if err := write(filepath.Join(p, "cpuset.cpus.exclusive"), FormatCPUList(Union(have, want))); err != nil {
			return "", err
		}
	}
	if err := write(filepath.Join(dir, "cpuset.cpus.partition"), "root"); err != nil {
		return "", err
	}
	if state := read(filepath.Join(dir, "cpuset.cpus.partition")); state != "root" {
		return "", fmt.Errorf("cpuset partition on CPUs %s: %s", cpus, state)
	}
	return cpus, nil
}

// Union of two CPU lists, in order.
func Union(a, b []int) []int {
	in := map[int]bool{}
	for _, c := range append(append([]int{}, a...), b...) {
		in[c] = true
	}
	out := make([]int, 0, len(in))
	for c := range in {
		out = append(out, c)
	}
	sort.Ints(out)
	return out
}

// Release narrows cpuset.cpus.exclusive of top (relative to CgroupRoot) to
// the CPUs its children still hold, once the pods that held the others are
// gone: those CPUs return to the kubelet's shared pool.
func Release(top string) error {
	dir := filepath.Join(CgroupRoot, top)
	have := read(filepath.Join(dir, "cpuset.cpus.exclusive"))
	if have == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var held []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cpus, _ := ParseCPUList(read(filepath.Join(dir, e.Name(), "cpuset.cpus.exclusive")))
		held = Union(held, cpus)
	}
	if FormatCPUList(held) == have {
		return nil
	}
	return write(filepath.Join(dir, "cpuset.cpus.exclusive"), FormatCPUList(held))
}

// FirstProcess is the process of a cgroup the others descend from: the
// container's own, whose parent (the runtime's shim) is outside.
func FirstProcess(cgroup string) (int, error) {
	raw := read(filepath.Join(CgroupRoot, cgroup, "cgroup.procs"))
	var pids []int
	in := map[int]bool{}
	for _, f := range strings.Fields(raw) {
		var pid int
		if _, err := fmt.Sscan(f, &pid); err == nil {
			pids, in[pid] = append(pids, pid), true
		}
	}
	sort.Ints(pids)
	for _, pid := range pids {
		if !in[parentPID(pid)] {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("cgroup %s has no processes", cgroup)
}

// ProcRoot is where procfs is mounted.
var ProcRoot = "/proc"

func parentPID(pid int) int {
	stat := read(filepath.Join(ProcRoot, fmt.Sprint(pid), "stat"))
	// pid (comm) state ppid ...: comm may hold spaces and parentheses.
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0
	}
	var state string
	var ppid int
	fmt.Sscan(stat[i+1:], &state, &ppid)
	return ppid
}
