package capikuberoot

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	infra "github.com/kuberoot-dev/kuberoot/pkg/apis/infrastructure/v1alpha1"
	vmv1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha2"
)

type Controller struct {
	Dynamic      dynamic.Interface
	Kube         kubernetes.Interface
	Namespace    string
	PollInterval time.Duration
	// Tests inject a workload API without loading credentials or opening a network
	// connection. The default accepts only embedded, TLS-verified kubeconfigs.
	WorkloadClient func(*rest.Config) (kubernetes.Interface, error)
}

// Run uses an authoritative periodic list rather than a cache: a missing watch
// event must not leave an imported or newly booted worker without its providerID.
func (c *Controller) Run(ctx context.Context) error {
	period := c.PollInterval
	if period <= 0 {
		period = 5 * time.Second
	}
	for ctx.Err() == nil {
		clusters, err := c.Dynamic.Resource(InfraClusters).Namespace(c.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("list KuberootClusters: %v", err)
		} else {
			for _, object := range clusters.Items {
				if ctx.Err() != nil {
					break
				}
				if err := c.ReconcileCluster(ctx, object.GetNamespace(), object.GetName()); err != nil {
					klog.Errorf("KuberootCluster %s/%s: %v", object.GetNamespace(), object.GetName(), err)
				}
			}
		}
		machines, err := c.Dynamic.Resource(InfraMachines).Namespace(c.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("list KuberootMachines: %v", err)
		} else {
			for _, object := range machines.Items {
				if ctx.Err() != nil {
					break
				}
				if err := c.ReconcileMachine(ctx, object.GetNamespace(), object.GetName()); err != nil {
					klog.Errorf("KuberootMachine %s/%s: %v", object.GetNamespace(), object.GetName(), err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(period):
		}
	}
	return nil
}

func (c *Controller) ReconcileCluster(ctx context.Context, namespace, name string) error {
	resource := c.Dynamic.Resource(InfraClusters).Namespace(namespace)
	object, err := resource.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var cluster infra.KuberootCluster
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &cluster); err != nil {
		return err
	}
	if paused(&cluster) {
		return nil
	}
	previous := cluster.Status
	previous.Conditions = append([]infra.Condition(nil), previous.Conditions...)
	cluster.Status.ObservedGeneration = cluster.Generation
	owner, err := capiOwner(&cluster, "Cluster")
	if err == nil {
		var capi *unstructured.Unstructured
		capi, err = c.Dynamic.Resource(Clusters).Namespace(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err == nil {
			isPaused, _, _ := unstructured.NestedBool(capi.Object, "spec", "paused")
			if paused(capi) || isPaused {
				return nil
			}
			if capi.GetUID() != owner.UID || !referenceMatches(capi, "infrastructureRef", &cluster, infra.GroupVersion.String(), "KuberootCluster") {
				err = fmt.Errorf("CAPI Cluster identity or infrastructure reference does not match")
			}
		}
	}
	_, managed := cluster.Annotations["cluster.x-k8s.io/managed-by"]
	if managed || !cluster.Spec.ExternalManagedControlPlane {
		err = fmt.Errorf("KuberootCluster requires externalManagedControlPlane=true and no managed-by annotation")
	}
	cluster.Status.Ready = err == nil && cluster.DeletionTimestamp == nil
	if cluster.Status.Ready {
		setCondition(&cluster.Status.Conditions, "Ready", corev1.ConditionTrue, "InfrastructureReady", "", "")
	} else {
		reason, message, severity := "Deleting", "infrastructure cluster is deleting", "Info"
		if err != nil {
			reason, message, severity = conditionError(err)
		}
		setCondition(&cluster.Status.Conditions, "Ready", corev1.ConditionFalse, reason, message, severity)
	}
	if apiequality.Semantic.DeepEqual(previous, cluster.Status) {
		return nil
	}
	updated, convertErr := toObject(&cluster)
	if convertErr != nil {
		return convertErr
	}
	_, updateErr := resource.UpdateStatus(ctx, updated, metav1.UpdateOptions{})
	return updateErr
}

func (c *Controller) ReconcileMachine(ctx context.Context, namespace, name string) error {
	object, err := c.Dynamic.Resource(InfraMachines).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var machine infra.KuberootMachine
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &machine); err != nil {
		return err
	}
	if machine.Namespace == "" || machine.UID == "" {
		return fmt.Errorf("infrastructure Machine lacks namespace or UID")
	}
	if paused(&machine) {
		return nil
	}
	previous := machine.Status
	// Keep a private copy: appending/updating a condition must not mutate the
	// comparison snapshot and silently suppress a status update.
	previous.Conditions = append([]infra.Condition(nil), previous.Conditions...)
	owner, err := capiOwner(&machine, "Machine")
	if err != nil {
		return c.machineError(ctx, &machine, previous, err)
	}
	capi, err := c.Dynamic.Resource(Machines).Namespace(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && machine.DeletionTimestamp != nil {
		if isPaused, pauseErr := c.boundClusterPaused(ctx, &machine); pauseErr != nil || isPaused {
			return pauseErr
		}
		return c.deleteMachine(ctx, &machine, object)
	}
	if err != nil {
		return c.machineError(ctx, &machine, previous, err)
	}
	if capi.GetUID() != owner.UID || !referenceMatches(capi, "infrastructureRef", &machine, infra.GroupVersion.String(), "KuberootMachine") {
		return c.machineError(ctx, &machine, previous, fmt.Errorf("CAPI Machine identity or infrastructure reference does not match"))
	}
	if paused(capi) {
		return nil
	}
	clusterName, _, err := unstructured.NestedString(capi.Object, "spec", "clusterName")
	if err != nil || clusterName == "" {
		return c.machineError(ctx, &machine, previous, fmt.Errorf("CAPI Machine has no valid spec.clusterName"))
	}
	cluster, err := c.Dynamic.Resource(Clusters).Namespace(namespace).Get(ctx, clusterName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && machine.DeletionTimestamp != nil {
		return c.deleteMachine(ctx, &machine, object)
	}
	if err != nil {
		return c.machineError(ctx, &machine, previous, err)
	}
	isPaused, _, _ := unstructured.NestedBool(cluster.Object, "spec", "paused")
	if paused(cluster) || isPaused {
		return nil
	}
	infraName, _, _ := unstructured.NestedString(cluster.Object, "spec", "infrastructureRef", "name")
	if infraName != "" {
		infraObject, getErr := c.Dynamic.Resource(InfraClusters).Namespace(namespace).Get(ctx, infraName, metav1.GetOptions{})
		if getErr == nil && paused(infraObject) {
			return nil
		}
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			return getErr
		}
	}
	if machine.DeletionTimestamp != nil {
		return c.deleteMachine(ctx, &machine, object)
	}
	if err = c.infrastructureReady(ctx, &machine, cluster); err != nil {
		return c.machineError(ctx, &machine, previous, err)
	}
	if added, err := c.addFinalizer(ctx, object, cluster); added || err != nil {
		if err != nil {
			return c.machineError(ctx, &machine, previous, err)
		}
		return nil
	}
	if err = validateMachine(&machine); err != nil {
		return c.machineError(ctx, &machine, previous, err)
	}
	providerID := ProviderPrefix + machine.Name
	if machine.Spec.ProviderID == nil {
		copy := object.DeepCopy()
		if err = unstructured.SetNestedField(copy.Object, providerID, "spec", "providerID"); err != nil {
			return err
		}
		_, err = c.Dynamic.Resource(InfraMachines).Namespace(namespace).Update(ctx, copy, metav1.UpdateOptions{})
		return err
	}
	if *machine.Spec.ProviderID != providerID {
		return c.machineError(ctx, &machine, previous, fmt.Errorf("providerID does not match this infrastructure Machine"))
	}
	vmObject, err := c.Dynamic.Resource(VirtualMachines).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return c.machineError(ctx, &machine, previous, err)
	}
	if exists && !owned(vmObject, &machine) {
		return c.machineError(ctx, &machine, previous, fmt.Errorf("VirtualMachine belongs to another owner; adoption is forbidden"))
	}
	if exists && !slices.Contains(vmObject.GetFinalizers(), VMFinalizer) {
		return c.machineError(ctx, &machine, previous, fmt.Errorf("VirtualMachine lifetime protection was removed; refusing automatic adoption or recovery"))
	}
	if machine.Status.VMRef != nil && (!exists || !sameReference(machine.Status.VMRef, vmObject, vmv1.GroupVersion.String(), "VirtualMachine")) {
		return c.machineError(ctx, &machine, previous, fmt.Errorf("the bound VirtualMachine disappeared or changed UID; automatic recreation is forbidden"))
	}
	secret, err := c.bootstrap(ctx, &machine, capi, exists)
	if err != nil {
		return c.machineError(ctx, &machine, previous, err)
	}
	desired := machineVM(&machine, secret.Name)
	if !exists {
		created, convertErr := toObject(desired)
		if convertErr != nil {
			return convertErr
		}
		vmObject, err = c.Dynamic.Resource(VirtualMachines).Namespace(namespace).Create(ctx, created, metav1.CreateOptions{})
		if err != nil {
			return c.machineError(ctx, &machine, previous, err)
		}
	}
	if vmObject.GetUID() == "" {
		return c.machineError(ctx, &machine, previous, fmt.Errorf("VirtualMachine has no API identity"))
	}
	if machine.Status.VMRef == nil {
		machine.Status.VMRef = objectRef(vmObject, vmv1.GroupVersion.String(), "VirtualMachine")
		return c.machineError(ctx, &machine, previous, waiting{"VMProvisioning", "VirtualMachine identity recorded; waiting for provisioning"})
	}
	var vm vmv1.VirtualMachine
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(vmObject.Object, &vm); err != nil {
		return c.machineError(ctx, &machine, previous, err)
	}
	if vm.DeletionTimestamp != nil {
		return c.machineError(ctx, &machine, previous, fmt.Errorf("the bound VirtualMachine is deleting"))
	}
	// Only current CPU/RAM may change in place. Replacing disk/bootstrap,
	// placement, replication or capacity ceilings requires a new CAPI Machine.
	immutable := vm.Spec
	immutable.CPUs, immutable.Memory = desired.Spec.CPUs, desired.Spec.Memory
	if !apiequality.Semantic.DeepEqual(immutable, desired.Spec) {
		return c.machineError(ctx, &machine, previous, fmt.Errorf("immutable VM configuration differs from the infrastructure Machine"))
	}
	if !apiequality.Semantic.DeepEqual(vm.Spec, desired.Spec) {
		vm.Spec = desired.Spec
		updated, convertErr := toObject(&vm)
		if convertErr != nil {
			return convertErr
		}
		_, err = c.Dynamic.Resource(VirtualMachines).Namespace(namespace).Update(ctx, updated, metav1.UpdateOptions{})
		if err != nil {
			return c.machineError(ctx, &machine, previous, err)
		}
		return c.machineError(ctx, &machine, previous, waiting{"VMResizing", "waiting for the VM to apply its requested resources"})
	}
	address, parseErr := netip.ParseAddr(vm.Status.Address)
	if vm.Status.Phase != "Running" || parseErr != nil || !address.IsGlobalUnicast() || address.IsLoopback() {
		return c.machineError(ctx, &machine, previous, waiting{"VMProvisioning", "waiting for a running VM and its unicast address"})
	}
	machine.Status.Ready = true
	machine.Status.ObservedGeneration = machine.Generation
	machine.Status.FailureReason, machine.Status.FailureMessage = nil, nil
	machine.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: address.String()}, {Type: corev1.NodeHostName, Address: vm.Name}}
	setCondition(&machine.Status.Conditions, "Ready", corev1.ConditionTrue, "VMRunning", "", "")
	nodeRef, bindErr := c.bindNode(ctx, &machine, cluster, &vm)
	if nodeRef != nil {
		machine.Status.NodeRef = nodeRef
	}
	if bindErr != nil {
		reason, message, severity := conditionError(bindErr)
		setCondition(&machine.Status.Conditions, "NodeProviderIDReady", corev1.ConditionFalse, reason, message, severity)
	} else {
		machine.Status.NodeRef = nodeRef
		setCondition(&machine.Status.Conditions, "NodeProviderIDReady", corev1.ConditionTrue, "NodeProviderIDBound", "", "")
	}
	return c.writeMachineStatus(ctx, &machine, previous)
}

func (c *Controller) boundClusterPaused(ctx context.Context, machine *infra.KuberootMachine) (bool, error) {
	name, uid := machine.Annotations[clusterNameBinding], machine.Annotations[clusterUIDBinding]
	if name == "" || uid == "" {
		return false, nil
	}
	cluster, err := c.Dynamic.Resource(Clusters).Namespace(machine.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if string(cluster.GetUID()) != uid {
		return false, nil
	}
	specPaused, _, err := unstructured.NestedBool(cluster.Object, "spec", "paused")
	if err != nil {
		return false, err
	}
	if specPaused || paused(cluster) {
		return true, nil
	}
	infraName, _, _ := unstructured.NestedString(cluster.Object, "spec", "infrastructureRef", "name")
	if infraName == "" {
		return false, nil
	}
	infraObject, err := c.Dynamic.Resource(InfraClusters).Namespace(machine.Namespace).Get(ctx, infraName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return paused(infraObject), nil
}

func (c *Controller) infrastructureReady(ctx context.Context, machine *infra.KuberootMachine, cluster *unstructured.Unstructured) error {
	refName, _, _ := unstructured.NestedString(cluster.Object, "spec", "infrastructureRef", "name")
	if refName == "" || cluster.GetUID() == "" {
		return fmt.Errorf("CAPI Cluster has no stable infrastructure identity")
	}
	object, err := c.Dynamic.Resource(InfraClusters).Namespace(machine.Namespace).Get(ctx, refName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !referenceMatches(cluster, "infrastructureRef", object, infra.GroupVersion.String(), "KuberootCluster") || !hasOwner(object, Clusters.GroupVersion().String(), "Cluster", cluster.GetName(), cluster.GetUID()) {
		return fmt.Errorf("KuberootCluster does not belong to this CAPI Cluster")
	}
	if paused(object) {
		return waiting{"InfrastructurePaused", "KuberootCluster is paused"}
	}
	if _, managed := object.GetAnnotations()["cluster.x-k8s.io/managed-by"]; managed {
		return fmt.Errorf("KuberootCluster must not have managed-by")
	}
	external, _, err := unstructured.NestedBool(object.Object, "spec", "externalManagedControlPlane")
	if err != nil || !external {
		return fmt.Errorf("KuberootCluster requires an external managed control plane")
	}
	if label := machine.Labels[ClusterLabel]; label != "" && label != cluster.GetName() {
		return fmt.Errorf("infrastructure Machine cluster label disagrees with its CAPI owner")
	}
	return nil
}

func validateMachine(machine *infra.KuberootMachine) error {
	spec := machine.Spec
	if spec.CPUs < 1 || spec.Memory.Sign() <= 0 || spec.Disk.Size.Sign() <= 0 || (spec.Replicas != 1 && spec.Replicas != 3) {
		return fmt.Errorf("invalid CPU, memory, disk size or replicas")
	}
	if spec.MaxCPUs != nil && *spec.MaxCPUs < spec.CPUs {
		return fmt.Errorf("maxCPUs is below cpus")
	}
	if spec.MaxMemory != nil {
		memory, ok := spec.Memory.AsInt64()
		maximum, maxOK := spec.MaxMemory.AsInt64()
		if !ok || !maxOK || maximum < memory || memory%(128<<20) != 0 || maximum%(128<<20) != 0 {
			return fmt.Errorf("memory/maxMemory must use 128Mi steps within their immutable ceiling")
		}
	}
	image, err := url.Parse(spec.Disk.Image)
	if err != nil || (image.Scheme != "https" && image.Scheme != "http") || image.Host == "" {
		return fmt.Errorf("disk.image requires an HTTP(S) UEFI image URL")
	}
	// Native volume import invokes qemu-img, which treats compressed archive
	// bytes as a raw disk rather than decompressing them. Refuse recognizable
	// archives before imageWritten could permanently checkpoint a broken disk.
	for _, suffix := range []string{".xz", ".gz", ".zst", ".zstd", ".bz2", ".zip"} {
		if strings.HasSuffix(strings.ToLower(image.Path), suffix) {
			return fmt.Errorf("disk.image must be an uncompressed UEFI raw or qcow2 image; decompress and host the image before creating the Machine")
		}
	}
	return nil
}

func machineVM(machine *infra.KuberootMachine, secret string) *vmv1.VirtualMachine {
	running := true
	maxCPUs := int32(0)
	if machine.Spec.MaxCPUs != nil {
		maxCPUs = *machine.Spec.MaxCPUs
	}
	return &vmv1.VirtualMachine{TypeMeta: metav1.TypeMeta{APIVersion: vmv1.GroupVersion.String(), Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Name: machine.Name, Namespace: machine.Namespace, OwnerReferences: []metav1.OwnerReference{machineOwner(machine)}, Finalizers: []string{VMFinalizer}},
		Spec: vmv1.VirtualMachineSpec{CPUs: machine.Spec.CPUs, MaxCPUs: maxCPUs, Memory: machine.Spec.Memory,
			MaxMemory: machine.Spec.MaxMemory, Disk: machine.Spec.Disk, Replicas: machine.Spec.Replicas, Node: machine.Spec.Node,
			Running: &running, UserDataSecret: &vmv1.SecretRef{Name: secret}, SeedMode: vmv1.SeedModeNoCloudNet}}
}

func conditionError(err error) (string, string, string) {
	var pending waiting
	if errors.As(err, &pending) {
		return pending.reason, pending.message, "Info"
	}
	return "ReconcileError", err.Error(), "Error"
}

func setCondition(conditions *[]infra.Condition, kind string, status corev1.ConditionStatus, reason, message, severity string) {
	next := infra.Condition{Type: kind, Status: status, Reason: reason, Message: message, Severity: severity, LastTransitionTime: metav1.Now()}
	for i, previous := range *conditions {
		if previous.Type == kind {
			if previous.Status == status {
				next.LastTransitionTime = previous.LastTransitionTime
			}
			(*conditions)[i] = next
			return
		}
	}
	*conditions = append(*conditions, next)
}

func (c *Controller) machineError(ctx context.Context, machine *infra.KuberootMachine, previous infra.KuberootMachineStatus, err error) error {
	machine.Status.Ready = false
	machine.Status.ObservedGeneration = machine.Generation
	reason, message, severity := conditionError(err)
	setCondition(&machine.Status.Conditions, "Ready", corev1.ConditionFalse, reason, message, severity)
	return c.writeMachineStatus(ctx, machine, previous)
}

func (c *Controller) writeMachineStatus(ctx context.Context, machine *infra.KuberootMachine, previous infra.KuberootMachineStatus) error {
	if apiequality.Semantic.DeepEqual(previous, machine.Status) {
		return nil
	}
	object, err := toObject(machine)
	if err != nil {
		return err
	}
	_, err = c.Dynamic.Resource(InfraMachines).Namespace(machine.Namespace).UpdateStatus(ctx, object, metav1.UpdateOptions{})
	return err
}
