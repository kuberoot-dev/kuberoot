package aictl

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/ai/v1alpha1"
)

func remedy(action string, approved bool, age time.Duration, now time.Time) v1.Remedy {
	return v1.Remedy{ObjectMeta: metav1.ObjectMeta{Name: "r", CreationTimestamp: metav1.NewTime(now.Add(-age))},
		Spec: v1.RemedySpec{Action: action, Approved: approved}}
}

func TestDecideApproval(t *testing.T) {
	now := time.Now()
	policy := &v1.Agent{Spec: v1.AgentSpec{AutoApprove: []string{v1.ActionRestartModelServer}, MaxPerHour: 2}}
	cases := []struct {
		r          v1.Remedy
		ran        int
		phase, by  string
		msgContain string
	}{
		{remedy(v1.ActionRestartService, false, time.Minute, now), 0, "Proposed", "", "kubectl patch remedy r"},
		{remedy(v1.ActionRestartService, true, time.Minute, now), 0, "Running", "person", ""},
		{remedy(v1.ActionRestartModelServer, false, time.Minute, now), 0, "Running", "policy", ""},
		{remedy(v1.ActionRestartModelServer, false, time.Minute, now), 2, "Proposed", "policy", "held back"},
		{remedy(v1.ActionRebootNode, false, 2*time.Hour, now), 0, "Expired", "", "not approved"},
		{remedy(v1.ActionEscalate, false, time.Minute, now), 0, "Escalated", "", "person"},
	}
	for i, c := range cases {
		phase, by, msg := Decide(c.r, policy, c.ran, now)
		if phase != c.phase || by != c.by || !strings.Contains(msg, c.msgContain) {
			t.Errorf("case %d: got %s/%s/%q, want %s/%s/…%q…", i, phase, by, msg, c.phase, c.by, c.msgContain)
		}
	}
	// No Agent: nothing runs unattended.
	if phase, _, _ := Decide(remedy(v1.ActionRestartModelServer, false, time.Minute, now), nil, 0, now); phase != "Proposed" {
		t.Errorf("ran without a policy: %s", phase)
	}
}

func TestCheckRefusesRebootsThatHurt(t *testing.T) {
	scheme := runtime.NewScheme()
	cp := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "node.kuberoot.dev/v1alpha1", "kind": "NodeService",
		"metadata": map[string]any{"name": "cp.kube-apiserver"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		servicesGVR: "NodeServiceList", serversGVR: "ModelServerList", modelsGVR: "ModelList"}, cp)
	c := &Controller{Dynamic: dyn}
	nodes := []Node{{Name: "cp", Ready: true}, {Name: "w1", Ready: true}, {Name: "w2", Ready: true}}
	reboot := func(node string) *v1.Remedy {
		return &v1.Remedy{Spec: v1.RemedySpec{Action: v1.ActionRebootNode, Node: node}}
	}
	if err := c.check(context.Background(), reboot("cp"), nodes); err == nil || !strings.Contains(err.Error(), "control plane") {
		t.Fatalf("the control plane may be rebooted: %v", err)
	}
	if err := c.check(context.Background(), reboot("w1"), nodes); err != nil {
		t.Fatalf("a worker may not be rebooted: %v", err)
	}
	nodes[2].Ready = false
	if err := c.check(context.Background(), reboot("w1"), nodes); err == nil {
		t.Fatal("a second node may go down")
	}
	if err := c.check(context.Background(), reboot("nope"), nodes); err == nil {
		t.Fatal("a node that is not there may be rebooted")
	}
}
