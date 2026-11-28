package nodeapi

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/upgrade"
)

func readyNode(name string, ready corev1.ConditionStatus) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready, Message: "network plugin not ready"}}}}
}

func pod(name, nodeName string, ready corev1.ConditionStatus) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Labels: map[string]string{"app": "web"}},
		Spec:   corev1.PodSpec{NodeName: nodeName},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}}}
}

func TestWorkloadHealthRequiresNodeReadyAndPods(t *testing.T) {
	ctx := context.Background()
	checks := []upgrade.HealthCheck{{Pods: &upgrade.PodsCheck{Namespace: "shop", Selector: "app=web"}}}
	for _, tt := range []struct {
		name    string
		objects []any
		want    string // empty: healthy
	}{
		{"healthy", []any{readyNode("edge", corev1.ConditionTrue), pod("web-1", "edge", corev1.ConditionTrue), pod("web-2", "other", corev1.ConditionFalse)}, ""},
		{"node not Ready", []any{readyNode("edge", corev1.ConditionFalse), pod("web-1", "edge", corev1.ConditionTrue)}, "not Ready"},
		{"node missing", []any{pod("web-1", "edge", corev1.ConditionTrue)}, "node edge"},
		{"pod not Ready", []any{readyNode("edge", corev1.ConditionTrue), pod("web-1", "edge", corev1.ConditionTrue), pod("web-3", "edge", corev1.ConditionFalse)}, "1 of 2 on this node Ready"},
		{"no pod on this node", []any{readyNode("edge", corev1.ConditionTrue), pod("web-2", "other", corev1.ConditionTrue)}, "0 of 0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientset()
			for _, o := range tt.objects {
				var err error
				switch o := o.(type) {
				case *corev1.Node:
					_, err = client.CoreV1().Nodes().Create(ctx, o, metav1.CreateOptions{})
				case *corev1.Pod:
					_, err = client.CoreV1().Pods(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			err := workloadHealth(ctx, client, "edge", checks)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want %q", err, tt.want)
			}
		})
	}
	if err := workloadHealth(ctx, nil, "edge", checks); err == nil {
		t.Fatal("healthy without cluster credentials")
	}
}

func TestUpgradeHealthChecksConversion(t *testing.T) {
	if upgradeHealthChecks(node.UpgradeSpec{}) != nil {
		t.Fatal("legacy upgrade got checks")
	}
	got := upgradeHealthChecks(node.UpgradeSpec{HealthChecks: []node.UpgradeHealthCheck{
		{URL: "http://10.0.0.1:30080/", StatusCode: 204, BodySHA256: strings.Repeat("ab", 32)},
		{Pods: &node.UpgradePodsCheck{Namespace: "shop", Selector: "app=web", MinReady: 2}},
	}})
	if len(got) != 2 || got[0].StatusCode != 204 || got[0].BodySHA256 == "" || got[1].Pods == nil || got[1].Pods.MinReady != 2 || got[1].URL != "" {
		t.Fatalf("%+v", got)
	}
}
