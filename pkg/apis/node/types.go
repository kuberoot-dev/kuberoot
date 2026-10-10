package node

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// OSConfig is the operating system of one node: its desired settings and what
// the node reports about itself. There is exactly one, named after the node.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type OSConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OSConfigSpec   `json:"spec,omitempty"`
	Status OSConfigStatus `json:"status,omitempty"`
}

type OSConfigSpec struct {
	// Nameservers replace the DNS servers handed out by DHCP.
	// +listType=atomic
	Nameservers []string `json:"nameservers,omitempty"`
	// Sysctls are kernel parameters applied on top of the distro defaults.
	Sysctls map[string]string `json:"sysctls,omitempty"`
	// RebootRequestedAt reboots the node when set to a time after its last boot.
	RebootRequestedAt *metav1.Time `json:"rebootRequestedAt,omitempty"`
}

type OSConfigStatus struct {
	Distro        string `json:"distro,omitempty"`
	Version       string `json:"version,omitempty"`
	KernelVersion string `json:"kernelVersion,omitempty"`
	Architecture  string `json:"architecture,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
	// +listType=atomic
	Addresses   []string    `json:"addresses,omitempty"`
	BootTime    metav1.Time `json:"bootTime,omitempty"`
	MemoryTotal int64       `json:"memoryTotal,omitempty"`
	MemoryFree  int64       `json:"memoryFree,omitempty"`
	// GPUs the node's NVIDIA driver drives.
	// +listType=atomic
	GPUs []NodeGPU `json:"gpus,omitempty"`
}

// NodeGPU is a GPU of the node.
type NodeGPU struct {
	// Index is the GPU's device minor: /dev/nvidia<index>, and its name as a
	// CDI device, nvidia.com/gpu=<index>.
	Index int32  `json:"index"`
	Model string `json:"model,omitempty"`
	UUID  string `json:"uuid,omitempty"`
	BusID string `json:"busID,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type OSConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []OSConfig `json:"items"`
}

// NodeService is one of the processes the node init supervises.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type NodeService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeServiceSpec   `json:"spec,omitempty"`
	Status NodeServiceStatus `json:"status,omitempty"`
}

type NodeServiceSpec struct {
	// RestartedAt restarts the service whenever it changes, like kubectl rollout restart.
	RestartedAt *metav1.Time `json:"restartedAt,omitempty"`
}

type NodeServiceStatus struct {
	State     string      `json:"state,omitempty"`
	PID       int32       `json:"pid,omitempty"`
	Restarts  int32       `json:"restarts"`
	StartedAt metav1.Time `json:"startedAt,omitempty"`
	LastExit  string      `json:"lastExit,omitempty"`
	// MemoryBytes and CPUUsageSeconds come from the service cgroup.
	MemoryBytes     int64 `json:"memoryBytes,omitempty"`
	CPUUsageSeconds int64 `json:"cpuUsageSeconds,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type NodeServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []NodeService `json:"items"`
}

// Kubeconfig is a credential for the Kubernetes cluster this node serves or belongs to.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type Kubeconfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Server is the API server URL written into the kubeconfig.
	Server string `json:"server,omitempty"`
	// Kubeconfig is the complete file, ready to save.
	Kubeconfig string `json:"kubeconfig,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type KubeconfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Kubeconfig `json:"items"`
}

// Disk is a block device of the node.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type Disk struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Status DiskStatus `json:"status,omitempty"`
}

type DiskStatus struct {
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	Model     string `json:"model,omitempty"`
	Removable bool   `json:"removable,omitempty"`
	// Role is what kuberoot uses the disk for: BootMedia, System or empty when free.
	Role string `json:"role,omitempty"`
	// +listType=atomic
	Partitions []DiskPartition `json:"partitions,omitempty"`
}

type DiskPartition struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type DiskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Disk `json:"items"`
}

// Installation writes kuberoot from the boot media onto a disk of this node.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type Installation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstallationSpec   `json:"spec,omitempty"`
	Status InstallationStatus `json:"status,omitempty"`
}

type InstallationSpec struct {
	// Disk is the name of the target disk; everything on it is erased.
	Disk string `json:"disk"`
	// Reboot into the installed system when done.
	Reboot bool `json:"reboot,omitempty"`
	// Restore installs the state of a control plane from one of its state
	// backups instead of this node's own: the node comes back as that
	// control plane, with its identity, certificates and cluster store.
	Restore *InstallationRestore `json:"restore,omitempty"`
}

type InstallationRestore struct {
	// URL of the state archive, such as a presigned link to object storage.
	URL string `json:"url"`
	// SHA256 of the archive, as its StateBackup reports it. Required: the
	// archive holds the cluster's certificate authorities.
	Sha256 string `json:"sha256"`
	// Identity is the age secret key (AGE-SECRET-KEY-1...) an encrypted
	// archive opens with. It is used once and never shown back.
	Identity string `json:"identity,omitempty"`
}

type InstallationStatus struct {
	Phase       string       `json:"phase,omitempty"`
	Message     string       `json:"message,omitempty"`
	Progress    int32        `json:"progress"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type InstallationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Installation `json:"items"`
}

// BootEntry is one of the two root slots as the bootloader sees it.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type BootEntry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BootEntrySpec   `json:"spec,omitempty"`
	Status BootEntryStatus `json:"status,omitempty"`
}

type BootEntrySpec struct {
	// Preferred makes this slot the default for the next boot: a manual rollback.
	Preferred bool `json:"preferred,omitempty"`
}

type BootEntryStatus struct {
	Release string `json:"release,omitempty"`
	// State is Good, Trying (on probation, with boot attempts left) or Bad.
	State       string `json:"state,omitempty"`
	TriesLeft   int32  `json:"triesLeft,omitempty"`
	Booted      bool   `json:"booted,omitempty"`
	Default     bool   `json:"default,omitempty"`
	SortVersion int32  `json:"sortVersion,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type BootEntryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []BootEntry `json:"items"`
}

// Upgrade stages a new release into the inactive slot and boots it on probation.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type Upgrade struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UpgradeSpec   `json:"spec,omitempty"`
	Status UpgradeStatus `json:"status,omitempty"`
}

type UpgradeSpec struct {
	// URL of the release bundle.
	URL string `json:"url"`
	// SHA256 of the bundle; checked when set.
	Sha256 string `json:"sha256,omitempty"`
	// Reboot into the new slot when staged.
	Reboot bool `json:"reboot,omitempty"`
}

type UpgradeStatus struct {
	Phase    string `json:"phase,omitempty"`
	Message  string `json:"message,omitempty"`
	Progress int32  `json:"progress"`
	Slot     string `json:"slot,omitempty"`
	Release  string `json:"release,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type UpgradeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Upgrade `json:"items"`
}

// Membership makes this node a member of a cluster run by another node. There is
// at most one, named "cluster"; deleting it turns the node back into a
// standalone single-node cluster.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type Membership struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MembershipSpec   `json:"spec,omitempty"`
	Status MembershipStatus `json:"status,omitempty"`
}

type MembershipSpec struct {
	// Role of this node in the cluster: Worker, or ControlPlane, which runs
	// a member of the cluster store and an API server of its own.
	Role string `json:"role"`
	// Server is the cluster API server URL.
	Server string `json:"server"`
	// Servers are the API server URLs of every control plane; a worker given
	// them reaches whichever answers, through a balancer on the node.
	// +listType=atomic
	Servers []string `json:"servers,omitempty"`
	// ControlPlane is what a joining control plane shares with the others:
	// set for the ControlPlane role only.
	ControlPlane *ControlPlaneShare `json:"controlPlane,omitempty"`
	// ClusterCA is the PEM bundle of the cluster CA.
	ClusterCA string `json:"clusterCA"`
	// BootstrapToken lets the kubelet request its client certificate.
	BootstrapToken string `json:"bootstrapToken"`
	// FrontProxyCA is the PEM bundle the cluster API server forwards requests with.
	FrontProxyCA string `json:"frontProxyCA,omitempty"`
	// NodeAPICert and NodeAPIKey are the serving certificate, signed by the
	// cluster CA, the node API presents to the cluster.
	NodeAPICert string `json:"nodeAPICert"`
	NodeAPIKey  string `json:"nodeAPIKey"`
	// PodCIDR and ServiceCIDR are the cluster's address ranges.
	PodCIDR string `json:"podCIDR,omitempty"`
	// PodNetwork is how the cluster carries pod traffic between nodes
	// (vxlan, host-gw, or none when a CNI package does); every node of a
	// cluster must use the same.
	PodNetwork  string `json:"podNetwork,omitempty"`
	ServiceCIDR string `json:"serviceCIDR,omitempty"`
}

// ControlPlaneShare is the keys every control plane of a cluster holds the
// same, and the cluster store the new one joins. The node keeps the keys
// with its own and drops them from the membership.
type ControlPlaneShare struct {
	ClusterCAKey      string `json:"clusterCAKey"`
	FrontProxyCAKey   string `json:"frontProxyCAKey"`
	ServiceAccountKey string `json:"serviceAccountKey"`
	ServiceAccountPub string `json:"serviceAccountPub"`
	// EncryptionKey encrypts Secrets at rest; every API server needs the same.
	EncryptionKey string `json:"encryptionKey"`
	// EtcdCA and EtcdCAKey: the store's own authority, apart from the
	// cluster's, which also signs kubelets' certificates.
	EtcdCA    string `json:"etcdCA"`
	EtcdCAKey string `json:"etcdCAKey"`
	// EtcdInitialCluster names the store's members, this node among them,
	// as etcd's --initial-cluster does.
	EtcdInitialCluster string `json:"etcdInitialCluster"`
}

type MembershipStatus struct {
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MembershipList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Membership `json:"items"`
}

// JoinTicket is issued by the control plane node: everything another node needs
// to join, ready to apply to that node as a Membership.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type JoinTicket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   JoinTicketSpec   `json:"spec,omitempty"`
	Status JoinTicketStatus `json:"status,omitempty"`
}

type JoinTicketSpec struct {
	// NodeName is the node that will use the ticket; its node API certificate
	// is issued for this name only.
	NodeName string `json:"nodeName"`
	// Role the node joins as: Worker, the default, or ControlPlane.
	Role string `json:"role,omitempty"`
	// Address of a joining control plane, where the other members of the
	// cluster store reach it.
	Address string `json:"address,omitempty"`
	// TTL of the ticket and its bootstrap token: one hour by default, one day at most.
	TTL *metav1.Duration `json:"ttl,omitempty"`
}

type JoinTicketStatus struct {
	Expires *metav1.Time `json:"expires,omitempty"`
	// Membership is a complete Membership manifest for the joining node.
	Membership string `json:"membership,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type JoinTicketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []JoinTicket `json:"items"`
}

// StateBackup is an archive of a control plane's state: its identity and
// certificates, the kubelet's credentials and a consistent snapshot of the
// cluster store. The node takes one on a schedule and on request (create
// one), keeps the latest locally and uploads them to object storage when
// kube-system/kuberoot-state-backup says where. Installation.spec.restore
// brings a control plane back from one.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type StateBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Status StateBackupStatus `json:"status,omitempty"`
}

type StateBackupStatus struct {
	CreatedAt metav1.Time `json:"createdAt"`
	SizeBytes int64       `json:"sizeBytes"`
	Sha256    string      `json:"sha256"`
	// Encrypted to the age recipients of kube-system/kuberoot-state-backup;
	// restoring it needs one of their identities.
	Encrypted bool `json:"encrypted,omitempty"`
	// Location in object storage once uploaded.
	Location string `json:"location,omitempty"`
	// Message says why the last upload failed.
	Message string `json:"message,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type StateBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []StateBackup `json:"items"`
}

// Volume is a disk for virtual machines on this node: a file, replicated
// with DRBD to the same volume on other nodes when peers are given. The
// node it runs on is its primary.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type Volume struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VolumeSpec   `json:"spec,omitempty"`
	Status VolumeStatus `json:"status,omitempty"`
}

type VolumeSpec struct {
	SizeBytes int64 `json:"sizeBytes"`
	// Minor of the DRBD device, /dev/drbd<minor>, and its port; the same on
	// every node of the volume.
	Minor int32 `json:"minor"`
	Port  int32 `json:"port"`
	// NodeID of this node in the volume, unique among its peers.
	NodeID int32 `json:"nodeID"`
	// Peers are the volume on the other nodes.
	Peers []VolumePeer `json:"peers,omitempty"`
	// Primary: this node writes to the volume (runs the machine using it).
	Primary bool `json:"primary,omitempty"`
	// AllowTwoPrimaries lets a second node write while a machine moves
	// between them alive; only one of them runs it at any moment.
	AllowTwoPrimaries bool `json:"allowTwoPrimaries,omitempty"`
	// Image is written onto the volume when it is created, here: a URL of a
	// qcow2 or raw disk image. Only one node of a volume gets an image.
	Image string `json:"image,omitempty"`
}

type VolumePeer struct {
	Node    string `json:"node"`
	Address string `json:"address"`
	NodeID  int32  `json:"nodeID"`
}

type VolumeStatus struct {
	// Phase: Creating, Ready or Failed.
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	// Device to give a machine, such as /dev/drbd100.
	Device string `json:"device,omitempty"`
	// Role (Primary, Secondary) and DiskState (UpToDate, Inconsistent...) as DRBD reports them.
	Role      string `json:"role,omitempty"`
	DiskState string `json:"diskState,omitempty"`
	// Quorum: this node may write.
	Quorum bool `json:"quorum,omitempty"`
	// PeerStates: the connection to each peer; PeerDisks: their disks.
	PeerStates map[string]string `json:"peerStates,omitempty"`
	PeerDisks  map[string]string `json:"peerDisks,omitempty"`
	// ImageWritten: the volume's data exists, written here or synced from
	// a peer; an image is never written over it.
	ImageWritten bool `json:"imageWritten,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type VolumeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Volume `json:"items"`
}

// Machine is a virtual machine on this node, run by cloud-hypervisor on KVM
// with no pod and no libvirt.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type Machine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MachineSpec   `json:"spec,omitempty"`
	Status MachineStatus `json:"status,omitempty"`
}

type MachineSpec struct {
	CPUs      int32 `json:"cpus"`
	MemoryMiB int64 `json:"memoryMiB"`
	// Volumes on this node, by name, as the machine's disks in order.
	Volumes []string `json:"volumes"`
	// MAC of the machine's network interface on the machines' network.
	MAC string `json:"mac,omitempty"`
	// Running: the machine is started when true and shut down when false.
	Running bool `json:"running"`
	// Receive starts the machine empty, waiting for a running machine to
	// arrive at this URL (tcp:<address>:<port>) and go on from there.
	Receive string `json:"receive,omitempty"`
	// SendTo sends the running machine, alive, to a node receiving it at
	// this URL; the machine then runs there, and stops here.
	SendTo string `json:"sendTo,omitempty"`
	// SystemSerial is the machine's DMI system serial number: cloud-init in
	// the guest reads its data source from it (ds=nocloud;s=<url>).
	SystemSerial string `json:"systemSerial,omitempty"`
}

type MachineStatus struct {
	// Phase: Starting, Running, Receiving, Sending, Sent, Stopped or Failed.
	Phase     string       `json:"phase,omitempty"`
	Message   string       `json:"message,omitempty"`
	PID       int32        `json:"pid,omitempty"`
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Machine `json:"items"`
}

// ModelServer is a language model this node serves, run by llama-server as
// a process of the node: no pod, no container image.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type ModelServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelServerSpec   `json:"spec,omitempty"`
	Status ModelServerStatus `json:"status,omitempty"`
}

type ModelServerSpec struct {
	// URL of the model's weights, a GGUF file, and their SHA-256: the
	// weights are used only when they match it.
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// Model is the name clients ask for.
	Model string `json:"model"`
	// Port the server listens on, on every address of the node.
	Port int32 `json:"port"`
	// ContextSize in tokens, shared by the Parallel requests served at once.
	ContextSize int32 `json:"contextSize,omitempty"`
	Parallel    int32 `json:"parallel,omitempty"`
	// Threads for generation; all the node's CPUs when 0.
	Threads int32 `json:"threads,omitempty"`
	// GPUs: NVIDIA GPUs of the node the server runs the model on, all of its
	// layers; none runs it on the CPUs. A server on GPUs needs an engine
	// built for CUDA.
	GPUs int32 `json:"gpus,omitempty"`
	// EngineURL and EngineSHA256 name the program that serves the model in
	// place of the llama-server built into the image, fetched like the
	// weights and used only when it matches its hash.
	EngineURL    string `json:"engineURL,omitempty"`
	EngineSHA256 string `json:"engineSHA256,omitempty"`
}

type ModelServerStatus struct {
	// Phase: Downloading, Loading, Ready or Failed.
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	PID     int32  `json:"pid,omitempty"`
	// SHA256 of the weights the running server has loaded.
	SHA256 string `json:"sha256,omitempty"`
	// EngineSHA256 of the program the running server is, when not the
	// built-in one.
	EngineSHA256 string `json:"engineSHA256,omitempty"`
	// GPUIndexes are the GPUs the server was given.
	// +listType=atomic
	GPUIndexes []int32 `json:"gpuIndexes,omitempty"`
	// Downloaded bytes of the weights, while they download.
	Downloaded int64        `json:"downloaded,omitempty"`
	StartedAt  *metav1.Time `json:"startedAt,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ModelServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ModelServer `json:"items"`
}

// LatencyTest measures how late the node wakes a real-time task: cyclictest
// runs a thread at a real-time priority on each CPU asked for, wakes it at a
// fixed interval, and records how long after the due time it ran.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type LatencyTest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LatencyTestSpec   `json:"spec,omitempty"`
	Status LatencyTestStatus `json:"status,omitempty"`
}

type LatencyTestSpec struct {
	// DurationSeconds the test runs; 60 when 0.
	DurationSeconds int32 `json:"durationSeconds,omitempty"`
	// CPUs to measure, as a list like 2-3; the CPUs kept for real-time work
	// (those without a timer tick) when empty, or all CPUs if none is.
	CPUs string `json:"cpus,omitempty"`
	// Priority of the measuring threads, SCHED_FIFO; 95 when 0.
	Priority int32 `json:"priority,omitempty"`
	// IntervalMicroseconds between wake-ups; 1000 when 0.
	IntervalMicroseconds int32 `json:"intervalMicroseconds,omitempty"`
}

type LatencyTestStatus struct {
	// Phase: Pending, Running, Succeeded or Failed.
	Phase      string       `json:"phase,omitempty"`
	Message    string       `json:"message,omitempty"`
	StartedAt  *metav1.Time `json:"startedAt,omitempty"`
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Kernel the test ran on, and whether it is fully preemptible.
	Kernel string `json:"kernel,omitempty"`
	// Results per CPU, then the worst of them: latencies in microseconds.
	CPUs              []CPULatency `json:"cpus,omitempty"`
	MaxMicroseconds   int64        `json:"maxMicroseconds,omitempty"`
	P99Microseconds   int64        `json:"p99Microseconds,omitempty"`
	P9999Microseconds int64        `json:"p9999Microseconds,omitempty"`
}

// CPULatency is what one CPU's measuring thread saw.
type CPULatency struct {
	CPU               int32 `json:"cpu"`
	Samples           int64 `json:"samples"`
	MinMicroseconds   int64 `json:"minMicroseconds"`
	AvgMicroseconds   int64 `json:"avgMicroseconds"`
	MaxMicroseconds   int64 `json:"maxMicroseconds"`
	P99Microseconds   int64 `json:"p99Microseconds"`
	P9999Microseconds int64 `json:"p9999Microseconds"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LatencyTestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []LatencyTest `json:"items"`
}

// EtcdMember is a member of the cluster store, as a control plane node sees
// it; deleting one removes the member, as replacing a control plane needs.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
type EtcdMember struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Status EtcdMemberStatus `json:"status,omitempty"`
}

type EtcdMemberStatus struct {
	// ID of the member, in hexadecimal as etcdctl shows it.
	ID string `json:"id"`
	// PeerURLs, where the other members reach it, and ClientURLs, where API
	// servers do.
	// +listType=atomic
	PeerURLs []string `json:"peerURLs,omitempty"`
	// +listType=atomic
	ClientURLs []string `json:"clientURLs,omitempty"`
	// Learner: it receives the store but has no vote until it has caught up.
	Learner bool `json:"learner,omitempty"`
	// Leader: it leads the store now.
	Leader bool `json:"leader,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type EtcdMemberList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []EtcdMember `json:"items"`
}
