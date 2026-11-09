package capikuberoot

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infra "github.com/kuberoot-dev/kuberoot/pkg/apis/infrastructure/v1alpha1"
)

// Cluster API writes the owner reference in whichever version of its own API
// it currently serves. Matching on group and version meant that the day core
// moved from v1beta1 to v1beta2 no owner would ever be found: every machine
// would wait for an owner that was already there, and the reconciler would
// report waiting rather than anything being wrong.
func TestOwnerIsFoundWhateverVersionCoreServes(t *testing.T) {
	for _, version := range []string{"v1beta1", "v1beta2", "v1beta3"} {
		object := &metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "cluster.x-k8s.io/" + version, Kind: "Machine", Name: "worker", UID: "u1"},
		}}
		owner, err := capiOwner(object, "Machine")
		if err != nil {
			t.Errorf("%s: owner must be found: %v", version, err)
			continue
		}
		if owner.Name != "worker" {
			t.Errorf("%s: wrong owner %q", version, owner.Name)
		}
	}
}

// Another group's object of the same kind is not our owner.
func TestOwnerOfAnotherGroupIsNotTaken(t *testing.T) {
	object := &metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{
		{APIVersion: "example.org/v1beta2", Kind: "Machine", Name: "worker", UID: "u1"},
	}}
	if _, err := capiOwner(object, "Machine"); err == nil {
		t.Error("an owner from another group must not be accepted")
	}
}

// The two names for the same fact must never disagree: core reads one of them
// and this provider the other, and a drift would have them disagreeing about
// whether a machine exists, with nothing to report it.
func TestProvisionedIsWrittenUnderBothNames(t *testing.T) {
	var machine infra.KuberootMachineStatus
	setMachineProvisioned(&machine, true)
	if !machine.Ready || machine.Initialization == nil || !machine.Initialization.Provisioned {
		t.Errorf("provisioned must set both names: %+v", machine)
	}
	setMachineProvisioned(&machine, false)
	if machine.Ready || machine.Initialization == nil || machine.Initialization.Provisioned {
		t.Errorf("not provisioned must clear both names: %+v", machine)
	}

	var cluster infra.KuberootClusterStatus
	setClusterProvisioned(&cluster, true)
	if !cluster.Ready || cluster.Initialization == nil || !cluster.Initialization.Provisioned {
		t.Errorf("provisioned must set both names: %+v", cluster)
	}
}
