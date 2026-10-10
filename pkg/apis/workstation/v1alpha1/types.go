// Package v1alpha1 is the workstation.kuberoot.dev API: a person's desktop
// as a virtual machine of the cluster, kept running between connections and
// opened from a browser or an RDP client.
// +kubebuilder:object:generate=true
// +groupName=workstation.kuberoot.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion of the workstation API.
var GroupVersion = schema.GroupVersion{Group: "workstation.kuberoot.dev", Version: "v1alpha1"}

var (
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &Workspace{}, &WorkspaceList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
	AddToScheme = SchemeBuilder.AddToScheme
)

// Workspace is a person's desktop: a virtual machine with a desktop
// environment, whose disk the cluster replicates and which moves between
// nodes like any machine. The person opens it from a browser at status.url,
// or from an RDP client at status.rdp; the credentials are in the Secret
// status.credentials names.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ws
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Owner,type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name=Node,type=string,JSONPath=`.status.node`
// +kubebuilder:printcolumn:name=URL,type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name=RDP,type=string,JSONPath=`.status.rdp`
type Workspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceSpec   `json:"spec"`
	Status WorkspaceStatus `json:"status,omitempty"`
}

type WorkspaceSpec struct {
	// Owner is the person's user name in the desktop.
	// +kubebuilder:validation:Pattern=`^[a-z_][a-z0-9_-]{0,31}$`
	Owner string `json:"owner"`
	// Image is the desktop's disk image when it is first created: a cloud
	// image (qcow2 or raw, UEFI) of a distribution that runs cloud-init and
	// has packages for a desktop; Ubuntu 24.04 when unset.
	// +optional
	Image string `json:"image,omitempty"`
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	CPUs int32 `json:"cpus,omitempty"`
	// +kubebuilder:default="2Gi"
	Memory resource.Quantity `json:"memory,omitempty"`
	// Disk of the desktop, the person's files on it, kept on as many nodes
	// as Replicas.
	// +kubebuilder:default="6Gi"
	Disk resource.Quantity `json:"disk,omitempty"`
	// +kubebuilder:default=3
	// +kubebuilder:validation:Enum=1;3
	Replicas int32 `json:"replicas,omitempty"`
	// Running: false shuts the desktop down and keeps its disk.
	// +kubebuilder:default=true
	Running *bool `json:"running,omitempty"`
	// Node to run on; changed while it runs, the desktop moves there alive.
	// +optional
	Node string `json:"node,omitempty"`
}

type WorkspaceStatus struct {
	// Phase: Pending, Provisioning (the desktop is being installed in the
	// machine), Ready, Stopped or Failed.
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	// Machine is the VirtualMachine the desktop runs in.
	Machine string `json:"machine,omitempty"`
	Node    string `json:"node,omitempty"`
	Address string `json:"address,omitempty"`
	// URL opens the desktop in a browser; it carries the workspace's token.
	URL string `json:"url,omitempty"`
	// RDP is where an RDP client reaches the desktop.
	RDP string `json:"rdp,omitempty"`
	// RDPPort of the gateway, the workspace's own.
	RDPPort int32 `json:"rdpPort,omitempty"`
	// Credentials names the Secret with the workspace's token and the
	// owner's password.
	Credentials string `json:"credentials,omitempty"`
}

// +kubebuilder:object:root=true
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workspace `json:"items"`
}
