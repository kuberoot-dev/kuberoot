package devices

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

var (
	servicesGVR       = schema.GroupVersionResource{Version: "v1", Resource: "services"}
	endpointSlicesGVR = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
)

// MetricsService names the Service through which the cluster scrapes the
// node's metrics.
const MetricsService = "kuberoot-devices"

// Advertise keeps a Service in kube-system, with no selector, and its
// EndpointSlice on the node's address: kuberoot-devices runs as a process
// of the node, not as a pod, and this is how a service scrape finds it.
func Advertise(ctx context.Context, client dynamic.Interface, address string, port int) {
	for ctx.Err() == nil {
		err := advertise(ctx, client, address, port)
		if err == nil {
			return
		}
		klog.Errorf("advertise the metrics: %v", err)
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

func advertise(ctx context.Context, client dynamic.Interface, address string, port int) error {
	labels := map[string]any{"app.kubernetes.io/name": MetricsService}
	svc := map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": MetricsService, "namespace": "kube-system", "labels": labels},
		"spec": map[string]any{
			"ports": []any{map[string]any{"name": "metrics", "port": int64(port), "protocol": "TCP"}},
		},
	}
	slice := map[string]any{
		"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice",
		"metadata": map[string]any{"name": MetricsService, "namespace": "kube-system",
			"labels": map[string]any{"kubernetes.io/service-name": MetricsService, "app.kubernetes.io/name": MetricsService}},
		"addressType": "IPv4",
		"endpoints":   []any{map[string]any{"addresses": []any{address}, "conditions": map[string]any{"ready": true}}},
		"ports":       []any{map[string]any{"name": "metrics", "port": int64(port), "protocol": "TCP"}},
	}
	for _, o := range []struct {
		gvr schema.GroupVersionResource
		obj map[string]any
	}{{servicesGVR, svc}, {endpointSlicesGVR, slice}} {
		if err := apply(ctx, client.Resource(o.gvr).Namespace("kube-system"), o.obj); err != nil {
			return fmt.Errorf("%s: %w", o.gvr.Resource, err)
		}
	}
	return nil
}

// apply creates an object, or updates it in place keeping what the API
// server fills in (a Service's cluster IP).
func apply(ctx context.Context, r dynamic.ResourceInterface, obj map[string]any) error {
	u := &unstructured.Unstructured{Object: obj}
	cur, err := r.Get(ctx, u.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = r.Create(ctx, u, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	for k, v := range obj {
		switch k {
		case "metadata":
			cur.SetLabels(u.GetLabels())
		case "spec":
			ports, _, _ := unstructured.NestedSlice(obj, "spec", "ports")
			if err := unstructured.SetNestedSlice(cur.Object, ports, "spec", "ports"); err != nil {
				return err
			}
		default:
			cur.Object[k] = v
		}
	}
	_, err = r.Update(ctx, cur, metav1.UpdateOptions{})
	return err
}
