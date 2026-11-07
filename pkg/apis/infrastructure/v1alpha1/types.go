// Package v1alpha1 defines the Kuberoot infrastructure provider's CAPI v1beta1 contract.
// +kubebuilder:object:generate=true
// +groupName=infrastructure.cluster.x-k8s.io
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	vmv1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha2"
)

var GroupVersion = schema.GroupVersion{Group: "infrastructure.cluster.x-k8s.io", Version: "v1alpha1"}

var (
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &KuberootCluster{}, &KuberootClusterList{}, &KuberootMachine{}, &KuberootMachineList{}, &KuberootMachineTemplate{}, &KuberootMachineTemplateList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
	AddToScheme = SchemeBuilder.AddToScheme
)

// Condition has the legacy CAPI v1beta1 wire shape. It does not introduce a
// dependency on a different Kubernetes SDK version into the node repository.
type Condition struct {
	Type               string                 `json:"type"`
	Status             corev1.ConditionStatus `json:"status"`
	Severity           string                 `json:"severity,omitempty"`
	LastTransitionTime metav1.Time            `json:"lastTransitionTime"`
	Reason             string                 `json:"reason,omitempty"`
	Message            string                 `json:"message,omitempty"`
}

type APIEndpoint struct {
	Host string `json:"host"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

// KuberootCluster describes infrastructure for a separately managed control plane.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Ready,type=boolean,JSONPath=`.status.ready`
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1alpha1"
// +kubebuilder:metadata:annotations="api-approved.kubernetes.io=unapproved, experimental-only"
type KuberootCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KuberootClusterSpec   `json:"spec"`
	Status            KuberootClusterStatus `json:"status,omitempty"`
}

type KuberootClusterSpec struct {
	// Kamaji owns the control plane endpoint. The infrastructure object has a
	// real controller and must not use cluster.x-k8s.io/managed-by.
	// +kubebuilder:default=true
	// +kubebuilder:validation:Enum=true
	ExternalManagedControlPlane bool `json:"externalManagedControlPlane"`
	// +optional
	ControlPlaneEndpoint *APIEndpoint `json:"controlPlaneEndpoint,omitempty"`
}

type KuberootClusterStatus struct {
	Ready              bool  `json:"ready"`
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type KuberootClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KuberootCluster `json:"items"`
}

// KuberootMachine owns one same-namespace native VirtualMachine.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.vmRef) || (has(self.status) && has(self.status.vmRef) && self.status.vmRef == oldSelf.status.vmRef)",message="the bound VM identity cannot be removed or changed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.nodeRef) || (has(self.status) && has(self.status.nodeRef) && self.status.nodeRef == oldSelf.status.nodeRef)",message="the bound Node identity cannot be removed or changed"
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Ready,type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name=ProviderID,type=string,JSONPath=`.spec.providerID`
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1alpha1"
// +kubebuilder:metadata:annotations="api-approved.kubernetes.io=unapproved, experimental-only"
type KuberootMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KuberootMachineSpec   `json:"spec"`
	Status            KuberootMachineStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.maxCPUs) || self.cpus <= self.maxCPUs",message="cpus exceeds maxCPUs"
// +kubebuilder:validation:XValidation:rule="!has(self.maxMemory) || quantity(string(self.memory)).compareTo(quantity(string(self.maxMemory))) <= 0",message="memory exceeds maxMemory"
// +kubebuilder:validation:XValidation:rule="quantity(string(self.memory)).compareTo(quantity('0')) > 0",message="memory must be positive"
// +kubebuilder:validation:XValidation:rule="!has(self.maxMemory) || (quantity(string(self.memory)).isInteger() && quantity(string(self.maxMemory)).isInteger() && quantity(string(self.memory)).asInteger() % 134217728 == 0 && quantity(string(self.maxMemory)).asInteger() % 134217728 == 0)",message="memory and maxMemory must be multiples of 128Mi when maxMemory is set"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.providerID) || (has(self.providerID) && self.providerID == oldSelf.providerID)",message="providerID cannot be removed or changed"
// +kubebuilder:validation:XValidation:rule="quantity(string(self.disk.size)).compareTo(quantity('0')) > 0",message="disk size must be positive"
type KuberootMachineSpec struct {
	// ProviderID is set by this controller and copied to the CAPI Machine by
	// the CAPI core controller. It must match the workload Node's providerID.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="providerID is immutable"
	ProviderID *string `json:"providerID,omitempty"`
	// +kubebuilder:validation:Minimum=1
	CPUs int32 `json:"cpus"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxCPUs *int32            `json:"maxCPUs,omitempty"`
	Memory  resource.Quantity `json:"memory"`
	// +optional
	MaxMemory *resource.Quantity `json:"maxMemory,omitempty"`
	Disk      vmv1.Disk          `json:"disk"`
	// +kubebuilder:default=3
	// +kubebuilder:validation:Enum=1;3
	Replicas int32 `json:"replicas,omitempty"`
	// Node is an optional placement constraint in the infrastructure cluster.
	// +optional
	Node string `json:"node,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(oldSelf.vmRef) || (has(self.vmRef) && self.vmRef == oldSelf.vmRef)",message="vmRef cannot be removed or changed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.nodeRef) || (has(self.nodeRef) && self.nodeRef == oldSelf.nodeRef)",message="nodeRef cannot be removed or changed"
type KuberootMachineStatus struct {
	Ready              bool                 `json:"ready"`
	ObservedGeneration int64                `json:"observedGeneration,omitempty"`
	Addresses          []corev1.NodeAddress `json:"addresses,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions     []Condition             `json:"conditions,omitempty"`
	FailureReason  *string                 `json:"failureReason,omitempty"`
	FailureMessage *string                 `json:"failureMessage,omitempty"`
	VMRef          *corev1.ObjectReference `json:"vmRef,omitempty"`
	// NodeRef binds the workload Node incarnation before its providerID is set.
	// A replacement Node needs reconciliation through a new Machine identity.
	NodeRef *corev1.ObjectReference `json:"nodeRef,omitempty"`
}

// +kubebuilder:object:root=true
type KuberootMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KuberootMachine `json:"items"`
}

type TemplateMetadata struct {
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1alpha1"
// +kubebuilder:metadata:annotations="api-approved.kubernetes.io=unapproved, experimental-only"
type KuberootMachineTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KuberootMachineTemplateSpec `json:"spec"`
}

// Template names are derived from their contents by the platform chart; an
// existing MachineSet must keep referencing the exact template it was given.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="machine template spec is immutable; create a new template name"
type KuberootMachineTemplateSpec struct {
	Template KuberootMachineTemplateResource `json:"template"`
}

type KuberootMachineTemplateResource struct {
	ObjectMeta TemplateMetadata    `json:"metadata,omitempty"`
	Spec       KuberootMachineSpec `json:"spec"`
}

// +kubebuilder:object:root=true
type KuberootMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KuberootMachineTemplate `json:"items"`
}
