package nodeapi

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kuberoot-dev/kuberoot/pkg/ai"
	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

// modelHost is the node's model servers, kept on disk and made to match
// their specs every few seconds.
type modelHost struct {
	*objects
	servers *ai.Servers
}

func newModelHost(reserve int64) *modelHost {
	return &modelHost{objects: newObjects(), servers: &ai.Servers{Reserve: reserve}}
}

// run applies every model server, each on its own, until ctx ends.
func (h *modelHost) run(ctx context.Context) {
	for {
		for _, n := range h.servers.Names() {
			h.async("modelserver/"+n, func() error {
				s, err := h.servers.Load(n)
				if err != nil {
					return err
				}
				return h.servers.Apply(ctx, n, s)
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

// modelServerStorage serves the node's model servers.
type modelServerStorage struct{ h *modelHost }

var (
	_ rest.Creater         = modelServerStorage{}
	_ rest.Updater         = modelServerStorage{}
	_ rest.GracefulDeleter = modelServerStorage{}
)

func (modelServerStorage) New() runtime.Object     { return &node.ModelServer{} }
func (modelServerStorage) NewList() runtime.Object { return &node.ModelServerList{} }
func (modelServerStorage) Destroy()                {}
func (modelServerStorage) NamespaceScoped() bool   { return false }
func (modelServerStorage) GetSingularName() string { return "modelserver" }

func (s modelServerStorage) object(name string) (*node.ModelServer, error) {
	spec, err := s.h.servers.Load(name)
	if err != nil {
		return nil, apierrors.NewNotFound(node.Resource("modelservers"), name)
	}
	m := &node.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(string(nodeUID()) + "-modelserver-" + name)},
		Spec: spec, Status: s.h.servers.Status(name, spec)}
	if e := s.h.errOf("modelserver/" + name); e != "" && m.Status.Phase != "Ready" {
		m.Status.Phase, m.Status.Message = "Failed", e
	}
	m.ResourceVersion = resourceVersion(m.Spec, m.Status)
	return m, nil
}

func (s modelServerStorage) Get(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	return s.object(name)
}

func (s modelServerStorage) List(context.Context, *metainternalversion.ListOptions) (runtime.Object, error) {
	list := &node.ModelServerList{}
	for _, n := range s.h.servers.Names() {
		if m, err := s.object(n); err == nil {
			list.Items = append(list.Items, *m)
		}
	}
	return list, nil
}

func (s modelServerStorage) snapshot(context.Context) (map[string]runtime.Object, error) {
	out := map[string]runtime.Object{}
	for _, n := range s.h.servers.Names() {
		if m, err := s.object(n); err == nil {
			out[n] = m
		}
	}
	return out, nil
}

func (s modelServerStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

func validModelServer(spec node.ModelServerSpec) error {
	if spec.URL == "" || len(spec.SHA256) != 64 || spec.Model == "" || spec.Port <= 0 {
		return apierrors.NewBadRequest("a model server needs a url, its sha256, a model name and a port")
	}
	return nil
}

func (s modelServerStorage) Create(_ context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	m := obj.(*node.ModelServer)
	if m.Name == "" {
		return nil, apierrors.NewBadRequest("a model server needs a name")
	}
	if err := validModelServer(m.Spec); err != nil {
		return nil, err
	}
	if _, err := s.h.servers.Load(m.Name); err == nil {
		return nil, apierrors.NewAlreadyExists(node.Resource("modelservers"), m.Name)
	}
	if err := s.h.servers.Save(m.Name, m.Spec); err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	s.h.changed()
	return s.object(m.Name)
}

func (s modelServerStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo,
	_ rest.ValidateObjectFunc, _ rest.ValidateObjectUpdateFunc, _ bool, _ *metav1.UpdateOptions) (runtime.Object, bool, error) {
	current, err := s.object(name)
	if err != nil {
		return nil, false, err
	}
	updated, err := objInfo.UpdatedObject(ctx, current)
	if err != nil {
		return nil, false, err
	}
	spec := updated.(*node.ModelServer).Spec
	if err := validModelServer(spec); err != nil {
		return nil, false, err
	}
	if err := s.h.servers.Save(name, spec); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	s.h.changed()
	obj, err := s.object(name)
	return obj, false, err
}

func (s modelServerStorage) Delete(_ context.Context, name string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	obj, err := s.object(name)
	if err != nil {
		return nil, false, err
	}
	l := s.h.lock("modelserver/" + name)
	l.Lock()
	defer l.Unlock()
	if err := s.h.servers.Delete(name); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	return obj, true, nil
}

func (modelServerStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.ModelServer:
		items = []runtime.Object{o}
	case *node.ModelServerList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Model", ""), col("Port", ""), col("Phase", ""),
		col("SHA256", ""), col("Downloaded", ""), col("PID", ""), col("Message", "")},
		items, func(o runtime.Object) []any {
			m := o.(*node.ModelServer)
			sha := m.Status.SHA256
			if len(sha) > 12 {
				sha = sha[:12]
			}
			return []any{m.Name, m.Spec.Model, m.Spec.Port, m.Status.Phase, sha, bytesHuman(m.Status.Downloaded), m.Status.PID, m.Status.Message}
		}), nil
}

// modelServerLog serves a model server's output, as text.
type modelServerLog struct{ h *modelHost }

func (modelServerLog) New() runtime.Object { return &node.ModelServer{} }
func (modelServerLog) Destroy()            {}

func (l modelServerLog) Get(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	if _, err := l.h.servers.Load(name); err != nil {
		return nil, apierrors.NewNotFound(node.Resource("modelservers"), name)
	}
	return &logStream{path: l.h.servers.LogFile(name)}, nil
}
