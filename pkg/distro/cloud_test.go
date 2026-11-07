package distro

import (
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Cloud must retain edge's pod and HA services when the VM services change.
// Missing one still produces a valid profile, but an unusable combined node.
func TestCloudRunsPodsPackagesAndMachines(t *testing.T) {
	p, err := Load("../../distros/cloud/profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{RoleControlPlane, RoleWorker} {
		r := p.Spec.Roles[role]
		services := map[string]Service{}
		for _, s := range r.Services {
			if _, duplicate := services[s.Name]; duplicate {
				t.Fatalf("%s repeats service %s", role, s.Name)
			}
			services[s.Name] = s
		}
		for _, name := range []string{"containerd", "kubelet", "kube-proxy", "kuberoot-node"} {
			if _, ok := services[name]; !ok {
				t.Errorf("%s has no %s", role, name)
			}
		}
		if !slices.Contains(services["kuberoot-node"].Args, "--machines") {
			t.Errorf("%s does not enable the node's VM runtime", role)
		}
		if role == RoleControlPlane {
			for _, name := range []string{"kube-scheduler", "etcd", "kubepkg-operator", "kuberoot-vmctl"} {
				if _, ok := services[name]; !ok {
					t.Errorf("control plane has no %s", name)
				}
			}
			if !r.Packages || !r.Addons || !slices.Contains(r.Generators, "etcd") {
				t.Error("cloud must bootstrap packages, VM CRDs and the etcd store")
			}
			if !slices.Contains(services["kuberoot-vmctl"].Args, "--leader-elect=true") {
				t.Error("several control planes must not own the VM gateway at once")
			}
		}
		var budgetMiB int64
		for _, arg := range services["kuberoot-node"].Args {
			if val, ok := strings.CutPrefix(arg, "--machine-memory-budget-mib="); ok {
				budgetMiB, err = strconv.ParseInt(val, 10, 64)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		reserved, ok := p.Spec.Kubelet["systemReserved"].(map[string]any)
		if !ok {
			t.Fatal("cloud needs a pod exclusion for native VM and OS memory")
		}
		q, err := resource.ParseQuantity(reserved["memory"].(string))
		if err != nil || budgetMiB <= 0 || q.Value() <= budgetMiB*1024*1024 {
			t.Errorf("%s: reserve %v must exceed VM budget %dMi and leave space for native services", role, reserved, budgetMiB)
		}
	}
	if !reflect.DeepEqual(p.Spec.Kubelet["enforceNodeAllocatable"], []any{"pods"}) {
		t.Error("pod cgroups must enforce the allocatable budget")
	}
}

func TestCloudCarriesNativeVMAPI(t *testing.T) {
	cloud, err := os.ReadFile("../../distros/cloud/addons/vm.kuberoot.dev_virtualmachines.yaml")
	if err != nil {
		t.Fatal(err)
	}
	hypervisor, err := os.ReadFile("../../distros/hypervisor/addons/vm.kuberoot.dev_virtualmachines.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(cloud) != string(hypervisor) {
		t.Error("cloud and hypervisor must serve the same VM API; run hack/update-codegen.sh")
	}
}
