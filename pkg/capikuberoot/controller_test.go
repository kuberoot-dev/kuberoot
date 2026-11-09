package capikuberoot

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"

	infra "github.com/kuberoot-dev/kuberoot/pkg/apis/infrastructure/v1alpha1"
	vmv1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha2"
)

const testNS = "tenant-a"

type fixture struct {
	t        *testing.T
	c        *Controller
	dynamic  *dynamicfake.FakeDynamicClient
	kube     *fake.Clientset
	workload *fake.Clientset
}

func object(t *testing.T, value any) *unstructured.Unstructured {
	t.Helper()
	u, err := toObject(value)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	created := metav1.NewTime(time.Unix(1_700_000_000, 0))
	cluster := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Clusters.GroupVersion().String(), "kind": "Cluster",
		"metadata": map[string]any{"name": "workload", "namespace": testNS, "uid": "cluster-a", "resourceVersion": "1"},
		"spec":     map[string]any{"infrastructureRef": map[string]any{"apiVersion": infra.GroupVersion.String(), "kind": "KuberootCluster", "name": "workload"}},
	}}
	capi := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Machines.GroupVersion().String(), "kind": "Machine",
		"metadata": map[string]any{"name": "capi-worker", "namespace": testNS, "uid": "capi-a", "resourceVersion": "1"},
		"spec":     map[string]any{"clusterName": "workload", "infrastructureRef": map[string]any{"apiVersion": infra.GroupVersion.String(), "kind": "KuberootMachine", "name": "worker"}, "bootstrap": map[string]any{"dataSecretName": "capi-bootstrap"}},
	}}
	km := &infra.KuberootMachine{TypeMeta: metav1.TypeMeta{APIVersion: infra.GroupVersion.String(), Kind: "KuberootMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: testNS, UID: "infra-a", ResourceVersion: "1", CreationTimestamp: created,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: Machines.GroupVersion().String(), Kind: "Machine", Name: "capi-worker", UID: "capi-a"}}},
		Spec: infra.KuberootMachineSpec{CPUs: 1, Memory: resource.MustParse("512Mi"), Disk: vmv1.Disk{Size: resource.MustParse("2Gi"), Image: "https://images.example/worker.raw"}, Replicas: 1}}
	kc := &infra.KuberootCluster{TypeMeta: metav1.TypeMeta{APIVersion: infra.GroupVersion.String(), Kind: "KuberootCluster"},
		ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: testNS, UID: "infra-cluster-a", ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: Clusters.GroupVersion().String(), Kind: "Cluster", Name: "workload", UID: "cluster-a"}}},
		Spec: infra.KuberootClusterSpec{ExternalManagedControlPlane: true}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		Clusters: "ClusterList", Machines: "MachineList", InfraClusters: "KuberootClusterList", InfraMachines: "KuberootMachineList", VirtualMachines: "VirtualMachineList", KamajiControlPlanes: "KamajiControlPlaneList",
	}, cluster, capi, object(t, km), object(t, kc))
	kube := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "capi-bootstrap", Namespace: testNS, UID: "bootstrap-a", ResourceVersion: "1"}, Data: map[string][]byte{"value": []byte("version: v1alpha1\nmachine: {token: initial}\n")}})
	workload := fake.NewClientset()
	f := &fixture{t: t, dynamic: dyn, kube: kube, workload: workload}
	f.c = &Controller{Dynamic: dyn, Kube: kube, WorkloadClient: func(*rest.Config) (kubernetes.Interface, error) { return workload, nil }}
	sequence := 0
	dyn.PrependReactor("create", "virtualmachines", func(action ktesting.Action) (bool, runtime.Object, error) {
		u := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		sequence++
		u.SetUID(types.UID(fmt.Sprintf("vm-%d", sequence)))
		u.SetResourceVersion("1")
		u.SetCreationTimestamp(metav1.NewTime(created.Add(time.Second)))
		ref, _, _ := unstructured.NestedString(u.Object, "spec", "userDataSecret", "name")
		secret, err := kube.CoreV1().Secrets(u.GetNamespace()).Get(context.Background(), ref, metav1.GetOptions{})
		if err != nil || secret.Immutable == nil || !*secret.Immutable || len(secret.Data["userData"]) == 0 {
			t.Errorf("VM created before immutable bootstrap data was available: %v", err)
		}
		return false, nil, nil
	})
	kube.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		secret.UID, secret.ResourceVersion = "local-bootstrap", "1"
		return false, nil, nil
	})
	return f
}

func (f *fixture) reconcile(count int) {
	f.t.Helper()
	for range count {
		if err := f.c.ReconcileMachine(context.Background(), testNS, "worker"); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) machine() *infra.KuberootMachine {
	f.t.Helper()
	u := f.get(InfraMachines, "worker")
	var m infra.KuberootMachine
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &m); err != nil {
		f.t.Fatal(err)
	}
	return &m
}

func (f *fixture) get(gvr schema.GroupVersionResource, name string) *unstructured.Unstructured {
	f.t.Helper()
	u, err := f.dynamic.Resource(gvr).Namespace(testNS).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *fixture) update(gvr schema.GroupVersionResource, object *unstructured.Unstructured) {
	f.t.Helper()
	if _, err := f.dynamic.Resource(gvr).Namespace(object.GetNamespace()).Update(context.Background(), object, metav1.UpdateOptions{}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) expectNoVM() {
	f.t.Helper()
	_, err := f.dynamic.Resource(VirtualMachines).Namespace(testNS).Get(context.Background(), "worker", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		f.t.Fatalf("unexpected VM: %v", err)
	}
}

func (f *fixture) expectFailure(message string) {
	f.t.Helper()
	m := f.machine()
	if m.Status.Ready {
		f.t.Fatal("conflict reported Ready")
	}
	for _, condition := range m.Status.Conditions {
		if condition.Type == "Ready" && strings.Contains(condition.Message, message) {
			return
		}
	}
	f.t.Fatalf("missing error %q in %#v", message, m.Status.Conditions)
}

func (f *fixture) running() {
	f.reconcile(4)
	vm := f.get(VirtualMachines, "worker")
	vm.Object["status"] = map[string]any{"phase": "Running", "address": "10.123.0.20", "instanceID": string(vm.GetUID()), "backendID": string(vm.GetUID())}
	f.update(VirtualMachines, vm)
}

func TestMachineCreatesBootstrapBeforeVMAndPersistsIdentity(t *testing.T) {
	f := newFixture(t)
	f.reconcile(4)
	machine, vm := f.machine(), f.get(VirtualMachines, "worker")
	if mode, _, _ := unstructured.NestedString(vm.Object, "spec", "seedMode"); mode != string(vmv1.SeedModeNoCloudNet) {
		t.Fatalf("new CAPI VM did not opt into the network seed protocol: %q", mode)
	}
	if !slices.Contains(vm.GetFinalizers(), VMFinalizer) {
		t.Fatal("initial VM Create lacked its independent lifetime protection")
	}
	if machine.Spec.ProviderID == nil || *machine.Spec.ProviderID != "kuberoot://worker" || !sameReference(machine.Status.VMRef, vm, vmv1.GroupVersion.String(), "VirtualMachine") {
		t.Fatal("provider or VM identity not recorded")
	}
	secret, err := f.kube.CoreV1().Secrets(testNS).Get(context.Background(), bootstrapName(machine), metav1.GetOptions{})
	if err != nil || !owned(secret, machine) || !slices.Contains(secret.Finalizers, BootstrapFinalizer) || string(secret.Data["userData"]) != "version: v1alpha1\nmachine: {token: initial}\n" {
		t.Fatalf("bootstrap not preserved: %v", err)
	}
	capi := f.get(Machines, "capi-worker")
	if _, exists, _ := unstructured.NestedString(capi.Object, "spec", "providerID"); exists {
		t.Fatal("provider edited CAPI core Machine; core must copy providerID")
	}
	// A create followed by process loss before UpdateStatus must recover the
	// owned object, without creating a second VM or replacing its credentials.
	u := f.get(InfraMachines, "worker")
	unstructured.RemoveNestedField(u.Object, "status", "vmRef")
	f.update(InfraMachines, u)
	f.reconcile(1)
	if f.machine().Status.VMRef.UID != vm.GetUID() {
		t.Fatal("crash recovery replaced VM")
	}
	if err := f.dynamic.Resource(VirtualMachines).Namespace(testNS).Delete(context.Background(), "worker", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	f.reconcile(2)
	f.expectNoVM()
	f.expectFailure("automatic recreation is forbidden")
}

func TestExistingVMSeedProtocolIsNeverChanged(t *testing.T) {
	for _, mode := range []string{"", string(vmv1.SeedModeNoCloud)} {
		t.Run("legacy-"+mode, func(t *testing.T) {
			f := newFixture(t)
			f.running()
			vm := f.get(VirtualMachines, "worker")
			if mode == "" {
				unstructured.RemoveNestedField(vm.Object, "spec", "seedMode")
			} else if err := unstructured.SetNestedField(vm.Object, mode, "spec", "seedMode"); err != nil {
				t.Fatal(err)
			}
			f.update(VirtualMachines, vm)
			f.dynamic.ClearActions()
			f.reconcile(2)
			f.expectFailure("immutable VM configuration")
			for _, action := range f.dynamic.Actions() {
				if action.GetResource() == VirtualMachines && action.GetVerb() != "get" {
					t.Fatalf("existing VM seed protocol was mutated: %s", action.GetVerb())
				}
			}
		})
	}
}

func TestForeignVMAndBootstrapAreNeverAdopted(t *testing.T) {
	for _, kind := range []string{"VM", "Secret"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			m := f.machine()
			foreign := machineOwner(m)
			foreign.UID = "another-infra-uid"
			if kind == "VM" {
				vm := machineVM(m, bootstrapName(m))
				vm.UID = "foreign-vm"
				vm.OwnerReferences = []metav1.OwnerReference{foreign}
				if err := f.dynamic.Tracker().Create(VirtualMachines, object(t, vm), testNS); err != nil {
					t.Fatal(err)
				}
			} else {
				immutable := true
				_, err := f.kube.CoreV1().Secrets(testNS).Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: bootstrapName(m), Namespace: testNS, OwnerReferences: []metav1.OwnerReference{foreign}}, Immutable: &immutable}, metav1.CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			f.reconcile(4)
			if kind == "VM" {
				f.expectFailure("adoption is forbidden")
				if f.get(VirtualMachines, "worker").GetUID() != "foreign-vm" {
					t.Fatal("foreign VM changed")
				}
			} else {
				f.expectFailure("bootstrap Secret is foreign")
				f.expectNoVM()
			}
		})
	}
}

func TestBootstrapChangeAndMissingInitialSecretFailClosed(t *testing.T) {
	for _, change := range []string{"data", "sourceUID", "deleteLocal"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			f.reconcile(4)
			vmUID := f.get(VirtualMachines, "worker").GetUID()
			if change == "deleteLocal" {
				if err := f.kube.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("secrets"), testNS, bootstrapName(f.machine())); err != nil {
					t.Fatal(err)
				}
			} else {
				secret, _ := f.kube.CoreV1().Secrets(testNS).Get(context.Background(), "capi-bootstrap", metav1.GetOptions{})
				if change == "data" {
					secret.Data["value"] = []byte("different credentials")
				} else {
					secret.UID = "replaced-source"
				}
				if _, err := f.kube.CoreV1().Secrets(testNS).Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			f.reconcile(1)
			if change == "deleteLocal" {
				f.expectFailure("original bootstrap Secret disappeared")
			} else {
				f.expectFailure("initial data")
			}
			if f.get(VirtualMachines, "worker").GetUID() != vmUID {
				t.Fatal("changed VM after credential conflict")
			}
		})
	}
}

func TestNamespaceIsolationAndCrossNamespaceReferenceRejection(t *testing.T) {
	f := newFixture(t)
	foreign := machineVM(f.machine(), "foreign-data")
	foreign.Namespace = "tenant-b"
	foreign.UID = "foreign-vm"
	if err := f.dynamic.Tracker().Create(VirtualMachines, object(t, foreign), "tenant-b"); err != nil {
		t.Fatal(err)
	}
	f.reconcile(4)
	if f.get(VirtualMachines, "worker").GetUID() == foreign.UID {
		t.Fatal("adopted VM from another namespace")
	}
	capi := f.get(Machines, "capi-worker")
	_ = unstructured.SetNestedField(capi.Object, "tenant-b", "spec", "infrastructureRef", "namespace")
	f.update(Machines, capi)
	f.reconcile(1)
	f.expectFailure("infrastructure reference does not match")
}

func TestPauseStopsCreationAndDeletion(t *testing.T) {
	for _, target := range []schema.GroupVersionResource{InfraMachines, Machines, Clusters, InfraClusters} {
		t.Run(target.Resource, func(t *testing.T) {
			f := newFixture(t)
			name := "workload"
			if target == InfraMachines {
				name = "worker"
			}
			if target == Machines {
				name = "capi-worker"
			}
			u := f.get(target, name)
			u.SetAnnotations(map[string]string{PausedAnnotation: ""})
			f.update(target, u)
			f.reconcile(3)
			f.expectNoVM()
			if len(f.machine().Finalizers) != 0 {
				t.Fatal("mutated paused Machine")
			}
		})
	}
	t.Run("deleting paused infrastructure", func(t *testing.T) {
		f := newFixture(t)
		f.reconcile(4)
		km := f.get(InfraMachines, "worker")
		now := metav1.Now()
		km.SetDeletionTimestamp(&now)
		f.update(InfraMachines, km)
		kc := f.get(InfraClusters, "workload")
		kc.SetAnnotations(map[string]string{PausedAnnotation: ""})
		f.update(InfraClusters, kc)
		before := len(f.dynamic.Actions())
		f.reconcile(1)
		for _, action := range f.dynamic.Actions()[before:] {
			if action.GetVerb() != "get" {
				t.Fatalf("mutated resources of a paused deleting Machine: %s", action.GetVerb())
			}
		}
	})
}

func TestReplacedVMAndClusterUIDAreNotRebound(t *testing.T) {
	for _, target := range []schema.GroupVersionResource{VirtualMachines, Clusters} {
		t.Run(target.Resource, func(t *testing.T) {
			f := newFixture(t)
			f.reconcile(4)
			bound := f.machine().Status.VMRef.UID
			name := "worker"
			if target == Clusters {
				name = "workload"
			}
			replaced := f.get(target, name)
			replaced.SetUID("replacement-uid")
			f.update(target, replaced)
			f.reconcile(1)
			if f.machine().Status.Ready || f.machine().Status.VMRef.UID != bound {
				t.Fatal("silently rebound infrastructure identity")
			}
			if target == VirtualMachines {
				f.expectFailure("changed UID")
			} else {
				f.expectFailure("does not belong")
			}
		})
	}
}

func TestCurrentResourcesMayGrowButCapacityAndDiskDoNotChange(t *testing.T) {
	f := newFixture(t)
	m := f.get(InfraMachines, "worker")
	_ = unstructured.SetNestedField(m.Object, int64(4), "spec", "maxCPUs")
	f.update(InfraMachines, m)
	f.running()
	m = f.get(InfraMachines, "worker")
	_ = unstructured.SetNestedField(m.Object, int64(2), "spec", "cpus")
	f.update(InfraMachines, m)
	f.reconcile(1)
	vm := f.get(VirtualMachines, "worker")
	if value, _, _ := unstructured.NestedInt64(vm.Object, "spec", "cpus"); value != 2 {
		t.Fatal("requested current CPU growth not applied")
	}
	m = f.get(InfraMachines, "worker")
	_ = unstructured.SetNestedField(m.Object, int64(8), "spec", "maxCPUs")
	f.update(InfraMachines, m)
	f.reconcile(1)
	f.expectFailure("immutable VM configuration")
	if value, _, _ := unstructured.NestedInt64(f.get(VirtualMachines, "worker").Object, "spec", "maxCPUs"); value != 4 {
		t.Fatal("changed immutable VM capacity")
	}
}

func TestDeletionWaitsForBackendAndBootstrapFinalizers(t *testing.T) {
	f := newFixture(t)
	f.reconcile(4)
	ctx := context.Background()
	vm := f.get(VirtualMachines, "worker")
	vm.SetFinalizers(append(vm.GetFinalizers(), nativeVMFinalizer))
	f.update(VirtualMachines, vm)
	f.dynamic.PrependReactor("delete", "virtualmachines", func(action ktesting.Action) (bool, runtime.Object, error) {
		deletion := action.(ktesting.DeleteAction)
		if deletion.GetDeleteOptions().Preconditions == nil || *deletion.GetDeleteOptions().Preconditions.UID != vm.GetUID() {
			t.Error("missing VM UID delete precondition")
		}
		copy := vm.DeepCopy()
		now := metav1.Now()
		copy.SetDeletionTimestamp(&now)
		return true, nil, f.dynamic.Tracker().Update(VirtualMachines, copy, testNS)
	})
	km := f.get(InfraMachines, "worker")
	now := metav1.Now()
	km.SetDeletionTimestamp(&now)
	f.update(InfraMachines, km)
	secretName := bootstrapName(f.machine())
	f.reconcile(2)
	secret, err := f.kube.CoreV1().Secrets(testNS).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil || secret.DeletionTimestamp != nil {
		t.Fatal("bootstrap deleted before VM backend cleanup")
	}
	// Simulate native cleanup finishing. Provider must remove only its own
	// guard, and still wait for NotFound before touching bootstrap credentials.
	cleaned := f.get(VirtualMachines, "worker")
	cleaned.SetFinalizers([]string{VMFinalizer})
	f.update(VirtualMachines, cleaned)
	f.reconcile(1)
	if slices.Contains(f.get(VirtualMachines, "worker").GetFinalizers(), VMFinalizer) {
		t.Fatal("provider VM guard was not released after native cleanup")
	}
	if retained, _ := f.kube.CoreV1().Secrets(testNS).Get(ctx, secretName, metav1.GetOptions{}); retained.DeletionTimestamp != nil {
		t.Fatal("bootstrap deleted before VM NotFound")
	}
	if err := f.dynamic.Tracker().Delete(VirtualMachines, testNS, "worker"); err != nil {
		t.Fatal(err)
	}
	f.kube.PrependReactor("delete", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		copy := secret.DeepCopy()
		now := metav1.Now()
		copy.DeletionTimestamp = &now
		return true, nil, f.kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("secrets"), copy, testNS)
	})
	f.reconcile(1)
	secret, _ = f.kube.CoreV1().Secrets(testNS).Get(ctx, secretName, metav1.GetOptions{})
	if secret.DeletionTimestamp == nil || !slices.Contains(secret.Finalizers, BootstrapFinalizer) {
		t.Fatal("bootstrap deletion not protected")
	}
	f.reconcile(1)
	secret, _ = f.kube.CoreV1().Secrets(testNS).Get(ctx, secretName, metav1.GetOptions{})
	if slices.Contains(secret.Finalizers, BootstrapFinalizer) {
		t.Fatal("bootstrap finalizer not released after VM disappeared")
	}
	if !slices.Contains(f.machine().Finalizers, Finalizer) {
		t.Fatal("Machine finalized before Secret NotFound")
	}
	if err := f.kube.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("secrets"), testNS, secretName); err != nil {
		t.Fatal(err)
	}
	f.reconcile(1)
	if slices.Contains(f.machine().Finalizers, Finalizer) {
		t.Fatal("Machine finalizer was not released")
	}
}

func TestVMDeletedBeforeStatusRemainsATombstone(t *testing.T) {
	f := newFixture(t)
	f.reconcile(4)
	vm := f.get(VirtualMachines, "worker")
	if !slices.Contains(vm.GetFinalizers(), VMFinalizer) {
		t.Fatal("birth protection missing")
	}
	km := f.get(InfraMachines, "worker")
	unstructured.RemoveNestedField(km.Object, "status", "vmRef")
	f.update(InfraMachines, km) // crash before the VM UID checkpoint
	now := metav1.Now()
	vm.SetDeletionTimestamp(&now)
	f.update(VirtualMachines, vm)
	before := len(f.dynamic.Actions())
	f.reconcile(2)
	if f.machine().Status.VMRef == nil || f.machine().Status.VMRef.UID != vm.GetUID() {
		t.Fatal("did not record the retained VM tombstone")
	}
	f.expectFailure("bound VirtualMachine is deleting")
	for _, action := range f.dynamic.Actions()[before:] {
		if action.GetResource() == VirtualMachines && action.GetVerb() != "get" {
			t.Fatalf("recreated or changed a tombstone: %s", action.GetVerb())
		}
	}
}

func TestCompressedImagesNeverReachNativeVMCreation(t *testing.T) {
	for _, image := range []string{
		"https://images.example/talos.raw.xz", "https://images.example/talos.raw.gz?token=download",
		"https://images.example/talos.qcow2.zst#image", "https://images.example/TALOS.RAW.XZ",
		"https://images.example/talos.raw%2Exz?file=disk.raw", "https://images.example/disk.zip",
	} {
		t.Run(image, func(t *testing.T) {
			f := newFixture(t)
			machine := f.get(InfraMachines, "worker")
			_ = unstructured.SetNestedField(machine.Object, image, "spec", "disk", "image")
			f.update(InfraMachines, machine)
			f.reconcile(3)
			f.expectFailure("uncompressed UEFI raw or qcow2")
			f.expectNoVM()
		})
	}
	f := newFixture(t)
	machine := f.get(InfraMachines, "worker")
	_ = unstructured.SetNestedField(machine.Object, "https://images.example/disk.raw?source=original.raw.xz", "spec", "disk", "image")
	f.update(InfraMachines, machine)
	f.reconcile(3)
	if f.machine().Status.VMRef == nil {
		t.Fatal("a query parameter was mistaken for the fetched image path")
	}
}

func TestClusterExternalContractAndManagedByRejection(t *testing.T) {
	f := newFixture(t)
	if err := f.c.ReconcileCluster(context.Background(), testNS, "workload"); err != nil {
		t.Fatal(err)
	}
	u := f.get(InfraClusters, "workload")
	if ready, _, _ := unstructured.NestedBool(u.Object, "status", "ready"); !ready {
		t.Fatal("valid external cluster not Ready")
	}
	u.SetAnnotations(map[string]string{"cluster.x-k8s.io/managed-by": ""})
	f.update(InfraClusters, u)
	if err := f.c.ReconcileCluster(context.Background(), testNS, "workload"); err != nil {
		t.Fatal(err)
	}
	if ready, _, _ := unstructured.NestedBool(f.get(InfraClusters, "workload").Object, "status", "ready"); ready {
		t.Fatal("managed-by cluster accepted")
	}
}
