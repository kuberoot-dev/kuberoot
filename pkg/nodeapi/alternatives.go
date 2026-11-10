package nodeapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/alternatives"
	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
)

// syncAlternativeLabels shows on the node's Node which build of each service
// with alternatives it runs, as alternative.kuberoot.dev/<service>=<choice>,
// so the cluster sees the mix its nodes chose. kinit makes the choice; the
// labels follow it, a switch included, and labels of services that no longer
// have alternatives go.
func syncAlternativeLabels(ctx context.Context, kinit *supervisor.Client, kubeconfig, self string) {
	var client kubernetes.Interface
	for ctx.Err() == nil {
		if client == nil {
			client = clientFrom(kubeconfig)
		}
		if client != nil {
			if err := applyAlternativeLabels(ctx, kinit, client, self); err != nil {
				klog.Errorf("alternative labels: %v", err)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

func applyAlternativeLabels(ctx context.Context, kinit *supervisor.Client, client kubernetes.Interface, self string) error {
	statuses, err := kinit.Services(ctx)
	if err != nil {
		return err
	}
	n, err := client.CoreV1().Nodes().Get(ctx, self, metav1.GetOptions{})
	if err != nil {
		return err // not registered yet, most likely
	}
	patch := alternativeLabelsPatch(n.Labels, statuses)
	if patch == nil {
		return nil
	}
	_, err = client.CoreV1().Nodes().Patch(ctx, self, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// alternativeLabelsPatch is the merge patch that brings a node's labels in
// line with its services' alternatives, or nil when they already are.
func alternativeLabelsPatch(current map[string]string, statuses []supervisor.ServiceStatus) []byte {
	want := map[string]string{}
	for _, st := range statuses {
		if st.Alternative != "" {
			want[alternatives.LabelPrefix+st.Name] = st.Alternative
		}
	}
	labels := map[string]any{}
	for k, v := range want {
		if current[k] != v {
			labels[k] = v
		}
	}
	for k := range current {
		if _, ok := want[k]; !ok && strings.HasPrefix(k, alternatives.LabelPrefix) {
			labels[k] = nil
		}
	}
	if len(labels) == 0 {
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"metadata": map[string]any{"labels": labels}})
	return raw
}
