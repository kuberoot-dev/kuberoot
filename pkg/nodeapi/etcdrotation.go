package nodeapi

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/etcdstore"
	"github.com/kuberoot-dev/kuberoot/pkg/pki"
	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
	"github.com/kuberoot-dev/kuberoot/pkg/upgrade"
)

// etcdRotationStorage serves the rotation of the store's certificates. The
// store keeps it, so every control plane serves the same one.
type etcdRotationStorage struct{}

var (
	_ rest.Creater         = etcdRotationStorage{}
	_ rest.GracefulDeleter = etcdRotationStorage{}
)

func (etcdRotationStorage) New() runtime.Object     { return &node.EtcdRotation{} }
func (etcdRotationStorage) NewList() runtime.Object { return &node.EtcdRotationList{} }
func (etcdRotationStorage) Destroy()                {}
func (etcdRotationStorage) NamespaceScoped() bool   { return false }
func (etcdRotationStorage) GetSingularName() string { return "etcdrotation" }

// load reads the rotation with the store's members, nil when there is none.
func (etcdRotationStorage) load(ctx context.Context) (*node.EtcdRotation, error) {
	c, err := etcdstore.Client()
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, _, err := etcdstore.LoadRotation(ctx, c)
	if err != nil {
		return nil, apierrors.NewServiceUnavailable("cluster store: " + err.Error())
	}
	if r == nil {
		return nil, nil
	}
	members, err := memberNames(ctx, c)
	if err != nil {
		return nil, apierrors.NewServiceUnavailable("cluster store: " + err.Error())
	}
	return toEtcdRotation(r, members), nil
}

func (s etcdRotationStorage) snapshot(ctx context.Context) (map[string]runtime.Object, error) {
	r, err := s.load(ctx)
	if err != nil || r == nil {
		return map[string]runtime.Object{}, err
	}
	return map[string]runtime.Object{r.Name: r}, nil
}

func (s etcdRotationStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	r, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	list := &node.EtcdRotationList{}
	if r != nil {
		list.Items = append(list.Items, *r)
	}
	return list, nil
}

func (s etcdRotationStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	r, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if r == nil || r.Name != name {
		return nil, apierrors.NewNotFound(node.Resource("etcdrotations"), name)
	}
	return r, nil
}

func (s etcdRotationStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

// Create starts a rotation; the control planes take it from there.
func (s etcdRotationStorage) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	want := obj.(*node.EtcdRotation)
	if want.Name == "" {
		return nil, apierrors.NewBadRequest("a rotation needs a name")
	}
	c, err := etcdstore.Client()
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := startRotation(ctx, c, want.Name, want.Spec.Authority); err != nil {
		return nil, err
	}
	return s.Get(ctx, want.Name, nil)
}

// startRotation writes a new rotation unless one is running.
func startRotation(ctx context.Context, c *clientv3.Client, name string, authority bool) error {
	current, rev, err := etcdstore.LoadRotation(ctx, c)
	if err != nil {
		return apierrors.NewServiceUnavailable("cluster store: " + err.Error())
	}
	if current.Active() {
		return apierrors.NewConflict(node.Resource("etcdrotations"), name,
			fmt.Errorf("rotation %s is in phase %s; one rotation runs at a time", current.Name, current.Phase))
	}
	ca, _, err := etcdstore.LoadAuthority()
	if err != nil {
		return apierrors.NewInternalError(err)
	}
	r, err := etcdstore.NewRotation(name, authority, ca.Certificate(), time.Now().UTC().Truncate(time.Second))
	if err != nil {
		return apierrors.NewInternalError(err)
	}
	ok, err := etcdstore.SaveRotation(ctx, c, r, rev)
	if err != nil {
		return apierrors.NewServiceUnavailable("cluster store: " + err.Error())
	}
	if !ok {
		return apierrors.NewConflict(node.Resource("etcdrotations"), name, errors.New("another rotation started meanwhile"))
	}
	klog.Infof("cluster store: rotation %s started (authority: %t)", name, authority)
	return nil
}

// Delete forgets a rotation that is done. One halfway cannot be stopped: it
// would leave members trusting what others no longer present, or the other
// way round; it finishes once the members it waits for are back.
func (s etcdRotationStorage) Delete(ctx context.Context, name string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	obj, err := s.Get(ctx, name, nil)
	if err != nil {
		return nil, false, err
	}
	if r := obj.(*node.EtcdRotation); r.Status.Phase != etcdstore.PhaseDone {
		return nil, false, apierrors.NewBadRequest(fmt.Sprintf("rotation %s is in phase %s: it cannot stop halfway, and finishes once every member has taken each step", name, r.Status.Phase))
	}
	c, err := etcdstore.Client()
	if err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	defer c.Close()
	if _, err := c.Delete(ctx, etcdstore.RotationKey); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	return obj, true, nil
}

func (etcdRotationStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.EtcdRotation:
		items = []runtime.Object{o}
	case *node.EtcdRotationList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Authority", ""), col("Phase", ""), col("Waiting for", ""), col("Started", "")},
		items, func(o runtime.Object) []any {
			r := o.(*node.EtcdRotation)
			var waiting []string
			for _, m := range r.Status.Members {
				if r.Status.Phase != etcdstore.PhaseDone && m.Completed != r.Status.Phase {
					waiting = append(waiting, m.Name)
				}
			}
			started := ""
			if r.Status.StartedAt != nil {
				started = r.Status.StartedAt.Format(time.RFC3339)
			}
			return []any{r.Name, r.Spec.Authority, r.Status.Phase, strings.Join(waiting, ","), started}
		}), nil
}

func toEtcdRotation(r *etcdstore.Rotation, members []string) *node.EtcdRotation {
	out := &node.EtcdRotation{
		ObjectMeta: metav1.ObjectMeta{Name: r.Name, UID: types.UID(fmt.Sprintf("etcd-rotation-%s-%d", r.Name, r.StartedAt.Unix())),
			CreationTimestamp: metav1.NewTime(r.StartedAt)},
		Spec:   node.EtcdRotationSpec{Authority: r.Authority},
		Status: node.EtcdRotationStatus{Phase: r.Phase, StartedAt: &metav1.Time{Time: r.StartedAt}},
	}
	if r.CompletedAt != nil {
		out.Status.CompletedAt = &metav1.Time{Time: *r.CompletedAt}
	}
	for _, m := range members {
		out.Status.Members = append(out.Status.Members, node.EtcdRotationMember{Name: m, Completed: r.Completed[m], Message: r.Messages[m]})
	}
	out.ResourceVersion = resourceVersion(out.Spec, out.Status)
	return out
}

// memberNames are the store's started members, by name.
func memberNames(ctx context.Context, c *clientv3.Client) ([]string, error) {
	resp, err := c.MemberList(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range resp.Members {
		if m.Name != "" {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// etcdRotator takes this control plane's steps of the store's rotations,
// one member at a time, and starts a rotation when a certificate nears
// the end of its validity.
type etcdRotator struct {
	name  string // the member's, the node's name
	kinit *supervisor.Client
	admin kubernetes.Interface
}

// rotationLock: one member takes a step at a time.
const rotationLock = "/kuberoot/etcd-rotation-lock"

func (r *etcdRotator) run(ctx context.Context) {
	for ctx.Err() == nil {
		wait := r.pass(ctx)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// pass does what this member has to do now and says when to look again.
func (r *etcdRotator) pass(ctx context.Context) time.Duration {
	all, err := storeClient(ctx)
	if err != nil {
		klog.V(2).Infof("cluster store rotation: %v", err)
		return 30 * time.Second
	}
	defer all.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	rot, rev, err := etcdstore.LoadRotation(ctx, all)
	if err != nil {
		return 30 * time.Second
	}
	members, err := memberNames(ctx, all)
	if err != nil {
		return 30 * time.Second
	}
	if !rot.Active() {
		if name, authority, due := r.renewalDue(time.Now()); due {
			if err := startRotation(ctx, all, name, authority); err != nil {
				klog.V(2).Infof("cluster store rotation: %v", err)
			}
			return 5 * time.Second
		}
		// Rotations started by hand on any control plane show up here.
		return 30 * time.Second
	}
	if rot.Advance(members, time.Now().UTC().Truncate(time.Second)) {
		if ok, err := etcdstore.SaveRotation(ctx, all, rot, rev); err == nil && ok {
			klog.Infof("cluster store: rotation %s now %s", rot.Name, rot.Phase)
		}
		return time.Second
	}
	if rot.Completed[r.name] == rot.Phase {
		return 10 * time.Second
	}
	if err := r.step(ctx, all); err != nil {
		klog.Warningf("cluster store rotation: %v", err)
		r.note(ctx, all, err.Error())
		return 30 * time.Second
	}
	return time.Second
}

// renewalDue: a rotation is to start, of the authority or of the
// certificates it issued, because a third of a validity is left.
func (r *etcdRotator) renewalDue(now time.Time) (name string, authority, due bool) {
	ca, _, err := etcdstore.LoadAuthority()
	if err != nil {
		return "", false, false
	}
	stamp := now.UTC().Format("20060102-1504")
	if etcdstore.DueForRenewal(ca.Certificate(), now) {
		return "authority-" + stamp, true, true
	}
	for _, leaf := range etcdstore.Leaves {
		if c, err := etcdstore.Certificate(leaf); err == nil && etcdstore.DueForRenewal(c, now) {
			return "renew-" + stamp, false, true
		}
	}
	return "", false, false
}

// errBusy: another member is taking its step.
var errBusy = errors.New("another member is taking its step")

// step takes this member's step of the current phase, alone among members.
func (r *etcdRotator) step(ctx context.Context, all *clientv3.Client) error {
	if onProbation() {
		return errors.New("waiting: the node runs a new release on probation")
	}
	session, err := concurrency.NewSession(all, concurrency.WithTTL(30), concurrency.WithContext(ctx))
	if err != nil {
		return err
	}
	defer session.Close()
	lock := concurrency.NewMutex(session, rotationLock)
	if err := lock.TryLock(ctx); err != nil {
		if errors.Is(err, concurrency.ErrLocked) {
			return nil
		}
		return err
	}
	defer func() { _ = lock.Unlock(context.Background()) }()
	// Read again under the lock: the phase may have moved on.
	rot, _, err := etcdstore.LoadRotation(ctx, all)
	if err != nil || !rot.Active() || rot.Completed[r.name] == rot.Phase {
		return err
	}
	if err := storeHealthy(ctx, all); err != nil {
		return fmt.Errorf("waiting: %w", err)
	}
	klog.Infof("cluster store: rotation %s, phase %s on %s", rot.Name, rot.Phase, r.name)
	if err := r.apply(ctx, rot); err != nil {
		return fmt.Errorf("%s: %w", rot.Phase, err)
	}
	return r.update(ctx, all, func(cur *etcdstore.Rotation) {
		if cur.Name != rot.Name || cur.Phase != rot.Phase {
			return
		}
		if cur.Completed == nil {
			cur.Completed = map[string]string{}
		}
		cur.Completed[r.name] = cur.Phase
		delete(cur.Messages, r.name)
	})
}

// apply changes this member's credentials for the phase and restarts what
// reads them once at start: etcd its trust, the API server its trust of
// etcd. Certificates alone etcd and its clients read on each connection.
func (r *etcdRotator) apply(ctx context.Context, rot *etcdstore.Rotation) error {
	switch rot.Phase {
	case etcdstore.PhaseTrusting:
		next, err := pki.ParseCertificates([]byte(rot.NewCA))
		if err != nil {
			return err
		}
		if err := etcdstore.Trust(next...); err != nil {
			return err
		}
		return r.restart(ctx)
	case etcdstore.PhaseReissuing:
		if rot.Authority {
			keyPEM, err := etcdstore.Unseal(rot.NewCAKey)
			if err != nil {
				return fmt.Errorf("the new authority's key: %w", err)
			}
			old, err := pki.ParseCertificates([]byte(rot.OldCA))
			if err != nil {
				return err
			}
			if err := etcdstore.WriteAuthority([]byte(rot.NewCA), keyPEM, old...); err != nil {
				return err
			}
		}
		ca, _, err := etcdstore.LoadAuthority()
		if err != nil {
			return err
		}
		// etcd and its clients read their certificates on each connection:
		// new connections present the new ones, open ones are not cut.
		return etcdstore.ReissueLeaves(ca)
	case etcdstore.PhaseDropping:
		if err := etcdstore.Trust(); err != nil {
			return err
		}
		return r.restart(ctx)
	}
	return fmt.Errorf("unknown phase %q", rot.Phase)
}

// restart restarts the API server and the member under it. The API server
// goes first: it leaves the kubernetes Service while etcd still answers and
// keeps serving what it has in flight, so clients move to the other control
// planes before etcd stops; it takes clients again once etcd is back.
func (r *etcdRotator) restart(ctx context.Context) error {
	apiAsked := time.Now()
	if err := r.kinit.Restart(ctx, "kube-apiserver"); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(deregisterTime):
	}
	etcdAsked := time.Now()
	if err := r.kinit.Restart(ctx, "etcd"); err != nil {
		return err
	}
	if err := r.await(ctx, "etcd", etcdAsked, func(ctx context.Context) error {
		c, err := etcdstore.Client()
		if err != nil {
			return err
		}
		defer c.Close()
		_, err = c.Get(ctx, etcdstore.RotationKey)
		return err
	}); err != nil {
		return err
	}
	return r.await(ctx, "kube-apiserver", apiAsked, func(ctx context.Context) error {
		body, err := r.admin.Discovery().RESTClient().Get().AbsPath("/readyz").DoRaw(ctx)
		if err == nil && string(body) != "ok" {
			err = fmt.Errorf("readyz: %s", body)
		}
		return err
	})
}

// deregisterTime is how long the API server is given to leave the
// kubernetes Service, and kube-proxy to follow, before etcd stops.
const deregisterTime = 3 * time.Second

// await waits until a service restarted after asked and ready says it serves.
func (r *etcdRotator) await(ctx context.Context, name string, asked time.Time, ready func(context.Context) error) error {
	deadline := time.Now().Add(3 * time.Minute)
	var last error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		if !restartedSince(ctx, r.kinit, name, asked) {
			last = fmt.Errorf("%s has not restarted", name)
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		last = ready(pctx)
		cancel()
		if last == nil {
			klog.Infof("cluster store rotation: %s restarted and serves", name)
			return nil
		}
	}
	return fmt.Errorf("%s does not serve after its restart: %v", name, last)
}

func restartedSince(ctx context.Context, kinit *supervisor.Client, name string, t time.Time) bool {
	services, err := kinit.Services(ctx)
	if err != nil {
		return false
	}
	for _, s := range services {
		if s.Name == name {
			return s.State == supervisor.StateRunning && s.StartedAt.After(t)
		}
	}
	return false
}

// note records why this member has not completed the phase.
func (r *etcdRotator) note(ctx context.Context, all *clientv3.Client, msg string) {
	_ = r.update(ctx, all, func(cur *etcdstore.Rotation) {
		if cur.Messages == nil {
			cur.Messages = map[string]string{}
		}
		cur.Messages[r.name] = msg
	})
}

// update changes the stored rotation, again if another member wrote it meanwhile.
func (r *etcdRotator) update(ctx context.Context, all *clientv3.Client, change func(*etcdstore.Rotation)) error {
	for range 10 {
		cur, rev, err := etcdstore.LoadRotation(ctx, all)
		if err != nil || cur == nil {
			return err
		}
		change(cur)
		ok, err := etcdstore.SaveRotation(ctx, all, cur, rev)
		if err != nil || ok {
			return err
		}
	}
	return errors.New("the rotation keeps changing under this member")
}

// storeClient reaches every member, so that a session survives this
// member's restart.
func storeClient(ctx context.Context) (*clientv3.Client, error) {
	local, err := etcdstore.Client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := local.MemberList(ctx)
	local.Close()
	if err != nil {
		return nil, err
	}
	var endpoints []string
	for _, m := range resp.Members {
		endpoints = append(endpoints, m.ClientURLs...)
	}
	if len(endpoints) == 0 {
		return nil, errors.New("the store lists no client addresses")
	}
	return etcdstore.Client(endpoints...)
}

// storeHealthy: every member answers and votes, so restarting one keeps the
// majority.
func storeHealthy(ctx context.Context, all *clientv3.Client) error {
	resp, err := all.MemberList(ctx)
	if err != nil {
		return err
	}
	for _, m := range resp.Members {
		if m.Name == "" || m.IsLearner {
			return fmt.Errorf("member %x has not caught up yet", m.ID)
		}
		if len(m.ClientURLs) == 0 {
			return fmt.Errorf("member %s lists no client address", m.Name)
		}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st, err := all.Status(sctx, m.ClientURLs[0])
		cancel()
		if err != nil {
			return fmt.Errorf("member %s does not answer: %v", m.Name, err)
		}
		if len(st.Errors) > 0 {
			return fmt.Errorf("member %s: %s", m.Name, strings.Join(st.Errors, "; "))
		}
	}
	return nil
}

// onProbation: the node runs a new release that is judged by its services
// not restarting; restarts wait until it is judged.
func onProbation() bool {
	if upgrade.CurrentSlot() == "" {
		return false
	}
	entries, err := upgrade.Entries()
	if err != nil {
		return false
	}
	on, _ := upgrade.Probation(entries)
	return on
}

// etcdCertificateStorage shows this control plane's credentials for the
// store: the authority and what it issued, and when each is renewed.
type etcdCertificateStorage struct{}

func (etcdCertificateStorage) New() runtime.Object     { return &node.EtcdCertificate{} }
func (etcdCertificateStorage) NewList() runtime.Object { return &node.EtcdCertificateList{} }
func (etcdCertificateStorage) Destroy()                {}
func (etcdCertificateStorage) NamespaceScoped() bool   { return false }
func (etcdCertificateStorage) GetSingularName() string { return "etcdcertificate" }

func (etcdCertificateStorage) list() (*node.EtcdCertificateList, error) {
	list := &node.EtcdCertificateList{}
	ca, trusted, err := etcdstore.LoadAuthority()
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	authority := toEtcdCertificate(etcdstore.CA, ca.Certificate())
	for _, t := range trusted {
		authority.Status.Trusted = append(authority.Status.Trusted, t.Subject.String()+", serial "+t.SerialNumber.Text(16))
	}
	list.Items = append(list.Items, *authority)
	for _, name := range etcdstore.Leaves {
		c, err := etcdstore.Certificate(name)
		if err != nil {
			return nil, apierrors.NewInternalError(err)
		}
		list.Items = append(list.Items, *toEtcdCertificate(name, c))
	}
	for i := range list.Items {
		list.Items[i].ResourceVersion = resourceVersion(list.Items[i].Status)
	}
	return list, nil
}

func toEtcdCertificate(name string, c *x509.Certificate) *node.EtcdCertificate {
	life := c.NotAfter.Sub(c.NotBefore)
	return &node.EtcdCertificate{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(string(nodeUID()) + "-etcd-certificate-" + name),
			CreationTimestamp: metav1.NewTime(c.NotBefore)},
		Status: node.EtcdCertificateStatus{
			Subject: c.Subject.String(), Issuer: c.Issuer.String(),
			NotBefore: metav1.NewTime(c.NotBefore), NotAfter: metav1.NewTime(c.NotAfter),
			RenewAfter: metav1.NewTime(c.NotAfter.Add(-life / 3)),
		},
	}
}

func (s etcdCertificateStorage) List(context.Context, *metainternalversion.ListOptions) (runtime.Object, error) {
	return s.list()
}

func (s etcdCertificateStorage) Get(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	list, err := s.list()
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Name == name {
			return &list.Items[i], nil
		}
	}
	return nil, apierrors.NewNotFound(node.Resource("etcdcertificates"), name)
}

func (etcdCertificateStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.EtcdCertificate:
		items = []runtime.Object{o}
	case *node.EtcdCertificateList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Issuer", ""), col("Not after", ""), col("Renew after", ""), col("Also trusted", "")},
		items, func(o runtime.Object) []any {
			c := o.(*node.EtcdCertificate)
			return []any{c.Name, c.Status.Issuer, c.Status.NotAfter.Format(time.RFC3339), c.Status.RenewAfter.Format(time.RFC3339), len(c.Status.Trusted)}
		}), nil
}
