package vmapimigrate

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/kuberoot-dev/kuberoot/pkg/vmmaintenance"
)

// This test is opt-in and only accepts the dedicated loopback test server.
// It must never be pointed at a preserved guest or a management cluster.
func TestAdmissionFenceWithTwoRealAPIServers(t *testing.T) {
	path := os.Getenv("KUBEROOT_ADMISSION_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("requires the disposable stage2 admission server")
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: path}, &clientcmd.ConfigOverrides{CurrentContext: "stage2-disposable"}).ClientConfig()
	if err != nil || config.Host != "https://127.0.0.1:16443" || !strings.Contains(path, "/stage2-admission/") {
		t.Fatalf("refusing non-disposable API configuration: %v", err)
	}
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
	factory := func(endpoint, user string) (dynamic.Interface, error) {
		copy := rest.CopyConfig(config)
		copy.Host, copy.TLSClientConfig.ServerName = endpoint, ""
		copy.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:authenticated"}}
		copy.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
		return dynamic.NewForConfig(copy)
	}
	endpoints := map[string]string{"cp-a": "https://127.0.0.1:16443", "cp-b": "https://127.0.0.1:16444"}
	c := &Client{Kube: kube, Dynamic: dyn, APIServers: endpoints, ProbeClient: factory}
	var nodes []corev1.Node
	for name := range endpoints {
		nodes = append(nodes, corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}}})
	}
	for _, ns := range []string{"kube-system", "source", "target"} {
		_, err := kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"../../distros/hypervisor/addons/vm.kuberoot.dev_virtualmachines.yaml", "../../distros/workstation/addons/workstation.kuberoot.dev_workspaces.yaml"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{}
		if err = yaml.Unmarshal(raw, &u.Object); err != nil {
			t.Fatal(err)
		}
		if _, err = dyn.Resource(CRDs).Create(ctx, u, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
		if err = wait(ctx, func() (bool, error) {
			current, err := dyn.Resource(CRDs).Get(ctx, u.GetName(), metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			conditions, _, _ := unstructured.NestedSlice(current.Object, "status", "conditions")
			for _, raw := range conditions {
				condition := raw.(map[string]any)
				if condition["type"] == "Established" && condition["status"] == "True" {
					return true, nil
				}
			}
			return false, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	create := func(gvr schema.GroupVersionResource, u *unstructured.Unstructured) *unstructured.Unstructured {
		t.Helper()
		r := objectClient(dyn, gvr, u.GetNamespace())
		current, err := r.Create(ctx, u, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			current, err = r.Get(ctx, u.GetName(), metav1.GetOptions{})
		}
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	vm := create(NewVM, &unstructured.Unstructured{Object: map[string]any{"apiVersion": NewVM.GroupVersion().String(), "kind": "VirtualMachine", "metadata": map[string]any{"name": "existing", "namespace": "source"}, "spec": map[string]any{"cpus": int64(1), "memory": "1Gi", "replicas": int64(1), "disk": map[string]any{"image": "https://example.invalid/disk.qcow2"}}}})
	// Validate the opt-in bootstrap protocol at the real API boundary before
	// the migration fence, including optional-field additions and removals.
	seeded := cleanObject(vm.DeepCopy())
	seeded.SetName("network-seeded")
	_ = unstructured.SetNestedField(seeded.Object, "NoCloudNet", "spec", "seedMode")
	seeded = create(NewVM, seeded)
	for _, mode := range []string{"NoCloud", "NoCloudNet"} {
		candidate := cleanObject(vm.DeepCopy())
		candidate.SetName("seed-mode-dry-run")
		_ = unstructured.SetNestedField(candidate.Object, mode, "spec", "seedMode")
		if _, err = dyn.Resource(NewVM).Namespace("source").Create(ctx, candidate, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
			t.Fatalf("valid seed mode %s rejected: %v", mode, err)
		}
	}
	invalidMode := cleanObject(vm.DeepCopy())
	invalidMode.SetName("invalid-seed-mode")
	_ = unstructured.SetNestedField(invalidMode.Object, "Other", "spec", "seedMode")
	if _, err = dyn.Resource(NewVM).Namespace("source").Create(ctx, invalidMode, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "seedMode") {
		t.Fatalf("invalid seed mode accepted: %v", err)
	}
	for _, variant := range []string{"add", "change", "remove", "null"} {
		candidate := seeded.DeepCopy()
		switch variant {
		case "add":
			candidate = vm.DeepCopy()
			_ = unstructured.SetNestedField(candidate.Object, "NoCloudNet", "spec", "seedMode")
		case "change":
			_ = unstructured.SetNestedField(candidate.Object, "NoCloud", "spec", "seedMode")
		case "remove":
			unstructured.RemoveNestedField(candidate.Object, "spec", "seedMode")
		case "null":
			_ = unstructured.SetNestedField(candidate.Object, nil, "spec", "seedMode")
		}
		if _, err = dyn.Resource(NewVM).Namespace("source").Update(ctx, candidate, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}}); !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "seedMode is immutable") {
			t.Fatalf("seedMode %s bypassed immutability: %v", variant, err)
		}
		t.Log("SEED_MODE_IMMUTABLE", variant)
	}
	workspaceGVR := schema.GroupVersionResource{Group: "workstation.kuberoot.dev", Version: "v1alpha1", Resource: "workspaces"}
	workspace := create(workspaceGVR, &unstructured.Unstructured{Object: map[string]any{"apiVersion": workspaceGVR.GroupVersion().String(), "kind": "Workspace", "metadata": map[string]any{"name": "anna"}, "spec": map[string]any{"owner": "anna"}}})
	secrets := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	var sourceSecret *unstructured.Unstructured
	for _, ns := range []string{"source", "target"} {
		u := create(secrets, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "bootstrap", "namespace": ns}, "type": "Opaque", "data": map[string]any{"userData": "cHJpdmF0ZQ=="}}})
		if ns == "source" {
			sourceSecret = u
		}
	}
	configmaps := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	dependent := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "dependent", "namespace": "source"}}}
	dependent.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: NewVM.GroupVersion().String(), Kind: "VirtualMachine", Name: vm.GetName(), UID: vm.GetUID()}})
	dependent = create(configmaps, dependent)
	cm, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := &Plan{Token: cm.Data["token"], GateUID: cm.UID, Executor: cm.Data["executor"], APIServers: endpoints, Machines: []Record{{Original: *vm, Namespace: "target", Secret: sourceSecret}}}
	if err = c.protectDependents(ctx, p); err != nil {
		t.Fatal(err)
	}
	cm, err = kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.probeFence(ctx, p, cm, nodes); err != nil {
		policy, _ := dyn.Resource(policies).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{})
		t.Fatalf("mandatory probe: %v; policy status=%v", err, policy.Object["status"])
	}
	t.Log("EXACT_POLICY_DENIAL_ON_BOTH_DIRECT_CP_ENDPOINTS")
	// The integration-only identity can exercise each protected branch. The
	// production probe identity still has only its tiny namespace Role.
	role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "stage2-admission-integration"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"", "vm.kuberoot.dev", "workstation.kuberoot.dev"}, Resources: []string{"configmaps", "secrets", "virtualmachines", "virtualmachines/status", "workspaces", "workspaces/status"}, Verbs: []string{"create", "get", "update", "delete"}}}}
	if _, err = kube.RbacV1().ClusterRoles().Create(ctx, role, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	user := "kuberoot:stage2-admission-integration"
	_, err = kube.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: role.Name}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name}, Subjects: []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: user}}}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	deny := func(name string, call func() error) {
		t.Helper()
		err := call()
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), fenceMessage(cm)) || !strings.Contains(err.Error(), vmmaintenance.ConfigMap) {
			t.Fatalf("%s did not hit this exact admission fence: %v", name, err)
		}
		t.Log("DENIED", name)
	}
	for node, endpoint := range endpoints {
		actor, err := factory(endpoint, user)
		if err != nil {
			t.Fatal(err)
		}
		update := func(name string, resource schema.GroupVersionResource, object *unstructured.Unstructured, status bool) {
			deny(node+"/"+name, func() error {
				r := objectClient(actor, resource, object.GetNamespace())
				if status {
					_, err := r.UpdateStatus(ctx, object, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
					return err
				}
				_, err := r.Update(ctx, object, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
				return err
			})
		}
		copy := vm.DeepCopy()
		_ = unstructured.SetNestedField(copy.Object, int64(2), "spec", "cpus")
		update("VM/spec", NewVM, copy, false)
		copy = vm.DeepCopy()
		copy.Object["status"] = map[string]any{"phase": "Running"}
		update("VM/status", NewVM, copy, true)
		copy = cleanObject(vm.DeepCopy())
		copy.SetName("new-machine")
		deny(node+"/VM/create", func() error {
			_, err := actor.Resource(NewVM).Namespace("source").Create(ctx, copy, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		})
		deny(node+"/VM/delete", func() error {
			return actor.Resource(NewVM).Namespace("source").Delete(ctx, vm.GetName(), metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}})
		})
		copy = workspace.DeepCopy()
		_ = unstructured.SetNestedField(copy.Object, int64(3), "spec", "cpus")
		update("Workspace/spec", workspaceGVR, copy, false)
		copy.Object["status"] = map[string]any{"phase": "Ready"}
		update("Workspace/status", workspaceGVR, copy, true)
		for _, ns := range []string{"source", "target"} {
			copy, err = dyn.Resource(secrets).Namespace(ns).Get(ctx, "bootstrap", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			copy.Object["data"] = map[string]any{"userData": "Y2hhbmdlZA=="}
			update(ns+"/Secret", secrets, copy, false)
			deny(node+"/"+ns+"/Secret/delete", func() error {
				return actor.Resource(secrets).Namespace(ns).Delete(ctx, "bootstrap", metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}})
			})
		}
		copy = dependent.DeepCopy()
		copy.SetOwnerReferences(nil)
		update("dependent/oldObject-owner", configmaps, copy, false)
		copy = cleanObject(dependent.DeepCopy())
		copy.SetName("new-dependent")
		deny(node+"/dependent/new-owner", func() error {
			_, err := actor.Resource(configmaps).Namespace("source").Create(ctx, copy, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
			return err
		})
		copy = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "unrelated", "namespace": "source"}}}
		if _, err = actor.Resource(configmaps).Namespace("source").Create(ctx, copy, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
			t.Fatalf("unrelated write blocked: %v", err)
		}
	}
	// A plain RBAC denial is deliberately insufficient proof.
	c.ProbeClient = func(endpoint, _ string) (dynamic.Interface, error) {
		return factory(endpoint, "kuberoot:unauthorized-probe")
	}
	negativeCtx, stop := context.WithTimeout(ctx, 400*time.Millisecond)
	if err = c.probeFence(negativeCtx, p, cm, nodes); err == nil {
		t.Fatal("RBAC denial accepted as admission proof")
	}
	stop()
	t.Log("RBAC_DENIAL_REJECTED_AS_PROOF")
	c.ProbeClient = factory
	// The controller-only binding is write-once at the real API boundary.
	vm.Object["status"] = map[string]any{"backendID": "preserved-backend", "instanceID": "preserved-guest"}
	vm, err = dyn.Resource(NewVM).Namespace("source").UpdateStatus(ctx, vm, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"backendID", "instanceID"} {
		for _, remove := range []bool{false, true} {
			copy := vm.DeepCopy()
			if remove {
				unstructured.RemoveNestedField(copy.Object, "status", field)
			} else {
				_ = unstructured.SetNestedField(copy.Object, "changed", "status", field)
			}
			_, err = dyn.Resource(NewVM).Namespace("source").UpdateStatus(ctx, copy, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
			if !apierrors.IsInvalid(err) {
				t.Fatalf("binding %s removal=%v was not immutable: %v", field, remove, err)
			}
			t.Log(fmt.Sprintf("BINDING_IMMUTABLE %s removal=%v", field, remove))
		}
	}
	for _, nullStatus := range []bool{false, true} {
		copy := vm.DeepCopy()
		if nullStatus {
			copy.Object["status"] = nil
		} else {
			delete(copy.Object, "status")
		}
		_, err = dyn.Resource(NewVM).Namespace("source").UpdateStatus(ctx, copy, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
		if !apierrors.IsInvalid(err) {
			t.Fatalf("whole status removal null=%v bypassed binding invariant: %v", nullStatus, err)
		}
		t.Logf("WHOLE_STATUS_IMMUTABLE null=%v", nullStatus)
	}
	t.Log("REAL_ADMISSION_BRANCHES_AND_BINDINGS_PASS")
}
