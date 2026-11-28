package nodeapi

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/upgrade"
)

func upgradeHealthChecks(spec node.UpgradeSpec) []upgrade.HealthCheck {
	if len(spec.HealthChecks) == 0 {
		return nil
	}
	checks := make([]upgrade.HealthCheck, len(spec.HealthChecks))
	for i, c := range spec.HealthChecks {
		checks[i] = upgrade.HealthCheck{URL: c.URL, StatusCode: c.StatusCode, BodySHA256: c.BodySHA256}
		if c.Pods != nil {
			checks[i].Pods = &upgrade.PodsCheck{Namespace: c.Pods.Namespace, Selector: c.Pods.Selector, MinReady: c.Pods.MinReady}
		}
	}
	return checks
}

// probationClient talks to the cluster as this node does: a control plane
// with its admin credentials, a member with the kubelet's.
func probationClient(controlPlane bool) kubernetes.Interface {
	if controlPlane {
		return clientFrom("/etc/kubernetes/admin.kubeconfig")
	}
	return clientFrom("/var/lib/kubelet/kubeconfig")
}

// workloadHealth is the gate a slot's own health checks add to probation:
// the node is Ready in the cluster, and every check passes.
func workloadHealth(ctx context.Context, client kubernetes.Interface, nodeName string, checks []upgrade.HealthCheck) error {
	if client == nil {
		return fmt.Errorf("no cluster credentials yet")
	}
	if err := nodeReady(ctx, client, nodeName); err != nil {
		return err
	}
	return upgrade.CheckHealth(ctx, checks, podReadiness(client, nodeName))
}

func nodeReady(ctx context.Context, client kubernetes.Interface, nodeName string) error {
	n, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("node %s: %w", nodeName, err)
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status == corev1.ConditionTrue {
				return nil
			}
			return fmt.Errorf("node %s is not Ready: %s", nodeName, c.Message)
		}
	}
	return fmt.Errorf("node %s reports no Ready condition", nodeName)
}

// podReadiness counts only pods scheduled on this node: this node's release
// is on trial, not the pods that run elsewhere.
func podReadiness(client kubernetes.Interface, nodeName string) upgrade.PodReadiness {
	return func(ctx context.Context, namespace, selector string) (int, int, error) {
		list, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
			FieldSelector: fields.OneTermEqualSelector("spec.nodeName", nodeName).String(),
		})
		if err != nil {
			return 0, 0, err
		}
		ready, total := 0, 0
		for _, p := range list.Items {
			if p.Spec.NodeName != nodeName || p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				continue
			}
			total++
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					ready++
				}
			}
		}
		return ready, total, nil
	}
}
