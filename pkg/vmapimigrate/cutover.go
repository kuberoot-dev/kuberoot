package vmapimigrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kuberoot-dev/kuberoot/pkg/vmmaintenance"
)

// Cutover requires an immutable plan hash and a durable state file. It leaves
// the admission fence and controller gate in place even after verification;
// Resume is an explicit final operation. There is no startup migration path.
func (c *Client) Cutover(ctx context.Context, p *Plan, hash, statePath string) error {
	if hash == "" || statePath == "" {
		return fmt.Errorf("explicit plan hash and checkpoint path are required")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if err := c.VerifyBarrier(ctx, p); err != nil {
		return err
	}
	state, err := loadState(statePath, hash)
	if err != nil {
		return err
	}
	checkpoint := func(phase string) error { state.Phase = phase; return SavePrivate(statePath, state) }
	if state.Phase == "" {
		if err = c.VerifyUnchanged(ctx, p); err != nil {
			return err
		}
		if err = checkpoint("detaching"); err != nil {
			return err
		}
	}
	known := map[types.UID]types.UID{}
	for _, r := range p.Machines {
		known[r.Original.GetUID()] = ""
	}
	if state.Phase == "detaching" {
		for _, dependent := range p.Dependents {
			if err = c.VerifyBarrier(ctx, p); err != nil {
				return err
			}
			if err = c.updateOwners(ctx, dependent, RewriteOwners(dependent.Object.GetOwnerReferences(), known, true)); err != nil {
				return err
			}
		}
		if err = c.copySecrets(ctx, p, hash); err != nil {
			return err
		}
		if err = checkpoint("deleting-objects"); err != nil {
			return err
		}
	}
	if state.Phase == "deleting-objects" {
		// Re-list every discoverable resource after detaching and before the
		// first API deletion, including recovery after a partial deletion.
		// The fence also prevents new references to the old VM UIDs.
		dependents, err := c.dependentInventory(ctx, p)
		if err != nil {
			return err
		}
		if len(dependents) != 0 {
			return fmt.Errorf("old VM owner references remain or appeared after export; refusing API deletion")
		}
		for _, record := range p.Machines {
			if err = c.VerifyBarrier(ctx, p); err != nil {
				return err
			}
			if err = c.VerifyBackends(ctx, p); err != nil {
				return err
			}
			current, err := c.Dynamic.Resource(OldVM).Get(ctx, record.Original.GetName(), metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			if current.GetUID() != record.Original.GetUID() || !equalJSON(current.Object["spec"], record.Original.Object["spec"]) || !equalJSON(current.Object["status"], record.Original.Object["status"]) {
				return fmt.Errorf("legacy VM %s changed after backup", current.GetName())
			}
			if current.GetDeletionTimestamp() != nil {
				return fmt.Errorf("legacy VM %s is unexpectedly deleting", current.GetName())
			}
			for _, finalizer := range current.GetFinalizers() {
				if finalizer != "vm.kuberoot.dev/cleanup" {
					return fmt.Errorf("VM %s has an unknown finalizer %s; refusing to bypass its lifecycle", current.GetName(), finalizer)
				}
			}
			if len(current.GetFinalizers()) > 0 {
				current.SetFinalizers(nil)
				current, err = c.Dynamic.Resource(OldVM).Update(ctx, current, metav1.UpdateOptions{})
				if err != nil {
					return err
				}
			}
			uid, rv := current.GetUID(), current.GetResourceVersion()
			propagation := metav1.DeletePropagationBackground
			if err = c.Dynamic.Resource(OldVM).Delete(ctx, current.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}, PropagationPolicy: &propagation}); err != nil {
				return err
			}
		}
		list, err := c.Dynamic.Resource(OldVM).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		if len(list.Items) != 0 {
			return fmt.Errorf("legacy objects remain; keep the barrier and resume after API orphaning completes")
		}
		if err = checkpoint("replacing-crd"); err != nil {
			return err
		}
	}
	if state.Phase == "replacing-crd" {
		if err = c.VerifyBarrier(ctx, p); err != nil {
			return err
		}
		current, err := c.Dynamic.Resource(CRDs).Get(ctx, CRDName, metav1.GetOptions{})
		if err == nil && current.GetUID() == p.OriginalCRD.GetUID() {
			if !equalJSON(current.Object["spec"], p.OriginalCRD.Object["spec"]) {
				return fmt.Errorf("legacy CRD definition changed")
			}
			uid, rv := current.GetUID(), current.GetResourceVersion()
			if err = c.Dynamic.Resource(CRDs).Delete(ctx, CRDName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}); err != nil {
				return err
			}
			if err = c.waitCRDGone(ctx); err != nil {
				return err
			}
			current = nil
		} else if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if current == nil || apierrors.IsNotFound(err) {
			target := cleanObject(p.TargetCRD.DeepCopy())
			target.SetAnnotations(map[string]string{PlanAnnotation: hash})
			current, err = c.Dynamic.Resource(CRDs).Create(ctx, target, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				current, err = c.Dynamic.Resource(CRDs).Get(ctx, CRDName, metav1.GetOptions{})
			}
			if err != nil {
				return err
			}
		}
		// A transition image may apply this exact add-on after the old CRD
		// disappears. Accept it only after comparing the complete spec.
		if !sameCRDSpec(current, &p.TargetCRD) {
			return fmt.Errorf("existing replacement CRD is not the prepared target")
		}
		if err = c.waitCRDReady(ctx); err != nil {
			return err
		}
		if err = checkpoint("restoring"); err != nil {
			return err
		}
	}
	if state.Phase == "restoring" {
		for _, record := range p.Machines {
			if err = c.VerifyBarrier(ctx, p); err != nil {
				return err
			}
			resource := c.Dynamic.Resource(NewVM).Namespace(record.Namespace)
			current, err := resource.Get(ctx, record.Original.GetName(), metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				current, err = resource.Create(ctx, record.Target(hash), metav1.CreateOptions{})
			}
			if err != nil {
				return err
			}
			if current.GetAnnotations()[PlanAnnotation] != hash || current.GetAnnotations()[SourceAnnotation] != string(record.Original.GetUID()) {
				return fmt.Errorf("target %s/%s belongs to another object or import", record.Namespace, current.GetName())
			}
			if previous := state.UIDs[record.Original.GetUID()]; previous != "" && previous != current.GetUID() {
				return fmt.Errorf("imported target was replaced after checkpoint")
			}
			state.UIDs[record.Original.GetUID()] = current.GetUID()
			if err = SavePrivate(statePath, state); err != nil {
				return err
			}
			if !equalJSON(current.Object["status"], record.Status()) {
				status, _, _ := unstructured.NestedMap(current.Object, "status")
				if len(status) != 0 {
					return fmt.Errorf("partially restored target has an unexpected status; refusing to overwrite it")
				}
				current.Object["status"] = record.Status()
				if _, err = resource.UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
					return err
				}
			}
		}
		if err = checkpoint("restoring-owners"); err != nil {
			return err
		}
	}
	if state.Phase == "restoring-owners" {
		if len(state.UIDs) != len(p.Machines) {
			return fmt.Errorf("incomplete restored UID map")
		}
		for _, record := range p.Machines {
			current, err := c.Dynamic.Resource(NewVM).Namespace(record.Namespace).Get(ctx, record.Original.GetName(), metav1.GetOptions{})
			if err != nil {
				return err
			}
			if current.GetUID() != state.UIDs[record.Original.GetUID()] {
				return fmt.Errorf("target UID changed")
			}
			want := RewriteOwners(record.Original.GetOwnerReferences(), state.UIDs, false)
			if !reflect.DeepEqual(current.GetOwnerReferences(), want) {
				current.SetOwnerReferences(want)
				if _, err = c.Dynamic.Resource(NewVM).Namespace(record.Namespace).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
					return err
				}
			}
		}
		for _, dependent := range p.Dependents {
			if err = c.VerifyBarrier(ctx, p); err != nil {
				return err
			}
			if err = c.updateOwners(ctx, dependent, RewriteOwners(dependent.Object.GetOwnerReferences(), state.UIDs, false)); err != nil {
				return err
			}
		}
		if err = c.VerifyImported(ctx, p, state, hash); err != nil {
			return err
		}
		if err = checkpoint("verified"); err != nil {
			return err
		}
	}
	if state.Phase != "verified" {
		return fmt.Errorf("unexpected cutover checkpoint %q", state.Phase)
	}
	return c.VerifyImported(ctx, p, state, hash)
}

func cleanObject(u *unstructured.Unstructured) *unstructured.Unstructured {
	u.SetUID("")
	u.SetResourceVersion("")
	u.SetGeneration(0)
	u.SetCreationTimestamp(metav1.Time{})
	u.SetManagedFields(nil)
	u.SetFinalizers(nil)
	delete(u.Object, "status")
	return u
}

func loadState(path, hash string) (*State, error) {
	state := &State{PlanHash: hash, UIDs: map[types.UID]types.UID{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, state); err != nil {
		return nil, err
	}
	if state.PlanHash != hash || state.UIDs == nil {
		return nil, fmt.Errorf("checkpoint belongs to another plan or is malformed")
	}
	return state, nil
}

func (c *Client) updateOwners(ctx context.Context, captured Captured, want []metav1.OwnerReference) error {
	old := &captured.Object
	r := objectClient(c.Dynamic, captured.Resource, old.GetNamespace())
	u, err := r.Get(ctx, old.GetName(), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if u.GetUID() != old.GetUID() {
		return fmt.Errorf("dependent %s was replaced", old.GetName())
	}
	if u.GetDeletionTimestamp() != nil {
		return fmt.Errorf("dependent %s is deleting", old.GetName())
	}
	if reflect.DeepEqual(u.GetOwnerReferences(), want) {
		return nil
	}
	original := old.GetOwnerReferences()
	known := map[types.UID]types.UID{}
	for _, ref := range original {
		if ref.APIVersion == "vm.kuberoot.dev/v1alpha1" {
			known[ref.UID] = ""
		}
	}
	if !equalJSON(u.GetOwnerReferences(), original) && !equalJSON(u.GetOwnerReferences(), RewriteOwners(original, known, true)) {
		return fmt.Errorf("dependent %s owner references changed outside this import", old.GetName())
	}
	// Preserve concurrent changes to the dependent's data. Only references
	// from the immutable backup are replaced, and the update has an RV check.
	u.SetOwnerReferences(want)
	_, err = r.Update(ctx, u, metav1.UpdateOptions{})
	return err
}

func (c *Client) copySecrets(ctx context.Context, p *Plan, hash string) error {
	secrets := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	for _, record := range p.Machines {
		if record.Secret == nil {
			continue
		}
		old := record.Secret
		source, err := c.Dynamic.Resource(secrets).Namespace(old.GetNamespace()).Get(ctx, old.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if source.GetUID() != old.GetUID() || !equalJSON(source.Object["data"], old.Object["data"]) || !equalJSON(source.Object["type"], old.Object["type"]) {
			return fmt.Errorf("source Secret changed since backup")
		}
		if old.GetNamespace() == record.Namespace {
			continue
		}
		r := c.Dynamic.Resource(secrets).Namespace(record.Namespace)
		current, err := r.Get(ctx, old.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			copy := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": old.GetName(), "namespace": record.Namespace, "annotations": map[string]any{PlanAnnotation: hash}}, "data": old.Object["data"], "type": old.Object["type"]}}
			current, err = r.Create(ctx, copy, metav1.CreateOptions{})
		}
		if err != nil {
			return err
		}
		if current.GetAnnotations()[PlanAnnotation] != hash || !equalJSON(current.Object["data"], old.Object["data"]) || !equalJSON(current.Object["type"], old.Object["type"]) {
			return fmt.Errorf("target Secret %s/%s already exists with another owner or value", record.Namespace, old.GetName())
		}
	}
	return nil
}

func (c *Client) waitCRDGone(ctx context.Context) error {
	return wait(ctx, func() (bool, error) {
		_, err := c.Dynamic.Resource(CRDs).Get(ctx, CRDName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

func (c *Client) waitCRDReady(ctx context.Context) error {
	return wait(ctx, func() (bool, error) {
		u, err := c.Dynamic.Resource(CRDs).Get(ctx, CRDName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		for _, raw := range conditions {
			v, _ := raw.(map[string]any)
			if v["type"] == "Established" && v["status"] == "True" {
				return true, nil
			}
		}
		return false, nil
	})
}

func wait(ctx context.Context, check func() (bool, error)) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			done, err := check()
			if err != nil || done {
				return err
			}
			timer.Reset(200 * time.Millisecond)
		}
	}
}

func (c *Client) VerifyImported(ctx context.Context, p *Plan, state *State, hash string) error {
	return c.verifyImported(ctx, p, state, hash, true)
}

func (c *Client) verifyImported(ctx context.Context, p *Plan, state *State, hash string, requireFence bool) error {
	if err := c.verifyBarrier(ctx, p, requireFence); err != nil {
		return err
	}
	if err := vmmaintenance.APIReady(ctx, c.Dynamic); err != nil {
		return err
	}
	list, err := c.Dynamic.Resource(NewVM).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(list.Items) != len(p.Machines) {
		return fmt.Errorf("target VM inventory differs from the plan")
	}
	for _, record := range p.Machines {
		u, err := c.Dynamic.Resource(NewVM).Namespace(record.Namespace).Get(ctx, record.Original.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if u.GetUID() != state.UIDs[record.Original.GetUID()] || u.GetAnnotations()[PlanAnnotation] != hash || !equalJSON(u.Object["spec"], record.Target(hash).Object["spec"]) || !equalJSON(u.Object["status"], record.Status()) || !equalJSON(u.GetOwnerReferences(), RewriteOwners(record.Original.GetOwnerReferences(), state.UIDs, false)) {
			return fmt.Errorf("imported VM %s/%s failed exact verification", record.Namespace, u.GetName())
		}
	}
	for _, dependent := range p.Dependents {
		u, err := objectClient(c.Dynamic, dependent.Resource, dependent.Object.GetNamespace()).Get(ctx, dependent.Object.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if u.GetUID() != dependent.Object.GetUID() || !equalJSON(u.GetOwnerReferences(), RewriteOwners(dependent.Object.GetOwnerReferences(), state.UIDs, false)) {
			return fmt.Errorf("dependent owner reference restoration is incomplete")
		}
	}
	if err = c.VerifySecrets(ctx, p); err != nil {
		return err
	}
	return c.VerifyBackends(ctx, p)
}

// VerifySecrets runs immediately before the verified checkpoint and again
// before resume. An absent or altered bootstrap credential is not a complete
// import, even when all VM objects and monitors have survived.
func (c *Client) VerifySecrets(ctx context.Context, p *Plan) error {
	secrets := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	for _, record := range p.Machines {
		if record.Secret == nil {
			continue
		}
		current, err := c.Dynamic.Resource(secrets).Namespace(record.Namespace).Get(ctx, record.Secret.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !equalJSON(current.Object["data"], record.Secret.Object["data"]) || !equalJSON(current.Object["type"], record.Secret.Object["type"]) {
			return fmt.Errorf("bootstrap Secret %s/%s differs from the prepared backup", record.Namespace, record.Secret.GetName())
		}
	}
	return nil
}
