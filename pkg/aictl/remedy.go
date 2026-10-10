package aictl

import (
	"context"
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/ai/v1alpha1"
)

var (
	remediesGVR  = v1.GroupVersion.WithResource("remedies")
	agentsGVR    = v1.GroupVersion.WithResource("agents")
	servicesGVR  = schema.GroupVersionResource{Group: "node.kuberoot.dev", Version: "v1alpha1", Resource: "nodeservices"}
	osconfigsGVR = schema.GroupVersionResource{Group: "node.kuberoot.dev", Version: "v1alpha1", Resource: "osconfigs"}
)

// RemedyExpiry: a remedy no one approved in this time is dropped; the
// problem, if it lasts, gets a new one.
var RemedyExpiry = time.Hour

// Decide is what becomes of a proposed remedy now: its phase, who approved
// it, and why it waits or was refused. Running is the go-ahead.
func Decide(r v1.Remedy, policy *v1.Agent, ranLastHour int, now time.Time) (phase, approvedBy, msg string) {
	if r.Spec.Action == v1.ActionEscalate {
		return "Escalated", "", "for a person to look at"
	}
	switch {
	case r.Spec.Approved:
		approvedBy = "person"
	case policy != nil && slices.Contains(policy.Spec.AutoApprove, r.Spec.Action):
		approvedBy = "policy"
	}
	if approvedBy == "" {
		if now.Sub(r.CreationTimestamp.Time) > RemedyExpiry {
			return "Expired", "", "not approved in " + RemedyExpiry.String()
		}
		return "Proposed", "", fmt.Sprintf("waiting for approval: kubectl patch remedy %s --type merge -p '{\"spec\":{\"approved\":true}}'", r.Name)
	}
	limit := int32(4)
	if policy != nil {
		limit = policy.Spec.MaxPerHour
	}
	if ranLastHour >= int(limit) {
		if now.Sub(r.CreationTimestamp.Time) > RemedyExpiry {
			return "Expired", approvedBy, fmt.Sprintf("held back by the limit of %d actions an hour", limit)
		}
		return "Proposed", approvedBy, fmt.Sprintf("approved, held back: %d actions ran in the last hour, the limit", ranLastHour)
	}
	return "Running", approvedBy, ""
}

// remedies runs approved remedies, one per pass, each after checks of its
// own that the agent's reasoning does not enter into.
func (c *Controller) remedies(ctx context.Context, nodes []Node) {
	list, err := c.Dynamic.Resource(remediesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	var policy *v1.Agent
	if u, err := c.Dynamic.Resource(agentsGVR).Get(ctx, "default", metav1.GetOptions{}); err == nil {
		policy = &v1.Agent{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, policy); err != nil {
			policy = nil
		}
	}
	var items []v1.Remedy
	ran := 0
	for _, u := range list.Items {
		var r v1.Remedy
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &r); err != nil {
			continue
		}
		items = append(items, r)
		if r.Status.DoneAt != nil && time.Since(r.Status.DoneAt.Time) < time.Hour && r.Status.ApprovedBy != "" {
			ran++
		}
	}
	slices.SortFunc(items, func(a, b v1.Remedy) int { return a.CreationTimestamp.Compare(b.CreationTimestamp.Time) })
	acted := false
	for i := range items {
		r := &items[i]
		if r.Status.Phase != "" && r.Status.Phase != "Proposed" {
			continue
		}
		phase, by, msg := Decide(*r, policy, ran, time.Now())
		if phase == "Running" {
			if acted {
				continue // one action a pass
			}
			acted = true
			ran++
			now := metav1.Now()
			if err := c.check(ctx, r, nodes); err != nil {
				phase, msg = "Refused", err.Error()
			} else if err := c.act(ctx, r); err != nil {
				phase, msg = "Failed", err.Error()
			} else {
				phase, msg = "Done", "done"
			}
			klog.Infof("remedy %s: %s %s %s approved by %s: %s", r.Name, r.Spec.Action, r.Spec.Node, r.Spec.Target, by, msg)
			c.remedyStatus(ctx, r, v1.RemedyStatus{Phase: phase, Message: msg, ApprovedBy: by, DoneAt: &now})
			continue
		}
		c.remedyStatus(ctx, r, v1.RemedyStatus{Phase: phase, Message: msg, ApprovedBy: by})
	}
}

// check refuses what no remedy may do, whoever approved it.
func (c *Controller) check(ctx context.Context, r *v1.Remedy, nodes []Node) error {
	byName := map[string]Node{}
	down := 0
	for _, n := range nodes {
		byName[n.Name] = n
		if !n.Ready {
			down++
		}
	}
	switch r.Spec.Action {
	case v1.ActionRestartService:
		if _, err := c.Dynamic.Resource(servicesGVR).Get(ctx, r.Spec.Node+"."+r.Spec.Target, metav1.GetOptions{}); err != nil {
			return fmt.Errorf("no service %s on node %s", r.Spec.Target, r.Spec.Node)
		}
	case v1.ActionRestartModelServer:
		if _, err := c.Dynamic.Resource(serversGVR).Get(ctx, r.Spec.Node+"."+r.Spec.Target, metav1.GetOptions{}); err != nil {
			return fmt.Errorf("model %s has no server on node %s", r.Spec.Target, r.Spec.Node)
		}
	case v1.ActionRollbackModel:
		m, err := c.Dynamic.Resource(modelsGVR).Get(ctx, r.Spec.Target, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("no model %s", r.Spec.Target)
		}
		if prev, _, _ := unstructured.NestedString(m.Object, "status", "previous", "sha256"); prev == "" {
			return fmt.Errorf("model %s has no version before the current one", r.Spec.Target)
		}
	case v1.ActionRebootNode:
		if _, ok := byName[r.Spec.Node]; !ok {
			return fmt.Errorf("no node %s", r.Spec.Node)
		}
		// The control plane runs the agent, the endpoint and the cluster's
		// API: a remedy does not take them down.
		if _, err := c.Dynamic.Resource(servicesGVR).Get(ctx, r.Spec.Node+".kube-apiserver", metav1.GetOptions{}); err == nil {
			return fmt.Errorf("node %s runs the control plane", r.Spec.Node)
		}
		if down > 0 {
			return fmt.Errorf("%d nodes are down already; no other goes down too", down)
		}
	default:
		return fmt.Errorf("no such action: %s", r.Spec.Action)
	}
	return nil
}

// act carries a remedy out through the node APIs and the models.
func (c *Controller) act(ctx context.Context, r *v1.Remedy) error {
	now := time.Now().UTC().Format(time.RFC3339)
	switch r.Spec.Action {
	case v1.ActionRestartService:
		patch := fmt.Sprintf(`{"spec":{"restartedAt":%q}}`, now)
		_, err := c.Dynamic.Resource(servicesGVR).Patch(ctx, r.Spec.Node+"."+r.Spec.Target, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
		return err
	case v1.ActionRestartModelServer:
		// The model's controller starts it again on its next pass.
		err := c.Dynamic.Resource(serversGVR).Delete(ctx, r.Spec.Node+"."+r.Spec.Target, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	case v1.ActionRollbackModel:
		m, err := c.Dynamic.Resource(modelsGVR).Get(ctx, r.Spec.Target, metav1.GetOptions{})
		if err != nil {
			return err
		}
		prev, _, _ := unstructured.NestedMap(m.Object, "status", "previous")
		if err := unstructured.SetNestedMap(m.Object, prev, "spec", "source"); err != nil {
			return err
		}
		_, err = c.Dynamic.Resource(modelsGVR).Update(ctx, m, metav1.UpdateOptions{})
		return err
	case v1.ActionRebootNode:
		patch := fmt.Sprintf(`{"spec":{"rebootRequestedAt":%q}}`, now)
		_, err := c.Dynamic.Resource(osconfigsGVR).Patch(ctx, r.Spec.Node, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
		return err
	}
	return fmt.Errorf("no such action: %s", r.Spec.Action)
}

func (c *Controller) remedyStatus(ctx context.Context, r *v1.Remedy, st v1.RemedyStatus) {
	if r.Status.Phase == st.Phase && r.Status.Message == st.Message && r.Status.ApprovedBy == st.ApprovedBy {
		return
	}
	u, err := c.Dynamic.Resource(remediesGVR).Get(ctx, r.Name, metav1.GetOptions{})
	if err != nil {
		return
	}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&st)
	if err != nil {
		return
	}
	u.Object["status"] = raw
	if _, err := c.Dynamic.Resource(remediesGVR).UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("status of remedy %s: %v", r.Name, err)
	}
}
