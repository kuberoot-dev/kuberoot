package legacyvmctl

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha1"
)

func TestMachineSpecCarriesCeilings(t *testing.T) {
	ceiling := resource.MustParse("4Gi")
	v := &v1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "resize"}, Spec: v1.VirtualMachineSpec{
		CPUs: 1, MaxCPUs: 4, Memory: resource.MustParse("1Gi"), MaxMemory: &ceiling}}
	s := machineSpec(v, Plan{}, true)
	if s.CPUs != 1 || s.MaxCPUs != 4 || s.MemoryMiB != 1024 || s.MaxMemoryMiB != 4096 {
		t.Fatalf("machine spec=%+v", s)
	}
	v.Spec.CPUs, v.Spec.Memory = 2, resource.MustParse("2Gi")
	s = machineSpec(v, Plan{}, true)
	if s.CPUs != 2 || s.MemoryMiB != 2048 || s.MaxCPUs != 4 || s.MaxMemoryMiB != 4096 {
		t.Fatalf("growth changed ceiling: %+v", s)
	}
	v.Spec.MaxCPUs, v.Spec.MaxMemory = 0, nil
	s = machineSpec(v, Plan{}, true)
	if s.MaxCPUs != 0 || s.MaxMemoryMiB != 0 {
		t.Fatalf("legacy machine acquired ceilings: %+v", s)
	}
}
