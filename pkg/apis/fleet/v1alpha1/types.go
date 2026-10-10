// Package v1alpha1 is the fleet.kuberoot.dev API: many independent kuberoot
// nodes, each its own cluster, managed from one central cluster. Every node
// calls the center and keeps the connection open; the center reaches the
// node API through it, so nodes need no inbound ports. A FleetNode is a node
// of the fleet, a FleetRollout moves a release across nodes in waves.
// +kubebuilder:object:generate=true
// +groupName=fleet.kuberoot.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// GroupVersion of the fleet API.
var GroupVersion = schema.GroupVersion{Group: "fleet.kuberoot.dev", Version: "v1alpha1"}

var (
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &FleetNode{}, &FleetNodeList{}, &FleetRollout{}, &FleetRolloutList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
	AddToScheme = SchemeBuilder.AddToScheme
)

// Release is a signed kuberoot release bundle, as an Upgrade installs it.
type Release struct {
	// Version the bundle carries, as the node's boot entries name it.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
	// URL of the bundle; its signature is at URL + ".sig".
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
	// Sha256 of the bundle.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	Sha256 string `json:"sha256"`
}

// FleetNode is one node of the fleet: a single kuberoot node, usually its own
// cluster, somewhere behind NAT. Creating one issues its enrollment token;
// labels group nodes by site, hardware or anything else.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=fn
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name=Release,type=string,JSONPath=`.status.release`
// +kubebuilder:printcolumn:name=Slot,type=string,JSONPath=`.status.slot`
// +kubebuilder:printcolumn:name=Health,type=string,JSONPath=`.status.health`
// +kubebuilder:printcolumn:name=Update,type=string,JSONPath=`.status.update.phase`
// +kubebuilder:printcolumn:name=Last Seen,type=date,JSONPath=`.status.lastSeen`
type FleetNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FleetNodeSpec   `json:"spec,omitempty"`
	Status FleetNodeStatus `json:"status,omitempty"`
}

type FleetNodeSpec struct {
	// Release the node should run. The center upgrades the node to it
	// whenever the node is connected, and not again once the node rolled it
	// back. A FleetRollout sets it; it can be set by hand.
	Release *Release `json:"release,omitempty"`
}

// Phases of a FleetNode.
const (
	PhasePending   = "Pending"   // waiting for the node to enroll
	PhaseConnected = "Connected" // the node's connection is open
	PhaseOffline   = "Offline"   // enrolled, not connected now
)

type FleetNodeStatus struct {
	// Phase: Pending until the node enrolls, then Connected or Offline.
	Phase string `json:"phase,omitempty"`
	// Enrollment names the Secret holding the node's one-time enrollment
	// while the node has not used it.
	Enrollment string `json:"enrollment,omitempty"`
	// Identity is the node's own certificate authority, pinned when it
	// enrolled: sha256 of the CA certificate. Only a client certificate this
	// CA issued, for this node's name, opens the node's connection.
	Identity string `json:"identity,omitempty"`
	// Address the node's connection comes from, as the center sees it.
	Address string `json:"address,omitempty"`
	// ConnectedSince is when the open connection was made.
	ConnectedSince *metav1.Time `json:"connectedSince,omitempty"`
	// LastSeen is the last time the node answered.
	LastSeen *metav1.Time `json:"lastSeen,omitempty"`

	// Distro, Release, Arch, Kernel describe what the node runs.
	Distro  string `json:"distro,omitempty"`
	Release string `json:"release,omitempty"`
	Arch    string `json:"arch,omitempty"`
	Kernel  string `json:"kernel,omitempty"`
	// MemoryBytes of the node.
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
	// Slot is the root slot the node booted, SlotState its boot state:
	// Good, or Trying while on probation.
	Slot      string `json:"slot,omitempty"`
	SlotState string `json:"slotState,omitempty"`
	// Health: Healthy when every service of the node runs, Degraded
	// otherwise, with the services that do not in Message.
	Health string `json:"health,omitempty"`
	// Services the node runs, and how many of them run.
	Services string `json:"services,omitempty"`
	Message  string `json:"message,omitempty"`

	// Update is how the node gets to spec.release.
	Update *UpdateStatus `json:"update,omitempty"`
}

// Update phases.
const (
	UpdateDownloading = "Downloading" // the node fetches and writes the release
	UpdateRebooting   = "Rebooting"   // staged; the node boots it next
	UpdateVerifying   = "Verifying"   // booted on probation
	UpdateUpdated     = "Updated"     // booted and Good
	UpdateRolledBack  = "RolledBack"  // the node went back to its previous slot
	UpdateFailed      = "Failed"      // the release could not be staged
	UpdateWaiting     = "Waiting"     // the node cannot take it yet
)

// UpdateStatus is a node's way to spec.release.
type UpdateStatus struct {
	// Version being installed.
	Version string `json:"version,omitempty"`
	// Phase: Downloading, Rebooting, Verifying, Updated, or the failures
	// RolledBack and Failed, which are final for this release.
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	// Since is when the phase began.
	Since *metav1.Time `json:"since,omitempty"`
}

// +kubebuilder:object:root=true
type FleetNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FleetNode `json:"items"`
}

// FleetRollout moves a release across the nodes its selector picks, in
// waves: each wave upgrades more nodes, and only once every node of the
// waves before is updated. A node that rolls the release back, or fails to
// stage it, is a failure; more than maxFailures stop the rollout.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=fro
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Version,type=string,JSONPath=`.spec.release.version`
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name=Wave,type=string,JSONPath=`.status.wave`
// +kubebuilder:printcolumn:name=Updated,type=string,JSONPath=`.status.updated`
// +kubebuilder:printcolumn:name=Failed,type=integer,JSONPath=`.status.failed`
// +kubebuilder:printcolumn:name=Message,type=string,JSONPath=`.status.message`
type FleetRollout struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FleetRolloutSpec   `json:"spec"`
	Status FleetRolloutStatus `json:"status,omitempty"`
}

// Offline policies.
const (
	OfflineWait = "Wait"
	OfflineSkip = "Skip"
)

type FleetRolloutSpec struct {
	Release Release `json:"release"`
	// Selector picks the nodes; all nodes when empty.
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
	// Waves are how many of the nodes have the release once each wave is
	// through: a number of nodes or a percentage, rounded up, growing wave
	// by wave. The last wave takes every node that is left.
	// +kubebuilder:default={"1","10%","100%"}
	// +kubebuilder:validation:MinItems=1
	Waves []intstr.IntOrString `json:"waves,omitempty"`
	// MaxFailures the rollout carries on through; one more stops it.
	// +kubebuilder:validation:Minimum=0
	MaxFailures int32 `json:"maxFailures,omitempty"`
	// Paused holds the next wave back; nodes already given the release go on.
	Paused bool `json:"paused,omitempty"`
	// Offline is what a wave does about a node that is not connected: Wait
	// for it, or Skip it, which lets the wave go on while the node takes the
	// release when it comes back.
	// +kubebuilder:validation:Enum=Wait;Skip
	// +kubebuilder:default=Wait
	Offline string `json:"offline,omitempty"`
	// Soak is how long a wave's nodes run the release before the next wave.
	Soak *metav1.Duration `json:"soak,omitempty"`
}

// Rollout phases.
const (
	RolloutProgressing = "Progressing"
	RolloutPaused      = "Paused"
	RolloutStopped     = "Stopped"
	RolloutComplete    = "Complete"
)

type FleetRolloutStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase: Progressing, Paused, Stopped by failures, or Complete.
	Phase string `json:"phase,omitempty"`
	// Release is the version the nodes below were picked for; a new
	// version in spec starts the rollout over.
	Release string `json:"release,omitempty"`
	// Wave in progress, as "<n>/<waves>".
	Wave string `json:"wave,omitempty"`
	// Nodes the rollout covers, fixed when it starts, in the order waves
	// take them, with the wave of each.
	Nodes []RolloutNode `json:"nodes,omitempty"`
	// Updated is "<updated>/<nodes>"; Failed counts failures.
	Updated string `json:"updated,omitempty"`
	Failed  int32  `json:"failed,omitempty"`
	// WaveDone is when the current wave's last node was updated.
	WaveDone *metav1.Time `json:"waveDone,omitempty"`
	Message  string       `json:"message,omitempty"`
}

// RolloutNode is a node of a rollout and where it is.
type RolloutNode struct {
	Name string `json:"name"`
	Wave int32  `json:"wave"`
	// State: Pending until its wave, then the node's update phase, Offline
	// or Skipped.
	State string `json:"state,omitempty"`
}

// +kubebuilder:object:root=true
type FleetRolloutList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FleetRollout `json:"items"`
}
