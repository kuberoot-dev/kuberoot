package vmapimigrate

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kuberoot-dev/kuberoot/pkg/vmmaintenance"
)

func probeName(token string) string { return "vm-api-probe-" + Digest([]byte(token))[:16] }
func probeUser(token string) string { return "kuberoot:vm-api-probe:" + token }

func fenceMessage(cm *corev1.ConfigMap) string {
	scope := Digest([]byte(cm.Data["ownerUIDs"] + "\n" + cm.Data["secrets"]))[:16]
	return "VM API maintenance " + cm.Data["token"] + " scope " + scope + ": protected writes are paused"
}

// The probe identity has no credentials. Its namespace Role only authorizes
// ConfigMap creation; all requests made with it are explicitly dry-run. An
// administrator must be able to impersonate this random session identity.
func (c *Client) ensureProbeRBAC(ctx context.Context, cm *corev1.ConfigMap) error {
	name := probeName(cm.Data["token"])
	meta := metav1.ObjectMeta{Name: name, Namespace: vmmaintenance.Namespace, Annotations: map[string]string{"vm.kuberoot.dev/barrier-token": cm.Data["token"], "vm.kuberoot.dev/gate-uid": string(cm.UID)}}
	role := &rbacv1.Role{ObjectMeta: meta, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"create"}}}}
	roles := c.Kube.RbacV1().Roles(vmmaintenance.Namespace)
	oldRole, err := roles.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = roles.Create(ctx, role, metav1.CreateOptions{})
	} else if err == nil && (!reflect.DeepEqual(oldRole.Rules, role.Rules) || !sameProbeOwner(oldRole.ObjectMeta, cm)) {
		return fmt.Errorf("admission probe Role belongs to another session or grants unexpected permissions")
	}
	if err != nil {
		return err
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: meta, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}, Subjects: []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: probeUser(cm.Data["token"])}}}
	bindings := c.Kube.RbacV1().RoleBindings(vmmaintenance.Namespace)
	oldBinding, err := bindings.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = bindings.Create(ctx, binding, metav1.CreateOptions{})
	} else if err == nil && (!reflect.DeepEqual(oldBinding.RoleRef, binding.RoleRef) || !reflect.DeepEqual(oldBinding.Subjects, binding.Subjects) || !sameProbeOwner(oldBinding.ObjectMeta, cm)) {
		return fmt.Errorf("admission probe RoleBinding differs from this session")
	}
	return err
}

func sameProbeOwner(meta metav1.ObjectMeta, cm *corev1.ConfigMap) bool {
	return meta.Annotations["vm.kuberoot.dev/barrier-token"] == cm.Data["token"] && meta.Annotations["vm.kuberoot.dev/gate-uid"] == string(cm.UID)
}

func (c *Client) deleteProbeRBAC(ctx context.Context, cm *corev1.ConfigMap) error {
	name := probeName(cm.Data["token"])
	roles, bindings := c.Kube.RbacV1().Roles(vmmaintenance.Namespace), c.Kube.RbacV1().RoleBindings(vmmaintenance.Namespace)
	binding, err := bindings.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if !sameProbeOwner(binding.ObjectMeta, cm) {
			return fmt.Errorf("refusing to remove another session's probe binding")
		}
		uid := binding.UID
		err = bindings.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	role, err := roles.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if !sameProbeOwner(role.ObjectMeta, cm) {
			return fmt.Errorf("refusing to remove another session's probe Role")
		}
		uid := role.UID
		err = roles.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	}
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// Check every known control-plane listener directly. A load-balanced endpoint
// cannot establish policy propagation on its other backends and is rejected.
// Operators must provide the actual local listeners, not proxy/NodePort URLs.
func (c *Client) probeFence(ctx context.Context, p *Plan, cm *corev1.ConfigMap, nodes []corev1.Node) error {
	if c.ProbeClient == nil || !reflect.DeepEqual(c.APIServers, p.APIServers) {
		return fmt.Errorf("direct per-control-plane admission probe configuration is missing or differs from the plan")
	}
	seen := map[string]bool{}
	for _, node := range nodes {
		if _, cp := node.Labels["node-role.kubernetes.io/control-plane"]; !cp {
			continue
		}
		endpoint := p.APIServers[node.Name]
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() == "" || net.ParseIP(u.Hostname()) == nil {
			return fmt.Errorf("%s requires an explicit direct https://InternalIP:port API endpoint", node.Name)
		}
		matched := false
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP && net.ParseIP(address.Address) != nil && net.ParseIP(address.Address).Equal(net.ParseIP(u.Hostname())) {
				matched = true
			}
		}
		if !matched || seen[endpoint] {
			return fmt.Errorf("API probe endpoint does not uniquely identify control-plane %s", node.Name)
		}
		seen[endpoint] = true
		client, err := c.ProbeClient(endpoint, probeUser(p.Token))
		if err != nil {
			return err
		}
		resource := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(vmmaintenance.Namespace)
		object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": probeName(p.Token), "namespace": vmmaintenance.Namespace}}}
		deadline := time.Now().Add(10 * time.Second)
		for {
			_, err = resource.Create(ctx, object.DeepCopy(), metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
			if apierrors.IsForbidden(err) && strings.Contains(err.Error(), vmmaintenance.ConfigMap) && strings.Contains(err.Error(), fenceMessage(cm)) {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s did not deny the dry-run through the exact current maintenance policy (RBAC/other-policy rejection is insufficient): %v", node.Name, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	if len(seen) == 0 || len(seen) != len(p.APIServers) {
		return fmt.Errorf("API endpoint map must match every current control-plane node exactly")
	}
	return nil
}
