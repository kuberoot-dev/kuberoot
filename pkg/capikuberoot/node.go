package capikuberoot

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	infra "github.com/kuberoot-dev/kuberoot/pkg/apis/infrastructure/v1alpha1"
	vmv1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha2"
)

const (
	nodeMachineUID = "infrastructure.cluster.x-k8s.io/kuberoot-machine-uid"
	nodeVMUID      = "infrastructure.cluster.x-k8s.io/kuberoot-vm-uid"
)

// bindNode closes the CAPI NodeRef gap for guests whose kubelet did not receive
// providerID from their boot metadata. It never searches the management cluster
// or claims a Node by IP alone: name, current address, creation and UID all bind
// this particular guest to this particular workload API.
func (c *Controller) bindNode(ctx context.Context, machine *infra.KuberootMachine, cluster *unstructured.Unstructured, vm *vmv1.VirtualMachine) (*corev1.ObjectReference, error) {
	secret, err := c.Kube.CoreV1().Secrets(machine.Namespace).Get(ctx, cluster.GetName()+"-kubeconfig", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, waiting{"WaitingForWorkloadKubeconfig", "waiting for the standard workload kubeconfig Secret"}
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the workload kubeconfig Secret")
	}
	if err = c.validateKubeconfigOwner(ctx, secret, cluster); err != nil {
		return nil, err
	}
	config, err := embeddedWorkloadConfig(secret)
	if err != nil {
		return nil, err
	}
	factory := c.WorkloadClient
	if factory == nil {
		factory = func(cfg *rest.Config) (kubernetes.Interface, error) { return kubernetes.NewForConfig(cfg) }
	}
	workload, err := factory(config)
	if err != nil {
		return nil, fmt.Errorf("cannot create the verified workload API client")
	}
	nodes := workload.CoreV1().Nodes()
	node, err := nodes.Get(ctx, vm.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if machine.Status.NodeRef != nil {
			return nil, fmt.Errorf("the bound workload Node disappeared; automatic rebinding is forbidden")
		}
		return nil, waiting{"WaitingForWorkloadNode", "waiting for the guest to register its workload Node"}
	}
	if err != nil {
		return nil, waiting{"WorkloadAPIUnavailable", "the workload Node API is not reachable yet"}
	}
	if err = validateNode(node, machine, vm); err != nil {
		return nil, err
	}
	if machine.Status.NodeRef == nil {
		// Commit the observed Node UID to the management API before changing
		// the workload API. A crash in between must not let a replacement
		// Node with the same name and IP inherit this provider identity.
		return objectRef(node, "v1", "Node"), waiting{"NodeIdentityRecorded", "workload Node identity observed; persisting it before providerID binding"}
	}
	providerID := ProviderPrefix + machine.Name
	if node.Spec.ProviderID != providerID || node.Annotations[nodeMachineUID] != string(machine.UID) || node.Annotations[nodeVMUID] != string(vm.UID) {
		copy := node.DeepCopy()
		copy.Spec.ProviderID = providerID
		if copy.Annotations == nil {
			copy.Annotations = map[string]string{}
		}
		copy.Annotations[nodeMachineUID], copy.Annotations[nodeVMUID] = string(machine.UID), string(vm.UID)
		// Update carries both the fetched UID and resourceVersion. A node that
		// was replaced or concurrently changed cannot receive this binding.
		node, err = nodes.Update(ctx, copy, metav1.UpdateOptions{})
		if err != nil {
			return nil, waiting{"NodeBindingPending", "workload Node changed while binding providerID; retrying"}
		}
		if err = validateNode(node, machine, vm); err != nil {
			return nil, err
		}
	}
	return objectRef(node, "v1", "Node"), nil
}

func validateNode(node *corev1.Node, machine *infra.KuberootMachine, vm *vmv1.VirtualMachine) error {
	if node.Name != vm.Name || node.Namespace != "" || node.UID == "" || node.DeletionTimestamp != nil {
		return fmt.Errorf("workload Node identity does not match the VM")
	}
	if machine.Status.NodeRef != nil && !sameReference(machine.Status.NodeRef, node, "v1", "Node") {
		return fmt.Errorf("the bound workload Node changed UID; automatic rebinding is forbidden")
	}
	if vm.CreationTimestamp.IsZero() || node.CreationTimestamp.IsZero() || node.CreationTimestamp.Before(&vm.CreationTimestamp) {
		return fmt.Errorf("workload Node predates this VM incarnation")
	}
	address, err := netip.ParseAddr(vm.Status.Address)
	if err != nil {
		return fmt.Errorf("VM address is invalid")
	}
	matched := false
	for _, candidate := range node.Status.Addresses {
		value, parseErr := netip.ParseAddr(candidate.Address)
		if candidate.Type == corev1.NodeInternalIP && parseErr == nil && value.Unmap() == address.Unmap() {
			matched = true
		}
	}
	if !matched {
		return waiting{"WaitingForNodeAddress", "workload Node does not yet advertise this VM's internal address"}
	}
	if node.Spec.ProviderID != "" && node.Spec.ProviderID != ProviderPrefix+machine.Name {
		return fmt.Errorf("workload Node already has another providerID")
	}
	if binding := node.Annotations[nodeMachineUID]; binding != "" && binding != string(machine.UID) {
		return fmt.Errorf("workload Node is bound to another infrastructure Machine")
	}
	if binding := node.Annotations[nodeVMUID]; binding != "" && binding != string(vm.UID) {
		return fmt.Errorf("workload Node is bound to another VM incarnation")
	}
	return nil
}

func (c *Controller) validateKubeconfigOwner(ctx context.Context, secret *corev1.Secret, cluster *unstructured.Unstructured) error {
	if secret.Namespace != cluster.GetNamespace() || secret.Name != cluster.GetName()+"-kubeconfig" || secret.UID == "" || secret.DeletionTimestamp != nil || secret.Type != corev1.SecretType("cluster.x-k8s.io/secret") || secret.Labels[ClusterLabel] != cluster.GetName() {
		return fmt.Errorf("workload kubeconfig does not match the standard CAPI identity")
	}
	if hasOwner(secret, Clusters.GroupVersion().String(), "Cluster", cluster.GetName(), cluster.GetUID()) {
		return nil
	}
	// Kamaji v0.19.0 resources.go creates this Secret with a controller
	// reference to KamajiControlPlane, not a direct Cluster owner reference.
	name, _, _ := unstructured.NestedString(cluster.Object, "spec", "controlPlaneRef", "name")
	if name == "" {
		return waiting{"WaitingForControlPlane", "CAPI Cluster has no control plane reference yet"}
	}
	controlPlane, err := c.Dynamic.Resource(KamajiControlPlanes).Namespace(cluster.GetNamespace()).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return waiting{"WaitingForControlPlane", "waiting for the referenced Kamaji control plane"}
	}
	if err != nil {
		return fmt.Errorf("cannot verify the workload kubeconfig producer")
	}
	if !referenceMatches(cluster, "controlPlaneRef", controlPlane, KamajiControlPlanes.GroupVersion().String(), "KamajiControlPlane") || !hasOwner(controlPlane, Clusters.GroupVersion().String(), "Cluster", cluster.GetName(), cluster.GetUID()) || !hasOwner(secret, KamajiControlPlanes.GroupVersion().String(), "KamajiControlPlane", controlPlane.GetName(), controlPlane.GetUID()) {
		return fmt.Errorf("workload kubeconfig producer does not belong to this CAPI Cluster")
	}
	return nil
}

func embeddedWorkloadConfig(secret *corev1.Secret) (*rest.Config, error) {
	if len(secret.Data["value"]) == 0 || len(secret.Data["value"]) > 1<<20 {
		return nil, fmt.Errorf("workload kubeconfig data.value is missing or oversized")
	}
	loaded, err := clientcmd.Load(secret.Data["value"])
	if err != nil {
		return nil, fmt.Errorf("workload kubeconfig cannot be parsed")
	}
	selected := loaded.Contexts[loaded.CurrentContext]
	if selected == nil {
		return nil, fmt.Errorf("workload kubeconfig has no selected context")
	}
	cluster, auth := loaded.Clusters[selected.Cluster], loaded.AuthInfos[selected.AuthInfo]
	if cluster == nil || auth == nil {
		return nil, fmt.Errorf("workload kubeconfig context is incomplete")
	}
	endpoint, err := url.Parse(cluster.Server)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, fmt.Errorf("workload kubeconfig requires a direct HTTPS API endpoint")
	}
	if cluster.InsecureSkipTLSVerify || len(cluster.CertificateAuthorityData) == 0 || cluster.CertificateAuthority != "" || cluster.ProxyURL != "" || auth.Exec != nil || auth.AuthProvider != nil || auth.ClientCertificate != "" || auth.ClientKey != "" || auth.TokenFile != "" || auth.Username != "" || auth.Password != "" || auth.Impersonate != "" || len(auth.ImpersonateGroups) != 0 || len(auth.ImpersonateUserExtra) != 0 {
		return nil, fmt.Errorf("workload kubeconfig must contain verified TLS and embedded credentials without files, plugins, proxies or impersonation")
	}
	if auth.Token == "" && (len(auth.ClientCertificateData) == 0 || len(auth.ClientKeyData) == 0) {
		return nil, fmt.Errorf("workload kubeconfig has no embedded client credentials")
	}
	config, err := clientcmd.NewDefaultClientConfig(*loaded, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("workload kubeconfig credentials cannot be loaded")
	}
	config.Timeout = 10 * time.Second
	config.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	return config, nil
}
