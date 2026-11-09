package vmapimigrate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/kuberoot-dev/kuberoot/pkg/vmmaintenance"
)

func sourcePlan() *Plan {
	vm := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "vm.kuberoot.dev/v1alpha1", "kind": "VirtualMachine", "metadata": map[string]any{"name": "legacy", "uid": "old-vm", "resourceVersion": "1", "finalizers": []any{"vm.kuberoot.dev/cleanup"}},
		"spec":   map[string]any{"cpus": int64(1), "memory": "1Gi", "disk": map[string]any{"size": "4Gi", "image": "https://example.invalid/disk.qcow2"}, "userDataSecret": map[string]any{"namespace": "tenant", "name": "bootstrap"}},
		"status": map[string]any{"phase": "Running", "node": "a", "replicaNodes": []any{"a"}, "mac": "52:54:00:00:00:01", "address": "10.123.0.3", "minor": int64(100), "port": int64(7800), "imageWritten": true}}}
	secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "bootstrap", "namespace": "tenant", "uid": "old-secret", "resourceVersion": "1"}, "type": "Opaque", "data": map[string]any{"userData": "cHJpdmF0ZQ=="}}}
	secret.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: OldVM.GroupVersion().String(), Kind: "VirtualMachine", Name: "legacy", UID: "old-vm"}})
	oldCRD := unstructured.Unstructured{Object: map[string]any{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]any{"name": CRDName, "uid": "old-crd", "resourceVersion": "1"}, "spec": map[string]any{
		"group": "vm.kuberoot.dev", "scope": "Cluster", "names": map[string]any{"kind": "VirtualMachine", "plural": "virtualmachines", "singular": "virtualmachine", "listKind": "VirtualMachineList"}, "versions": []any{map[string]any{"name": "v1alpha1", "served": true, "storage": true}}}}}
	target := oldCRD.DeepCopy()
	_ = unstructured.SetNestedField(target.Object, "Namespaced", "spec", "scope")
	_ = unstructured.SetNestedSlice(target.Object, []any{map[string]any{"name": "v1alpha2", "served": true, "storage": true}}, "spec", "versions")
	machine := unstructured.Unstructured{Object: map[string]any{"apiVersion": "node.kuberoot.dev/v1alpha1", "kind": "Machine", "metadata": map[string]any{"name": "a.legacy", "uid": "machine-uid", "resourceVersion": "1", "labels": map[string]any{"kuberoot.dev/node": "a"}},
		"spec": map[string]any{"cpus": int64(1), "memoryMiB": int64(1024), "volumes": []any{"legacy"}, "mac": "52:54:00:00:00:01", "running": true}, "status": map[string]any{"phase": "Running", "pid": int64(99), "startedAt": "2026-11-03T09:00:00Z"}}}
	volume := unstructured.Unstructured{Object: map[string]any{"apiVersion": "node.kuberoot.dev/v1alpha1", "kind": "Volume", "metadata": map[string]any{"name": "a.legacy", "uid": "volume-uid", "resourceVersion": "1", "labels": map[string]any{"kuberoot.dev/node": "a"}}, "spec": map[string]any{"minor": int64(100), "port": int64(7800), "nodeID": int64(0), "primary": true}, "status": map[string]any{"imageWritten": true}}}
	return &Plan{Version: 1, ClusterUID: "cluster", GateUID: "gate", Token: "token", APIServers: map[string]string{"a": "https://10.0.0.1:6443"}, Executor: "operator", OriginalCRD: oldCRD, TargetCRD: *target,
		Machines:   []Record{{Original: vm, Namespace: "tenant", Secret: secret}},
		Dependents: []Captured{{Resource: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, Object: *secret.DeepCopy()}}, Backends: []Captured{{Resource: Machines, Object: machine}, {Resource: Volumes, Object: volume}}}
}

func TestPlanPreservesBackendAndGuestIdentityAcrossNewUID(t *testing.T) {
	p := sourcePlan()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	u := p.Machines[0].Target("reviewed")
	if u.GetUID() != "" || u.GetNamespace() != "tenant" || u.GetAPIVersion() != NewVM.GroupVersion().String() {
		t.Fatalf("target=%v", u)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(u.Object, "spec", "userDataSecret", "namespace"); found {
		t.Fatal("cross-namespace Secret reference survived conversion")
	}
	status := p.Machines[0].Status()
	if status["backendID"] != "legacy" || status["instanceID"] != "old-vm" || status["address"] != "10.123.0.3" || status["imageWritten"] != true {
		t.Fatalf("guest identity lost: %v", status)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := SavePrivate(path, p); err != nil {
		t.Fatal(err)
	}
	_, hash, err := LoadPlan(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPlan(path, hash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPlan(path, "another"); err == nil {
		t.Fatal("different reviewed backup accepted")
	}
}

func TestPlanRejectsIncompleteBackendCheckpoints(t *testing.T) {
	for _, test := range []string{"replicas absent", "wrong node", "monitor stopped", "dual primary", "missing volume", "missing machine", "unknown backend", "cross-namespace owner"} {
		t.Run(test, func(t *testing.T) {
			p := sourcePlan()
			switch test {
			case "replicas absent":
				unstructured.RemoveNestedField(p.Machines[0].Original.Object, "status", "replicaNodes")
			case "wrong node":
				_ = unstructured.SetNestedField(p.Machines[0].Original.Object, "b", "status", "node")
			case "monitor stopped":
				_ = unstructured.SetNestedField(p.Backends[0].Object.Object, "Stopped", "status", "phase")
				_ = unstructured.SetNestedField(p.Backends[0].Object.Object, int64(0), "status", "pid")
			case "dual primary":
				_ = unstructured.SetNestedField(p.Backends[1].Object.Object, true, "spec", "allowTwoPrimaries")
			case "missing volume":
				p.Backends = p.Backends[:1]
			case "missing machine":
				p.Backends = p.Backends[1:]
			case "unknown backend":
				p.Backends[0].Object.SetName("a.orphan")
			case "cross-namespace owner":
				p.Machines[0].Namespace = "another-tenant"
			}
			if err := p.Validate(); err == nil {
				t.Fatal("unsafe import plan accepted")
			}
		})
	}
}

func TestWorkspaceTargetCannotEscapeItsControllerNamespace(t *testing.T) {
	p := sourcePlan()
	p.Machines[0].Original.SetName("ws-anna")
	p.Machines[0].Original.SetLabels(map[string]string{"workstation.kuberoot.dev/workspace": "anna"})
	p.Workspaces = []unstructured.Unstructured{{Object: map[string]any{"apiVersion": "workstation.kuberoot.dev/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": "anna", "uid": "workspace", "resourceVersion": "1"}, "status": map[string]any{"machine": "ws-anna"}}}}
	for i := range p.Backends {
		p.Backends[i].Object.SetName("a.ws-anna")
	}
	_ = unstructured.SetNestedStringSlice(p.Backends[0].Object.Object, []string{"ws-anna"}, "spec", "volumes")
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "workstation namespace") {
		t.Fatalf("workspace duplicated after resume: %v", err)
	}
}

type fixtureDiscovery struct{ *discoveryfake.FakeDiscovery }

func (f fixtureDiscovery) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	return []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "secrets", Namespaced: true, Verbs: []string{"get", "list", "update"}},
		{Name: "configmaps", Namespaced: true, Verbs: []string{"get", "list", "update"}},
	}}}, nil
}

func migrationFixture(t *testing.T) (*Client, *Plan, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	p := sourcePlan()
	kube := kubefake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant", UID: "tenant"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: vmmaintenance.ConfigMap, Namespace: vmmaintenance.Namespace, UID: "gate"}, Data: map[string]string{"token": "token", "executor": "operator"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}})
	kube.PrependReactor("create", "selfsubjectreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{UserInfo: authenticationv1.UserInfo{Username: "operator"}}}, nil
	})
	vmctl := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "node.kuberoot.dev/v1alpha1", "kind": "NodeService", "metadata": map[string]any{"name": "a.kuberoot-vmctl"}, "status": map[string]any{
		"state": "Running", "pid": int64(42), "startedAt": "2026-11-03T09:00:00Z", "controllerInventoryObserved": true, "controllerProcesses": []any{map[string]any{"pid": int64(42), "executable": "/usr/bin/kuberoot-vmctl", "startTicks": int64(12345), "bootID": "boot-a"}}}}}
	wsctl := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "node.kuberoot.dev/v1alpha1", "kind": "NodeService", "metadata": map[string]any{"name": "a.kuberoot-wsctl"}, "status": map[string]any{"state": "Absent", "controllerInventoryObserved": true}}}
	objects := []runtime.Object{p.OriginalCRD.DeepCopy(), p.Machines[0].Original.DeepCopy(), p.Machines[0].Secret.DeepCopy(), vmctl, wsctl}
	for _, backend := range p.Backends {
		objects = append(objects, backend.Object.DeepCopy())
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{OldVM: "VirtualMachineList", NewVM: "VirtualMachineList", Machines: "MachineList", Volumes: "VolumeList", CRDs: "CustomResourceDefinitionList", {Version: "v1", Resource: "configmaps"}: "ConfigMapList", {Version: "v1", Resource: "secrets"}: "SecretList"}, objects...)
	dyn.PrependReactor("create", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		u := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		if u.GetUID() == "" {
			u.SetUID(types.UID("new-" + u.GetKind() + "-" + u.GetName()))
		}
		u.SetResourceVersion("new-rv")
		if action.GetResource() == CRDs {
			_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Established", "status": "True"}}, "status", "conditions")
		}
		return false, nil, nil
	})
	client := &Client{Kube: kube, Dynamic: dyn, APIServers: p.APIServers, Discovery: fixtureDiscovery{&discoveryfake.FakeDiscovery{Fake: &ktesting.Fake{}}}}
	client.ProbeClient = func(endpoint, user string) (dynamic.Interface, error) {
		probe := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
		probe.PrependReactor("create", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
			if user != probeUser(p.Token) || len(action.(interface{ GetCreateOptions() metav1.CreateOptions }).GetCreateOptions().DryRun) != 1 {
				t.Fatal("probe did not use session identity and dry-run")
			}
			cm, err := kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(context.Background(), vmmaintenance.ConfigMap, metav1.GetOptions{})
			if err != nil {
				return true, nil, err
			}
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, probeName(p.Token), errors.New(vmmaintenance.ConfigMap+": "+fenceMessage(cm)))
		})
		return probe, nil
	}
	guard := &vmmaintenance.Guard{Principal: "controller", Mode: vmmaintenance.ModeAlpha2, Kube: kube, Dynamic: dyn, Node: "a", Service: "kuberoot-vmctl", PID: 42, Identity: "process-1"}
	if err := guard.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.protectDependents(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return client, p, dyn
}

func TestCutoverRecoversAfterCreatedObjectBeforeStatusCheckpoint(t *testing.T) {
	c, p, dyn := migrationFixture(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	failed := false
	dyn.PrependReactor("update", "virtualmachines", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "status" && !failed {
			failed = true
			return true, nil, errors.New("simulated crash after create")
		}
		return false, nil, nil
	})
	if err := c.Cutover(ctx, p, "reviewed", path); err == nil || !strings.Contains(err.Error(), "simulated crash") {
		t.Fatalf("expected interrupted import, got %v", err)
	}
	if _, err := c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{}); err != nil {
		t.Fatal("failure removed the maintenance barrier")
	}
	if err := c.Cutover(ctx, p, "reviewed", path); err != nil {
		t.Fatal(err)
	}
	u, err := dyn.Resource(NewVM).Namespace("tenant").Get(ctx, "legacy", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if u.GetUID() == "old-vm" {
		t.Fatal("test failed to exercise API UID replacement")
	}
	secret, err := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace("tenant").Get(ctx, "bootstrap", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs := secret.GetOwnerReferences()
	if len(refs) != 1 || refs[0].UID != u.GetUID() || refs[0].APIVersion != NewVM.GroupVersion().String() {
		t.Fatalf("owner UID not restored: %+v", refs)
	}
	for _, action := range dyn.Actions() {
		if action.GetResource().Group == "node.kuberoot.dev" && action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("cutover changed a native backend: %v", action)
		}
	}
	if err := c.Resume(ctx, p, "reviewed", path); err != nil {
		t.Fatal(err)
	}
	if err := c.Resume(ctx, p, "reviewed", path); err != nil {
		t.Fatal("resume is not idempotent:", err)
	}
	if _, err := c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("verified barrier not released: %v", err)
	}
}

func TestChangedBootstrapSecretPreventsVerifiedImport(t *testing.T) {
	for _, action := range []string{"mutate", "delete"} {
		t.Run(action, func(t *testing.T) {
			c, p, dyn := migrationFixture(t)
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.json")
			interrupted := false
			dyn.PrependReactor("update", "virtualmachines", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetSubresource() == "status" && !interrupted {
					interrupted = true
					return true, nil, errors.New("pause")
				}
				return false, nil, nil
			})
			if err := c.Cutover(ctx, p, "reviewed", path); err == nil {
				t.Fatal("did not pause after create")
			}
			resource := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace("tenant")
			if action == "delete" {
				if err := resource.Delete(ctx, "bootstrap", metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			} else {
				secret, err := resource.Get(ctx, "bootstrap", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				_ = unstructured.SetNestedField(secret.Object, "Y2hhbmdlZA==", "data", "userData")
				if _, err = resource.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Cutover(ctx, p, "reviewed", path); err == nil {
				t.Fatal("changed/deleted bootstrap Secret was verified")
			}
			state, err := loadState(path, "reviewed")
			if err != nil {
				t.Fatal(err)
			}
			if state.Phase == "verified" || state.Phase == "complete" {
				t.Fatalf("unsafe checkpoint %s", state.Phase)
			}
			if _, err := c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{}); err != nil {
				t.Fatal("failed verification removed gate")
			}
		})
	}
}

func TestBarrierRejectsOrphanWithoutSupervisor(t *testing.T) {
	c, p, dyn := migrationFixture(t)
	ctx := context.Background()
	u, err := dyn.Resource(services).Get(ctx, "a.kuberoot-wsctl", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"pid": int64(77), "executable": "/usr/bin/kuberoot-wsctl", "startTicks": int64(12345), "bootID": "boot-a"}}, "status", "controllerProcesses")
	if _, err = dyn.Resource(services).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.VerifyBarrier(ctx, p); err == nil {
		t.Fatal("orphaned workspace controller bypassed maintenance")
	}
}

func TestResumeRevalidatesAfterCrashBeforeFenceRemoval(t *testing.T) {
	c, p, dyn := migrationFixture(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := c.Cutover(ctx, p, "reviewed", path); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(path, "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	state.Phase = "resuming"
	if err = SavePrivate(path, state); err != nil {
		t.Fatal(err)
	}
	resource := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace("tenant")
	secret, err := resource.Get(ctx, "bootstrap", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(secret.Object, "YWx0ZXJlZA==", "data", "userData")
	if _, err = resource.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.Resume(ctx, p, "reviewed", path); err == nil {
		t.Fatal("resuming checkpoint bypassed fresh credential verification")
	}
	if _, err = c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{}); err != nil {
		t.Fatal("stale verified checkpoint released gate")
	}
	if _, err = dyn.Resource(bindings).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{}); err != nil {
		t.Fatal("stale verified checkpoint removed admission fence")
	}
}

func TestResumeCompletesCleanupAfterReleaseWithoutFreezingNewWrites(t *testing.T) {
	c, p, dyn := migrationFixture(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := c.Cutover(ctx, p, "reviewed", path); err != nil {
		t.Fatal(err)
	}
	interrupted := false
	dyn.PrependReactor("delete", "validatingadmissionpolicybindings", func(ktesting.Action) (bool, runtime.Object, error) {
		if !interrupted {
			interrupted = true
			return true, nil, errors.New("crash after release")
		}
		return false, nil, nil
	})
	if err := c.Resume(ctx, p, "reviewed", path); err == nil {
		t.Fatal("cleanup interruption was hidden")
	}
	if _, err := c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("release boundary not crossed")
	}
	r := dyn.Resource(NewVM).Namespace("tenant")
	u, err := r.Get(ctx, "legacy", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(u.Object, "ordinary reconcile after release", "status", "message")
	if _, err = r.UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.Resume(ctx, p, "reviewed", path); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(path, "reviewed")
	if err != nil || state.Phase != "complete" {
		t.Fatalf("cleanup checkpoint: %+v %v", state, err)
	}
	if _, err = dyn.Resource(bindings).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("fence remained after cleanup")
	}
}

func TestNewOwnerReferenceAfterExportPreventsDeletion(t *testing.T) {
	c, p, dyn := migrationFixture(t)
	dyn.PrependReactor("update", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		u := action.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		if len(u.GetOwnerReferences()) == 0 {
			late := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "late-dependent", "namespace": "tenant", "uid": "late"}}}
			late.SetOwnerReferences(p.Dependents[0].Object.GetOwnerReferences())
			if err := dyn.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, late, "tenant"); err != nil {
				t.Fatal(err)
			}
		}
		return false, nil, nil
	})
	err := c.Cutover(context.Background(), p, "reviewed", filepath.Join(t.TempDir(), "state.json"))
	if err == nil || !strings.Contains(err.Error(), "owner references remain") {
		t.Fatalf("new dependent bypassed final inventory: %v", err)
	}
	if _, err := dyn.Resource(OldVM).Get(context.Background(), "legacy", metav1.GetOptions{}); err != nil {
		t.Fatal("old VM was deleted despite new dependent")
	}
}

func TestProbeRejectsMissingControlPlaneAndWrongDenial(t *testing.T) {
	for _, variant := range []string{"missing endpoint", "load balancer", "RBAC denial", "old policy scope"} {
		t.Run(variant, func(t *testing.T) {
			c, p, _ := migrationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			cm, _ := c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{})
			nodes, _ := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			switch variant {
			case "missing endpoint":
				delete(p.APIServers, "a")
			case "load balancer":
				p.APIServers["a"] = "https://10.0.0.99:6443"
			default:
				c.ProbeClient = func(string, string) (dynamic.Interface, error) {
					client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
					client.PrependReactor("create", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
						message := "RBAC denies this user"
						if variant == "old policy scope" {
							message = vmmaintenance.ConfigMap + ": VM API maintenance " + p.Token + " scope outdated"
						}
						return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, probeName(p.Token), errors.New(message))
					})
					return client, nil
				}
			}
			if err := c.probeFence(ctx, p, cm, nodes.Items); err == nil {
				t.Fatal("incomplete proof was accepted")
			}
		})
	}
}

func TestResumeRequiresRestartedAlpha2Controller(t *testing.T) {
	c, p, _ := migrationFixture(t)
	ctx := context.Background()
	leaseName := vmmaintenance.LeaseName("a", "kuberoot-vmctl")
	lease, err := c.Kube.CoordinationV1().Leases(vmmaintenance.Namespace).Get(ctx, leaseName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lease.Annotations["api-mode"] = vmmaintenance.ModeAlpha1
	if _, err = c.Kube.CoordinationV1().Leases(vmmaintenance.Namespace).Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err = c.Cutover(ctx, p, "reviewed", path); err != nil {
		t.Fatal(err)
	}
	if err = c.Resume(ctx, p, "reviewed", path); err == nil || !strings.Contains(err.Error(), "NodeService") {
		t.Fatalf("old controller resumed: %v", err)
	}
	if _, err = c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{}); err != nil {
		t.Fatal("gate released before alpha2 readiness")
	}
	lease, err = c.Kube.CoordinationV1().Leases(vmmaintenance.Namespace).Get(ctx, leaseName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lease.Annotations["api-mode"] = vmmaintenance.ModeAlpha2
	if _, err = c.Kube.CoordinationV1().Leases(vmmaintenance.Namespace).Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.Resume(ctx, p, "reviewed", path); err != nil {
		t.Fatal(err)
	}
}

func TestSharedExecutorFailsBeforeCreatingMaintenanceGate(t *testing.T) {
	c, _, _ := migrationFixture(t)
	ctx := context.Background()
	if err := c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Delete(ctx, vmmaintenance.ConfigMap, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	leaseName := vmmaintenance.LeaseName("a", "kuberoot-vmctl")
	lease, err := c.Kube.CoordinationV1().Leases(vmmaintenance.Namespace).Get(ctx, leaseName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lease.Annotations["principal"] = "operator"
	if _, err = c.Kube.CoordinationV1().Leases(vmmaintenance.Namespace).Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Begin(ctx); err == nil || !strings.Contains(err.Error(), "dedicated executor") {
		t.Fatalf("shared executor accepted: %v", err)
	}
	if _, err = c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("wrong executor acquired a maintenance session")
	}
}

func TestCleanupAfterReleaseStillRequiresOriginalExecutor(t *testing.T) {
	c, p, dyn := migrationFixture(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := c.Cutover(ctx, p, "reviewed", path); err != nil {
		t.Fatal(err)
	}
	dyn.PrependReactor("delete", "validatingadmissionpolicybindings", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("cleanup interrupted")
	})
	if err := c.Resume(ctx, p, "reviewed", path); err == nil {
		t.Fatal("expected incomplete cleanup")
	}
	c.Kube.(*kubefake.Clientset).PrependReactor("create", "selfsubjectreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{UserInfo: authenticationv1.UserInfo{Username: "another-operator"}}}, nil
	})
	if err := c.Resume(ctx, p, "reviewed", path); err == nil || !strings.Contains(err.Error(), "original dedicated executor") {
		t.Fatalf("another principal resumed cleanup: %v", err)
	}
	if _, err := dyn.Resource(bindings).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{}); err != nil {
		t.Fatal("another principal removed the remaining fence")
	}
}
