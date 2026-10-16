package nodeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/rt"
)

// kubepodsCgroup is the kubelet's cgroup for pods (cgroupfs driver);
// Guaranteed pods sit right under it.
const kubepodsCgroup = "kubepods"

// syncDeadlines runs the containers of this node's pods that ask for it
// (deadline.rt.kuberoot.dev/<container>) under SCHED_DEADLINE: each such
// container's CPUs become a cpuset partition, a scheduling domain of their
// own, and its first process gets the reservation. The outcome is the pod's
// condition rt.kuberoot.dev/deadline, and an event when it is refused.
func syncDeadlines(ctx context.Context, kubeconfig, self string) {
	var client kubernetes.Interface
	for ctx.Err() == nil {
		if client == nil {
			client = clientFrom(kubeconfig)
		}
		if client != nil {
			pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + self})
			if err != nil {
				klog.Errorf("deadline pods: %v", err)
			} else {
				for i := range pods.Items {
					if hasDeadline(&pods.Items[i]) {
						syncPodDeadline(ctx, client, &pods.Items[i])
					}
				}
			}
			// CPUs of pods that are gone go back to the shared pool.
			if err := rt.Release(kubepodsCgroup); err != nil && !os.IsNotExist(err) {
				klog.Errorf("deadline pods: %v", err)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func hasDeadline(pod *corev1.Pod) bool {
	for k := range pod.Annotations {
		if strings.HasPrefix(k, rt.DeadlineAnnotationPrefix) {
			return true
		}
	}
	return false
}

func syncPodDeadline(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) {
	if pod.Status.Phase != corev1.PodRunning {
		return
	}
	want, err := rt.PodDeadlines(pod)
	if err != nil {
		reportDeadline(ctx, client, pod, false, "Invalid", err.Error())
		return
	}
	var applied []string
	for _, cs := range pod.Status.ContainerStatuses {
		d, ok := want[cs.Name]
		if !ok {
			continue
		}
		if cs.State.Running == nil || cs.ContainerID == "" {
			reportDeadline(ctx, client, pod, false, "ContainerNotRunning", fmt.Sprintf("container %s is not running", cs.Name))
			return
		}
		_, id, _ := strings.Cut(cs.ContainerID, "://")
		cgroup := path.Join(kubepodsCgroup, "pod"+string(pod.UID), id)
		msg, err := applyDeadline(cgroup, d)
		if err != nil {
			reportDeadline(ctx, client, pod, false, "Refused", fmt.Sprintf("container %s: %v", cs.Name, err))
			return
		}
		applied = append(applied, fmt.Sprintf("container %s: %s", cs.Name, msg))
	}
	if len(applied) < len(want) {
		return // the containers' statuses are not all in yet
	}
	reportDeadline(ctx, client, pod, true, "Running", strings.Join(applied, "; "))
}

// applyDeadline partitions the container's CPUs and gives its first process
// the reservation, unless it already runs with it.
func applyDeadline(cgroup string, d rt.Deadline) (string, error) {
	cpus, err := rt.Partition(cgroup)
	if err != nil {
		return "", err
	}
	pid, err := rt.FirstProcess(cgroup)
	if err != nil {
		return "", err
	}
	if cur, ok, err := rt.CurrentDeadline(pid); err == nil && ok && cur == d {
		return fmt.Sprintf("process %d runs %s on CPUs %s", pid, d, cpus), nil
	}
	if err := rt.SetDeadline(pid, d); err != nil {
		return "", fmt.Errorf("%w on CPUs %s: %s", err, cpus, deadlineCapacity())
	}
	return fmt.Sprintf("process %d runs %s on CPUs %s", pid, d, cpus), nil
}

// deadlineCapacity says how much deadline bandwidth the kernel admits, for
// refusals to explain themselves.
func deadlineCapacity() string {
	var runtime, period int64
	fmt.Sscan(strings.TrimSpace(readFile("/proc/sys/kernel/sched_rt_runtime_us")), &runtime)
	fmt.Sscan(strings.TrimSpace(readFile("/proc/sys/kernel/sched_rt_period_us")), &period)
	if runtime < 0 || period <= 0 {
		return "the kernel's admission control is off (kernel.sched_rt_runtime_us is -1)"
	}
	return fmt.Sprintf("the kernel admits deadline tasks up to %d%% of each CPU, the 5%% its fair server keeps for other work included",
		runtime*100/period)
}

func readFile(p string) string {
	raw, _ := os.ReadFile(p)
	return string(raw)
}

// reportDeadline sets the pod's condition when it changes, with an event
// for a refusal.
func reportDeadline(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, ok bool, reason, msg string) {
	status := corev1.ConditionFalse
	if ok {
		status = corev1.ConditionTrue
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == rt.DeadlineCondition && c.Status == status && c.Reason == reason && c.Message == msg {
			return
		}
	}
	cond := corev1.PodCondition{Type: rt.DeadlineCondition, Status: status, Reason: reason, Message: msg,
		LastTransitionTime: metav1.Now()}
	patch, _ := json.Marshal(map[string]any{"status": map[string]any{"conditions": []corev1.PodCondition{cond}}})
	if _, err := client.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.StrategicMergePatchType, patch,
		metav1.PatchOptions{}, "status"); err != nil {
		klog.Errorf("deadline %s/%s: %v", pod.Namespace, pod.Name, err)
	}
	if ok {
		klog.Infof("deadline %s/%s: %s", pod.Namespace, pod.Name, msg)
		return
	}
	klog.Warningf("deadline %s/%s: %s: %s", pod.Namespace, pod.Name, reason, msg)
	now := metav1.Now()
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{GenerateName: pod.Name + ".", Namespace: pod.Namespace},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", APIVersion: "v1", Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID},
		Reason:         "Deadline" + reason, Message: msg, Type: corev1.EventTypeWarning,
		Source:         corev1.EventSource{Component: "kuberoot-node", Host: pod.Spec.NodeName},
		FirstTimestamp: now, LastTimestamp: now, Count: 1,
	}
	if _, err := client.CoreV1().Events(pod.Namespace).Create(ctx, ev, metav1.CreateOptions{}); err != nil {
		klog.Errorf("deadline %s/%s: event: %v", pod.Namespace, pod.Name, err)
	}
}
