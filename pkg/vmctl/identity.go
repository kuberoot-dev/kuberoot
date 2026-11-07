package vmctl

import (
	"context"
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha2"
)

// key is an API identity; backend is a node identity. They must never be
// interchangeable: two tenants may use the same name, and importing an old
// machine changes its API UID while retaining its already running backend.
func key(m *v1.VirtualMachine) string     { return m.Namespace + "/" + m.Name }
func backend(m *v1.VirtualMachine) string { return m.Status.BackendID }

func (c *Controller) machines(namespace string) dynamic.ResourceInterface {
	return c.Dynamic.Resource(vmsGVR).Namespace(namespace)
}

func validateBindings(machines []v1.VirtualMachine) error {
	owners := map[string]string{}
	instances := map[string]string{}
	for i := range machines {
		m := &machines[i]
		if m.Namespace == "" || m.UID == "" {
			return fmt.Errorf("machine %s must have a namespace and UID", key(m))
		}
		if backend(m) == "" || len(validation.IsDNS1123Subdomain(backend(m))) != 0 || m.Status.InstanceID == "" {
			return fmt.Errorf("machine %s has no valid backend/guest identity; import its recorded binding before resuming", key(m))
		}
		if other, ok := owners[backend(m)]; ok {
			return fmt.Errorf("machines %s and %s claim backend %s", other, key(m), backend(m))
		}
		if other, ok := instances[m.Status.InstanceID]; ok {
			return fmt.Errorf("machines %s and %s share guest instance identity %s", other, key(m), m.Status.InstanceID)
		}
		owners[backend(m)], instances[m.Status.InstanceID] = key(m), key(m)
	}
	return nil
}

// boundMachines persists and validates every binding before a caller may
// touch DHCP, a node Machine, a Volume or orphan collection. A malformed or
// partially imported object stops the entire pass instead of making a live
// backend appear orphaned. CRD /status writes are unavailable to tenants.
func (c *Controller) boundMachines(ctx context.Context) ([]v1.VirtualMachine, error) {
	list, err := c.Dynamic.Resource(vmsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	machines := make([]v1.VirtualMachine, len(list.Items))
	changed := make([]bool, len(list.Items))
	for i := range list.Items {
		m := &machines[i]
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, m); err != nil {
			return nil, fmt.Errorf("decode machine: %w", err)
		}
		if m.Status.BackendID == "" && reflect.DeepEqual(m.Status, v1.VirtualMachineStatus{}) && m.UID != "" {
			m.Status.BackendID, m.Status.InstanceID = "vm-"+string(m.UID), string(m.UID)
			changed[i] = true
		}
	}
	if err := validateBindings(machines); err != nil {
		return nil, err
	}
	for i, change := range changed {
		if !change {
			continue
		}
		u, m := &list.Items[i], &machines[i]
		u.Object["status"] = toMap(&m.Status)
		if _, err := c.machines(m.Namespace).UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
			return nil, fmt.Errorf("bind machine %s: %w", key(m), err)
		}
	}
	return machines, nil
}
