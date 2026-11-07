package nodeapi

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/vm"
)

func TestDirectMachineAPIMemoryBudget(t *testing.T) {
	oldState, oldRun := vm.StateDir, vm.RunDir
	vm.StateDir, vm.RunDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { vm.StateDir, vm.RunDir = oldState, oldRun })
	s := machineStorage{newVMHost("node-a", "10.0.0.1", 1024)}
	ctx := context.Background()
	machine := func(name string, memory int64) *node.Machine {
		return &node.Machine{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: node.MachineSpec{
			CPUs: 1, MemoryMiB: memory, Running: true, Volumes: []string{"root"}}}
	}
	receiver := machine("receiving", 128)
	receiver.Spec.Receive = "tcp:0.0.0.0:9001"
	if _, err := s.Create(ctx, receiver, nil, nil); !apierrors.IsForbidden(err) {
		t.Fatalf("direct receive bypassed unverified migration guard: %v", err)
	}
	if _, err := s.Create(ctx, machine("a", 768), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, machine("b", 512), nil, nil); !apierrors.IsForbidden(err) {
		t.Fatalf("direct create bypassed the pool: %v", err)
	}
	if _, err := s.Create(ctx, machine("b", 256), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Update(ctx, "b", rest.DefaultUpdatedObjectInfo(machine("b", 512)), nil, nil, false, nil); !apierrors.IsForbidden(err) {
		t.Fatalf("direct update bypassed the pool: %v", err)
	}
	got, err := s.h.machines.Load("b")
	if err != nil || got.MemoryMiB != 256 {
		t.Fatalf("rejected update changed persisted spec: %+v, %v", got, err)
	}
	invalid := machine("b", 0)
	if _, _, err := s.Update(ctx, "b", rest.DefaultUpdatedObjectInfo(invalid), nil, nil, false, nil); !apierrors.IsBadRequest(err) {
		t.Fatalf("invalid update accepted: %v", err)
	}
	if _, _, err := s.Delete(ctx, "a", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, machine("c", 128), nil, nil); !apierrors.IsForbidden(err) {
		t.Fatalf("asynchronous deletion released memory early: %v", err)
	}
}

func TestDirectMachineAPICeilings(t *testing.T) {
	oldState, oldRun := vm.StateDir, vm.RunDir
	vm.StateDir, vm.RunDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { vm.StateDir, vm.RunDir = oldState, oldRun })
	store := machineStorage{newVMHost("node-a", "10.0.0.1", 4096)}
	ctx := context.Background()
	m := &node.Machine{ObjectMeta: metav1.ObjectMeta{Name: "bounded"}, Spec: node.MachineSpec{
		CPUs: 1, MaxCPUs: 4, MemoryMiB: 1024, MaxMemoryMiB: 4096, Running: true, Volumes: []string{"root"}}}
	if _, err := store.Create(ctx, m, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*node.MachineSpec){
		func(s *node.MachineSpec) { s.CPUs = 5 },
		func(s *node.MachineSpec) { s.MaxCPUs = 8 },
		func(s *node.MachineSpec) { s.MaxMemoryMiB = 8192 },
		func(s *node.MachineSpec) { s.MaxMemoryMiB = 0 },
	} {
		bad := m.DeepCopy()
		change(&bad.Spec)
		if _, _, err := store.Update(ctx, m.Name, rest.DefaultUpdatedObjectInfo(bad), nil, nil, false, nil); !apierrors.IsBadRequest(err) {
			t.Fatalf("bad ceiling update was not BadRequest: %v", err)
		}
	}
	other := m.DeepCopy()
	other.Name = "other"
	other.Spec.MaxMemoryMiB = 0
	other.Spec.MemoryMiB = 128
	if _, err := store.Create(ctx, other, nil, nil); !apierrors.IsForbidden(err) {
		t.Fatalf("reserved current RAM instead of ceiling: %v", err)
	}
	grown := m.DeepCopy()
	grown.Spec.CPUs, grown.Spec.MemoryMiB = 2, 2048
	if _, _, err := store.Update(ctx, m.Name, rest.DefaultUpdatedObjectInfo(grown), nil, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.h.machines.Load(m.Name)
	if err != nil || got.CPUs != 2 || got.MemoryMiB != 2048 || got.MaxMemoryMiB != 4096 {
		t.Fatalf("persisted %+v: %v", got, err)
	}
}
