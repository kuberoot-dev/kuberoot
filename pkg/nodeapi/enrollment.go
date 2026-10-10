package nodeapi

import (
	"context"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	fleetagent "github.com/kuberoot-dev/kuberoot/pkg/fleet"
)

const enrollmentName = "center"

// enrollmentStorage makes the node a node of a fleet, or takes it out: the
// agent calls the center the enrollment names and keeps the connection open.
type enrollmentStorage struct{ agent *fleetagent.Agent }

var (
	_ rest.Creater         = enrollmentStorage{}
	_ rest.Updater         = enrollmentStorage{}
	_ rest.GracefulDeleter = enrollmentStorage{}
)

func (enrollmentStorage) New() runtime.Object     { return &node.FleetEnrollment{} }
func (enrollmentStorage) NewList() runtime.Object { return &node.FleetEnrollmentList{} }
func (enrollmentStorage) Destroy()                {}
func (enrollmentStorage) NamespaceScoped() bool   { return false }
func (enrollmentStorage) GetSingularName() string { return "fleetenrollment" }

func (s enrollmentStorage) current() (*node.FleetEnrollment, error) {
	e, err := s.agent.Load()
	if err != nil || e == nil {
		return nil, err
	}
	obj := &node.FleetEnrollment{
		ObjectMeta: metav1.ObjectMeta{Name: enrollmentName, UID: types.UID(string(nodeUID()) + "-fleet")},
		// The token is never handed back out.
		Spec: node.FleetEnrollmentSpec{Name: e.Name, Servers: e.Servers, CAHash: e.CAHash},
	}
	st := s.agent.Status()
	obj.Status = node.FleetEnrollmentStatus{Phase: st.Phase, Message: st.Message, Server: st.Server}
	if !st.ConnectedSince.IsZero() {
		t := metav1.NewTime(st.ConnectedSince)
		obj.Status.ConnectedSince = &t
	}
	if obj.Status.Phase == "" {
		obj.Status.Phase = fleetagent.AgentDisconnected
		if !e.Enrolled {
			obj.Status.Phase = fleetagent.AgentEnrolling
		}
	}
	obj.ResourceVersion = resourceVersion(obj.Spec, obj.Status)
	return obj, nil
}

func (s enrollmentStorage) Get(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	obj, err := s.current()
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	if obj == nil || name != enrollmentName {
		return nil, apierrors.NewNotFound(node.Resource("fleetenrollments"), name)
	}
	return obj, nil
}

func (s enrollmentStorage) List(context.Context, *metainternalversion.ListOptions) (runtime.Object, error) {
	list := &node.FleetEnrollmentList{}
	if obj, err := s.current(); err == nil && obj != nil {
		list.Items = append(list.Items, *obj)
	}
	return list, nil
}

func (s enrollmentStorage) snapshot(context.Context) (map[string]runtime.Object, error) {
	out := map[string]runtime.Object{}
	if obj, err := s.current(); err == nil && obj != nil {
		out[obj.Name] = obj
	}
	return out, nil
}

func (s enrollmentStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

func (s enrollmentStorage) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	e := obj.(*node.FleetEnrollment)
	if existing, _ := s.current(); existing != nil {
		return nil, apierrors.NewAlreadyExists(node.Resource("fleetenrollments"), e.Name)
	}
	return s.apply(ctx, e)
}

func (s enrollmentStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo,
	_ rest.ValidateObjectFunc, _ rest.ValidateObjectUpdateFunc, _ bool, _ *metav1.UpdateOptions) (runtime.Object, bool, error) {
	current, _ := s.current()
	var base runtime.Object = &node.FleetEnrollment{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if current != nil {
		base = current
	}
	updated, err := objInfo.UpdatedObject(ctx, base)
	if err != nil {
		return nil, false, err
	}
	out, err := s.apply(ctx, updated.(*node.FleetEnrollment))
	return out, current == nil, err
}

func (s enrollmentStorage) apply(_ context.Context, e *node.FleetEnrollment) (runtime.Object, error) {
	if e.Name != enrollmentName {
		return nil, apierrors.NewBadRequest(`the enrollment must be named "center"`)
	}
	if e.Spec.Name == "" || len(e.Spec.Servers) == 0 || !strings.HasPrefix(e.Spec.CAHash, "sha256:") {
		return nil, apierrors.NewBadRequest("spec.name, spec.servers and spec.caHash (sha256:<hex>) are required; apply the enrollment the center issued")
	}
	next := &fleetagent.Enrollment{Name: e.Spec.Name, Servers: e.Spec.Servers, CAHash: e.Spec.CAHash, Token: e.Spec.Token}
	prev, err := s.agent.Load()
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	// The same fleet and name, with new addresses: still enrolled.
	if prev != nil && prev.Enrolled && prev.Name == next.Name && prev.CAHash == next.CAHash && next.Token == "" {
		next.Enrolled = true
	}
	if !next.Enrolled && next.Token == "" {
		return nil, apierrors.NewBadRequest("spec.token is required to enroll")
	}
	if err := s.agent.Save(next); err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	out, err := s.current()
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	return out, nil
}

func (s enrollmentStorage) Delete(ctx context.Context, name string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	current, err := s.Get(ctx, name, nil)
	if err != nil {
		return nil, false, err
	}
	if err := s.agent.Remove(); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	return current, true, nil
}

func (enrollmentStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.FleetEnrollment:
		items = []runtime.Object{o}
	case *node.FleetEnrollmentList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Fleet Name", ""), col("Phase", ""), col("Server", ""), col("Message", "")},
		items, func(o runtime.Object) []any {
			e := o.(*node.FleetEnrollment)
			return []any{e.Name, e.Spec.Name, e.Status.Phase, e.Status.Server, e.Status.Message}
		}), nil
}
