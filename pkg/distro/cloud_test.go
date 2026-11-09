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

// The API server's soft memory limit must stay above the live heap a platform
// the size of Cozystack keeps there, around 800 MiB. Held at a limit it cannot
// meet, Go collects without pause; the server grows slow enough that its watch
// cache falls behind the store, and controllers start reading versions older
// than the ones they have just written. A DaemonSet controller reading a stale
// DaemonSet recreates the pods it has just created, for as long as it lasts.
func TestCloudLeavesTheAPIServerRoomToCollect(t *testing.T) {
	p, err := Load("../../distros/cloud/profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	const floorMiB = 1536
	for _, role := range []string{RoleControlPlane} {
		var limit string
		for _, s := range p.Spec.Roles[role].Services {
			if s.Name != "kube-apiserver" {
				continue
			}
			for _, e := range s.Env {
				if v, ok := strings.CutPrefix(e, "GOMEMLIMIT="); ok {
					limit = v
				}
			}
		}
		if limit == "" {
			t.Fatalf("%s: the API server runs without a memory limit", role)
		}
		q, err := resource.ParseQuantity(strings.TrimSuffix(limit, "B"))
		if err != nil {
			t.Fatalf("%s: GOMEMLIMIT %q: %v", role, limit, err)
		}
		if q.Value() < floorMiB*1024*1024 {
			t.Errorf("%s: GOMEMLIMIT %s leaves the collector no room above the live heap; %dMiB is the measured floor", role, limit, floorMiB)
		}
	}
}
