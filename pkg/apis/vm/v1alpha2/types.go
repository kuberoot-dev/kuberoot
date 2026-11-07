// Package v1alpha2 is the vm.kuberoot.dev API: virtual machines of a
// hypervisor cluster. The cluster places each machine on a node, keeps its
// disk replicated on others, and starts it on another node when its node
// fails.
// +kubebuilder:object:generate=true
// +groupName=vm.kuberoot.dev
package v1alpha2

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion of the virtual machines API.
var GroupVersion = schema.GroupVersion{Group: "vm.kuberoot.dev", Version: "v1alpha2"}

var (
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &VirtualMachine{}, &VirtualMachineList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
	AddToScheme = SchemeBuilder.AddToScheme
)

// VirtualMachine is a machine of the cluster, wherever it runs.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=vm
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Running,type=boolean,JSONPath=`.spec.running`
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name=Node,type=string,JSONPath=`.status.node`
// +kubebuilder:printcolumn:name=Address,type=string,JSONPath=`.status.address`
// +kubebuilder:printcolumn:name=Replicas,type=string,JSONPath=`.status.replicaNodes`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.backendID) || (has(self.status) && has(self.status.backendID) && self.status.backendID == oldSelf.status.backendID)",message="backendID cannot be removed, including by removing status"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.instanceID) || (has(self.status) && has(self.status.instanceID) && self.status.instanceID == oldSelf.status.instanceID)",message="instanceID cannot be removed, including by removing status"
type VirtualMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualMachineSpec   `json:"spec"`
	Status VirtualMachineStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.maxCPUs) || self.maxCPUs >= self.cpus",message="maxCPUs must be at least cpus"
// +kubebuilder:validation:XValidation:rule="has(self.maxCPUs) == has(oldSelf.maxCPUs) && (!has(self.maxCPUs) || self.maxCPUs == oldSelf.maxCPUs)",message="maxCPUs is immutable; create a new machine to change its ceiling"
// +kubebuilder:validation:XValidation:rule="has(self.maxMemory) == has(oldSelf.maxMemory) && (!has(self.maxMemory) || quantity(string(self.maxMemory)).compareTo(quantity(string(oldSelf.maxMemory))) == 0)",message="maxMemory is immutable; create a new machine to change its ceiling"
// +kubebuilder:validation:XValidation:rule="!has(self.maxMemory) || quantity(string(self.maxMemory)).compareTo(quantity(string(self.memory))) >= 0",message="maxMemory must be at least memory"
// +kubebuilder:validation:XValidation:rule="!has(self.maxMemory) || (quantity(string(self.memory)).isInteger() && quantity(string(self.maxMemory)).isInteger() && quantity(string(self.memory)).asInteger() > 0 && quantity(string(self.memory)).asInteger() % 134217728 == 0 && quantity(string(self.maxMemory)).asInteger() % 134217728 == 0)",message="memory and maxMemory must be positive multiples of 128Mi when maxMemory is set"
// +kubebuilder:validation:XValidation:rule="has(self.seedMode) == has(oldSelf.seedMode) && (!has(self.seedMode) || self.seedMode == oldSelf.seedMode)",message="seedMode is immutable; create a new machine to change its bootstrap protocol"
type VirtualMachineSpec struct {
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	CPUs int32 `json:"cpus,omitempty"`
	// MaxCPUs is fixed at creation. Growth within it does not reboot the
	// guest; shrinking does. Omit it to restart on every CPU change.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxCPUs int32 `json:"maxCPUs,omitempty"`
	// +kubebuilder:default="512Mi"
	Memory resource.Quantity `json:"memory,omitempty"`
	// MaxMemory is fixed at creation and reserved in full on the host.
	// With it, memory and maxMemory use 128Mi steps. Growth is live;
	// shrinking restarts the guest. Omit it for restart-on-change behavior.
	// +optional
	MaxMemory *resource.Quantity `json:"maxMemory,omitempty"`
	Disk      Disk               `json:"disk"`
	// Running: the machine runs; false shuts it down and keeps its disk.
	// +kubebuilder:default=true
	Running *bool `json:"running,omitempty"`
	// Replicas of the disk, on as many nodes: 3, so that the machine moves
	// when its node fails and two nodes keep the majority its disk needs to
	// be written; or 1, a disk on one node that stays with it.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Enum=1;3
	Replicas int32 `json:"replicas,omitempty"`
	// Node to run on, among the replicas; any when unset. Changed while the
	// machine runs, the machine moves there alive.
	// +optional
	Node string `json:"node,omitempty"`
	// UserData is cloud-init's user data for the machine, a #cloud-config
	// document or a script, which the machines' gateway serves to it alone;
	// an image that runs cloud-init reads it at its first boot.
	// +optional
	UserData string `json:"userData,omitempty"`
	// UserDataSecret names a Secret whose key userData holds the user data
	// instead, for user data with credentials in it.
	// +optional
	UserDataSecret *SecretRef `json:"userDataSecret,omitempty"`
	// SeedMode selects the SMBIOS NoCloud protocol. Unset preserves the
	// original ds=nocloud serial. NoCloudNet uses ds=nocloud-net and serves
	// a MAC-specific DHCP network-config, required by Talos HTTP bootstrap.
	// It is fixed at creation, including whether the field is present.
	// +optional
	SeedMode SeedMode `json:"seedMode,omitempty"`
}

// SeedMode identifies the guest bootstrap protocol.
// +kubebuilder:validation:Enum=NoCloud;NoCloudNet
type SeedMode string

const (
	SeedModeNoCloud    SeedMode = "NoCloud"
	SeedModeNoCloudNet SeedMode = "NoCloudNet"
)

// SecretRef names a Secret in the VirtualMachine namespace. Cross-namespace
// references are deliberately absent from the tenant API.
type SecretRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type Disk struct {
	// +kubebuilder:default="4Gi"
	Size resource.Quantity `json:"size,omitempty"`
	// Image written onto the disk when it is created: a URL of a qcow2 or
	// raw disk image that boots with UEFI.
	Image string `json:"image"`
}

// +kubebuilder:validation:XValidation:rule="!has(oldSelf.backendID) || (has(self.backendID) && self.backendID == oldSelf.backendID)",message="backendID cannot be removed or changed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.instanceID) || (has(self.instanceID) && self.instanceID == oldSelf.instanceID)",message="instanceID cannot be removed or changed"
type VirtualMachineStatus struct {
	// BackendID binds this API object to the node Machine and Volume names.
	// Only the controller or the privileged migration tool writes it. New
	// objects use their UID; imports retain the old backend name.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="backendID is immutable"
	BackendID string `json:"backendID,omitempty"`
	// InstanceID is cloud-init's persistent guest identity. An imported VM
	// keeps its original UID even though recreating the API changes its UID.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9-]+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="instanceID is immutable"
	InstanceID string `json:"instanceID,omitempty"`
	// Phase: Pending, Starting, Running, Stopped, Moving or Failed.
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	// Node the machine runs on, and the nodes holding its disk.
	Node         string   `json:"node,omitempty"`
	ReplicaNodes []string `json:"replicaNodes,omitempty"`
	// MAC of its network interface, and the address it took.
	MAC     string `json:"mac,omitempty"`
	Address string `json:"address,omitempty"`
	// Minor and Port of its replicated disk.
	Minor int32 `json:"minor,omitempty"`
	Port  int32 `json:"port,omitempty"`
	// Moves counts the times the machine was started on another node.
	Moves int32 `json:"moves,omitempty"`
	// ReplicaAddresses are the replica nodes' addresses as last seen, for
	// the disk to keep its peers while a node is away.
	ReplicaAddresses map[string]string `json:"replicaAddresses,omitempty"`
	// ImageWritten: the disk holds its data; the image is never written again.
	ImageWritten bool `json:"imageWritten,omitempty"`
	// Migration is the machine moving alive to another node.
	// +optional
	Migration *Migration `json:"migration,omitempty"`
	// FailedMigration names the node the last live move to failed for; it
	// is not tried again until spec.node changes.
	FailedMigration string `json:"failedMigration,omitempty"`
}

type Migration struct {
	Target string `json:"target"`
	// Phase: Preparing, Receiving, Sending.
	Phase     string      `json:"phase"`
	StartedAt metav1.Time `json:"startedAt"`
}

// +kubebuilder:object:root=true
type VirtualMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMachine `json:"items"`
}
