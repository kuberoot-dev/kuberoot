// Package v1alpha1 is the devices.kuberoot.dev API: the devices a node
// talks to, and where what they say goes, as Kubernetes resources. A device
// is anything the node reads over the network: a PLC over Modbus, a server's
// BMC over Redfish, a switch or a PDU over SNMP, or a service it probes.
// kuberoot-devices polls them, evaluates the routes and publishes, exports
// every value as a metric and sends heartbeats, with no pod and no
// container runtime.
// +kubebuilder:object:generate=true
// +groupName=devices.kuberoot.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion of the devices API.
var GroupVersion = schema.GroupVersion{Group: "devices.kuberoot.dev", Version: "v1alpha1"}

var (
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &Device{}, &DeviceList{}, &Route{}, &RouteList{}, &Safeguard{}, &SafeguardList{},
			&Heartbeat{}, &HeartbeatList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
	AddToScheme = SchemeBuilder.AddToScheme
)

// Status is what every devices resource reports: whether the node runs it.
type Status struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions: Applied says whether the node runs this configuration.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Device is a device the node reads: a PLC, a meter, a sensor, a server's
// BMC, a switch, a PDU, or a service it probes.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=dev
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Protocol,type=string,JSONPath=`.spec.protocol`
// +kubebuilder:printcolumn:name=Address,type=string,JSONPath=`.spec.address`
// +kubebuilder:printcolumn:name=Connected,type=boolean,JSONPath=`.status.connected`
// +kubebuilder:printcolumn:name=Values,type=string,JSONPath=`.status.summary`
// +kubebuilder:printcolumn:name=Error,type=string,JSONPath=`.status.error`
type Device struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceSpec   `json:"spec"`
	Status DeviceStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.protocol != 'modbus-tcp' || (has(self.points) && size(self.points) > 0)",message="a Modbus device needs points"
// +kubebuilder:validation:XValidation:rule="self.protocol != 'snmp' || (has(self.points) && size(self.points) > 0 && self.points.all(p, has(p.oid)))",message="an SNMP device needs points with an oid"
type DeviceSpec struct {
	// Protocol the device speaks: modbus-tcp; snmp; redfish, a server's
	// BMC; or a probe of a service: http, tcp, icmp.
	// +kubebuilder:validation:Enum=modbus-tcp;snmp;redfish;http;tcp;icmp
	Protocol string `json:"protocol"`
	// Address of the device: host:port for modbus-tcp, snmp (port 161 when
	// left out) and tcp; the BMC's https://host for redfish; the URL for
	// http; the host for icmp.
	Address string `json:"address"`
	// UnitID addresses a Modbus device behind a gateway; 1 when unset.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=255
	UnitID int32 `json:"unitID,omitempty"`
	// Every is how often the device is read.
	// +kubebuilder:default="1s"
	Every metav1.Duration `json:"every,omitempty"`
	// Timeout of one request.
	// +kubebuilder:default="2s"
	Timeout metav1.Duration `json:"timeout,omitempty"`
	// Credentials names a Secret with what the device asks for: username
	// and password for redfish; community for snmp version 2c; username,
	// and authProtocol (MD5, SHA, SHA256, SHA512), authPassword, privProtocol
	// (DES, AES, AES256) and privPassword as it needs them, for version 3.
	// +optional
	Credentials *SecretRef `json:"credentials,omitempty"`
	// SNMP settings.
	// +optional
	SNMP *SNMP `json:"snmp,omitempty"`
	// TLS for redfish and https probes.
	// +optional
	TLS *TLS `json:"tls,omitempty"`
	// Points are the values read from the device. A BMC and a probe report
	// theirs by themselves; points there only pick which to keep.
	// +optional
	// +listType=map
	// +listMapKey=name
	Points []Point `json:"points,omitempty"`
}

// SecretRef names a Secret; in kube-system when no namespace is given.
type SecretRef struct {
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

type SNMP struct {
	// +kubebuilder:default="2c"
	// +kubebuilder:validation:Enum="2c";"3"
	Version string `json:"version,omitempty"`
}

type TLS struct {
	// InsecureSkipVerify accepts any certificate: BMCs mostly have their
	// own, self-signed.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// Point is a value of the device: where it is and how to read it.
type Point struct {
	// Name of the value, as routes refer to it: a CEL identifier.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_][a-zA-Z0-9_]*$`
	Name string `json:"name"`
	// Kind of Modbus table: holding or input registers, coils or discrete
	// inputs. Modbus only.
	// +kubebuilder:default=holding
	// +kubebuilder:validation:Enum=holding;input;coil;discrete
	Kind string `json:"kind,omitempty"`
	// Register is the address in the Modbus table, counted from 0.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Register int32 `json:"register,omitempty"`
	// OID of an SNMP value, as 1.3.6.1.2.1.1.3.0.
	// +optional
	// +kubebuilder:validation:Pattern=`^\.?[0-9]+(\.[0-9]+)*$`
	OID string `json:"oid,omitempty"`
	// Type of a Modbus value: 16-bit registers as they are, 32-bit ones from
	// two registers, the high word first; bool for coils and discrete
	// inputs. SNMP values carry their own types.
	// +kubebuilder:default=uint16
	// +kubebuilder:validation:Enum=int16;uint16;int32;uint32;float32;bool
	Type string `json:"type,omitempty"`
	// Scale multiplies the value read, as a decimal: "0.1" for tenths.
	// +optional
	// +kubebuilder:validation:Pattern=`^-?[0-9]+(\.[0-9]+)?$`
	Scale string `json:"scale,omitempty"`
	// Unit of the value, for people.
	// +optional
	Unit string `json:"unit,omitempty"`
}

type DeviceStatus struct {
	Status `json:",inline"`
	// Connected: the last read of the device succeeded.
	Connected bool         `json:"connected,omitempty"`
	LastRead  *metav1.Time `json:"lastRead,omitempty"`
	Error     string       `json:"error,omitempty"`
	// Values as last read.
	// +listType=map
	// +listMapKey=name
	Values []Value `json:"values,omitempty"`
	// Summary of the values, for kubectl get.
	Summary string `json:"summary,omitempty"`
}

// Value is a point as last read.
type Value struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
}

// Route sends what a device says somewhere: every reading, or only when a
// condition becomes true.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Device,type=string,JSONPath=`.spec.device`
// +kubebuilder:printcolumn:name=When,type=string,JSONPath=`.spec.when`
// +kubebuilder:printcolumn:name=Sent,type=integer,JSONPath=`.status.sent`
// +kubebuilder:printcolumn:name=Error,type=string,JSONPath=`.status.error`
type Route struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RouteSpec   `json:"spec"`
	Status RouteStatus `json:"status,omitempty"`
}

type RouteSpec struct {
	// Device whose readings the route sends.
	Device string `json:"device"`
	// Points to send; all of the device's when empty.
	// +optional
	Points []string `json:"points,omitempty"`
	// When is a CEL expression over the device's points, by name: the route
	// sends once each time it becomes true. Without it, every reading goes.
	// +optional
	When string `json:"when,omitempty"`
	// To is where the readings go.
	To Destination `json:"to"`
}

// Destination: an MQTT topic.
type Destination struct {
	MQTT *MQTT `json:"mqtt,omitempty"`
}

type MQTT struct {
	// Broker, as tcp://host:1883.
	Broker string `json:"broker"`
	Topic  string `json:"topic"`
	// +kubebuilder:default=1
	// +kubebuilder:validation:Enum=0;1;2
	QoS int32 `json:"qos,omitempty"`
	// +optional
	Retain bool `json:"retain,omitempty"`
}

type RouteStatus struct {
	Status `json:",inline"`
	// Sent counts the messages sent since the controller started, or since
	// the route's device or condition last changed.
	Sent     int64        `json:"sent,omitempty"`
	LastSent *metav1.Time `json:"lastSent,omitempty"`
	// Connected: the destination takes messages.
	Connected bool   `json:"connected,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Safeguard runs changes to the devices and routes on trial. A change that
// leaves a device that was answering silent, or a destination that was
// taking messages unreachable, is undone at once; one that runs its trial
// healthy becomes final by itself, unless autoConfirm is off. There is one,
// named default; without it every change is final at once.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Running,type=string,JSONPath=`.status.running`
// +kubebuilder:printcolumn:name=Pending,type=string,JSONPath=`.status.pending`
// +kubebuilder:printcolumn:name=RolledBack,type=string,JSONPath=`.status.rolledBack`
// +kubebuilder:printcolumn:name=Why,type=string,JSONPath=`.status.why`
type Safeguard struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SafeguardSpec   `json:"spec,omitempty"`
	Status SafeguardStatus `json:"status,omitempty"`
}

type SafeguardSpec struct {
	// ConfirmWithin is how long a change runs on trial.
	// +kubebuilder:default="1m"
	ConfirmWithin metav1.Duration `json:"confirmWithin,omitempty"`
	// AutoConfirm makes a change that ran its trial healthy final; without
	// it, only confirm does.
	// +kubebuilder:default=true
	AutoConfirm *bool `json:"autoConfirm,omitempty"`
	// Confirm makes the pending revision final.
	// +optional
	Confirm string `json:"confirm,omitempty"`
}

type SafeguardStatus struct {
	Status `json:",inline"`
	// Running is the revision the node runs; Confirmed the last one final.
	Running   string `json:"running,omitempty"`
	Confirmed string `json:"confirmed,omitempty"`
	// Pending is the revision on trial, until Deadline.
	Pending  string       `json:"pending,omitempty"`
	Deadline *metav1.Time `json:"deadline,omitempty"`
	// RolledBack is the last revision undone, and Why.
	RolledBack string `json:"rolledBack,omitempty"`
	Why        string `json:"why,omitempty"`
}

// Heartbeat tells someone outside the node that it is alive and its own
// checks work: every period it calls a URL, a dead man's switch that alerts
// when the calls stop. With a query, it calls only while the query finds
// what it looks for: a monitoring cluster that stopped collecting is as
// silent as one that is down.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Healthy,type=boolean,JSONPath=`.status.healthy`
// +kubebuilder:printcolumn:name=Sent,type=integer,JSONPath=`.status.sent`
// +kubebuilder:printcolumn:name=Last,type=date,JSONPath=`.status.lastSent`
// +kubebuilder:printcolumn:name=Error,type=string,JSONPath=`.status.error`
type Heartbeat struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HeartbeatSpec   `json:"spec"`
	Status HeartbeatStatus `json:"status,omitempty"`
}

type HeartbeatSpec struct {
	// URL called with GET at every beat.
	// +kubebuilder:validation:Pattern=`^https?://`
	URL string `json:"url"`
	// Every is the period of the beats.
	// +kubebuilder:default="1m"
	Every metav1.Duration `json:"every,omitempty"`
	// Query, when set, must return at least one series for a beat to go.
	// +optional
	Query *HeartbeatQuery `json:"query,omitempty"`
}

// HeartbeatQuery is a PromQL query to a Prometheus-compatible API.
type HeartbeatQuery struct {
	// URL of the API, as http://vmsingle.monitoring:8428.
	// +kubebuilder:validation:Pattern=`^https?://`
	URL string `json:"url"`
	// Expr: a beat goes while it returns a series, as
	// max(time() - timestamp(up)) < 120.
	Expr string `json:"expr"`
}

type HeartbeatStatus struct {
	Status `json:",inline"`
	// Healthy: the last beat went.
	Healthy  bool         `json:"healthy,omitempty"`
	Sent     int64        `json:"sent,omitempty"`
	LastSent *metav1.Time `json:"lastSent,omitempty"`
	Error    string       `json:"error,omitempty"`
}

// +kubebuilder:object:root=true
type HeartbeatList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Heartbeat `json:"items"`
}

// +kubebuilder:object:root=true
type DeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Device `json:"items"`
}

// +kubebuilder:object:root=true
type RouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Route `json:"items"`
}

// +kubebuilder:object:root=true
type SafeguardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Safeguard `json:"items"`
}
