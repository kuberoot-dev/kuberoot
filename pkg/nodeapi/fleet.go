package nodeapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	nodev1 "github.com/kuberoot-dev/kuberoot/pkg/apis/node/v1alpha1"
)

// viaClusterGroup marks requests the cluster API server forwarded: they get the
// fleet view, every node of the cluster, instead of this node alone.
const viaClusterGroup = "kuberoot:via-cluster"

// NodeLabel names the node an object in the fleet view comes from.
const NodeLabel = "kuberoot.dev/node"

func viaCluster(ctx context.Context) bool {
	u, ok := genericapirequest.UserFrom(ctx)
	if !ok {
		return false
	}
	for _, g := range u.GetGroups() {
		if g == viaClusterGroup {
			return true
		}
	}
	return false
}

// markViaCluster adds viaClusterGroup to users the front proxy authenticated.
func markViaCluster(inner authenticator.Request) authenticator.Request {
	return authenticator.RequestFunc(func(r *http.Request) (*authenticator.Response, bool, error) {
		resp, ok, err := inner.AuthenticateRequest(r)
		if !ok || err != nil {
			return resp, ok, err
		}
		u := resp.User
		resp.User = &user.DefaultInfo{Name: u.GetName(), UID: u.GetUID(), Extra: u.GetExtra(),
			Groups: append(append([]string{}, u.GetGroups()...), viaClusterGroup)}
		return resp, true, nil
	})
}

type member struct{ name, address string }

// fleet reaches the node APIs of the other members of the cluster.
type fleet struct {
	self    string
	cluster kubernetes.Interface
	cert    tls.Certificate
	pool    *x509.CertPool

	mu      sync.Mutex
	clients map[string]*http.Client
	cached  []member
	fetched time.Time
}

func newFleet(self string, cluster kubernetes.Interface, caFile, certFile, keyFile string) (*fleet, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	return &fleet{self: self, cluster: cluster, cert: cert, pool: pool}, nil
}

// clientFor talks to one member and accepts only the certificate issued for
// that member's name: a node that took another's address cannot answer for it.
func (f *fleet) clientFor(m member) *http.Client {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.clients[m.name]; ok {
		return c
	}
	if f.clients == nil {
		f.clients = map[string]*http.Client{}
	}
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{f.cert}, RootCAs: f.pool,
			ServerName: MemberServerName(m.name)},
	}}
	f.clients[m.name] = c
	return c
}

func (f *fleet) members(ctx context.Context) []member {
	f.mu.Lock()
	defer f.mu.Unlock()
	if time.Since(f.fetched) < 10*time.Second {
		return f.cached
	}
	nodes, err := f.cluster.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("fleet: list nodes: %v", err)
		return f.cached
	}
	var out []member
	for _, n := range nodes.Items {
		if n.Name == f.self {
			continue
		}
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				out = append(out, member{name: n.Name, address: a.Address})
				break
			}
		}
	}
	f.cached, f.fetched = out, time.Now()
	return out
}

func (f *fleet) member(ctx context.Context, name string) (member, bool) {
	for _, m := range f.members(ctx) {
		if m.name == name {
			return m, true
		}
	}
	return member{}, false
}

func (f *fleet) do(ctx context.Context, m member, method, path string, body io.Reader) (*http.Response, error) {
	url := "https://" + net.JoinHostPort(m.address, "50000") + "/apis/node.kuberoot.dev/v1alpha1/" + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return f.clientFor(m).Do(req)
}

// call performs a request on a member and decodes the answer into the internal version.
func (f *fleet) call(ctx context.Context, m member, method, path string, in runtime.Object) (runtime.Object, error) {
	var body io.Reader
	if in != nil {
		raw, err := runtime.Encode(codecs.LegacyCodec(nodev1.SchemeGroupVersion), in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	resp, err := f.do(ctx, m, method, path, body)
	if err != nil {
		return nil, apierrors.NewServiceUnavailable(fmt.Sprintf("node %s: %v", m.name, err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	obj, err := runtime.Decode(codecs.UniversalDecoder(node.SchemeGroupVersion), raw)
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", m.name, err)
	}
	if status, ok := obj.(*metav1.Status); ok && status.Status == metav1.StatusFailure {
		return nil, &apierrors.StatusError{ErrStatus: *status}
	}
	return obj, nil
}

// fleetStorage presents one resource across every node of the cluster when a
// request comes through the cluster, and this node alone otherwise. Objects are
// named "<node>.<name>"; per-node singletons, like OSConfig, keep the node name.
type fleetStorage struct {
	resource  string
	local     rest.Storage
	f         *fleet
	singleton bool
}

func (s *fleetStorage) New() runtime.Object { return s.local.New() }
func (s *fleetStorage) NewList() runtime.Object {
	return s.local.(rest.Lister).NewList()
}
func (s *fleetStorage) Destroy()              {}
func (s *fleetStorage) NamespaceScoped() bool { return false }
func (s *fleetStorage) GetSingularName() string {
	return s.local.(rest.SingularNameProvider).GetSingularName()
}

func (s *fleetStorage) split(name string) (nodeName, local string) {
	if s.singleton {
		return name, name
	}
	nodeName, local, _ = strings.Cut(name, ".")
	return nodeName, local
}

func (s *fleetStorage) rename(obj runtime.Object, nodeName string) runtime.Object {
	obj = obj.DeepCopyObject()
	if acc, err := meta.Accessor(obj); err == nil {
		if !s.singleton {
			acc.SetName(nodeName + "." + acc.GetName())
		}
		labels := acc.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[NodeLabel] = nodeName
		acc.SetLabels(labels)
	}
	return obj
}

func (s *fleetStorage) unrename(obj runtime.Object, local string) runtime.Object {
	obj = obj.DeepCopyObject()
	if acc, err := meta.Accessor(obj); err == nil {
		acc.SetName(local)
	}
	return obj
}

func (s *fleetStorage) Get(ctx context.Context, name string, opts *metav1.GetOptions) (runtime.Object, error) {
	getter := s.local.(rest.Getter)
	if !viaCluster(ctx) {
		return getter.Get(ctx, name, opts)
	}
	nodeName, local := s.split(name)
	if nodeName == s.f.self {
		obj, err := getter.Get(ctx, local, opts)
		if err != nil {
			return nil, err
		}
		return s.rename(obj, nodeName), nil
	}
	m, ok := s.f.member(ctx, nodeName)
	if !ok {
		return nil, apierrors.NewNotFound(node.Resource(s.resource), name)
	}
	obj, err := s.f.call(ctx, m, http.MethodGet, s.resource+"/"+local, nil)
	if err != nil {
		return nil, err
	}
	return s.rename(obj, nodeName), nil
}

func (s *fleetStorage) List(ctx context.Context, opts *metainternalversion.ListOptions) (runtime.Object, error) {
	lister := s.local.(rest.Lister)
	if !viaCluster(ctx) {
		return lister.List(ctx, opts)
	}
	all, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	list := lister.NewList()
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]runtime.Object, 0, len(all))
	for _, name := range names {
		obj := all[name]
		if opts != nil && opts.LabelSelector != nil {
			if acc, err := meta.Accessor(obj); err == nil && !opts.LabelSelector.Matches(labels.Set(acc.GetLabels())) {
				continue
			}
		}
		items = append(items, obj)
	}
	return list, meta.SetList(list, items)
}

// snapshot gathers the resource from this node and every reachable member.
func (s *fleetStorage) snapshot(ctx context.Context) (map[string]runtime.Object, error) {
	out := map[string]runtime.Object{}
	add := func(list runtime.Object, nodeName string) {
		items, _ := meta.ExtractList(list)
		for _, item := range items {
			obj := s.rename(item, nodeName)
			if acc, err := meta.Accessor(obj); err == nil {
				out[acc.GetName()] = obj
			}
		}
	}
	local, err := s.local.(rest.Lister).List(ctx, &metainternalversion.ListOptions{})
	if err != nil {
		return nil, err
	}
	add(local, s.f.self)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range s.f.members(ctx) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := s.f.call(ctx, m, http.MethodGet, s.resource, nil)
			if err != nil {
				klog.V(2).Infof("fleet: %s on %s: %v", s.resource, m.name, err)
				return
			}
			mu.Lock()
			add(list, m.name)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out, nil
}

func (s *fleetStorage) Watch(ctx context.Context, opts *metainternalversion.ListOptions) (watch.Interface, error) {
	if !viaCluster(ctx) {
		return s.local.(rest.Watcher).Watch(ctx, opts)
	}
	return pollWatch(ctx, s.snapshot), nil
}

func (s *fleetStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo,
	createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceCreate bool, opts *metav1.UpdateOptions) (runtime.Object, bool, error) {
	updater, ok := s.local.(rest.Updater)
	if !ok {
		return nil, false, apierrors.NewMethodNotSupported(node.Resource(s.resource), "update")
	}
	if !viaCluster(ctx) {
		return updater.Update(ctx, name, objInfo, createValidation, updateValidation, forceCreate, opts)
	}
	current, err := s.Get(ctx, name, nil)
	if err != nil {
		return nil, false, err
	}
	updated, err := objInfo.UpdatedObject(ctx, current)
	if err != nil {
		return nil, false, err
	}
	nodeName, local := s.split(name)
	updated = s.unrename(updated, local)
	if nodeName == s.f.self {
		obj, created, err := updater.Update(ctx, local, rest.DefaultUpdatedObjectInfo(updated), createValidation, updateValidation, forceCreate, opts)
		if err != nil {
			return nil, false, err
		}
		return s.rename(obj, nodeName), created, nil
	}
	m, _ := s.f.member(ctx, nodeName)
	obj, err := s.f.call(ctx, m, http.MethodPut, s.resource+"/"+local, updated)
	if err != nil {
		return nil, false, err
	}
	return s.rename(obj, nodeName), false, nil
}

func (s *fleetStorage) Create(ctx context.Context, obj runtime.Object, validate rest.ValidateObjectFunc, opts *metav1.CreateOptions) (runtime.Object, error) {
	creater, ok := s.local.(rest.Creater)
	if !ok {
		return nil, apierrors.NewMethodNotSupported(node.Resource(s.resource), "create")
	}
	if !viaCluster(ctx) {
		return creater.Create(ctx, obj, validate, opts)
	}
	acc, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}
	nodeName, local := s.split(acc.GetName())
	obj = s.unrename(obj, local)
	if nodeName == s.f.self {
		created, err := creater.Create(ctx, obj, validate, opts)
		if err != nil {
			return nil, err
		}
		return s.rename(created, nodeName), nil
	}
	m, ok := s.f.member(ctx, nodeName)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("name the object <node>.<name>; no node %q", nodeName))
	}
	created, err := s.f.call(ctx, m, http.MethodPost, s.resource, obj)
	if err != nil {
		return nil, err
	}
	return s.rename(created, nodeName), nil
}

func (s *fleetStorage) Delete(ctx context.Context, name string, validate rest.ValidateObjectFunc, opts *metav1.DeleteOptions) (runtime.Object, bool, error) {
	deleter, ok := s.local.(rest.GracefulDeleter)
	if !ok {
		return nil, false, apierrors.NewMethodNotSupported(node.Resource(s.resource), "delete")
	}
	if !viaCluster(ctx) {
		return deleter.Delete(ctx, name, validate, opts)
	}
	nodeName, local := s.split(name)
	if nodeName == s.f.self {
		return deleter.Delete(ctx, local, validate, opts)
	}
	m, _ := s.f.member(ctx, nodeName)
	obj, err := s.f.call(ctx, m, http.MethodDelete, s.resource+"/"+local, nil)
	if err == nil && s.resource == "memberships" {
		// The node left: it no longer has credentials to remove itself.
		_ = s.f.cluster.CoreV1().Nodes().Delete(ctx, nodeName, metav1.DeleteOptions{})
	}
	return obj, true, err
}

func (s *fleetStorage) ConvertToTable(ctx context.Context, obj runtime.Object, opts runtime.Object) (*metav1.Table, error) {
	return s.local.(rest.TableConvertor).ConvertToTable(ctx, obj, opts)
}

// fleetLog streams nodeservices/<node>.<service>/log from whichever node runs the service.
// fleetLog serves a node's text stream (a service's log, a machine's
// console) from any node, as <node>.<name>.
type fleetLog struct {
	local streamGetter
	f     *fleet
	// resource and sub name the stream on a member: <resource>/<name>/<sub>.
	resource, sub string
}

type streamGetter interface {
	New() runtime.Object
	Get(ctx context.Context, name string, opts *metav1.GetOptions) (runtime.Object, error)
}

func (l *fleetLog) New() runtime.Object { return l.local.New() }
func (l *fleetLog) Destroy()            {}

func (l *fleetLog) Get(ctx context.Context, name string, opts *metav1.GetOptions) (runtime.Object, error) {
	if !viaCluster(ctx) {
		return l.local.Get(ctx, name, opts)
	}
	nodeName, svc, _ := strings.Cut(name, ".")
	if nodeName == l.f.self {
		return l.local.Get(ctx, svc, opts)
	}
	m, ok := l.f.member(ctx, nodeName)
	if !ok {
		return nil, apierrors.NewNotFound(node.Resource(l.resource), name)
	}
	return &remoteStream{f: l.f, m: m, path: l.resource + "/" + svc + "/" + l.sub}, nil
}

type remoteStream struct {
	f    *fleet
	m    member
	path string
}

func (r *remoteStream) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }
func (r *remoteStream) DeepCopyObject() runtime.Object   { c := *r; return &c }

func (r *remoteStream) InputStream(ctx context.Context, _, _ string) (io.ReadCloser, bool, string, error) {
	resp, err := r.f.do(ctx, r.m, http.MethodGet, r.path, nil)
	if err != nil {
		return nil, false, "", err
	}
	return resp.Body, false, "text/plain", nil
}
