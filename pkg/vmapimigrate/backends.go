package vmapimigrate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// backendInventory lists each node explicitly. The transition node API treats
// this selector as a strict single-node request and propagates read failures;
// the ordinary fleet list deliberately omits unreachable members.
func (c *Client) backendInventory(ctx context.Context) ([]Captured, error) {
	nodes, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var inventory []Captured
	for _, node := range nodes.Items {
		selector := labels.Set{"kuberoot.dev/node": node.Name}.AsSelector().String()
		for _, resource := range []schema.GroupVersionResource{Machines, Volumes} {
			list, err := c.Dynamic.Resource(resource).List(ctx, metav1.ListOptions{LabelSelector: selector})
			if err != nil {
				return nil, fmt.Errorf("complete %s inventory on %s: %w", resource.Resource, node.Name, err)
			}
			for _, object := range list.Items {
				if object.GetLabels()["kuberoot.dev/node"] != node.Name || !strings.HasPrefix(object.GetName(), node.Name+".") {
					return nil, fmt.Errorf("node-scoped inventory returned a foreign backend")
				}
				inventory = append(inventory, Captured{Resource: resource, Object: object})
			}
		}
	}
	return inventory, nil
}

// ValidateBackendTopology rejects uncheckpointed placement after a controller
// crash. Importing incomplete replicaNodes would otherwise let the first
// orphan sweep delete an existing disk or running native monitor.
func ValidateBackendTopology(records []Record, inventory []Captured) error {
	vms := map[string]*unstructured.Unstructured{}
	for i := range records {
		u := &records[i].Original
		vms[u.GetName()] = u
	}
	seen := map[string]bool{}
	for _, captured := range inventory {
		u := &captured.Object
		nodeName := u.GetLabels()["kuberoot.dev/node"]
		name, ok := strings.CutPrefix(u.GetName(), nodeName+".")
		old := vms[name]
		if !ok || nodeName == "" || old == nil {
			return fmt.Errorf("backend %s has no unambiguous VM owner; resolve it before cutover", u.GetName())
		}
		replicas, _, _ := unstructured.NestedStringSlice(old.Object, "status", "replicaNodes")
		index := slices.Index(replicas, nodeName)
		if index < 0 {
			return fmt.Errorf("backend %s exists outside its checkpointed replicaNodes", u.GetName())
		}
		seen[captured.Resource.Resource+"/"+u.GetName()] = true
		if captured.Resource == Volumes {
			dual, _, _ := unstructured.NestedBool(u.Object, "spec", "allowTwoPrimaries")
			if dual {
				return fmt.Errorf("volume %s still allows two primaries; finish migration cleanup before API cutover", u.GetName())
			}
			for _, field := range []string{"minor", "port"} {
				a, _, _ := unstructured.NestedInt64(old.Object, "status", field)
				b, _, _ := unstructured.NestedInt64(u.Object, "spec", field)
				if a <= 0 || a != b {
					return fmt.Errorf("volume %s has uncheckpointed %s", u.GetName(), field)
				}
			}
			id, _, _ := unstructured.NestedInt64(u.Object, "spec", "nodeID")
			if id != int64(index) {
				return fmt.Errorf("volume %s replica order differs from its checkpoint", u.GetName())
			}
		} else if captured.Resource == Machines {
			current, _, _ := unstructured.NestedString(old.Object, "status", "node")
			if current != nodeName {
				return fmt.Errorf("machine %s is outside its checkpointed current node", u.GetName())
			}
			phase, _, _ := unstructured.NestedString(old.Object, "status", "phase")
			if phase == "Running" {
				actualPhase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
				pid, _, _ := unstructured.NestedInt64(u.Object, "status", "pid")
				started, _, _ := unstructured.NestedString(u.Object, "status", "startedAt")
				if actualPhase != "Running" || pid <= 0 || started == "" {
					return fmt.Errorf("VM %s says Running without a live confirmed monitor incarnation", name)
				}
			}
			volumes, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "volumes")
			if len(volumes) != 1 || volumes[0] != name {
				return fmt.Errorf("machine %s uses an unexpected disk", u.GetName())
			}
			mac, _, _ := unstructured.NestedString(old.Object, "status", "mac")
			actual, _, _ := unstructured.NestedString(u.Object, "spec", "mac")
			if mac == "" || mac != actual {
				return fmt.Errorf("machine %s has uncheckpointed MAC", u.GetName())
			}
			for _, field := range []string{"sendTo", "receive"} {
				value, _, _ := unstructured.NestedString(u.Object, "spec", field)
				if value != "" {
					return fmt.Errorf("machine %s still has migration intent %s", u.GetName(), field)
				}
			}
		} else {
			return fmt.Errorf("unexpected backend resource")
		}
	}
	for _, record := range records {
		old := &record.Original
		replicas, _, _ := unstructured.NestedStringSlice(old.Object, "status", "replicaNodes")
		for _, replica := range replicas {
			if !seen["volumes/"+replica+"."+old.GetName()] {
				return fmt.Errorf("checkpointed volume %s.%s is missing", replica, old.GetName())
			}
		}
		phase, _, _ := unstructured.NestedString(old.Object, "status", "phase")
		current, _, _ := unstructured.NestedString(old.Object, "status", "node")
		if phase == "Running" && !seen["machines/"+current+"."+old.GetName()] {
			return fmt.Errorf("running VM %s has no observed native monitor", old.GetName())
		}
	}
	return nil
}
