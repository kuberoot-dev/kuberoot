package vmapimigrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"maps"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

func objectClient(client dynamic.Interface, resource schema.GroupVersionResource, namespace string) dynamic.ResourceInterface {
	if namespace != "" {
		return client.Resource(resource).Namespace(namespace)
	}
	return client.Resource(resource)
}

// Prepare reads a consistent backup only after the barrier is acknowledged.
// The namespace map is explicit: moving credential-bearing VMs to a tenant
// namespace is an administrative decision, never inferred from their names.
func (c *Client) Prepare(ctx context.Context, namespaces map[string]string, target *unstructured.Unstructured) (*Plan, error) {
	cm, err := c.Begin(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := c.Kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	p := &Plan{Version: 1, ClusterUID: ns.UID, GateUID: cm.UID, Token: cm.Data["token"], APIServers: maps.Clone(c.APIServers), Executor: cm.Data["executor"], TargetCRD: *target.DeepCopy()}
	if err = c.VerifyBarrier(ctx, p); err != nil {
		return nil, err
	}
	crd, err := c.Dynamic.Resource(CRDs).Get(ctx, CRDName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	p.OriginalCRD = *crd
	list, err := c.Dynamic.Resource(OldVM).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	workspaces, workspaceErr := c.Dynamic.Resource(schema.GroupVersionResource{Group: "workstation.kuberoot.dev", Version: "v1alpha1", Resource: "workspaces"}).List(ctx, metav1.ListOptions{})
	if workspaceErr != nil && !apierrors.IsNotFound(workspaceErr) {
		return nil, workspaceErr
	}
	if workspaceErr == nil {
		p.Workspaces = workspaces.Items
	}
	for _, old := range list.Items {
		namespace, ok := namespaces[old.GetName()]
		if !ok {
			return nil, fmt.Errorf("VM %s has no explicit target namespace", old.GetName())
		}
		targetNS, err := c.Kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if targetNS.DeletionTimestamp != nil {
			return nil, fmt.Errorf("target namespace %s is terminating", namespace)
		}
		r := Record{Original: old, Namespace: namespace}
		if ref, found, err := unstructured.NestedMap(old.Object, "spec", "userDataSecret"); err != nil {
			return nil, err
		} else if found {
			secretNS, _ := ref["namespace"].(string)
			secretName, _ := ref["name"].(string)
			if secretNS == "" || secretName == "" {
				return nil, fmt.Errorf("VM %s has an invalid legacy Secret reference", old.GetName())
			}
			secret, err := c.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace(secretNS).Get(ctx, secretName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			r.Secret = secret
		}
		p.Machines = append(p.Machines, r)

	}
	if len(namespaces) != len(p.Machines) {
		return nil, fmt.Errorf("namespace map must match the complete legacy VM inventory exactly")
	}
	if err = c.protectDependents(ctx, p); err != nil {
		return nil, err
	}
	p.Backends, err = c.backendInventory(ctx)
	if err != nil {
		return nil, err
	}
	if err = ValidateBackendTopology(p.Machines, p.Backends); err != nil {
		return nil, err
	}
	p.Dependents, err = c.dependentInventory(ctx, p)
	if err != nil {
		return nil, err
	}
	if err = p.Validate(); err != nil {
		return nil, err
	}
	if err = c.VerifyUnchanged(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (c *Client) VerifyUnchanged(ctx context.Context, p *Plan) error {
	if err := c.VerifyBarrier(ctx, p); err != nil {
		return err
	}
	old, err := c.Dynamic.Resource(CRDs).Get(ctx, CRDName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if old.GetUID() != p.OriginalCRD.GetUID() || old.GetResourceVersion() != p.OriginalCRD.GetResourceVersion() {
		return fmt.Errorf("legacy CRD changed after export")
	}
	list, err := c.Dynamic.Resource(OldVM).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(list.Items) != len(p.Machines) {
		return fmt.Errorf("legacy VM inventory changed after export")
	}
	for _, workspace := range p.Workspaces {
		if err = c.verifyCaptured(ctx, Captured{Resource: schema.GroupVersionResource{Group: "workstation.kuberoot.dev", Version: "v1alpha1", Resource: "workspaces"}, Object: workspace}, true); err != nil {
			return err
		}
	}
	for _, r := range p.Machines {
		if err = c.verifyCaptured(ctx, Captured{Resource: OldVM, Object: r.Original}, true); err != nil {
			return err
		}
		if r.Secret != nil {
			if err = c.verifyCaptured(ctx, Captured{Resource: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, Object: *r.Secret}, true); err != nil {
				return err
			}
		}
	}
	for _, object := range p.Dependents {
		if err = c.verifyCaptured(ctx, object, true); err != nil {
			return err
		}
	}
	currentDependents, err := c.dependentInventory(ctx, p)
	if err != nil {
		return err
	}
	if !sameCapturedIdentities(currentDependents, p.Dependents) {
		return fmt.Errorf("complete dependent inventory changed after export")
	}
	return c.VerifyBackends(ctx, p)
}

func (c *Client) verifyCaptured(ctx context.Context, captured Captured, resourceVersion bool) error {
	u := &captured.Object
	current, err := objectClient(c.Dynamic, captured.Resource, u.GetNamespace()).Get(ctx, u.GetName(), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if current.GetUID() != u.GetUID() || resourceVersion && current.GetResourceVersion() != u.GetResourceVersion() {
		return fmt.Errorf("%s %s/%s changed after export", captured.Resource.Resource, u.GetNamespace(), u.GetName())
	}
	return nil
}

// VerifyBackends intentionally ignores status usage counters and instead
// checks the node API binding, configured disk identity and running monitor.
// Native Volume UIDs are name-derived; durable disk preservation also needs
// runtime guest filesystem UUID/marker evidence, not this API check alone.
func (c *Client) VerifyBackends(ctx context.Context, p *Plan) error {
	inventory, err := c.backendInventory(ctx)
	if err != nil {
		return err
	}
	if err = ValidateBackendTopology(p.Machines, inventory); err != nil {
		return err
	}
	if len(inventory) != len(p.Backends) {
		return fmt.Errorf("complete backend inventory changed during maintenance")
	}
	known := map[string]bool{}
	for _, captured := range p.Backends {
		known[captured.Resource.String()+"/"+captured.Object.GetName()] = true
	}
	for _, captured := range inventory {
		if !known[captured.Resource.String()+"/"+captured.Object.GetName()] {
			return fmt.Errorf("new backend appeared during maintenance")
		}
	}
	for _, captured := range p.Backends {
		old := &captured.Object
		current, err := c.Dynamic.Resource(captured.Resource).Get(ctx, old.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.GetUID() != old.GetUID() {
			return fmt.Errorf("backend %s was replaced", old.GetName())
		}
		if !equalJSON(current.Object["spec"], old.Object["spec"]) {
			return fmt.Errorf("backend %s spec changed during maintenance", old.GetName())
		}
		fields := []string{"imageWritten"}
		if captured.Resource == Machines {
			fields = []string{"pid", "startedAt", "phase"}
		}
		for _, field := range fields {
			a, _, _ := unstructured.NestedFieldNoCopy(old.Object, "status", field)
			b, _, _ := unstructured.NestedFieldNoCopy(current.Object, "status", field)
			if !equalJSON(a, b) {
				return fmt.Errorf("backend %s status.%s changed during maintenance", old.GetName(), field)
			}
		}
	}
	return nil
}

func equalJSON(a, b any) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

func sameCRDSpec(a, b *unstructured.Unstructured) bool {
	var x, y apiextensionsv1.CustomResourceDefinition
	if runtime.DefaultUnstructuredConverter.FromUnstructured(a.Object, &x) != nil || runtime.DefaultUnstructuredConverter.FromUnstructured(b.Object, &y) != nil {
		return false
	}
	scheme := runtime.NewScheme()
	if apiextensionsv1.AddToScheme(scheme) != nil {
		return false
	}
	scheme.Default(&x)
	scheme.Default(&y)
	return equalJSON(x.Spec, y.Spec)
}
