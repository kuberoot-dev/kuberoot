package nodeapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"

	"github.com/tym83/kuberoot/pkg/apis/node"
	"github.com/tym83/kuberoot/pkg/membership"
	"github.com/tym83/kuberoot/pkg/supervisor"
	"github.com/tym83/kuberoot/pkg/upgrade"
)

// bootEntryStorage shows the A/B slots; setting spec.preferred rolls back or forward.
type bootEntryStorage struct{}

func (bootEntryStorage) New() runtime.Object     { return &node.BootEntry{} }
func (bootEntryStorage) NewList() runtime.Object { return &node.BootEntryList{} }
func (bootEntryStorage) Destroy()                {}
func (bootEntryStorage) NamespaceScoped() bool   { return false }
func (bootEntryStorage) GetSingularName() string { return "bootentry" }

func (bootEntryStorage) snapshot(context.Context) (map[string]runtime.Object, error) {
	if upgrade.CurrentSlot() == "" {
		return map[string]runtime.Object{}, nil // a live or development system has no slots
	}
	entries, err := upgrade.Entries()
	if err != nil {
		return nil, apierrors.NewServiceUnavailable(err.Error())
	}
	out := map[string]runtime.Object{}
	for _, e := range entries {
		st := node.BootEntryStatus{Release: e.Release, State: e.State, TriesLeft: int32(e.Left), Booted: e.Booted, Default: e.Default, SortVersion: int32(e.Version)}
		name := "slot-" + e.Slot
		out[name] = &node.BootEntry{
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(string(nodeUID()) + "-" + name), ResourceVersion: resourceVersion(st)},
			Status:     st,
		}
	}
	return out, nil
}

func (s bootEntryStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	all, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if obj, ok := all[name]; ok {
		return obj, nil
	}
	return nil, apierrors.NewNotFound(node.Resource("bootentries"), name)
}

func (s bootEntryStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	all, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	list := &node.BootEntryList{}
	for _, obj := range all {
		list.Items = append(list.Items, *obj.(*node.BootEntry))
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list, nil
}

func (s bootEntryStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

func (s bootEntryStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo,
	_ rest.ValidateObjectFunc, _ rest.ValidateObjectUpdateFunc, _ bool, _ *metav1.UpdateOptions) (runtime.Object, bool, error) {
	current, err := s.Get(ctx, name, nil)
	if err != nil {
		return nil, false, err
	}
	updated, err := objInfo.UpdatedObject(ctx, current)
	if err != nil {
		return nil, false, err
	}
	if updated.(*node.BootEntry).Spec.Preferred {
		if err := upgrade.Prefer(name[len("slot-"):]); err != nil {
			return nil, false, apierrors.NewBadRequest(err.Error())
		}
	}
	obj, err := s.Get(ctx, name, nil)
	return obj, false, err
}

func (bootEntryStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.BootEntry:
		items = []runtime.Object{o}
	case *node.BootEntryList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Release", ""), col("State", ""), col("Tries Left", ""), col("Booted", ""), col("Default", "")},
		items, func(o runtime.Object) []any {
			e := o.(*node.BootEntry)
			return []any{e.Name, e.Status.Release, e.Status.State, e.Status.TriesLeft, e.Status.Booted, e.Status.Default}
		}), nil
}

// upgradeStorage downloads and stages releases, one at a time.
type upgradeStorage struct {
	kinit *supervisor.Client
	mu    sync.Mutex
	items map[string]*node.Upgrade
}

func (s *upgradeStorage) New() runtime.Object     { return &node.Upgrade{} }
func (s *upgradeStorage) NewList() runtime.Object { return &node.UpgradeList{} }
func (s *upgradeStorage) Destroy()                {}
func (s *upgradeStorage) NamespaceScoped() bool   { return false }
func (s *upgradeStorage) GetSingularName() string { return "upgrade" }

func (s *upgradeStorage) Create(_ context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	u := obj.(*node.Upgrade).DeepCopy()
	if u.Name == "" || u.Spec.URL == "" {
		return nil, apierrors.NewBadRequest("metadata.name and spec.url are required")
	}
	if upgrade.CurrentSlot() == "" {
		return nil, apierrors.NewBadRequest("not an installed system: install first")
	}
	// The inactive slot is the fallback while the running one is on probation;
	// writing over it would leave nothing known-good to return to.
	entries, err := upgrade.Entries()
	if err != nil {
		return nil, apierrors.NewServiceUnavailable(err.Error())
	}
	for _, e := range entries {
		if e.Booted && e.State != upgrade.StateGood {
			return nil, apierrors.NewConflict(node.Resource("upgrades"), u.Name,
				fmt.Errorf("slot %s is still on probation; wait until it is Good before the next upgrade", e.Slot))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]*node.Upgrade{}
	}
	if _, ok := s.items[u.Name]; ok {
		return nil, apierrors.NewAlreadyExists(node.Resource("upgrades"), u.Name)
	}
	for _, other := range s.items {
		if other.Status.Phase != "Staged" && other.Status.Phase != "Failed" {
			return nil, apierrors.NewConflict(node.Resource("upgrades"), u.Name, fmt.Errorf("upgrade %s is still running", other.Name))
		}
	}
	u.UID, u.CreationTimestamp = uuid.NewUUID(), metav1.Now()
	u.Status = node.UpgradeStatus{Phase: "Downloading"}
	u.ResourceVersion = resourceVersion(u.Status)
	s.items[u.Name] = u
	go s.run(u.Name, u.Spec)
	return u.DeepCopy(), nil
}

func (s *upgradeStorage) set(name string, fn func(*node.UpgradeStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.items[name].Status)
	s.items[name].ResourceVersion = resourceVersion(s.items[name].Status)
}

func (s *upgradeStorage) run(name string, spec node.UpgradeSpec) {
	fail := func(err error) {
		s.set(name, func(st *node.UpgradeStatus) { st.Phase, st.Message = "Failed", err.Error() })
	}
	a, err := upgrade.Fetch(context.Background(), spec.URL, spec.Sha256, func(done, total int64) {
		s.set(name, func(st *node.UpgradeStatus) {
			st.Message = fmt.Sprintf("downloaded %d MiB", done>>20)
			if total > 0 {
				st.Progress = int32(50 * done / total)
			}
		})
	})
	if err != nil {
		fail(err)
		return
	}
	s.set(name, func(st *node.UpgradeStatus) { st.Phase, st.Release = "Writing", a.Version })
	slot, err := upgrade.Stage(a, func(done, total int64) {
		s.set(name, func(st *node.UpgradeStatus) {
			st.Message = fmt.Sprintf("writing the inactive slot: %d of %d MiB", done>>20, total>>20)
			st.Progress = 50 + int32(45*done/max(total, 1))
		})
	})
	if err != nil {
		fail(err)
		return
	}
	_ = os.RemoveAll("/var/lib/kuberoot/upgrade")
	s.set(name, func(st *node.UpgradeStatus) {
		st.Phase, st.Slot, st.Progress = "Staged", slot, 100
		st.Message = fmt.Sprintf("slot %s boots next, with %d attempts to come up healthy", slot, upgrade.Tries)
	})
	if spec.Reboot {
		time.Sleep(3 * time.Second)
		_ = s.kinit.Reboot(context.Background())
	}
}

func (s *upgradeStorage) snapshot(context.Context) (map[string]runtime.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]runtime.Object{}
	for name, u := range s.items {
		out[name] = u.DeepCopy()
	}
	return out, nil
}

func (s *upgradeStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	all, _ := s.snapshot(ctx)
	if obj, ok := all[name]; ok {
		return obj, nil
	}
	return nil, apierrors.NewNotFound(node.Resource("upgrades"), name)
}

func (s *upgradeStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	all, _ := s.snapshot(ctx)
	list := &node.UpgradeList{}
	for _, obj := range all {
		list.Items = append(list.Items, *obj.(*node.Upgrade))
	}
	return list, nil
}

func (s *upgradeStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

func (s *upgradeStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.Upgrade:
		items = []runtime.Object{o}
	case *node.UpgradeList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Release", ""), col("Phase", ""), col("Progress", ""), col("Message", "")},
		items, func(o runtime.Object) []any {
			u := o.(*node.Upgrade)
			return []any{u.Name, u.Status.Release, u.Status.Phase, fmt.Sprintf("%d%%", u.Status.Progress), u.Status.Message}
		}), nil
}

// assessBoot decides whether a slot on probation stays. Healthy means every
// service running without restarts and the cluster API answering, for a full
// window; otherwise the node reboots and systemd-boot spends another attempt.
func assessBoot(ctx context.Context, kinit *supervisor.Client, nodeName string) {
	if upgrade.CurrentSlot() == "" {
		return
	}
	entries, err := upgrade.Entries()
	if err != nil {
		klog.Errorf("boot assessment: %v", err)
		return
	}
	onProbation, fallback := upgrade.Probation(entries)
	if !onProbation {
		return
	}
	klog.Infof("boot assessment: slot %s is on probation", upgrade.CurrentSlot())
	const window, deadline = 30 * time.Second, 5 * time.Minute
	start := time.Now()
	var healthySince time.Time
	var restarts int
	for time.Since(start) < deadline {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		ok, total := healthy(ctx, kinit, nodeName)
		if !ok || total != restarts {
			healthySince, restarts = time.Time{}, total
			continue
		}
		if healthySince.IsZero() {
			healthySince = time.Now()
		}
		if time.Since(healthySince) >= window {
			if _, err := upgrade.MarkGood(); err != nil {
				klog.Errorf("boot assessment: mark good: %v", err)
				return
			}
			klog.Infof("boot assessment: slot %s is good", upgrade.CurrentSlot())
			return
		}
	}
	if !fallback {
		// Rebooting would only boot this slot again.
		klog.Errorf("boot assessment: slot %s did not come up healthy and no other slot ever did; staying", upgrade.CurrentSlot())
		return
	}
	klog.Errorf("boot assessment: slot %s did not come up healthy, rebooting", upgrade.CurrentSlot())
	_ = kinit.Reboot(ctx)
}

func healthy(ctx context.Context, kinit *supervisor.Client, nodeName string) (bool, int) {
	statuses, err := kinit.Services(ctx)
	if err != nil {
		return false, 0
	}
	restarts := 0
	for _, st := range statuses {
		restarts += st.Restarts
		if st.State != supervisor.StateRunning {
			return false, restarts
		}
	}
	// A control plane that joined runs its own API server: it is healthy as
	// any cluster is, not as a worker with only the kubelet's credentials.
	if m, _ := membership.Load(); m != nil && m.Role != membership.RoleControlPlane {
		return memberReady(ctx, nodeName), restarts
	}
	return clusterReady(), restarts
}

// memberReady: a member has no API server of its own; it is healthy when the
// cluster sees it Ready, asked with the kubelet's own credentials.
func memberReady(ctx context.Context, nodeName string) bool {
	client := clientFrom("/var/lib/kubelet/kubeconfig")
	if client == nil {
		return false
	}
	n, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// clusterReady: the control plane node is healthy when its API server is ready.
func clusterReady() bool {
	const pki = "/var/lib/kuberoot/pki/"
	cert, err := tls.LoadX509KeyPair(pki+"admin.crt", pki+"admin.key")
	if err != nil {
		return false
	}
	ca, err := os.ReadFile(pki + "ca.crt")
	if err != nil {
		return false
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool}}}
	resp, err := client.Get("https://127.0.0.1:6443/readyz")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
