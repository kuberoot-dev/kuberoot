package rt

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// DeadlineAnnotationPrefix starts the annotation that asks for SCHED_DEADLINE:
// deadline.rt.kuberoot.dev/<container>: "runtime=200us,deadline=1ms,period=1ms".
// The node runs the container's first process with that reservation: up to
// runtime of CPU time in every period, done by deadline after its start.
const DeadlineAnnotationPrefix = "deadline.rt.kuberoot.dev/"

// DeadlineCondition is the pod condition the node reports the reservation
// in. Listed in the pod's readinessGates, it keeps the pod unready until
// its control loop runs with the reservation it asked for.
const DeadlineCondition = "rt.kuberoot.dev/deadline"

// The kernel's bounds on a reservation: runtime of at least 1024 ns, and a
// period of at most sched_deadline_period_max_us (4 s by default).
const (
	MinRuntime = 1024 * time.Nanosecond
	MaxPeriod  = 4 * time.Second
)

// Deadline is a SCHED_DEADLINE reservation.
type Deadline struct {
	Runtime, Deadline, Period time.Duration
}

func (d Deadline) String() string {
	return fmt.Sprintf("runtime=%s,deadline=%s,period=%s", d.Runtime, d.Deadline, d.Period)
}

// Bandwidth is the share of one CPU the reservation takes.
func (d Deadline) Bandwidth() float64 { return float64(d.Runtime) / float64(d.Period) }

// ParseDeadline reads "runtime=200us,deadline=1ms,period=1ms"; deadline and
// period may be left out, period then equals deadline and deadline period.
func ParseDeadline(s string) (Deadline, error) {
	var d Deadline
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return d, fmt.Errorf("%q: want key=duration", part)
		}
		dur, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			return d, fmt.Errorf("%s: %w", k, err)
		}
		switch strings.TrimSpace(k) {
		case "runtime":
			d.Runtime = dur
		case "deadline":
			d.Deadline = dur
		case "period":
			d.Period = dur
		default:
			return d, fmt.Errorf("%q: only runtime, deadline and period", k)
		}
	}
	switch {
	case d.Deadline == 0:
		d.Deadline = d.Period
	case d.Period == 0:
		d.Period = d.Deadline
	}
	return d, d.Validate()
}

// Validate holds a reservation to what the kernel accepts: runtime <=
// deadline <= period, runtime of at least 1024 ns, period of at most 4 s.
func (d Deadline) Validate() error {
	switch {
	case d.Runtime < MinRuntime:
		return fmt.Errorf("runtime %s: at least %s", d.Runtime, MinRuntime)
	case d.Deadline == 0:
		return fmt.Errorf("deadline or period is needed")
	case d.Runtime > d.Deadline:
		return fmt.Errorf("runtime %s is longer than deadline %s", d.Runtime, d.Deadline)
	case d.Deadline > d.Period:
		return fmt.Errorf("deadline %s is longer than period %s", d.Deadline, d.Period)
	case d.Period > MaxPeriod:
		return fmt.Errorf("period %s: at most %s", d.Period, MaxPeriod)
	}
	return nil
}

// PodDeadlines are the reservations a pod asks for, by container. A
// container may have one only if it gets CPUs of its own: the pod is of the
// Guaranteed class and the container asks for whole CPUs.
func PodDeadlines(pod *corev1.Pod) (map[string]Deadline, error) {
	out := map[string]Deadline{}
	guaranteed := pod.Status.QOSClass == "" || pod.Status.QOSClass == corev1.PodQOSGuaranteed
	for k, v := range pod.Annotations {
		name, ok := strings.CutPrefix(k, DeadlineAnnotationPrefix)
		if !ok {
			continue
		}
		d, err := ParseDeadline(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		c := container(pod, name)
		if c == nil {
			return nil, fmt.Errorf("%s: the pod has no container %q", k, name)
		}
		if !guaranteed {
			return nil, fmt.Errorf("%s: the pod is of the %s class, not Guaranteed: its containers share CPUs", k, pod.Status.QOSClass)
		}
		if err := exclusiveCPUs(c); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[name] = d
	}
	return out, nil
}

func container(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

// exclusiveCPUs: whole CPUs, requests equal to limits for CPU and memory,
// which the static CPU manager answers with CPUs for the container alone.
func exclusiveCPUs(c *corev1.Container) error {
	cpu, lim := c.Resources.Requests[corev1.ResourceCPU], c.Resources.Limits[corev1.ResourceCPU]
	if lim.IsZero() || (!cpu.IsZero() && cpu.Cmp(lim) != 0) || lim.MilliValue()%1000 != 0 {
		return fmt.Errorf("container %q needs whole CPUs of its own: a CPU limit of whole CPUs, equal to its request", c.Name)
	}
	mem, mlim := c.Resources.Requests[corev1.ResourceMemory], c.Resources.Limits[corev1.ResourceMemory]
	if mlim.IsZero() || (!mem.IsZero() && mem.Cmp(mlim) != 0) {
		return fmt.Errorf("container %q needs a memory limit equal to its request (the Guaranteed class)", c.Name)
	}
	return nil
}
