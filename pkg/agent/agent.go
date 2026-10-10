package agent

import (
	"context"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/ai/v1alpha1"
)

var (
	agentsGVR   = schema.GroupVersionResource{Group: "ai.kuberoot.dev", Version: "v1alpha1", Resource: "agents"}
	remediesGVR = schema.GroupVersionResource{Group: "ai.kuberoot.dev", Version: "v1alpha1", Resource: "remedies"}
)

// SubjectAnnotation keeps on a Remedy the finding it answers.
const SubjectAnnotation = "ai.kuberoot.dev/subject"

// DefaultEndpoint is the cluster's own: the models it serves.
const DefaultEndpoint = "http://127.0.0.1:8000/v1"

// Agent looks at the cluster every Interval and proposes a remedy for each
// problem that has none open nor one from the last Quiet period.
type Agent struct {
	Dynamic   dynamic.Interface
	Kube      kubernetes.Interface
	Collector *Collector
	Interval  time.Duration
	Quiet     time.Duration
	// PerCheck bounds the problems put to the model on one check.
	PerCheck int
}

func (a *Agent) Run(ctx context.Context) error {
	for {
		a.check(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(a.Interval):
		}
	}
}

func (a *Agent) check(ctx context.Context) {
	u, err := a.Dynamic.Resource(agentsGVR).Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Errorf("agent: %v", err)
		}
		return
	}
	var cfg v1.Agent
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &cfg); err != nil {
		klog.Errorf("agent: %v", err)
		return
	}
	if cfg.Spec.Paused {
		a.status(ctx, u, 0, "paused")
		return
	}
	findings, err := a.Collector.Collect(ctx)
	if err != nil {
		a.status(ctx, u, 0, "looking: "+err.Error())
		return
	}
	open, err := a.answered(ctx)
	if err != nil {
		a.status(ctx, u, int32(len(findings)), "remedies: "+err.Error())
		return
	}
	llm := &LLM{Endpoint: cfg.Spec.Endpoint, Model: cfg.Spec.Model}
	if llm.Endpoint == "" {
		llm.Endpoint = DefaultEndpoint
	}
	if cfg.Spec.APIKeySecret {
		s, err := a.Kube.CoreV1().Secrets("kube-system").Get(ctx, "kuberoot-agent", metav1.GetOptions{})
		if err != nil {
			a.status(ctx, u, int32(len(findings)), "the endpoint's key: "+err.Error())
			return
		}
		llm.APIKey = strings.TrimSpace(string(s.Data["api-key"]))
	}
	asked, msg := 0, ""
	for _, f := range findings {
		if open[f.Subject] || asked >= a.PerCheck {
			continue
		}
		asked++
		d, err := llm.Decide(ctx, f)
		if err != nil {
			msg = "asking " + cfg.Spec.Model + " about " + f.Subject + ": " + err.Error()
			klog.Error(msg)
			continue
		}
		name, err := a.propose(ctx, f, d)
		if err != nil {
			msg = "proposing for " + f.Subject + ": " + err.Error()
			klog.Error(msg)
			continue
		}
		klog.Infof("proposed %s: %s for %s: %s", name, d.Action, f.Subject, d.Reason)
		msg = "proposed " + name + " for " + f.Subject
	}
	a.status(ctx, u, int32(len(findings)), msg)
}

// answered: subjects with a remedy still waiting, or one from the last
// Quiet period, which the agent leaves be.
func (a *Agent) answered(ctx context.Context) (map[string]bool, error) {
	list, err := a.Dynamic.Resource(remediesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range list.Items {
		phase, _, _ := unstructured.NestedString(r.Object, "status", "phase")
		if phase == "" || phase == "Proposed" || phase == "Running" || time.Since(r.GetCreationTimestamp().Time) < a.Quiet {
			out[r.GetAnnotations()[SubjectAnnotation]] = true
		}
	}
	return out, nil
}

// propose creates the Remedy: the action the model chose, on what the
// finding names.
func (a *Agent) propose(ctx context.Context, f Finding, d Decision) (string, error) {
	spec := v1.RemedySpec{Action: d.Action, Reason: d.Reason, Evidence: []string{truncate(f.Text, 4000)}}
	switch d.Action {
	case v1.ActionRestartService, v1.ActionRestartModelServer:
		spec.Node, spec.Target = f.Node, f.Target
	case v1.ActionRollbackModel:
		spec.Target = f.Target
	case v1.ActionRebootNode:
		spec.Node = f.Node
	default:
		spec.Node, spec.Target = f.Node, f.Target
	}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
	if err != nil {
		return "", err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.kuberoot.dev/v1alpha1", "kind": "Remedy",
		"metadata": map[string]any{"generateName": strings.ToLower(d.Action) + "-",
			"annotations": map[string]any{SubjectAnnotation: f.Subject}},
		"spec": raw,
	}}
	created, err := a.Dynamic.Resource(remediesGVR).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return created.GetName(), nil
}

func (a *Agent) status(ctx context.Context, u *unstructured.Unstructured, findings int32, msg string) {
	now := metav1.Now()
	st := v1.AgentStatus{LastCheck: &now, Findings: findings, Message: msg}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&st)
	if err != nil {
		return
	}
	u.Object["status"] = raw
	if _, err := a.Dynamic.Resource(agentsGVR).UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
		klog.V(2).Infof("agent status: %v", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
