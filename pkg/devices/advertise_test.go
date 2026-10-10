package devices

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestAdvertise(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		servicesGVR: "ServiceList", endpointSlicesGVR: "EndpointSliceList"})
	ctx := context.Background()
	if err := advertise(ctx, client, "10.0.0.5", 9790); err != nil {
		t.Fatal(err)
	}
	// The API server gives the Service its address; a second pass keeps it
	// and moves the endpoint to the node's new address.
	svc, _ := client.Resource(servicesGVR).Namespace("kube-system").Get(ctx, MetricsService, metav1.GetOptions{})
	_ = unstructured.SetNestedField(svc.Object, "10.96.0.40", "spec", "clusterIP")
	_, _ = client.Resource(servicesGVR).Namespace("kube-system").Update(ctx, svc, metav1.UpdateOptions{})
	if err := advertise(ctx, client, "10.0.0.6", 9790); err != nil {
		t.Fatal(err)
	}
	svc, _ = client.Resource(servicesGVR).Namespace("kube-system").Get(ctx, MetricsService, metav1.GetOptions{})
	if ip, _, _ := unstructured.NestedString(svc.Object, "spec", "clusterIP"); ip != "10.96.0.40" {
		t.Errorf("cluster IP lost: %q", ip)
	}
	slice, err := client.Resource(endpointSlicesGVR).Namespace("kube-system").Get(ctx, MetricsService, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	eps, _, _ := unstructured.NestedSlice(slice.Object, "endpoints")
	addrs, _, _ := unstructured.NestedStringSlice(eps[0].(map[string]any), "addresses")
	if len(addrs) != 1 || addrs[0] != "10.0.0.6" || slice.GetLabels()["kubernetes.io/service-name"] != MetricsService {
		t.Errorf("endpoint slice %v", slice.Object)
	}
}
