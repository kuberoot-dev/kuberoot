package vmapimigrate

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/kuberoot-dev/kuberoot/pkg/vmmaintenance"
)

type Client struct {
	Kube        kubernetes.Interface
	Dynamic     dynamic.Interface
	Discovery   discovery.DiscoveryInterface
	APIServers  map[string]string
	ProbeClient func(endpoint, username string) (dynamic.Interface, error)
}

var (
	policies = schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicies"}
	bindings = schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicybindings"}
	services = schema.GroupVersionResource{Group: "node.kuberoot.dev", Version: "v1alpha1", Resource: "nodeservices"}
)

func (c *Client) username(ctx context.Context) (string, error) {
	identity, err := c.Kube.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	name := identity.Status.UserInfo.Username
	if name == "" || name == "system:anonymous" {
		return "", fmt.Errorf("cutover requires an authenticated administrator")
	}
	return name, nil
}

// Begin closes the API write fence and controller barrier. It is idempotent
// for the same authenticated executor; it never removes a previous session.
// Operators must leave both fences in place after any later failure.
func (c *Client) Begin(ctx context.Context) (*corev1.ConfigMap, error) {
	user, err := c.username(ctx)
	if err != nil {
		return nil, err
	}
	if err = c.checkDedicatedExecutor(ctx, user); err != nil {
		return nil, err
	}
	cms := c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace)
	cm, err := cms.Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm, err = cms.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: vmmaintenance.ConfigMap, Namespace: vmmaintenance.Namespace}, Data: map[string]string{"token": string(uuid.NewUUID()), "executor": user}}, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	if cm.Data["executor"] != user || cm.Data["token"] == "" {
		return nil, fmt.Errorf("another or malformed maintenance session already exists")
	}
	if err = c.ensureProbeRBAC(ctx, cm); err != nil {
		return nil, err
	}
	if err = c.fence(ctx, cm, true); err != nil {
		return nil, err
	}
	return cm, nil
}

// Reject shared controller credentials before creating a gate that would
// otherwise be owned by the wrong executor and prevent a corrected retry.
func (c *Client) checkDedicatedExecutor(ctx context.Context, executor string) error {
	nodes, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, node := range nodes.Items {
		_, cp := node.Labels["node-role.kubernetes.io/control-plane"]
		for _, service := range []string{"kuberoot-vmctl", "kuberoot-wsctl"} {
			u, err := c.Dynamic.Resource(services).Get(ctx, node.Name+"."+service, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("transition inventory required before prepare: %w", err)
			}
			state, _, _ := unstructured.NestedString(u.Object, "status", "state")
			observed, _, _ := unstructured.NestedBool(u.Object, "status", "controllerInventoryObserved")
			message, _, _ := unstructured.NestedString(u.Object, "status", "controllerInventoryError")
			processes, _, _ := unstructured.NestedSlice(u.Object, "status", "controllerProcesses")
			pid, _, _ := unstructured.NestedInt64(u.Object, "status", "pid")
			if state == "Absent" && pid == 0 && observed && message == "" && len(processes) == 0 && (!cp || service == "kuberoot-wsctl") {
				continue
			}
			identity, err := vmmaintenance.ControllerIdentity(u, service)
			if err != nil {
				return err
			}
			lease, err := c.Kube.CoordinationV1().Leases(vmmaintenance.Namespace).Get(ctx, vmmaintenance.LeaseName(node.Name, service), metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("guarded transition controller required before prepare: %w", err)
			}
			for field, value := range identity {
				if lease.Annotations[field] != value {
					return fmt.Errorf("controller principal acknowledgement does not match its current process")
				}
			}
			principal := lease.Annotations["principal"]
			if principal == "" || principal == "system:anonymous" || lease.Spec.RenewTime == nil || time.Since(lease.Spec.RenewTime.Time) > 10*time.Second {
				return fmt.Errorf("controller authenticated principal is unknown or stale")
			}
			if principal == executor {
				return fmt.Errorf("migration requires a dedicated executor identity: %s on %s uses the same authenticated username", service, node.Name)
			}
		}
	}
	return nil
}

func (c *Client) fence(ctx context.Context, cm *corev1.ConfigMap, create bool) error {
	quoted, _ := json.Marshal(cm.Data["executor"])
	owners := []string{}
	secrets := []string{}
	if raw := cm.Data["ownerUIDs"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &owners); err != nil {
			return err
		}
	}
	if raw := cm.Data["secrets"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &secrets); err != nil {
			return err
		}
	}
	ownerJSON, _ := json.Marshal(owners)
	secretJSON, _ := json.Marshal(secrets)
	owner := func(object string) string {
		return "(" + object + " != null && has(" + object + ".metadata) && has(" + object + ".metadata.ownerReferences) && " + object + ".metadata.ownerReferences.exists(r, r.uid in " + string(ownerJSON) + "))"
	}
	protected := "(request.resource.group == '' && request.resource.resource == 'configmaps' && request.namespace == 'kube-system' && request.name == '" + probeName(cm.Data["token"]) + "') || (request.resource.group == 'vm.kuberoot.dev' && request.resource.resource == 'virtualmachines') || (request.resource.group == 'workstation.kuberoot.dev' && request.resource.resource == 'workspaces') || (request.resource.group == '' && request.resource.resource == 'secrets' && request.namespace + '/' + request.name in " + string(secretJSON) + ") || " + owner("object") + " || " + owner("oldObject")
	expression := "request.userInfo.username == " + string(quoted) + " || !(" + protected + ")"
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicy",
		"metadata": map[string]any{"name": vmmaintenance.ConfigMap, "annotations": map[string]any{"vm.kuberoot.dev/barrier-token": cm.Data["token"]}},
		"spec": map[string]any{"failurePolicy": "Fail", "matchConstraints": map[string]any{"matchPolicy": "Equivalent", "namespaceSelector": map[string]any{}, "objectSelector": map[string]any{}, "resourceRules": []any{map[string]any{
			"apiGroups": []any{"*"}, "apiVersions": []any{"*"},
			"scope": "*", "operations": []any{"CREATE", "UPDATE", "DELETE"}, "resources": []any{"*/*"}}}},
			"validations": []any{map[string]any{"expression": expression, "message": fenceMessage(cm), "reason": "Forbidden"}}}}}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicyBinding",
		"metadata": map[string]any{"name": vmmaintenance.ConfigMap, "annotations": map[string]any{"vm.kuberoot.dev/barrier-token": cm.Data["token"]}},
		"spec":     map[string]any{"policyName": vmmaintenance.ConfigMap, "validationActions": []any{"Deny"}}}}
	for i, obj := range []*unstructured.Unstructured{policy, binding} {
		resource := policies
		if i == 1 {
			resource = bindings
		}
		old, err := c.Dynamic.Resource(resource).Get(ctx, obj.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) && create {
			_, err = c.Dynamic.Resource(resource).Create(ctx, obj, metav1.CreateOptions{})
		} else if err == nil {
			if old.GetAnnotations()["vm.kuberoot.dev/barrier-token"] != cm.Data["token"] {
				return fmt.Errorf("maintenance admission fence belongs to another session")
			}
			if !reflect.DeepEqual(old.Object["spec"], obj.Object["spec"]) {
				if !create {
					return fmt.Errorf("maintenance admission fence differs from this session")
				}
				old.Object["spec"] = obj.Object["spec"]
				_, err = c.Dynamic.Resource(resource).Update(ctx, old, metav1.UpdateOptions{})
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// VerifyBarrier checks every actual supervised controller on every reachable
// node, including nonleaders. A missing control-plane service or old process
// without a fresh acknowledgement is a hard rejection, never an empty pass.
func (c *Client) VerifyBarrier(ctx context.Context, p *Plan) error {
	return c.verifyBarrierWithMode(ctx, p, true, "")
}

func (c *Client) verifyBarrier(ctx context.Context, p *Plan, requireFence bool) error {
	return c.verifyBarrierWithMode(ctx, p, requireFence, "")
}

func (c *Client) verifyBarrierWithMode(ctx context.Context, p *Plan, requireFence bool, requiredMode string) error {
	user, err := c.username(ctx)
	if err != nil {
		return err
	}
	if user != p.Executor {
		return fmt.Errorf("executor differs from the prepared plan")
	}
	ns, err := c.Kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if ns.UID != p.ClusterUID {
		return fmt.Errorf("cluster UID differs from the prepared plan")
	}
	cm, err := c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if cm.UID != p.GateUID || cm.Data["token"] != p.Token || cm.Data["executor"] != p.Executor {
		return fmt.Errorf("maintenance session changed")
	}
	if len(p.Machines) > 0 {
		owners, secrets := fenceScope(p)
		if cm.Data["ownerUIDs"] != owners || cm.Data["secrets"] != secrets {
			return fmt.Errorf("maintenance protection scope differs from the immutable plan")
		}
	}
	if requireFence {
		if err = c.fence(ctx, cm, false); err != nil {
			return err
		}
	}
	nodes, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	participants := 0
	controlPlanes := 0
	for _, node := range nodes.Items {
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				ready = condition.Status == corev1.ConditionTrue
			}
		}
		if !ready {
			return fmt.Errorf("node %s is not Ready; its controller processes cannot be fenced", node.Name)
		}
		_, cp := node.Labels["node-role.kubernetes.io/control-plane"]
		if cp {
			controlPlanes++
		}
		for _, service := range []string{"kuberoot-vmctl", "kuberoot-wsctl"} {
			observed, err := c.Dynamic.Resource(services).Get(ctx, node.Name+"."+service, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("read %s on %s: %w", service, node.Name, err)
			}
			state, _, _ := unstructured.NestedString(observed.Object, "status", "state")
			pid, _, _ := unstructured.NestedInt64(observed.Object, "status", "pid")
			if state == "Absent" && pid == 0 && (!cp || service == "kuberoot-wsctl") {
				observedInventory, _, _ := unstructured.NestedBool(observed.Object, "status", "controllerInventoryObserved")
				message, _, _ := unstructured.NestedString(observed.Object, "status", "controllerInventoryError")
				processes, _, _ := unstructured.NestedSlice(observed.Object, "status", "controllerProcesses")
				if observedInventory && message == "" && len(processes) == 0 {
					continue
				}
				return fmt.Errorf("%s on %s has no supervisor but an orphaned or unverified process inventory", service, node.Name)
			}
			if state != "Running" || pid <= 0 {
				return fmt.Errorf("%s on %s is not a known running barrier participant", service, node.Name)
			}
			identity, err := vmmaintenance.ControllerIdentity(observed, service)
			if err != nil {
				return err
			}
			lease, err := c.Kube.CoordinationV1().Leases(vmmaintenance.Namespace).Get(ctx, vmmaintenance.LeaseName(node.Name, service), metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("%s on %s has not acknowledged maintenance: %w", service, node.Name, err)
			}
			a := lease.Annotations
			if a["principal"] == "" || a["principal"] == "system:anonymous" {
				return fmt.Errorf("%s on %s has no authenticated controller principal", service, node.Name)
			}
			if a["principal"] == p.Executor {
				return fmt.Errorf("migration requires a dedicated executor identity: %s on %s uses the same authenticated username", service, node.Name)
			}
			if a["api-mode"] != vmmaintenance.ModeAlpha1 && a["api-mode"] != vmmaintenance.ModeAlpha2 {
				return fmt.Errorf("%s on %s has no known controller API mode", service, node.Name)
			}
			if requiredMode != "" && a["api-mode"] != requiredMode {
				return fmt.Errorf("%s on %s must be restarted through NodeService to acknowledge %s before release", service, node.Name, requiredMode)
			}
			for name, value := range identity {
				if a[name] != value {
					return fmt.Errorf("%s on %s acknowledgement belongs to another process incarnation", service, node.Name)
				}
			}
			if lease.Labels[vmmaintenance.Label] != "v1alpha2" || a["node"] != node.Name || a["service"] != service || a["pid"] != strconv.FormatInt(pid, 10) || a["token"] != p.Token || a["gate-uid"] != string(p.GateUID) || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" || lease.Spec.RenewTime == nil || time.Since(lease.Spec.RenewTime.Time) > 10*time.Second || lease.Spec.RenewTime.Time.After(time.Now().Add(time.Second)) {
				return fmt.Errorf("%s on %s has an old, stale or different process acknowledgement", service, node.Name)
			}
			participants++
		}
	}
	if controlPlanes == 0 || participants == 0 {
		return fmt.Errorf("no known control-plane barrier participants")
	}
	if requireFence {
		if err = c.probeFence(ctx, p, cm, nodes.Items); err != nil {
			return err
		}
	}
	return nil
}

func fenceScope(p *Plan) (string, string) {
	owners := []string{}
	secrets := []string{}
	for _, r := range p.Machines {
		owners = append(owners, string(r.Original.GetUID()))
		if r.Secret != nil {
			secrets = append(secrets, r.Secret.GetNamespace()+"/"+r.Secret.GetName(), r.Namespace+"/"+r.Secret.GetName())
		}
	}
	slices.Sort(owners)
	owners = slices.Compact(owners)
	slices.Sort(secrets)
	secrets = slices.Compact(secrets)
	a, _ := json.Marshal(owners)
	b, _ := json.Marshal(secrets)
	return string(a), string(b)
}

func (c *Client) protectDependents(ctx context.Context, p *Plan) error {
	cms := c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace)
	cm, err := cms.Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if cm.UID != p.GateUID || cm.Data["token"] != p.Token {
		return fmt.Errorf("maintenance session changed")
	}
	owners, secrets := fenceScope(p)
	if cm.Data["ownerUIDs"] != owners || cm.Data["secrets"] != secrets {
		cm.Data["ownerUIDs"], cm.Data["secrets"] = owners, secrets
		cm, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}
	return c.fence(ctx, cm, true)
}
