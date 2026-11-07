// Package vmmaintenance provides a cluster-wide barrier for the breaking VM
// API cutover. Acknowledgement is published only after all in-flight work in
// this controller process has completed. Standby replicas participate too.
package vmmaintenance

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	Namespace = "kube-system"
	ConfigMap = "kuberoot-vm-api-maintenance"
	Label     = "vm.kuberoot.dev/barrier"
)

// Guard must be started before leader election, so an old or unresponsive
// standby cannot be mistaken for a participant in the maintenance barrier.
type Guard struct {
	Kube          kubernetes.Interface
	Dynamic       dynamic.Interface
	Node, Service string
	PID           int
	Identity      string
	Mode          string
	Principal     string
	mu            sync.RWMutex
	allowed       bool
}

func New(ctx context.Context, kube kubernetes.Interface, client dynamic.Interface, service, mode string) (*Guard, error) {
	if mode != ModeAlpha1 && mode != ModeAlpha2 {
		return nil, fmt.Errorf("unknown controller API mode")
	}
	identity, err := kube.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("authenticate controller principal: %w", err)
	}
	principal := identity.Status.UserInfo.Username
	if principal == "" || principal == "system:anonymous" {
		return nil, fmt.Errorf("controller authenticated username is unknown")
	}
	node, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	return &Guard{Kube: kube, Dynamic: client, Node: node, Service: service, Mode: mode, Principal: principal, PID: os.Getpid(), Identity: string(uuid.NewUUID())}, nil
}

// Handler drains seed requests under the same barrier as reconciliation.
// Paused requests receive 503 and never consult partially imported bindings.
func (g *Guard) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.RLock()
		defer g.mu.RUnlock()
		if !g.allowed {
			http.Error(w, "VM API maintenance is active", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Work holds the read lock over the complete reconcile/sweep/gateway pass.
// A failed gate read closes the barrier, never leaving stale permission.
func (g *Guard) Work(ctx context.Context, work func(context.Context) error) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.allowed {
		return nil
	}
	return work(ctx)
}

func (g *Guard) Run(ctx context.Context) {
	for ctx.Err() == nil {
		_ = g.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// Refresh is public for deterministic tests of acknowledgement ordering.
func (g *Guard) Refresh(ctx context.Context) error {
	if g.Principal == "" || g.Principal == "system:anonymous" {
		return fmt.Errorf("controller authenticated principal is unknown")
	}
	if g.Mode != ModeAlpha1 && g.Mode != ModeAlpha2 {
		return fmt.Errorf("controller API mode is unknown")
	}
	cm, err := g.Kube.CoreV1().ConfigMaps(Namespace).Get(ctx, ConfigMap, metav1.GetOptions{})
	token, gateUID := "", ""
	allowed := apierrors.IsNotFound(err)
	if err == nil {
		token = cm.Data["token"]
		gateUID = string(cm.UID)
	}
	identity, identityErr := g.processIdentity(ctx)
	g.mu.Lock()
	// Taking this lock drains the last pass before we acknowledge token.
	g.allowed = allowed && identityErr == nil
	g.mu.Unlock()
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if identityErr != nil {
		return identityErr
	}
	if !allowed && token == "" {
		return fmt.Errorf("maintenance ConfigMap must contain a nonempty token")
	}
	leases := g.Kube.CoordinationV1().Leases(Namespace)
	name := LeaseName(g.Node, g.Service)
	lease, err := leases.Get(ctx, name, metav1.GetOptions{})
	create := apierrors.IsNotFound(err)
	if create {
		lease = &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace}}
	} else if err != nil {
		return err
	}
	lease.Labels = map[string]string{Label: "v1alpha2"}
	lease.Annotations = map[string]string{"node": g.Node, "service": g.Service, "pid": strconv.Itoa(g.PID), "token": token, "gate-uid": gateUID, "api-mode": g.Mode, "principal": g.Principal}
	for name, value := range identity {
		lease.Annotations[name] = value
	}
	now := metav1.NewMicroTime(time.Now())
	duration := int32(15)
	lease.Spec.HolderIdentity = &g.Identity
	lease.Spec.RenewTime = &now
	lease.Spec.LeaseDurationSeconds = &duration
	if create {
		_, err = leases.Create(ctx, lease, metav1.CreateOptions{})
	} else {
		_, err = leases.Update(ctx, lease, metav1.UpdateOptions{})
	}
	return err
}

func (g *Guard) processIdentity(ctx context.Context) (map[string]string, error) {
	if g.Dynamic == nil {
		return nil, fmt.Errorf("controller process inventory client is required")
	}
	services := schema.GroupVersionResource{Group: "node.kuberoot.dev", Version: "v1alpha1", Resource: "nodeservices"}
	u, err := g.Dynamic.Resource(services).Get(ctx, g.Node+"."+g.Service, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	identity, err := ControllerIdentity(u, g.Service)
	if err != nil {
		return nil, err
	}
	if identity["pid"] != strconv.Itoa(g.PID) {
		return nil, fmt.Errorf("this controller is not the supervised process")
	}
	return identity, nil
}

// ControllerIdentity rejects absent inventory from an old node API and any
// extra, orphaned or unverified process. Only public process identity leaves
// the node; command arguments and environment are never copied here.
func ControllerIdentity(u *unstructured.Unstructured, service string) (map[string]string, error) {
	state, _, _ := unstructured.NestedString(u.Object, "status", "state")
	pid, _, _ := unstructured.NestedInt64(u.Object, "status", "pid")
	started, _, _ := unstructured.NestedString(u.Object, "status", "startedAt")
	message, _, _ := unstructured.NestedString(u.Object, "status", "controllerInventoryError")
	observedInventory, _, _ := unstructured.NestedBool(u.Object, "status", "controllerInventoryObserved")
	processes, _, _ := unstructured.NestedSlice(u.Object, "status", "controllerProcesses")
	if state != "Running" || pid <= 0 || started == "" || !observedInventory || message != "" || len(processes) != 1 {
		return nil, fmt.Errorf("%s has missing, extra or unverified controller processes: %s", u.GetName(), message)
	}
	p, ok := processes[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid process inventory")
	}
	observed, _, _ := unstructured.NestedInt64(p, "pid")
	ticks, _, _ := unstructured.NestedInt64(p, "startTicks")
	exe, _, _ := unstructured.NestedString(p, "executable")
	boot, _, _ := unstructured.NestedString(p, "bootID")
	if observed != pid || ticks <= 0 || exe != "/usr/bin/"+service || boot == "" {
		return nil, fmt.Errorf("%s process identity does not match its supervisor", u.GetName())
	}
	return map[string]string{"pid": strconv.FormatInt(pid, 10), "service-started-at": started, "start-ticks": strconv.FormatInt(ticks, 10), "boot-id": boot, "executable": exe}, nil
}

// APIReady rejects a cluster-scoped or partially installed CRD before any
// node resource is touched. Startup never deletes/recreates a CRD.
func APIReady(ctx context.Context, client dynamic.Interface) error {
	mode, err := APIMode(ctx, client)
	if err != nil {
		return err
	}
	if mode != ModeAlpha2 {
		return fmt.Errorf("VM CRD is not namespaced v1alpha2; complete the explicit maintenance cutover first")
	}
	return nil
}

func LeaseName(node, service string) string {
	sum := sha256.Sum256([]byte(node + "/" + service))
	return fmt.Sprintf("vm-api-%x", sum[:16])
}

// BarrierUID is the immutable identity of one maintenance session.
func BarrierUID(uid types.UID, token string) string { return string(uid) + "/" + token }
