package legacyvmctl

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha1"
)

func TestAddressReservationsSurviveLossOfLocalLeases(t *testing.T) {
	network := netip.MustParsePrefix("10.123.0.0/29")
	deleting := metav1.Now()
	machines := []v1.VirtualMachine{
		{ObjectMeta: metav1.ObjectMeta{Name: "new"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "old"}, Status: v1.VirtualMachineStatus{MAC: MAC("old"), Address: "10.123.0.3"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "removing", DeletionTimestamp: &deleting}, Status: v1.VirtualMachineStatus{MAC: MAC("removing"), Address: "10.123.0.4"}},
	}
	first, err := addressReservations(network, machines, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first["new"].Address != "10.123.0.5" || first["old"].Address != "10.123.0.3" || first["removing"].Address != "10.123.0.4" {
		t.Fatalf("did not preserve existing/deleting reservations: %v", first)
	}
	for i := range machines {
		r := first[machines[i].Name]
		machines[i].Status.MAC, machines[i].Status.Address = r.MAC, r.Address
	}
	second, err := addressReservations(network, machines, nil) // new leader, empty lease file
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range first {
		if second[name] != want {
			t.Errorf("%s changed address after takeover: %v -> %v", name, want, second[name])
		}
	}
}

func TestAddressReservationsRejectConflictsAndExhaustion(t *testing.T) {
	for _, tc := range []struct {
		name, address, errorText string
	}{
		{"duplicate", "10.123.0.3", "share address"},
		{"gateway", "10.123.0.1", "outside the DHCP range"},
		{"foreign subnet", "192.0.2.5", "outside the DHCP range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			machines := []v1.VirtualMachine{
				{ObjectMeta: metav1.ObjectMeta{Name: "one"}, Status: v1.VirtualMachineStatus{Address: "10.123.0.3"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "two"}, Status: v1.VirtualMachineStatus{Address: tc.address}},
			}
			_, err := addressReservations(netip.MustParsePrefix("10.123.0.0/29"), machines, nil)
			if err == nil || !strings.Contains(err.Error(), tc.errorText) {
				t.Fatalf("expected %q, got %v", tc.errorText, err)
			}
		})
	}
	var machines []v1.VirtualMachine
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		machines = append(machines, v1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}
	if _, err := addressReservations(netip.MustParsePrefix("10.123.0.0/29"), machines, nil); err == nil {
		t.Fatal("assigned five machines into four available addresses")
	}
}

func TestReservationsAreNotPublishedUntilSaved(t *testing.T) {
	machine := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": v1.GroupVersion.String(), "kind": "VirtualMachine",
		"metadata": map[string]any{"name": "one"},
	}}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{vmsGVR: "VirtualMachineList"}, machine)
	failure := errors.New("API write failed")
	dyn.PrependReactor("update", "virtualmachines", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, failure
	})
	c := &Controller{Dynamic: dyn, Network: netip.MustParsePrefix("10.123.0.0/24"), StateDir: t.TempDir()}
	reservations, err := c.reserveAddresses(context.Background())
	if !errors.Is(err, failure) || reservations != nil {
		t.Fatalf("uncommitted reservation reached DHCP: %v, %v", reservations, err)
	}
}

func TestDeletingMachineKeepsAddressWhileReplicaIsDown(t *testing.T) {
	now := metav1.Now()
	machine := &v1.VirtualMachine{TypeMeta: metav1.TypeMeta{APIVersion: v1.GroupVersion.String(), Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: "one", DeletionTimestamp: &now, Finalizers: []string{finalizer}},
		Status:     v1.VirtualMachineStatus{Node: "down", ReplicaNodes: []string{"down"}, MAC: MAC("one"), Address: "10.123.0.3", ImageWritten: true}}
	u := &unstructured.Unstructured{Object: toMap(machine)}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{vmsGVR: "VirtualMachineList"}, u)
	c := &Controller{Dynamic: dyn}
	c.cleanup(context.Background(), machine, []Node{{Name: "down"}})
	got, err := dyn.Resource(vmsGVR).Get(context.Background(), "one", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var after v1.VirtualMachine
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(got.Object, &after); err != nil {
		t.Fatal(err)
	}
	if after.Status.Address != "10.123.0.3" || !after.Status.ImageWritten || after.Status.Phase != "Deleting" {
		t.Fatalf("cleanup lost the machine's identity or reservation: %+v", after.Status)
	}
	reservations, err := addressReservations(netip.MustParsePrefix("10.123.0.0/29"), []v1.VirtualMachine{
		after, {ObjectMeta: metav1.ObjectMeta{Name: "new"}},
	}, nil)
	if err != nil || reservations["new"].Address == after.Status.Address {
		t.Fatalf("deleting machine's address reused after takeover: %v, %v", reservations, err)
	}
}

func TestPlacedMachineWithoutCheckpointRequiresLeaseAdoption(t *testing.T) {
	machines := []v1.VirtualMachine{{ObjectMeta: metav1.ObjectMeta{Name: "old"}, Status: v1.VirtualMachineStatus{Node: "a"}}}
	network := netip.MustParsePrefix("10.123.0.0/24")
	if _, err := addressReservations(network, machines, nil); err == nil {
		t.Fatal("an existing machine with an unknown address was assigned a new one")
	}
	got, err := addressReservations(network, machines, map[string]string{MAC("old"): "10.123.0.100"})
	if err != nil || got["old"].Address != "10.123.0.100" {
		t.Fatalf("could not adopt the original gateway's lease: %v, %v", got, err)
	}
}

func TestAddressReservationsCanonicalizeMAC(t *testing.T) {
	network := netip.MustParsePrefix("10.123.0.0/24")
	machines := []v1.VirtualMachine{{ObjectMeta: metav1.ObjectMeta{Name: "one"},
		Status: v1.VirtualMachineStatus{MAC: "52-54-00-AB-CD-EF", Node: "a"}}}
	got, err := addressReservations(network, machines, map[string]string{"52:54:00:ab:cd:ef": "10.123.0.100"})
	if err != nil || got["one"].MAC != "52:54:00:ab:cd:ef" || got["one"].Address != "10.123.0.100" {
		t.Fatalf("canonical MAC did not adopt its lease: %v, %v", got, err)
	}
	machines = append(machines, v1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "two"},
		Status: v1.VirtualMachineStatus{MAC: "52:54:00:ab:cd:ef"}})
	if _, err := addressReservations(network, machines, map[string]string{"52:54:00:ab:cd:ef": "10.123.0.100"}); err == nil || !strings.Contains(err.Error(), "share MAC") {
		t.Fatalf("equivalent MAC formats bypassed duplicate detection: %v", err)
	}
}
