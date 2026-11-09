package capikuberoot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infra "github.com/kuberoot-dev/kuberoot/pkg/apis/infrastructure/v1alpha1"
)

const (
	bootstrapSource = "infrastructure.cluster.x-k8s.io/bootstrap-source"
	bootstrapUID    = "infrastructure.cluster.x-k8s.io/bootstrap-source-uid"
	bootstrapDigest = "infrastructure.cluster.x-k8s.io/bootstrap-sha256"
)

func bootstrapName(machine *infra.KuberootMachine) string {
	// A UID suffix avoids collisions with CAPI's original bootstrap Secret and
	// with an older infrastructure Machine that had the same name.
	sum := sha256.Sum256([]byte(machine.UID))
	return strings.TrimRight(truncate(machine.Name, 220), ".-") + fmt.Sprintf("-bootstrap-%x", sum[:8])
}

func truncate(value string, length int) string {
	if len(value) > length {
		return value[:length]
	}
	return value
}

func (c *Controller) bootstrap(ctx context.Context, machine *infra.KuberootMachine, capi *unstructured.Unstructured, vmExists bool) (*corev1.Secret, error) {
	sourceName, found, err := unstructured.NestedString(capi.Object, "spec", "bootstrap", "dataSecretName")
	if err != nil {
		return nil, fmt.Errorf("invalid CAPI bootstrap reference")
	}
	if !found || sourceName == "" {
		return nil, waiting{"WaitingForBootstrap", "waiting for CAPI bootstrap dataSecretName"}
	}
	secrets := c.Kube.CoreV1().Secrets(machine.Namespace)
	source, err := secrets.Get(ctx, sourceName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, waiting{"WaitingForBootstrap", "waiting for the referenced CAPI bootstrap Secret"}
	}
	if err != nil {
		return nil, err
	}
	if source.UID == "" || source.DeletionTimestamp != nil || len(source.Data["value"]) == 0 {
		return nil, fmt.Errorf("CAPI bootstrap Secret has no stable identity or nonempty data.value")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(source.Data["value"]))
	name := bootstrapName(machine)
	current, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if vmExists || machine.Status.VMRef != nil {
			return nil, fmt.Errorf("the original bootstrap Secret disappeared after VM creation; refusing to replace credentials")
		}
		immutable := true
		return secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: machine.Namespace,
				OwnerReferences: []metav1.OwnerReference{machineOwner(machine)}, Finalizers: []string{BootstrapFinalizer},
				Annotations: map[string]string{bootstrapSource: source.Name, bootstrapUID: string(source.UID), bootstrapDigest: digest}},
			Type: corev1.SecretTypeOpaque, Immutable: &immutable,
			Data: map[string][]byte{"userData": bytes.Clone(source.Data["value"])},
		}, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	if !owned(current, machine) || current.DeletionTimestamp != nil || current.Immutable == nil || !*current.Immutable || current.Type != corev1.SecretTypeOpaque || !slices.Contains(current.Finalizers, BootstrapFinalizer) {
		return nil, fmt.Errorf("bootstrap Secret is foreign, mutable, deleting, or lacks its cleanup protection")
	}
	if current.Annotations[bootstrapSource] != source.Name || current.Annotations[bootstrapUID] != string(source.UID) || current.Annotations[bootstrapDigest] != digest || len(current.Data) != 1 || !bytes.Equal(current.Data["userData"], source.Data["value"]) {
		return nil, fmt.Errorf("bootstrap source or original credentials changed; refusing to replace initial data")
	}
	return current, nil
}

func (c *Controller) deleteMachine(ctx context.Context, machine *infra.KuberootMachine, object *unstructured.Unstructured) error {
	if !slices.Contains(machine.Finalizers, Finalizer) {
		return nil
	}
	resource := c.Dynamic.Resource(VirtualMachines).Namespace(machine.Namespace)
	vm, err := resource.Get(ctx, machine.Name, metav1.GetOptions{})
	if err == nil {
		if !owned(vm, machine) || (machine.Status.VMRef != nil && !sameReference(machine.Status.VMRef, vm, VirtualMachines.GroupVersion().String(), "VirtualMachine")) {
			return fmt.Errorf("refusing to delete a foreign or replaced VirtualMachine")
		}
		if vm.GetDeletionTimestamp() == nil {
			return resource.Delete(ctx, vm.GetName(), deleteOptions(vm))
		}
		// Native vmctl commits its cleanup finalizer before creating any
		// backend. Its disappearance on a deleting VM is the cleanup barrier.
		// This independent guard was present at initial Create, so a VM deleted
		// before status.vmRef was written remains an observable tombstone.
		if slices.Contains(vm.GetFinalizers(), nativeVMFinalizer) {
			return nil
		}
		if slices.Contains(vm.GetFinalizers(), VMFinalizer) {
			copy := vm.DeepCopy()
			copy.SetFinalizers(slices.DeleteFunc(copy.GetFinalizers(), func(value string) bool { return value == VMFinalizer }))
			_, err = resource.Update(ctx, copy, metav1.UpdateOptions{})
			return err
		}
		return nil // Wait for API NotFound, including any unrelated finalizers.
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	secrets := c.Kube.CoreV1().Secrets(machine.Namespace)
	secret, err := secrets.Get(ctx, bootstrapName(machine), metav1.GetOptions{})
	if err == nil {
		if !owned(secret, machine) {
			return fmt.Errorf("refusing to delete a foreign bootstrap Secret")
		}
		if secret.DeletionTimestamp == nil {
			if err = secrets.Delete(ctx, secret.Name, deleteOptions(secret)); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			return nil
		}
		if slices.Contains(secret.Finalizers, BootstrapFinalizer) {
			copy := secret.DeepCopy()
			copy.Finalizers = slices.DeleteFunc(copy.Finalizers, func(value string) bool { return value == BootstrapFinalizer })
			_, err = secrets.Update(ctx, copy, metav1.UpdateOptions{})
			return err
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	copy := object.DeepCopy()
	copy.SetFinalizers(slices.DeleteFunc(copy.GetFinalizers(), func(value string) bool { return value == Finalizer }))
	_, err = c.Dynamic.Resource(InfraMachines).Namespace(machine.Namespace).Update(ctx, copy, metav1.UpdateOptions{})
	return err
}
