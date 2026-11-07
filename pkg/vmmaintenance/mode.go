package vmmaintenance

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	ModeAlpha1 = "v1alpha1"
	ModeAlpha2 = "v1alpha2"
)

// APIMode selects a controller implementation at process startup. It never
// converts API objects or relaxes the namespaced controller's scope check.
func APIMode(ctx context.Context, client dynamic.Interface) (string, error) {
	crds := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	u, err := client.Resource(crds).Get(ctx, "virtualmachines.vm.kuberoot.dev", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	scope, _, _ := unstructured.NestedString(u.Object, "spec", "scope")
	versions, _, _ := unstructured.NestedSlice(u.Object, "spec", "versions")
	if len(versions) != 1 {
		return "", fmt.Errorf("VM CRD must have exactly one supported served/storage version")
	}
	v, ok := versions[0].(map[string]any)
	if !ok || v["served"] != true || v["storage"] != true {
		return "", fmt.Errorf("VM CRD has no unambiguous served/storage version")
	}
	if scope == "Cluster" && v["name"] == ModeAlpha1 {
		return ModeAlpha1, nil
	}
	if scope == "Namespaced" && v["name"] == ModeAlpha2 {
		return ModeAlpha2, nil
	}
	return "", fmt.Errorf("unsupported VM API scope/version; no controller may reconcile it")
}

// WaitAPIMode allows first boot or the explicit CRD replacement to finish.
// The process does no reconciliation and publishes no readiness ACK while
// the API is absent, unreadable or ambiguous.
func WaitAPIMode(ctx context.Context, client dynamic.Interface) (string, error) {
	for {
		mode, err := APIMode(ctx, client)
		if err == nil {
			return mode, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wait for VM API: %w: %v", ctx.Err(), err)
		case <-time.After(time.Second):
		}
	}
}

func LegacyAPIReady(ctx context.Context, client dynamic.Interface) error {
	mode, err := APIMode(ctx, client)
	if err != nil {
		return err
	}
	if mode != ModeAlpha1 {
		return fmt.Errorf("legacy controller requires cluster-scoped v1alpha1; restart its NodeService after verified import")
	}
	return nil
}
