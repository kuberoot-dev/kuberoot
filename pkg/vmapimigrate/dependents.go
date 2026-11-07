package vmapimigrate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// dependentInventory fails closed on discovery and list failures. The node
// API's ordinary fleet view is best effort, so inspect each node explicitly.
func (c *Client) dependentInventory(ctx context.Context, p *Plan) ([]Captured, error) {
	if c.Discovery == nil {
		return nil, fmt.Errorf("complete owner-reference discovery is required")
	}
	resources, err := c.Discovery.ServerPreferredResources()
	if err != nil {
		return nil, fmt.Errorf("discover dependents: %w", err)
	}
	if len(resources) == 0 {
		return nil, fmt.Errorf("empty resource discovery cannot prove absence of dependents")
	}
	nodes, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	owners := map[types.UID]bool{}
	for _, record := range p.Machines {
		owners[record.Original.GetUID()] = true
	}
	var result []Captured
	seen := map[string]bool{}
	for _, group := range resources {
		gv, err := schema.ParseGroupVersion(group.GroupVersion)
		if err != nil {
			return nil, err
		}
		for _, resource := range group.APIResources {
			skip, err := syntheticCredentialResource(gv, resource)
			if err != nil {
				return nil, err
			}
			if skip {
				continue
			}
			if strings.Contains(resource.Name, "/") || !slices.Contains(resource.Verbs, "list") {
				continue
			}
			gvr := gv.WithResource(resource.Name)
			if gvr.Group == OldVM.Group && gvr.Resource == OldVM.Resource {
				continue // VM-to-VM references are captured in the VM records.
			}
			selectors := []string{""}
			if gv.Group == Machines.Group {
				selectors = nil
				for _, node := range nodes.Items {
					selectors = append(selectors, labels.Set{"kuberoot.dev/node": node.Name}.AsSelector().String())
				}
			}
			for _, selector := range selectors {
				options := metav1.ListOptions{Limit: 500, LabelSelector: selector}
				for {
					objects, err := c.Dynamic.Resource(gvr).List(ctx, options)
					if err != nil {
						return nil, fmt.Errorf("inventory owner references in %s (%s): %w", gvr, selector, err)
					}
					for _, object := range objects.Items {
						for _, ref := range object.GetOwnerReferences() {
							if !owners[ref.UID] {
								continue
							}
							if !slices.Contains(resource.Verbs, "update") {
								return nil, fmt.Errorf("dependent %s %s cannot be safely detached", gvr, object.GetName())
							}
							captured := Captured{Resource: gvr, Object: object}
							key := capturedIdentity(captured)
							if !seen[key] {
								result = append(result, captured)
								seen[key] = true
							}
							break
						}
					}
					options.Continue = objects.GetContinue()
					if options.Continue == "" {
						break
					}
				}
			}
		}
	}
	return result, nil
}

// kubeconfigStorage.Get constructs this synthetic object's metadata from fixed
// Name/UID/RV fields and List delegates to Get. It has no persisted ownerRefs or
// mutation storage, so it cannot be a GC dependent. Listing it would disclose an
// administrator kubeconfig to the migration executor. This exception is ONLY for
// that pinned handler; a changed discovery contract must fail before any list.
// Other resources, including Secrets and unfamiliar read-only APIs, still need
// the complete inventory below; insufficient list permission remains an error.
func syntheticCredentialResource(gv schema.GroupVersion, resource metav1.APIResource) (bool, error) {
	if gv.Group != "node.kuberoot.dev" || resource.Name != "kubeconfigs" {
		return false, nil
	}
	if gv.Version != "v1alpha1" || resource.Kind != "Kubeconfig" || resource.Namespaced ||
		(resource.Group != "" && resource.Group != gv.Group) ||
		(resource.Version != "" && resource.Version != gv.Version) ||
		len(resource.Verbs) != 2 || !slices.Contains(resource.Verbs, "get") || !slices.Contains(resource.Verbs, "list") {
		return false, fmt.Errorf("synthetic kubeconfigs discovery changed; refusing credential access or incomplete dependent inventory")
	}
	return true, nil
}

func capturedIdentity(c Captured) string {
	return c.Resource.String() + "/" + c.Object.GetNamespace() + "/" + c.Object.GetName() + "/" + string(c.Object.GetUID())
}

func sameCapturedIdentities(a, b []Captured) bool {
	if len(a) != len(b) {
		return false
	}
	want := map[string]bool{}
	for _, c := range b {
		want[capturedIdentity(c)] = true
	}
	for _, c := range a {
		if !want[capturedIdentity(c)] {
			return false
		}
	}
	return true
}
