// Package capikuberoot implements the CAPI v1beta2 infrastructure contract
// without importing a second, older Kubernetes SDK through the CAPI module.
package capikuberoot

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	infra "github.com/kuberoot-dev/kuberoot/pkg/apis/infrastructure/v1alpha1"
)

const (
	Finalizer          = "infrastructure.cluster.x-k8s.io/kuberoot-machine"
	BootstrapFinalizer = "infrastructure.cluster.x-k8s.io/kuberoot-bootstrap"
	VMFinalizer        = "infrastructure.cluster.x-k8s.io/kuberoot-vm"
	nativeVMFinalizer  = "vm.kuberoot.dev/cleanup"
	PausedAnnotation   = "cluster.x-k8s.io/paused"
	ClusterLabel       = "cluster.x-k8s.io/cluster-name"
	ProviderPrefix     = "kuberoot://"
	clusterNameBinding = "infrastructure.cluster.x-k8s.io/kuberoot-cluster-name"
	clusterUIDBinding  = "infrastructure.cluster.x-k8s.io/kuberoot-cluster-uid"
)

var (
	Clusters            = schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "clusters"}
	Machines            = schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "machines"}
	InfraClusters       = infra.GroupVersion.WithResource("kuberootclusters")
	InfraMachines       = infra.GroupVersion.WithResource("kuberootmachines")
	VirtualMachines     = schema.GroupVersionResource{Group: "vm.kuberoot.dev", Version: "v1alpha2", Resource: "virtualmachines"}
	KamajiControlPlanes = schema.GroupVersionResource{Group: "controlplane.cluster.x-k8s.io", Version: "v1alpha1", Resource: "kamajicontrolplanes"}
)

type waiting struct{ reason, message string }

func (w waiting) Error() string { return w.message }

func paused(object metav1.Object) bool {
	_, exists := object.GetAnnotations()[PausedAnnotation]
	return exists
}

func capiOwner(object metav1.Object, kind string) (metav1.OwnerReference, error) {
	var match *metav1.OwnerReference
	for _, owner := range object.GetOwnerReferences() {
		// By group, not by group and version. Cluster API writes the owner
		// reference in whichever version it currently serves, so pinning the
		// version here means that the day core moves on, no owner is ever
		// found: every machine waits for an owner that is already there, and
		// the reconciler reports waiting rather than anything being wrong.
		group, _, found := strings.Cut(owner.APIVersion, "/")
		if found && group == "cluster.x-k8s.io" && owner.Kind == kind {
			if match != nil || owner.Name == "" || owner.UID == "" {
				return metav1.OwnerReference{}, fmt.Errorf("invalid or ambiguous CAPI %s owner", kind)
			}
			copy := owner
			match = &copy
		}
	}
	if match == nil {
		return metav1.OwnerReference{}, waiting{"WaitingForOwner", "waiting for the CAPI controller to set the owner reference"}
	}
	return *match, nil
}

func hasOwner(object metav1.Object, apiVersion, kind, name string, uid types.UID) bool {
	if uid == "" {
		return false
	}
	for _, ref := range object.GetOwnerReferences() {
		if ref.APIVersion == apiVersion && ref.Kind == kind && ref.Name == name && ref.UID == uid {
			return true
		}
	}
	return false
}

func machineOwner(machine *infra.KuberootMachine) metav1.OwnerReference {
	control, block := true, true
	return metav1.OwnerReference{APIVersion: infra.GroupVersion.String(), Kind: "KuberootMachine",
		Name: machine.Name, UID: machine.UID, Controller: &control, BlockOwnerDeletion: &block}
}

func owned(object metav1.Object, machine *infra.KuberootMachine) bool {
	if object.GetNamespace() != machine.Namespace || machine.UID == "" {
		return false
	}
	owner := metav1.GetControllerOf(object)
	return owner != nil && owner.APIVersion == infra.GroupVersion.String() && owner.Kind == "KuberootMachine" && owner.Name == machine.Name && owner.UID == machine.UID
}

func referenceMatches(object *unstructured.Unstructured, field string, target metav1.Object, apiVersion, kind string) bool {
	ref, found, err := unstructured.NestedMap(object.Object, "spec", field)
	if err != nil || !found {
		return false
	}
	namespace, _ := ref["namespace"].(string)
	return ref["apiVersion"] == apiVersion && ref["kind"] == kind && ref["name"] == target.GetName() && (namespace == "" || namespace == target.GetNamespace()) && object.GetNamespace() == target.GetNamespace()
}

func toObject(value any) (*unstructured.Unstructured, error) {
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(value)
	return &unstructured.Unstructured{Object: data}, err
}

func objectRef(object metav1.Object, apiVersion, kind string) *corev1.ObjectReference {
	return &corev1.ObjectReference{APIVersion: apiVersion, Kind: kind, Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
}

func sameReference(ref *corev1.ObjectReference, object metav1.Object, apiVersion, kind string) bool {
	return ref != nil && ref.UID != "" && ref.UID == object.GetUID() && ref.Name == object.GetName() && ref.Namespace == object.GetNamespace() && ref.APIVersion == apiVersion && ref.Kind == kind
}

func deleteOptions(object metav1.Object) metav1.DeleteOptions {
	uid, version := object.GetUID(), object.GetResourceVersion()
	return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}
}

func (c *Controller) addFinalizer(ctx context.Context, object, cluster *unstructured.Unstructured) (bool, error) {
	if slices.Contains(object.GetFinalizers(), Finalizer) {
		if object.GetAnnotations()[clusterNameBinding] != cluster.GetName() || object.GetAnnotations()[clusterUIDBinding] != string(cluster.GetUID()) {
			return false, fmt.Errorf("the infrastructure Machine's original Cluster binding is missing or changed")
		}
		return false, nil
	}
	copy := object.DeepCopy()
	copy.SetFinalizers(append(copy.GetFinalizers(), Finalizer))
	annotations := copy.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[clusterNameBinding], annotations[clusterUIDBinding] = cluster.GetName(), string(cluster.GetUID())
	copy.SetAnnotations(annotations)
	_, err := c.Dynamic.Resource(InfraMachines).Namespace(object.GetNamespace()).Update(ctx, copy, metav1.UpdateOptions{})
	return true, err
}
