package wsctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	vmv1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha1"
	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/workstation/v1alpha1"
)

const (
	finalizer = "workstation.kuberoot.dev/cleanup"
	// Namespace holds the workspaces' Secrets.
	Namespace = "workstation"
	// FirstRDPPort is where the gateway's RDP ports start, one a workspace.
	FirstRDPPort = 33890
	// DefaultImage is the desktop's image when a workspace names none.
	DefaultImage = "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img"
)

var (
	workspacesGVR = v1.GroupVersion.WithResource("workspaces")
	vmsGVR        = vmv1.GroupVersion.WithResource("virtualmachines")
)

// Controller keeps each workspace's machine, credentials and status, and
// tells the gateway where each desktop is.
type Controller struct {
	Dynamic dynamic.Interface
	Kube    kubernetes.Interface
	Gateway *Gateway
	// Advertise is the address people reach the gateway at; HTTPPort its
	// port for browsers.
	Advertise string
	HTTPPort  int
	// Reachable tells whether a desktop answers; a TCP dial when nil.
	Reachable func(address string, port int) bool
	// Certificates is the gateway's serving certificate, which the Secret
	// workstation/gateway-tls replaces when there is one.
	Certificates *Certificates

	oidc     []byte // the gateway-oidc Secret's data the gateway runs with
	accessMu sync.Mutex
	accessed map[string]accessAnswer
}

type accessAnswer struct {
	ok   bool
	when time.Time
}

const (
	// TLSSecret holds the gateway's own certificate (kubernetes.io/tls).
	TLSSecret = "gateway-tls"
	// OIDCSecret turns single sign-on on; see OIDCConfig.
	OIDCSecret = "gateway-oidc"
)

func (c *Controller) Run(ctx context.Context) error {
	if c.Reachable == nil {
		c.Reachable = func(address string, port int) bool {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(address, fmt.Sprint(port)), 2*time.Second)
			if err != nil {
				return false
			}
			conn.Close()
			return true
		}
	}
	for {
		c.reconcile(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *Controller) reconcile(ctx context.Context) {
	list, err := c.Dynamic.Resource(workspacesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("workspaces: %v", err)
		return
	}
	var spaces []v1.Workspace
	used := map[int32]bool{}
	for _, u := range list.Items {
		var w v1.Workspace
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &w); err != nil {
			continue
		}
		spaces = append(spaces, w)
		if w.Status.RDPPort != 0 {
			used[w.Status.RDPPort] = true
		}
	}
	sort.Slice(spaces, func(i, j int) bool { return spaces[i].CreationTimestamp.Before(&spaces[j].CreationTimestamp) })
	routes := map[string]Route{}
	for i := range spaces {
		w := &spaces[i]
		if w.DeletionTimestamp != nil {
			c.cleanup(ctx, w)
			continue
		}
		if err := c.ensureFinalizer(ctx, w); err != nil {
			klog.Errorf("%s: %v", w.Name, err)
			continue
		}
		st, route, err := c.apply(ctx, w, used)
		if err != nil {
			st.Message = err.Error()
		}
		if route.Token != "" {
			routes[w.Name] = route
		}
		c.writeStatus(ctx, w, st)
	}
	if c.Gateway != nil {
		c.Gateway.SetRoutes(routes)
		c.gatewaySettings(ctx)
	}
}

// gatewaySettings follows the gateway's Secrets: its certificate and its
// single sign-on.
func (c *Controller) gatewaySettings(ctx context.Context) {
	secrets := c.Kube.CoreV1().Secrets(Namespace)
	if c.Certificates != nil {
		s, err := secrets.Get(ctx, TLSSecret, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			_ = c.Certificates.SetCustom(nil, nil)
		case err != nil:
			klog.Errorf("%s: %v", TLSSecret, err)
		default:
			if err := c.Certificates.SetCustom(s.Data[corev1.TLSCertKey], s.Data[corev1.TLSPrivateKeyKey]); err != nil {
				klog.Errorf("%s: %v", TLSSecret, err)
			}
		}
	}
	s, err := secrets.Get(ctx, OIDCSecret, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		klog.Errorf("%s: %v", OIDCSecret, err)
		return
	}
	var data map[string][]byte
	if err == nil {
		data = s.Data
	}
	fp := fingerprint(data)
	if bytes.Equal(fp, c.oidc) {
		return
	}
	if data == nil {
		klog.Infof("single sign-on off: browsers open workspaces by their tokens")
		c.Gateway.SetAuth(nil)
		c.oidc = fp
		return
	}
	cfg, err := configFromSecret(data)
	var a *Auth
	if err == nil {
		a, err = NewAuth(cfg, c.mayOpen)
	}
	if err != nil {
		// Closed rather than open: a broken setting must not bring the
		// tokens back.
		klog.Errorf("%s: %v; the gateway lets no one in until it is fixed", OIDCSecret, err)
		a, _ = NewAuth(OIDCConfig{IssuerURL: "https://invalid.", ClientID: "invalid"}, func(context.Context, Person, string) (bool, error) { return false, nil })
	} else {
		klog.Infof("single sign-on with %s", cfg.IssuerURL)
	}
	c.Gateway.SetAuth(a)
	c.oidc = fp
}

func fingerprint(data map[string][]byte) []byte {
	if data == nil {
		return []byte{}
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s%d:", len(k), k, len(data[k]))
		h.Write(data[k])
	}
	return h.Sum(nil)
}

// mayOpen asks the cluster whether a person may get workspaces/desktop of
// the workspace, the permission RBAC grants to open others' desktops;
// answers are kept for half a minute.
func (c *Controller) mayOpen(ctx context.Context, p Person, workspace string) (bool, error) {
	key := p.User + "\x00" + strings.Join(p.Groups, "\x00") + "\x00" + workspace
	c.accessMu.Lock()
	if a, ok := c.accessed[key]; ok && time.Since(a.when) < 30*time.Second {
		c.accessMu.Unlock()
		return a.ok, nil
	}
	c.accessMu.Unlock()
	review, err := c.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{User: p.User, Groups: p.Groups,
			ResourceAttributes: &authorizationv1.ResourceAttributes{Group: v1.GroupVersion.Group, Resource: "workspaces",
				Subresource: "desktop", Verb: "get", Name: workspace}}}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	c.accessMu.Lock()
	if c.accessed == nil || len(c.accessed) > 10000 {
		c.accessed = map[string]accessAnswer{}
	}
	c.accessed[key] = accessAnswer{review.Status.Allowed, time.Now()}
	c.accessMu.Unlock()
	return review.Status.Allowed, nil
}

// MachineName is the VirtualMachine a workspace's desktop runs in.
func MachineName(workspace string) string { return "ws-" + workspace }

// apply keeps the workspace's credentials, user data and machine, and
// reports where the desktop is.
func (c *Controller) apply(ctx context.Context, w *v1.Workspace, used map[int32]bool) (v1.WorkspaceStatus, Route, error) {
	st := v1.WorkspaceStatus{Phase: "Pending", Machine: MachineName(w.Name), RDPPort: w.Status.RDPPort,
		Credentials: Namespace + "/" + w.Name + "-access"}
	for st.RDPPort == 0 {
		for p := int32(FirstRDPPort); ; p++ {
			if !used[p] {
				st.RDPPort, used[p] = p, true
				break
			}
		}
	}
	access, err := c.access(ctx, w)
	if err != nil {
		return st, Route{}, fmt.Errorf("credentials: %w", err)
	}
	userData, err := UserData(w.Name, w.Spec.Owner, string(access.Data["password"]))
	if err != nil {
		return st, Route{}, err
	}
	if err := c.ensureSecret(ctx, w.Name+"-userdata", map[string][]byte{"userData": []byte(userData)}, false); err != nil {
		return st, Route{}, fmt.Errorf("user data: %w", err)
	}
	if err := c.ensureMachine(ctx, w); err != nil {
		return st, Route{}, fmt.Errorf("machine: %w", err)
	}
	token := string(access.Data["token"])
	base := fmt.Sprintf("https://%s/ws/%s/", net.JoinHostPort(c.Advertise, fmt.Sprint(c.HTTPPort)), w.Name)
	st.URL = base + "?token=" + token
	if c.Gateway != nil && c.Gateway.currentAuth() != nil {
		st.URL = base
	}
	st.RDP = net.JoinHostPort(c.Advertise, fmt.Sprint(st.RDPPort))

	u, err := c.Dynamic.Resource(vmsGVR).Get(ctx, MachineName(w.Name), metav1.GetOptions{})
	if err != nil {
		return st, Route{}, err
	}
	var m vmv1.VirtualMachine
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &m); err != nil {
		return st, Route{}, err
	}
	st.Node, st.Address = m.Status.Node, m.Status.Address
	st.Phase, st.Message = Phase(m.Status.Phase, m.Status.Address != "" && c.Reachable(m.Status.Address, VNCPort)), m.Status.Message
	user := w.Spec.User
	if user == "" {
		user = w.Spec.Owner
	}
	return st, Route{Address: m.Status.Address, Token: token, RDPPort: st.RDPPort, User: user, Owner: w.Spec.Owner}, nil
}

// Phase is a workspace's phase from its machine's and whether its desktop
// answers: a machine running without one is still installing it.
func Phase(machine string, desktopAnswers bool) string {
	switch {
	case machine == "Running" && desktopAnswers:
		return "Ready"
	case machine == "Running":
		return "Provisioning"
	case machine == "":
		return "Pending"
	}
	return machine
}

// access is the workspace's Secret of its token and the owner's password,
// made once.
func (c *Controller) access(ctx context.Context, w *v1.Workspace) (*corev1.Secret, error) {
	name := w.Name + "-access"
	s, err := c.Kube.CoreV1().Secrets(Namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return s, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	token, err := Token()
	if err != nil {
		return nil, err
	}
	password, err := Password()
	if err != nil {
		return nil, err
	}
	data := map[string][]byte{"token": []byte(token), "password": []byte(password), "user": []byte(w.Spec.Owner)}
	if err := c.ensureSecret(ctx, name, data, true); err != nil {
		return nil, err
	}
	return c.Kube.CoreV1().Secrets(Namespace).Get(ctx, name, metav1.GetOptions{})
}

// ensureSecret creates a Secret, or updates its data unless keep says to
// leave an existing one.
func (c *Controller) ensureSecret(ctx context.Context, name string, data map[string][]byte, keep bool) error {
	secrets := c.Kube.CoreV1().Secrets(Namespace)
	cur, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace}, Data: data}, metav1.CreateOptions{})
		return err
	}
	if err != nil || keep || reflect.DeepEqual(cur.Data, data) {
		return err
	}
	cur.Data = data
	_, err = secrets.Update(ctx, cur, metav1.UpdateOptions{})
	return err
}

// machineSpec is the VirtualMachine a workspace asks for.
func machineSpec(w *v1.Workspace) vmv1.VirtualMachineSpec {
	image := w.Spec.Image
	if image == "" {
		image = DefaultImage
	}
	spec := vmv1.VirtualMachineSpec{CPUs: w.Spec.CPUs, Memory: w.Spec.Memory, Running: w.Spec.Running,
		Replicas: w.Spec.Replicas, Node: w.Spec.Node,
		Disk:           vmv1.Disk{Size: w.Spec.Disk, Image: image},
		UserDataSecret: &vmv1.SecretRef{Namespace: Namespace, Name: w.Name + "-userdata"}}
	if spec.CPUs == 0 {
		spec.CPUs = 2
	}
	if spec.Memory.IsZero() {
		spec.Memory = resource.MustParse("2Gi")
	}
	if spec.Disk.Size.IsZero() {
		spec.Disk.Size = resource.MustParse("6Gi")
	}
	if spec.Replicas == 0 {
		spec.Replicas = 3
	}
	return spec
}

// ensureMachine creates the workspace's machine, or brings its spec to
// what the workspace asks; the disk's size and image stay as created.
func (c *Controller) ensureMachine(ctx context.Context, w *v1.Workspace) error {
	want, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ptr(machineSpec(w)))
	if err != nil {
		return err
	}
	r := c.Dynamic.Resource(vmsGVR)
	cur, err := r.Get(ctx, MachineName(w.Name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": vmv1.GroupVersion.String(), "kind": "VirtualMachine",
			"metadata": map[string]any{"name": MachineName(w.Name),
				"labels": map[string]any{"workstation.kuberoot.dev/workspace": w.Name}},
			"spec": want}}
		_, err = r.Create(ctx, obj, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	have, _, _ := unstructured.NestedMap(cur.Object, "spec")
	if disk, ok := have["disk"]; ok {
		want["disk"] = disk
	}
	var a, b vmv1.VirtualMachineSpec
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(have, &a)
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(want, &b)
	if reflect.DeepEqual(a, b) {
		return nil
	}
	cur.Object["spec"] = want
	_, err = r.Update(ctx, cur, metav1.UpdateOptions{})
	return err
}

func ptr[T any](v T) *T { return &v }

// cleanup removes a deleted workspace's machine and Secrets, then lets it
// go once the machine is gone.
func (c *Controller) cleanup(ctx context.Context, w *v1.Workspace) {
	err := c.Dynamic.Resource(vmsGVR).Delete(ctx, MachineName(w.Name), metav1.DeleteOptions{})
	if err == nil {
		return // gone on a later pass, once its nodes let it go
	}
	if !apierrors.IsNotFound(err) {
		klog.Errorf("%s: machine: %v", w.Name, err)
		return
	}
	for _, s := range []string{w.Name + "-access", w.Name + "-userdata"} {
		_ = c.Kube.CoreV1().Secrets(Namespace).Delete(ctx, s, metav1.DeleteOptions{})
	}
	u, err := c.Dynamic.Resource(workspacesGVR).Get(ctx, w.Name, metav1.GetOptions{})
	if err != nil {
		return
	}
	var keep []string
	for _, f := range u.GetFinalizers() {
		if f != finalizer {
			keep = append(keep, f)
		}
	}
	u.SetFinalizers(keep)
	_, _ = c.Dynamic.Resource(workspacesGVR).Update(ctx, u, metav1.UpdateOptions{})
}

func (c *Controller) ensureFinalizer(ctx context.Context, w *v1.Workspace) error {
	for _, f := range w.Finalizers {
		if f == finalizer {
			return nil
		}
	}
	u, err := c.Dynamic.Resource(workspacesGVR).Get(ctx, w.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	u.SetFinalizers(append(u.GetFinalizers(), finalizer))
	_, err = c.Dynamic.Resource(workspacesGVR).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

func (c *Controller) writeStatus(ctx context.Context, w *v1.Workspace, st v1.WorkspaceStatus) {
	if reflect.DeepEqual(w.Status, st) {
		return
	}
	u, err := c.Dynamic.Resource(workspacesGVR).Get(ctx, w.Name, metav1.GetOptions{})
	if err != nil {
		return
	}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&st)
	if err != nil {
		return
	}
	u.Object["status"] = raw
	if _, err := c.Dynamic.Resource(workspacesGVR).UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
		klog.V(2).Infof("status of %s: %v", w.Name, err)
	}
}
