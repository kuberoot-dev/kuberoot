package nodeapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/authentication/user"
	request "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/vm"
)

func TestBackendInventoryRejectsCorruptAndUnreadableCollections(t *testing.T) {
	oldState, oldRun := vm.StateDir, vm.RunDir
	vm.StateDir, vm.RunDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { vm.StateDir, vm.RunDir = oldState, oldRun })
	host := newVMHost("node", "192.0.2.1", 0)
	for _, kind := range []string{"machines", "volumes"} {
		dir := filepath.Join(vm.StateDir, kind)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		var err error
		if kind == "machines" {
			_, err = (machineStorage{host}).List(context.Background(), nil)
		} else {
			_, err = (volumeStorage{host}).List(context.Background(), nil)
		}
		if !apierrors.IsInternalError(err) {
			t.Fatalf("%s silently omitted a corrupt spec: %v", kind, err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		if kind == "machines" {
			_, err = (machineStorage{host}).List(context.Background(), nil)
		} else {
			_, err = (volumeStorage{host}).List(context.Background(), nil)
		}
		if !apierrors.IsInternalError(err) {
			t.Fatalf("%s silently treated a failed directory read as empty: %v", kind, err)
		}
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
	}
}

type inventoryStorage struct{}

func (inventoryStorage) New() runtime.Object     { return &node.Machine{} }
func (inventoryStorage) NewList() runtime.Object { return &node.MachineList{} }
func (inventoryStorage) Destroy()                {}
func (inventoryStorage) List(context.Context, *metainternalversion.ListOptions) (runtime.Object, error) {
	return &node.MachineList{Items: []node.Machine{{ObjectMeta: metav1.ObjectMeta{Name: "legacy"}}}}, nil
}
func (inventoryStorage) ConvertToTable(context.Context, runtime.Object, runtime.Object) (*metav1.Table, error) {
	return nil, nil
}

func TestNodeScopedFleetInventoryCannotOmitAnUnreachableNode(t *testing.T) {
	storage := &fleetStorage{resource: "machines", local: inventoryStorage{}, f: &fleet{self: "local", cluster: fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "unreachable"}})}}
	ctx := request.WithUser(context.Background(), &user.DefaultInfo{Name: "admin", Groups: []string{viaClusterGroup}})
	for _, name := range []string{"missing", "unreachable"} {
		_, err := storage.List(ctx, &metainternalversion.ListOptions{LabelSelector: labels.SelectorFromSet(labels.Set{NodeLabel: name})})
		if err == nil {
			t.Fatalf("%s was returned as an empty successful inventory", name)
		}
	}
	list, err := storage.List(ctx, &metainternalversion.ListOptions{LabelSelector: labels.SelectorFromSet(labels.Set{NodeLabel: "local"})})
	if err != nil {
		t.Fatal(err)
	}
	machines := list.(*node.MachineList)
	if len(machines.Items) != 1 || machines.Items[0].Name != "local.legacy" {
		t.Fatalf("local inventory=%+v", machines)
	}
}

func TestAbsentSupervisorStillReportsOrphanController(t *testing.T) {
	proc := t.TempDir()
	boot := filepath.Join(proc, "sys/kernel/random")
	if err := os.MkdirAll(boot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boot, "boot_id"), []byte("boot-1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fakeControllerProcess(t, proc, 20, "/usr/bin/kuberoot-wsctl", "kuberoot-wsctl")
	objects := map[string]runtime.Object{}
	addAbsentControllers(objects, proc)
	status := objects["kuberoot-wsctl"].(*node.NodeService).Status
	if status.State != "Absent" || status.PID != 0 || !status.ControllerInventoryObserved || len(status.ControllerProcesses) != 1 || status.ControllerProcesses[0].PID != 20 {
		t.Fatalf("orphan hidden by absent supervisor: %+v", status)
	}
	empty := objects["kuberoot-vmctl"].(*node.NodeService).Status
	if empty.State != "Absent" || !empty.ControllerInventoryObserved || len(empty.ControllerProcesses) != 0 {
		t.Fatalf("empty inventory not explicit: %+v", empty)
	}
}
