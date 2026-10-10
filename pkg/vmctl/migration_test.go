package vmctl

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha1"
)

func TestMigratingOnlyWhenEverythingIsReady(t *testing.T) {
	ready := map[string]Node{"a": {Name: "a", Ready: true}, "b": {Name: "b", Ready: true}, "c": {Name: "c", Ready: true}}
	current := map[string]disk{"a": {diskState: "UpToDate"}, "b": {diskState: "UpToDate"}, "c": {diskState: "Inconsistent"}}
	p := Plan{Node: "a", ReplicaNodes: []string{"a", "b", "c"}, Minor: 100}
	vm := func(target, phase, failed string) *v1.VirtualMachine {
		return &v1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "web"}, Spec: v1.VirtualMachineSpec{Node: target},
			Status: v1.VirtualMachineStatus{Node: "a", Phase: phase, FailedMigration: failed}}
	}
	c := &Controller{}
	for _, tc := range []struct {
		name string
		vm   *v1.VirtualMachine
		want bool
	}{
		{"asked for, all ready", vm("b", "Running", ""), true},
		{"already there", vm("a", "Running", ""), false},
		{"not a replica", vm("d", "Running", ""), false},
		{"copy not current", vm("c", "Running", ""), false},
		{"not running yet", vm("b", "Starting", ""), false},
		{"failed before", vm("b", "Running", "b"), false},
	} {
		if got := c.migrating(tc.vm, p, true, ready, current); got != tc.want {
			t.Errorf("%s: migrating = %v", tc.name, got)
		}
	}
	under := vm("b", "Migrating", "")
	under.Status.Migration = &v1.Migration{Target: "b", Phase: "Sending"}
	if !c.migrating(under, p, true, ready, current) {
		t.Error("a move under way stopped")
	}
	if migrationPort(p) != 9000 {
		t.Errorf("port %d", migrationPort(p))
	}
}

func TestBothPrimary(t *testing.T) {
	if bothPrimary(map[string]disk{"a": {role: "Primary"}, "b": {role: "Secondary"}}) {
		t.Fatal("one primary taken for two")
	}
	if !bothPrimary(map[string]disk{"a": {role: "Primary"}, "b": {role: "Primary"}}) {
		t.Fatal("two primaries not seen")
	}
}
