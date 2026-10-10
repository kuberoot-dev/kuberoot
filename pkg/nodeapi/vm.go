package nodeapi

import (
	"context"
	"fmt"
	"net"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/vm"
)

// vmHost is the node's virtual machines: their volumes and the machines,
// kept on disk and made to match their specs every few seconds.
type vmHost struct {
	*objects
	volumes  *vm.Volumes
	machines *vm.Machines
}

func newVMHost(nodeName, address string) *vmHost {
	v := &vm.Volumes{Node: nodeName, Address: address}
	return &vmHost{objects: newObjects(), volumes: v, machines: &vm.Machines{Volumes: v}}
}

// run applies every volume and every machine, each on its own, skipping
// those still busy from the last pass, until ctx ends.
func (h *vmHost) run(ctx context.Context) {
	for {
		for _, n := range h.volumes.Names() {
			h.async("volume/"+n, func() error {
				s, err := h.volumes.Load(n)
				if err != nil {
					return err
				}
				return h.volumes.Apply(ctx, n, s)
			})
		}
		for _, n := range h.machines.Names() {
			h.async("machine/"+n, func() error {
				if h.machines.Deleting(n) {
					return h.machines.Delete(ctx, n)
				}
				s, err := h.machines.Load(n)
				if err != nil {
					return err
				}
				return h.machines.Apply(ctx, n, s)
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-h.poke:
		case <-time.After(10 * time.Second):
		}
	}
}

// runNetwork keeps the machines' network joined to the other nodes.
func (h *vmHost) runNetwork(ctx context.Context, kubeconfig string, self net.IP) {
	for ctx.Err() == nil {
		var peers []net.IP
		if c := clientFrom(kubeconfig); c != nil {
			if nodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
				for _, n := range nodes.Items {
					for _, a := range n.Status.Addresses {
						if ip := net.ParseIP(a.Address); a.Type == "InternalIP" && ip != nil && !ip.Equal(self) {
							peers = append(peers, ip)
						}
					}
				}
			}
		}
		if err := vm.EnsureNetwork(self, peers); err != nil {
			klog.Errorf("machines' network: %v", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(15 * time.Second):
		}
	}
}

// volumeStorage serves the node's volumes.
type volumeStorage struct{ h *vmHost }

var (
	_ rest.Creater         = volumeStorage{}
	_ rest.Updater         = volumeStorage{}
	_ rest.GracefulDeleter = volumeStorage{}
)

func (volumeStorage) New() runtime.Object     { return &node.Volume{} }
func (volumeStorage) NewList() runtime.Object { return &node.VolumeList{} }
func (volumeStorage) Destroy()                {}
func (volumeStorage) NamespaceScoped() bool   { return false }
func (volumeStorage) GetSingularName() string { return "volume" }

func (s volumeStorage) object(ctx context.Context, name string) (*node.Volume, error) {
	spec, err := s.h.volumes.Load(name)
	if err != nil {
		return nil, apierrors.NewNotFound(node.Resource("volumes"), name)
	}
	v := &node.Volume{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(string(nodeUID()) + "-volume-" + name)}, Spec: spec,
		Status: s.h.volumes.Status(ctx, name)}
	v.Status.ImageWritten = s.h.volumes.ImageWritten(name)
	if spec.Primary && spec.Image != "" && !v.Status.ImageWritten {
		v.Status.Phase = "WritingImage"
	}
	if e := s.h.errOf("volume/" + name); e != "" {
		v.Status.Phase, v.Status.Message = "Failed", e
	}
	v.ResourceVersion = resourceVersion(v.Spec, v.Status)
	return v, nil
}

func (s volumeStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	return s.object(ctx, name)
}

func (s volumeStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	list := &node.VolumeList{}
	for _, n := range s.h.volumes.Names() {
		if v, err := s.object(ctx, n); err == nil {
			list.Items = append(list.Items, *v)
		}
	}
	return list, nil
}

func (s volumeStorage) snapshot(ctx context.Context) (map[string]runtime.Object, error) {
	out := map[string]runtime.Object{}
	for _, n := range s.h.volumes.Names() {
		if v, err := s.object(ctx, n); err == nil {
			out[n] = v
		}
	}
	return out, nil
}

func (s volumeStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

func (s volumeStorage) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	v := obj.(*node.Volume)
	if v.Name == "" || v.Spec.SizeBytes <= 0 || v.Spec.Minor <= 0 || v.Spec.Port <= 0 {
		return nil, apierrors.NewBadRequest("a volume needs a name, sizeBytes, minor and port")
	}
	if _, err := s.h.volumes.Load(v.Name); err == nil {
		return nil, apierrors.NewAlreadyExists(node.Resource("volumes"), v.Name)
	}
	if err := s.h.volumes.Save(v.Name, v.Spec); err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	s.h.changed()
	return s.object(ctx, v.Name)
}

func (s volumeStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo,
	_ rest.ValidateObjectFunc, _ rest.ValidateObjectUpdateFunc, _ bool, _ *metav1.UpdateOptions) (runtime.Object, bool, error) {
	current, err := s.object(ctx, name)
	if err != nil {
		return nil, false, err
	}
	updated, err := objInfo.UpdatedObject(ctx, current)
	if err != nil {
		return nil, false, err
	}
	spec := updated.(*node.Volume).Spec
	if spec.SizeBytes != current.Spec.SizeBytes || spec.Minor != current.Spec.Minor {
		return nil, false, apierrors.NewBadRequest("a volume's size and minor do not change")
	}
	if err := s.h.volumes.Save(name, spec); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	s.h.changed()
	obj, err := s.object(ctx, name)
	return obj, false, err
}

func (s volumeStorage) Delete(ctx context.Context, name string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	obj, err := s.object(ctx, name)
	if err != nil {
		return nil, false, err
	}
	l := s.h.lock("volume/" + name)
	l.Lock()
	defer l.Unlock()
	for _, m := range s.h.machines.Names() {
		if spec, err := s.h.machines.Load(m); err == nil {
			for _, v := range spec.Volumes {
				if v == name {
					return nil, false, apierrors.NewConflict(node.Resource("volumes"), name, fmt.Errorf("machine %s uses it", m))
				}
			}
		}
	}
	if err := s.h.volumes.Delete(ctx, name); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	return obj, true, nil
}

func (volumeStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.Volume:
		items = []runtime.Object{o}
	case *node.VolumeList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Size", ""), col("Device", ""), col("Role", ""), col("Disk", ""),
		col("Quorum", ""), col("Peers", ""), col("Phase", "")},
		items, func(o runtime.Object) []any {
			v := o.(*node.Volume)
			peers := ""
			for p, st := range v.Status.PeerStates {
				peers += p + "=" + st + " "
			}
			return []any{v.Name, bytesHuman(v.Spec.SizeBytes), v.Status.Device, v.Status.Role, v.Status.DiskState, v.Status.Quorum, peers, v.Status.Phase}
		}), nil
}

// machineStorage serves the node's machines.
type machineStorage struct{ h *vmHost }

var (
	_ rest.Creater         = machineStorage{}
	_ rest.Updater         = machineStorage{}
	_ rest.GracefulDeleter = machineStorage{}
)

func (machineStorage) New() runtime.Object     { return &node.Machine{} }
func (machineStorage) NewList() runtime.Object { return &node.MachineList{} }
func (machineStorage) Destroy()                {}
func (machineStorage) NamespaceScoped() bool   { return false }
func (machineStorage) GetSingularName() string { return "machine" }

func (s machineStorage) object(name string) (*node.Machine, error) {
	spec, err := s.h.machines.Load(name)
	if err != nil {
		return nil, apierrors.NewNotFound(node.Resource("machines"), name)
	}
	m := &node.Machine{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(string(nodeUID()) + "-machine-" + name)}, Spec: spec,
		Status: s.h.machines.Status(name, spec)}
	if e := s.h.errOf("machine/" + name); e != "" && m.Status.Phase != "Running" {
		m.Status.Phase, m.Status.Message = "Failed", e
	}
	if s.h.machines.Deleting(name) {
		m.Status.Phase = "Deleting"
	}
	m.ResourceVersion = resourceVersion(m.Spec, m.Status)
	return m, nil
}

func (s machineStorage) Get(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	return s.object(name)
}

func (s machineStorage) List(context.Context, *metainternalversion.ListOptions) (runtime.Object, error) {
	list := &node.MachineList{}
	for _, n := range s.h.machines.Names() {
		if m, err := s.object(n); err == nil {
			list.Items = append(list.Items, *m)
		}
	}
	return list, nil
}

func (s machineStorage) snapshot(context.Context) (map[string]runtime.Object, error) {
	out := map[string]runtime.Object{}
	for _, n := range s.h.machines.Names() {
		if m, err := s.object(n); err == nil {
			out[n] = m
		}
	}
	return out, nil
}

func (s machineStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

func (s machineStorage) Create(_ context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	m := obj.(*node.Machine)
	if m.Name == "" || m.Spec.CPUs <= 0 || m.Spec.MemoryMiB <= 0 || len(m.Spec.Volumes) == 0 {
		return nil, apierrors.NewBadRequest("a machine needs a name, cpus, memoryMiB and volumes")
	}
	if _, err := s.h.machines.Load(m.Name); err == nil {
		return nil, apierrors.NewAlreadyExists(node.Resource("machines"), m.Name)
	}
	if err := s.h.machines.Save(m.Name, m.Spec); err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	s.h.changed()
	return s.object(m.Name)
}

func (s machineStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo,
	_ rest.ValidateObjectFunc, _ rest.ValidateObjectUpdateFunc, _ bool, _ *metav1.UpdateOptions) (runtime.Object, bool, error) {
	current, err := s.object(name)
	if err != nil {
		return nil, false, err
	}
	updated, err := objInfo.UpdatedObject(ctx, current)
	if err != nil {
		return nil, false, err
	}
	if err := s.h.machines.Save(name, updated.(*node.Machine).Spec); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	s.h.changed()
	obj, err := s.object(name)
	return obj, false, err
}

func (s machineStorage) Delete(ctx context.Context, name string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	obj, err := s.object(name)
	if err != nil {
		return nil, false, err
	}
	// The machine shuts down on the node's next pass, which then forgets it.
	if err := s.h.machines.MarkDeleting(name); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	s.h.changed()
	return obj, false, nil
}

func (machineStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.Machine:
		items = []runtime.Object{o}
	case *node.MachineList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("CPUs", ""), col("Memory", ""), col("Running", ""),
		col("Phase", ""), col("PID", ""), col("Message", "")},
		items, func(o runtime.Object) []any {
			m := o.(*node.Machine)
			return []any{m.Name, m.Spec.CPUs, fmt.Sprintf("%dMi", m.Spec.MemoryMiB), m.Spec.Running, m.Status.Phase, m.Status.PID, m.Status.Message}
		}), nil
}

// consoleStorage serves a machine's serial console, as text.
type consoleStorage struct{ h *vmHost }

func (consoleStorage) New() runtime.Object { return &node.Machine{} }
func (consoleStorage) Destroy()            {}

func (c consoleStorage) Get(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	if _, err := c.h.machines.Load(name); err != nil {
		return nil, apierrors.NewNotFound(node.Resource("machines"), name)
	}
	return &logStream{path: c.h.machines.ConsoleFile(name)}, nil
}
