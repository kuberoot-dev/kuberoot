package fleet

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/fleet/v1alpha1"
	nodev1 "github.com/kuberoot-dev/kuberoot/pkg/apis/node/v1alpha1"
)

var (
	nodesGVR    = v1.GroupVersion.WithResource("fleetnodes")
	rolloutsGVR = v1.GroupVersion.WithResource("fleetrollouts")
)

// Labels the center keeps on every FleetNode from what the node reports.
const (
	LabelArch   = "fleet.kuberoot.dev/arch"
	LabelDistro = "fleet.kuberoot.dev/distro"
	// AnnotationRollout names the rollout that gave a node its release.
	AnnotationRollout = "fleet.kuberoot.dev/rollout"
)

// Center keeps the fleet's FleetNodes and FleetRollouts: it issues
// enrollments, reads the connected nodes, upgrades them to their release
// and moves rollouts on.
type Center struct {
	Dyn  dynamic.Interface
	Kube kubernetes.Interface
	Hub  *Hub
	// Namespace keeps the enrollment Secrets.
	Namespace string
	// Servers are the center's addresses that enrollments give the nodes.
	Servers []string
	// CAHash pins the center in enrollments.
	CAHash string

	locks sync.Map // node name -> *sync.Mutex
	poke  chan struct{}
}

func (c *Center) lock(name string) *sync.Mutex {
	l, _ := c.locks.LoadOrStore(name, &sync.Mutex{})
	return l.(*sync.Mutex)
}

// Changed asks for a pass now, as when a node connects.
func (c *Center) Changed(string) {
	select {
	case c.pokes() <- struct{}{}:
	default:
	}
}

func (c *Center) pokes() chan struct{} {
	if c.poke == nil {
		c.poke = make(chan struct{}, 1)
	}
	return c.poke
}

// Run reconciles every few seconds, and at once when a node connects.
func (c *Center) Run(ctx context.Context) {
	c.pokes()
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		c.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.poke:
		}
	}
}

func (c *Center) pass(ctx context.Context) {
	nodes, err := c.listNodes(ctx)
	if err != nil {
		klog.Errorf("fleet: list nodes: %v", err)
		return
	}
	var wg sync.WaitGroup
	for i := range nodes {
		n := &nodes[i]
		l := c.lock(n.Name)
		if !l.TryLock() {
			continue // a slow node holds no other back
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer l.Unlock()
			if err := c.reconcileNode(ctx, n); err != nil && !apierrors.IsConflict(err) {
				klog.Errorf("fleet: %s: %v", n.Name, err)
			}
		}()
	}
	// Rollouts read the nodes as this pass left them; slow nodes finish later.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
	}
	if err := c.reconcileRollouts(ctx); err != nil {
		klog.Errorf("fleet: rollouts: %v", err)
	}
}

func (c *Center) listNodes(ctx context.Context) ([]v1.FleetNode, error) {
	list, err := c.Dyn.Resource(nodesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]v1.FleetNode, 0, len(list.Items))
	for _, u := range list.Items {
		var n v1.FleetNode
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &n); err != nil {
			klog.Errorf("fleet: node %s: %v", u.GetName(), err)
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

func (c *Center) getNode(ctx context.Context, name string) (*v1.FleetNode, error) {
	u, err := c.Dyn.Resource(nodesGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	n := &v1.FleetNode{}
	return n, runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, n)
}

func (c *Center) writeStatus(ctx context.Context, gvr schema.GroupVersionResource, obj any) error {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return err
	}
	_, err = c.Dyn.Resource(gvr).UpdateStatus(ctx, &unstructured.Unstructured{Object: raw}, metav1.UpdateOptions{})
	return err
}

// Identity is the pinned authority of an enrolled node.
func (c *Center) Identity(ctx context.Context, name string) string {
	n, err := c.getNode(ctx, name)
	if err != nil {
		return ""
	}
	return n.Status.Identity
}

// EnrollmentSecret names the Secret with a node's one-time enrollment.
func EnrollmentSecret(node string) string { return "fleet-enroll-" + node }

// Enroll checks a node's token against its enrollment, pins the node's
// authority and spends the token.
func (c *Center) Enroll(ctx context.Context, req EnrollRequest, from string) error {
	if req.Name == "" || req.Token == "" {
		return errors.New("name and token are required")
	}
	l := c.lock(req.Name)
	l.Lock()
	defer l.Unlock()
	n, err := c.getNode(ctx, req.Name)
	if err != nil {
		return errors.New("token refused") // say nothing about which nodes exist
	}
	if n.Status.Identity != "" {
		return errors.New("token refused")
	}
	sec, err := c.Kube.CoreV1().Secrets(c.Namespace).Get(ctx, EnrollmentSecret(req.Name), metav1.GetOptions{})
	if err != nil || subtle.ConstantTimeCompare(sec.Data["token"], []byte(req.Token)) != 1 {
		return errors.New("token refused")
	}
	ca, err := PEMCert([]byte(req.CA))
	if err != nil || !ca.IsCA {
		return errors.New("the node's certificate authority is not one")
	}
	n.Status.Identity = Hash(ca)
	n.Status.Phase, n.Status.Enrollment = v1.PhaseOffline, ""
	n.Status.Message = "enrolled from " + from
	if err := c.writeStatus(ctx, nodesGVR, n); err != nil {
		return fmt.Errorf("record the enrollment: %w", err)
	}
	if err := c.Kube.CoreV1().Secrets(c.Namespace).Delete(ctx, sec.Name, metav1.DeleteOptions{}); err != nil {
		klog.Errorf("fleet: spend the token of %s: %v", req.Name, err)
	}
	return nil
}

// enrollment makes sure a node that has not enrolled has a token, in a
// Secret with the FleetEnrollment to apply to the node.
func (c *Center) enrollment(ctx context.Context, n *v1.FleetNode) (string, error) {
	name := EnrollmentSecret(n.Name)
	if _, err := c.Kube.CoreV1().Secrets(c.Namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return name, nil
	} else if !apierrors.IsNotFound(err) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	manifest, err := yaml.Marshal(map[string]any{
		"apiVersion": nodev1.SchemeGroupVersion.String(),
		"kind":       "FleetEnrollment",
		"metadata":   map[string]any{"name": "center"},
		"spec":       map[string]any{"name": n.Name, "servers": c.Servers, "caHash": c.CAHash, "token": token},
	})
	if err != nil {
		return "", err
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.Namespace,
			Labels: map[string]string{"fleet.kuberoot.dev/node": n.Name},
			// Deleting the FleetNode takes its unused enrollment with it.
			OwnerReferences: []metav1.OwnerReference{{APIVersion: v1.GroupVersion.String(), Kind: "FleetNode", Name: n.Name, UID: n.UID}}},
		Data: map[string][]byte{"token": []byte(token), "enrollment.yaml": manifest},
	}
	_, err = c.Kube.CoreV1().Secrets(c.Namespace).Create(ctx, sec, metav1.CreateOptions{})
	return name, err
}

// observed is what the center reads of a node.
type observed struct {
	os       nodev1.OSConfig
	entries  []nodev1.BootEntry
	services []nodev1.NodeService
	upgrades []nodev1.Upgrade
}

const nodeAPI = "https://node/apis/node.kuberoot.dev/v1alpha1/"

func get(ctx context.Context, client *http.Client, resource string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodeAPI+resource, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resource, resp.Status)
	}
	return json.Unmarshal(raw, into)
}

func (c *Center) observe(ctx context.Context, client *http.Client) (*observed, error) {
	var o observed
	var oses nodev1.OSConfigList
	var entries nodev1.BootEntryList
	var services nodev1.NodeServiceList
	var upgrades nodev1.UpgradeList
	for resource, into := range map[string]any{"osconfigs": &oses, "bootentries": &entries, "nodeservices": &services, "upgrades": &upgrades} {
		if err := get(ctx, client, resource, into); err != nil {
			return nil, err
		}
	}
	if len(oses.Items) > 0 {
		o.os = oses.Items[0]
	}
	o.entries, o.services, o.upgrades = entries.Items, services.Items, upgrades.Items
	return &o, nil
}

// createUpgrade stages a release on the node; it reboots into it when staged.
func createUpgrade(ctx context.Context, client *http.Client, r v1.Release) error {
	u := nodev1.Upgrade{
		TypeMeta:   metav1.TypeMeta{APIVersion: nodev1.SchemeGroupVersion.String(), Kind: "Upgrade"},
		ObjectMeta: metav1.ObjectMeta{Name: UpgradeName(r)},
		Spec:       nodev1.UpgradeSpec{URL: r.URL, Sha256: r.Sha256, Reboot: true},
	}
	body, _ := json.Marshal(u)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nodeAPI+"upgrades", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		return nil
	}
	var status metav1.Status
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(raw, &status) == nil && status.Message != "" {
		return &apierrors.StatusError{ErrStatus: status}
	}
	return fmt.Errorf("create upgrade: %s", resp.Status)
}

func (c *Center) reconcileNode(ctx context.Context, n *v1.FleetNode) error {
	st := n.Status.DeepCopy()
	if st.Identity == "" {
		secret, err := c.enrollment(ctx, n)
		if err != nil {
			return err
		}
		st.Phase, st.Enrollment = v1.PhasePending, secret
		st.Message = fmt.Sprintf("apply the FleetEnrollment in Secret %s/%s to the node", c.Namespace, secret)
		return c.updateNode(ctx, n, st)
	}
	conn, ok := c.Hub.Connected(n.Name)
	if !ok {
		// What it ran when last seen stays; how it is now is not known.
		st.Phase, st.Address, st.ConnectedSince, st.Health = v1.PhaseOffline, "", nil, "Unknown"
		return c.updateNode(ctx, n, st)
	}
	since := metav1.NewTime(conn.Since.Truncate(time.Second))
	st.Phase, st.Address, st.ConnectedSince = v1.PhaseConnected, conn.From, &since
	client, err := c.Hub.Client(n.Name)
	if err != nil {
		return c.updateNode(ctx, n, st)
	}
	octx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	o, err := c.observe(octx, client)
	if err != nil {
		st.Message = "reading the node: " + err.Error()
		return c.updateNode(ctx, n, st)
	}
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	st.LastSeen = &now
	describe(st, o)
	c.relabel(ctx, n, o)
	if n.Spec.Release == nil {
		st.Update = nil
		return c.updateNode(ctx, n, st)
	}
	s := decide(*n.Spec.Release, st.Update, o.entries, o.upgrades)
	if s.Create {
		if err := createUpgrade(octx, client, *n.Spec.Release); err != nil {
			s.Phase, s.Message = v1.UpdateFailed, err.Error()
			if apierrors.IsConflict(err) || apierrors.IsServiceUnavailable(err) {
				s.Phase = v1.UpdateWaiting
			}
		} else {
			klog.Infof("fleet: %s: staging %s", n.Name, n.Spec.Release.Version)
		}
	}
	next := &v1.UpdateStatus{Version: n.Spec.Release.Version, Phase: s.Phase, Message: s.Message, Since: &now}
	if prev := st.Update; prev != nil && prev.Version == next.Version && prev.Phase == next.Phase {
		next.Since = prev.Since
	} else {
		klog.Infof("fleet: %s: %s %s: %s", n.Name, n.Spec.Release.Version, s.Phase, s.Message)
	}
	st.Update = next
	return c.updateNode(ctx, n, st)
}

// describe fills a node's status from what it reported.
func describe(st *v1.FleetNodeStatus, o *observed) {
	st.Distro, st.Release, st.Arch = o.os.Status.Distro, o.os.Status.Version, o.os.Status.Architecture
	st.Kernel, st.MemoryBytes = o.os.Status.KernelVersion, o.os.Status.MemoryTotal
	st.Slot, st.SlotState = "", ""
	for _, e := range o.entries {
		if e.Status.Booted {
			st.Slot, st.SlotState, st.Release = slotOf(e.Name), e.Status.State, e.Status.Release
		}
	}
	var down []string
	for _, s := range o.services {
		if s.Status.State != "Running" {
			down = append(down, s.Name+" "+strings.ToLower(s.Status.State))
		}
	}
	sort.Strings(down)
	st.Services = fmt.Sprintf("%d/%d running", len(o.services)-len(down), len(o.services))
	st.Health, st.Message = "Healthy", ""
	if len(down) > 0 {
		st.Health, st.Message = "Degraded", strings.Join(down, ", ")
	}
}

// relabel keeps the node's architecture and distribution as labels, for
// rollouts to select by.
func (c *Center) relabel(ctx context.Context, n *v1.FleetNode, o *observed) {
	want := map[string]string{LabelArch: o.os.Status.Architecture, LabelDistro: o.os.Status.Distro}
	patch := map[string]any{}
	for k, v := range want {
		if v != "" && n.Labels[k] != v {
			patch[k] = v
		}
	}
	if len(patch) == 0 {
		return
	}
	raw, _ := json.Marshal(map[string]any{"metadata": map[string]any{"labels": patch}})
	if _, err := c.Dyn.Resource(nodesGVR).Patch(ctx, n.Name, types.MergePatchType, raw, metav1.PatchOptions{}); err != nil {
		klog.Errorf("fleet: label %s: %v", n.Name, err)
	}
}

func (c *Center) updateNode(ctx context.Context, n *v1.FleetNode, st *v1.FleetNodeStatus) error {
	// Seen again, and nothing else new: written once a minute, not every pass.
	if st.LastSeen != nil && n.Status.LastSeen != nil && st.LastSeen.Sub(n.Status.LastSeen.Time) < time.Minute {
		seen := st.LastSeen
		st.LastSeen = n.Status.LastSeen
		if equality.Semantic.DeepEqual(*st, n.Status) {
			return nil
		}
		st.LastSeen = seen
	}
	if equality.Semantic.DeepEqual(*st, n.Status) {
		return nil
	}
	out := n.DeepCopy()
	out.Status = *st
	return c.writeStatus(ctx, nodesGVR, out)
}

func (c *Center) reconcileRollouts(ctx context.Context) error {
	list, err := c.Dyn.Resource(rolloutsGVR).List(ctx, metav1.ListOptions{})
	if err != nil || len(list.Items) == 0 {
		return err
	}
	nodes, err := c.listNodes(ctx)
	if err != nil {
		return err
	}
	views := map[string]nodeView{}
	for _, n := range nodes {
		_, connected := c.Hub.Connected(n.Name)
		views[n.Name] = nodeView{Labels: n.Labels, Connected: connected, Release: n.Spec.Release, Update: n.Status.Update,
			Rollout: n.Annotations[AnnotationRollout]}
	}
	created := map[string]metav1.Time{}
	for _, u := range list.Items {
		created[u.GetName()] = u.GetCreationTimestamp()
	}
	for _, u := range list.Items {
		var r v1.FleetRollout
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &r); err != nil {
			klog.Errorf("fleet: rollout %s: %v", u.GetName(), err)
			continue
		}
		for name, v := range views {
			v.Newer = false
			if t, ok := created[v.Rollout]; ok && v.Rollout != r.Name {
				v.Newer = r.CreationTimestamp.Before(&t) || (t.Equal(&r.CreationTimestamp) && v.Rollout > r.Name)
			}
			views[name] = v
		}
		st, assign, err := plan(&r, views, time.Now())
		if err != nil {
			st = r.Status
			st.Message = err.Error()
		}
		for _, name := range assign {
			if err := c.assign(ctx, name, r); err != nil {
				klog.Errorf("fleet: rollout %s: give %s the release: %v", r.Name, name, err)
				continue
			}
			klog.Infof("fleet: rollout %s: %s gets %s", r.Name, name, r.Spec.Release.Version)
		}
		if !equality.Semantic.DeepEqual(st, r.Status) {
			if st.Phase != r.Status.Phase || st.Wave != r.Status.Wave {
				klog.Infof("fleet: rollout %s: %s, wave %s: %s", r.Name, st.Phase, st.Wave, st.Message)
			}
			r.Status = st
			if err := c.writeStatus(ctx, rolloutsGVR, &r); err != nil && !apierrors.IsConflict(err) {
				klog.Errorf("fleet: rollout %s: %v", r.Name, err)
			}
		}
	}
	return nil
}

// assign sets a node's release to a rollout's.
func (c *Center) assign(ctx context.Context, node string, r v1.FleetRollout) error {
	raw, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{AnnotationRollout: r.Name}},
		"spec":     map[string]any{"release": r.Spec.Release},
	})
	_, err := c.Dyn.Resource(nodesGVR).Patch(ctx, node, types.MergePatchType, raw, metav1.PatchOptions{})
	return err
}

// Authorize lets a user reach a node's API through the center when the
// center's RBAC allows get on fleetnodes/proxy for that node.
func (c *Center) Authorize(ctx context.Context, user string, groups []string, node string) error {
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User: user, Groups: groups,
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Group: v1.GroupVersion.Group, Resource: "fleetnodes", Subresource: "proxy", Name: node, Verb: "get"},
	}}
	out, err := c.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	if !out.Status.Allowed {
		return fmt.Errorf("%s may not reach node %s: get fleetnodes/proxy is not allowed", user, node)
	}
	return nil
}
