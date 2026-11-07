package vmctl

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha2"
)

func cleanupFixture() (*Controller, *v1.VirtualMachine, *fake.FakeDynamicClient) {
	now := metav1.Now()
	m := &v1.VirtualMachine{TypeMeta: metav1.TypeMeta{APIVersion: v1.GroupVersion.String(), Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: "one", DeletionTimestamp: &now, Finalizers: []string{finalizer}},
		Status:     v1.VirtualMachineStatus{BackendID: "one", InstanceID: "old-one", Node: "a", ReplicaNodes: []string{"a"}, MAC: MAC("one"), Address: "10.123.0.3"}}
	machine := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "node.kuberoot.dev/v1alpha1", "kind": "Machine", "metadata": map[string]any{"name": "a.one"},
	}}
	// The Volume is deliberately missing: its absence cannot prove that
	// the asynchronous Machine deletion has stopped the guest.
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), &unstructured.Unstructured{Object: toMap(m)}, machine)
	return &Controller{Dynamic: dyn}, m, dyn
}

func assertCleanupHeld(t *testing.T, dyn *fake.FakeDynamicClient) {
	t.Helper()
	u, err := dyn.Resource(vmsGVR).Get(context.Background(), "one", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	address, _, _ := unstructured.NestedString(u.Object, "status", "address")
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	if len(u.GetFinalizers()) != 1 || address != "10.123.0.3" || phase != "Deleting" {
		t.Fatalf("cleanup released a possibly running guest: %v", u.Object)
	}
}

func TestCleanupWaitsForAsynchronousMachineDeletion(t *testing.T) {
	c, m, dyn := cleanupFixture()
	dyn.PrependReactor("delete", "machines", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil // accepted; the node still reports the Machine
	})
	c.cleanup(context.Background(), m, []Node{{Name: "a", Ready: true}})
	assertCleanupHeld(t, dyn)
	if err := dyn.Tracker().Delete(machinesGVR, "", "a.one"); err != nil {
		t.Fatal(err)
	}
	c.cleanup(context.Background(), m, []Node{{Name: "a", Ready: true}})
	u, err := dyn.Resource(vmsGVR).Get(context.Background(), "one", metav1.GetOptions{})
	if err != nil || len(u.GetFinalizers()) != 0 {
		t.Fatalf("confirmed deletion did not release the finalizer: %v, %v", u, err)
	}
}

func TestCleanupKeepsReservationOnNodeErrors(t *testing.T) {
	for _, verb := range []string{"delete", "get"} {
		t.Run(verb, func(t *testing.T) {
			c, m, dyn := cleanupFixture()
			dyn.PrependReactor(verb, "machines", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("node API unavailable")
			})
			c.cleanup(context.Background(), m, []Node{{Name: "a", Ready: true}})
			assertCleanupHeld(t, dyn)
		})
	}
}

func TestCleanupDoesNotTreatMissingNodeAsFenced(t *testing.T) {
	c, m, dyn := cleanupFixture()
	c.cleanup(context.Background(), m, nil)
	assertCleanupHeld(t, dyn)
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "delete" {
			t.Fatalf("attempted deletion through a missing node: %v", a)
		}
	}
}
