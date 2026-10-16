package rt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParseDeadline(t *testing.T) {
	cases := []struct {
		in   string
		want Deadline
		err  string
	}{
		{in: "runtime=200us,deadline=1ms,period=1ms", want: Deadline{200 * time.Microsecond, time.Millisecond, time.Millisecond}},
		{in: "runtime=200us, period=2ms", want: Deadline{200 * time.Microsecond, 2 * time.Millisecond, 2 * time.Millisecond}},
		{in: "runtime=200us,deadline=500us", want: Deadline{200 * time.Microsecond, 500 * time.Microsecond, 500 * time.Microsecond}},
		{in: "runtime=2ms,period=1ms", err: "longer than deadline"},
		{in: "runtime=100us,deadline=2ms,period=1ms", err: "longer than period"},
		{in: "runtime=500ns,period=1ms", err: "at least"},
		{in: "runtime=1ms,period=5s", err: "at most"},
		{in: "runtime=1ms", err: "deadline or period"},
		{in: "budget=1ms", err: "only runtime"},
		{in: "runtime=fast", err: "runtime"},
	}
	for _, c := range cases {
		got, err := ParseDeadline(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: error %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: %v %v, want %v", c.in, got, err, c.want)
		}
	}
}

func pod(cpu, mem string, ann map[string]string) *corev1.Pod {
	res := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: ann},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "loop",
			Resources: corev1.ResourceRequirements{Requests: res, Limits: res}}}}}
}

func TestPodDeadlines(t *testing.T) {
	ann := map[string]string{DeadlineAnnotationPrefix + "loop": "runtime=300us,period=1ms"}
	got, err := PodDeadlines(pod("1", "64Mi", ann))
	if err != nil || got["loop"].Runtime != 300*time.Microsecond {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := PodDeadlines(pod("500m", "64Mi", ann)); err == nil || !strings.Contains(err.Error(), "whole CPUs") {
		t.Errorf("half a CPU: %v", err)
	}
	p := pod("1", "64Mi", map[string]string{DeadlineAnnotationPrefix + "other": "runtime=300us,period=1ms"})
	if _, err := PodDeadlines(p); err == nil || !strings.Contains(err.Error(), "no container") {
		t.Errorf("unknown container: %v", err)
	}
	p = pod("1", "64Mi", ann)
	p.Status.QOSClass = corev1.PodQOSBurstable
	if _, err := PodDeadlines(p); err == nil || !strings.Contains(err.Error(), "Guaranteed") {
		t.Errorf("burstable: %v", err)
	}
}

func fakeCgroup(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, v := range files {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(v), 0o644)
	}
	CgroupRoot = root
	t.Cleanup(func() { CgroupRoot = "/sys/fs/cgroup" })
	return root
}

func TestPartition(t *testing.T) {
	root := fakeCgroup(t, map[string]string{
		"kubepods/cpuset.cpus.exclusive":         "3\n",
		"kubepods/podA/c1/cpuset.cpus":           "2\n",
		"kubepods/podA/c1/cpuset.cpus.partition": "member\n",
	})
	// A fake cgroup takes "root" as written; the kernel would answer
	// "root invalid (...)" when it cannot.
	cpus, err := Partition("kubepods/podA/c1")
	if err != nil || cpus != "2" {
		t.Fatalf("%q %v", cpus, err)
	}
	for f, want := range map[string]string{
		"kubepods/cpuset.cpus.exclusive":         "2-3",
		"kubepods/podA/cpuset.cpus.exclusive":    "2",
		"kubepods/podA/c1/cpuset.cpus.exclusive": "2",
		"kubepods/podA/c1/cpuset.cpus.partition": "root",
	} {
		if got := read(filepath.Join(root, f)); got != want {
			t.Errorf("%s = %q, want %q", f, got, want)
		}
	}
	if _, err := Partition("kubepods/podB/c2"); err == nil {
		t.Error("a cgroup without CPUs was partitioned")
	}
}

func TestRelease(t *testing.T) {
	root := fakeCgroup(t, map[string]string{
		"kubepods/cpuset.cpus.exclusive":      "2-3",
		"kubepods/podA/cpuset.cpus.exclusive": "3",
		"kubepods/podB/cpuset.cpus":           "",
	})
	if err := Release("kubepods"); err != nil {
		t.Fatal(err)
	}
	if got := read(filepath.Join(root, "kubepods/cpuset.cpus.exclusive")); got != "3" {
		t.Errorf("exclusive %q, want 3", got)
	}
}

func TestFirstProcess(t *testing.T) {
	fakeCgroup(t, map[string]string{"pod/c/cgroup.procs": "120\n7\n130\n"})
	proc := t.TempDir()
	ProcRoot = proc
	t.Cleanup(func() { ProcRoot = "/proc" })
	for pid, stat := range map[string]string{
		"7":   "7 (sh) S 120 7 7",
		"120": "120 (my (odd) loop) S 99 120 120",
		"130": "130 (worker) S 120 120 120",
	} {
		os.MkdirAll(filepath.Join(proc, pid), 0o755)
		os.WriteFile(filepath.Join(proc, pid, "stat"), []byte(stat), 0o644)
	}
	if pid, err := FirstProcess("pod/c"); err != nil || pid != 120 {
		t.Errorf("%d %v, want 120", pid, err)
	}
}
