package capikuberoot

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

// The disposable server shares no state with the installed stage0/1 guests.
// Explicit context, loopback endpoint and path restrictions prevent accidental
// execution against a workload or management cluster.
func TestCAPIRealAdmissionContracts(t *testing.T) {
	path := os.Getenv("KUBEROOT_ADMISSION_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("requires the disposable stage2 admission server")
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: path}, &clientcmd.ConfigOverrides{CurrentContext: "stage2-disposable"}).ClientConfig()
	if err != nil || config.Host != "https://127.0.0.1:16443" || config.Insecure || !strings.Contains(path, "/stage2-admission/") {
		t.Fatalf("refusing non-disposable API configuration: %v", err)
	}
	config.Timeout = 15 * time.Second
	config.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	crds := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	files, err := filepath.Glob("../../config/capi/crd/*.yaml")
	if err != nil || len(files) != 3 {
		t.Fatalf("expected three generated CAPI CRDs, got %d: %v", len(files), err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		object := &unstructured.Unstructured{}
		if err = yaml.Unmarshal(raw, &object.Object); err != nil {
			t.Fatal(err)
		}
		if object.GetLabels()["cluster.x-k8s.io/v1beta2"] != "v1alpha1" {
			t.Fatalf("missing CAPI contract label in %s", file)
		}
		created, err := dyn.Resource(crds).Create(ctx, object, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("real CRD CREATE %s: %v", object.GetName(), err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			uid := created.GetUID()
			if err := dyn.Resource(crds).Delete(cleanup, created.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("cleanup own CRD %s: %v", created.GetName(), err)
			}
		})
		for {
			current, err := dyn.Resource(crds).Get(ctx, created.GetName(), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			conditions, _, _ := unstructured.NestedSlice(current.Object, "status", "conditions")
			established := false
			for _, raw := range conditions {
				condition, _ := raw.(map[string]any)
				established = established || (condition["type"] == "Established" && condition["status"] == "True")
			}
			if established {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("CRD establishment timed out")
			case <-time.After(50 * time.Millisecond):
			}
		}
		t.Log("CRD_CREATE_ESTABLISHED", created.GetName())
	}
	ns, err := kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "capi-admission-"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		uid := ns.UID
		if err := kube.CoreV1().Namespaces().Delete(cleanup, ns.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup own namespace: %v", err)
		}
	})
	newObject := func(kind, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": InfraMachines.GroupVersion().String(), "kind": kind, "metadata": map[string]any{"name": name, "namespace": ns.Name}, "spec": spec}}
	}
	create := func(gvr schema.GroupVersionResource, object *unstructured.Unstructured) *unstructured.Unstructured {
		t.Helper()
		created, err := dyn.Resource(gvr).Namespace(ns.Name).Create(ctx, object, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("create %s: %v", object.GetKind(), err)
		}
		return created
	}
	invalid := func(name, message string, call func() error) {
		t.Helper()
		err := call()
		if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), message) {
			t.Fatalf("%s did not hit the expected schema rejection %q: %v", name, message, err)
		}
		t.Log("SCHEMA_REJECTED", name)
	}
	create(InfraClusters, newObject("KuberootCluster", "cluster", map[string]any{"externalManagedControlPlane": true}))
	invalid("external control plane false", "externalManagedControlPlane", func() error {
		_, err := dyn.Resource(InfraClusters).Namespace(ns.Name).Create(ctx, newObject("KuberootCluster", "invalid-cluster", map[string]any{"externalManagedControlPlane": false}), metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		return err
	})
	spec := map[string]any{"cpus": int64(1), "maxCPUs": int64(4), "memory": "512Mi", "maxMemory": "1Gi", "replicas": int64(1), "disk": map[string]any{"image": "https://example.invalid/talos-nocloud.raw", "size": "2Gi"}}
	machine := create(InfraMachines, newObject("KuberootMachine", "worker", spec))
	for _, scenario := range []struct {
		field, message string
		value          any
	}{
		{"cpus", "cpus exceeds maxCPUs", int64(5)},
		{"memory", "memory exceeds maxMemory", "2Gi"},
		{"memory", "memory must be positive", "0"},
		{"memory", "multiples of 128Mi", "130Mi"},
	} {
		copy := machine.DeepCopy()
		_ = unstructured.SetNestedField(copy.Object, scenario.value, "spec", scenario.field)
		invalid(fmt.Sprintf("invalid resource %s=%v", scenario.field, scenario.value), scenario.message, func() error {
			_, err := dyn.Resource(InfraMachines).Namespace(ns.Name).Update(ctx, copy, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		})
	}
	_ = unstructured.SetNestedField(machine.Object, "kuberoot://worker", "spec", "providerID")
	machine, err = dyn.Resource(InfraMachines).Namespace(ns.Name).Update(ctx, machine, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, remove := range []bool{false, true} {
		copy := machine.DeepCopy()
		if remove {
			unstructured.RemoveNestedField(copy.Object, "spec", "providerID")
		} else {
			_ = unstructured.SetNestedField(copy.Object, "kuberoot://other", "spec", "providerID")
		}
		invalid(fmt.Sprintf("providerID removal=%v", remove), "providerID", func() error {
			_, err := dyn.Resource(InfraMachines).Namespace(ns.Name).Update(ctx, copy, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		})
	}
	templates := InfraMachines.GroupVersion().WithResource("kuberootmachinetemplates")
	template := create(templates, newObject("KuberootMachineTemplate", "worker-contents-hash", map[string]any{"template": map[string]any{"spec": spec}}))
	copy := template.DeepCopy()
	_ = unstructured.SetNestedField(copy.Object, int64(2), "spec", "template", "spec", "cpus")
	invalid("template mutation", "machine template spec is immutable", func() error {
		_, err := dyn.Resource(templates).Namespace(ns.Name).Update(ctx, copy, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
		return err
	})
	machine.Object["status"] = map[string]any{"ready": false,
		"vmRef":   map[string]any{"apiVersion": "vm.kuberoot.dev/v1alpha2", "kind": "VirtualMachine", "name": "worker", "namespace": ns.Name, "uid": "vm-initial"},
		"nodeRef": map[string]any{"apiVersion": "v1", "kind": "Node", "name": "worker", "uid": "node-initial"}}
	machine, err = dyn.Resource(InfraMachines).Namespace(ns.Name).UpdateStatus(ctx, machine, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"vmRef", "nodeRef"} {
		for _, remove := range []bool{false, true} {
			copy := machine.DeepCopy()
			if remove {
				unstructured.RemoveNestedField(copy.Object, "status", field)
			} else {
				_ = unstructured.SetNestedField(copy.Object, "replacement-uid", "status", field, "uid")
			}
			invalid(fmt.Sprintf("%s removal=%v", field, remove), field+" cannot be removed or changed", func() error {
				_, err := dyn.Resource(InfraMachines).Namespace(ns.Name).UpdateStatus(ctx, copy, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
				return err
			})
		}
	}
	for _, null := range []bool{false, true} {
		copy = machine.DeepCopy()
		if null {
			copy.Object["status"] = nil
		} else {
			unstructured.RemoveNestedField(copy.Object, "status")
		}
		invalid(fmt.Sprintf("whole status removal null=%v", null), "cannot be removed or changed", func() error {
			_, err := dyn.Resource(InfraMachines).Namespace(ns.Name).UpdateStatus(ctx, copy, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		})
	}
	_ = unstructured.SetNestedField(machine.Object, true, "status", "ready")
	if _, err = dyn.Resource(InfraMachines).Namespace(ns.Name).UpdateStatus(ctx, machine, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("ordinary readiness update was blocked: %v", err)
	}
	t.Log("CAPI_REAL_CRD_ADMISSION_CONTRACTS_PASS")
}
