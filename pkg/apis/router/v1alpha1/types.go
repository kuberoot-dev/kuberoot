// Package v1alpha1 is the router.kuberoot.dev API: a router's configuration
// as Kubernetes resources. The router distribution's controller makes the
// node match them: links and addresses, routes, NAT, the firewall, DHCP and
// DNS for its networks, and BGP.
// +kubebuilder:object:generate=true
// +groupName=router.kuberoot.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Status is what every router resource reports: whether the node matches it.
type Status struct {
	// ObservedGeneration is the generation the conditions describe.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions: Applied says whether the node runs this configuration.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Interface configures a network link: its addresses and MTU, or a VLAN to
// create on a parent link. Links without an Interface are left alone.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=rif
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Link,type=string,JSONPath=`.spec.link`
// +kubebuilder:printcolumn:name=Addresses,type=string,JSONPath=`.spec.addresses`
// +kubebuilder:printcolumn:name=State,type=string,JSONPath=`.status.operState`
// +kubebuilder:printcolumn:name=Applied,type=string,JSONPath=`.status.conditions[?(@.type=="Applied")].status`
type Interface struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InterfaceSpec   `json:"spec"`
	Status InterfaceStatus `json:"status,omitempty"`
}

type InterfaceSpec struct {
	// Link is the kernel's name of the link, or of the VLAN link to create.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9._-]{1,15}$`
	Link string `json:"link"`
	// VLAN creates the link as a VLAN on a parent link.
	// +optional
	VLAN *VLAN `json:"vlan,omitempty"`
	// Addresses in CIDR notation, IPv4 or IPv6. When set, the link has exactly
	// these (and its IPv6 link-local address); when unset, the link's
	// addresses are left as they are, as on an uplink configured by DHCP.
	// +optional
	Addresses []string `json:"addresses,omitempty"`
	// MTU; the link's own when unset.
	// +kubebuilder:validation:Minimum=576
	// +kubebuilder:validation:Maximum=9216
	// +optional
	MTU int32 `json:"mtu,omitempty"`
}

type VLAN struct {
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9._-]{1,15}$`
	Parent string `json:"parent"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	ID int32 `json:"id"`
}

type InterfaceStatus struct {
	Status    `json:",inline"`
	MAC       string   `json:"mac,omitempty"`
	OperState string   `json:"operState,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
}

// +kubebuilder:object:root=true
type InterfaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Interface `json:"items"`
}

// Route is a static route. The router's routes carry a protocol of their own,
// so routes other programs add are left alone.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Destination,type=string,JSONPath=`.spec.destination`
// +kubebuilder:printcolumn:name=Gateway,type=string,JSONPath=`.spec.gateway`
// +kubebuilder:printcolumn:name=Link,type=string,JSONPath=`.spec.link`
// +kubebuilder:printcolumn:name=Applied,type=string,JSONPath=`.status.conditions[?(@.type=="Applied")].status`
type Route struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RouteSpec `json:"spec"`
	Status Status    `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.gateway) || has(self.link)",message="a route needs a gateway, a link or both"
type RouteSpec struct {
	// Destination in CIDR notation; 0.0.0.0/0 or ::/0 for a default route.
	Destination string `json:"destination"`
	// +optional
	Gateway string `json:"gateway,omitempty"`
	// +optional
	Link string `json:"link,omitempty"`
	// +optional
	Metric int32 `json:"metric,omitempty"`
}

// +kubebuilder:object:root=true
type RouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Route `json:"items"`
}

// NATRule is source NAT behind the address of an outgoing link, or a port
// of the router forwarded to a host behind it.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Applied,type=string,JSONPath=`.status.conditions[?(@.type=="Applied")].status`
type NATRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NATRuleSpec `json:"spec"`
	Status Status      `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.masquerade) != has(self.portForward)",message="exactly one of masquerade and portForward"
type NATRuleSpec struct {
	// +optional
	Masquerade *Masquerade `json:"masquerade,omitempty"`
	// +optional
	PortForward *PortForward `json:"portForward,omitempty"`
}

type Masquerade struct {
	// OutLink is the link whose address traffic leaves with.
	OutLink string `json:"outLink"`
	// Sources limits it to these networks; all traffic when empty.
	// +optional
	Sources []string `json:"sources,omitempty"`
}

type PortForward struct {
	// InLink is the link the traffic arrives on.
	InLink string `json:"inLink"`
	// +kubebuilder:validation:Enum=tcp;udp
	Protocol string `json:"protocol"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
	// To is the address of the host behind the router.
	To string `json:"to"`
	// ToPort; the same port when unset.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	ToPort int32 `json:"toPort,omitempty"`
}

// +kubebuilder:object:root=true
type NATRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NATRule `json:"items"`
}

// FirewallZone groups links. Traffic is filtered once any zone exists:
// what arrives for the router is accepted per the zone's input policy,
// traffic between links only to the zones a zone forwards to, and the
// rules come first. The router's own API stays open on management zones.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=zone
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Links,type=string,JSONPath=`.spec.links`
// +kubebuilder:printcolumn:name=Input,type=string,JSONPath=`.spec.input`
// +kubebuilder:printcolumn:name=Forward To,type=string,JSONPath=`.spec.forwardTo`
// +kubebuilder:printcolumn:name=Applied,type=string,JSONPath=`.status.conditions[?(@.type=="Applied")].status`
type FirewallZone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirewallZoneSpec `json:"spec"`
	Status Status           `json:"status,omitempty"`
}

type FirewallZoneSpec struct {
	// +kubebuilder:validation:MinItems=1
	Links []string `json:"links"`
	// Input decides what arrives for the router itself.
	// +kubebuilder:validation:Enum=Accept;Drop
	// +kubebuilder:default=Drop
	Input string `json:"input,omitempty"`
	// ForwardTo names the zones this zone's traffic may go to.
	// +optional
	ForwardTo []string `json:"forwardTo,omitempty"`
	// Management keeps the router's API (6443) and node API (50000) open
	// here whatever the rules say, so a configuration cannot lock its
	// administrators out.
	// +optional
	Management bool `json:"management,omitempty"`
}

// +kubebuilder:object:root=true
type FirewallZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirewallZone `json:"items"`
}

// FirewallRule accepts or drops traffic from a zone, to another zone or to
// the router itself, before the zones' policies. Rules apply in priority
// order, then by name.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=From,type=string,JSONPath=`.spec.from`
// +kubebuilder:printcolumn:name=To,type=string,JSONPath=`.spec.to`
// +kubebuilder:printcolumn:name=Action,type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name=Applied,type=string,JSONPath=`.status.conditions[?(@.type=="Applied")].status`
type FirewallRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirewallRuleSpec `json:"spec"`
	Status Status           `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.ports) || self.protocol in ['tcp', 'udp']",message="ports need protocol tcp or udp"
type FirewallRuleSpec struct {
	// +optional
	Priority int32 `json:"priority,omitempty"`
	// From is a zone.
	From string `json:"from"`
	// To is a zone, or "self" for the router itself.
	To string `json:"to"`
	// +kubebuilder:validation:Enum=tcp;udp;icmp;any
	// +kubebuilder:default=any
	Protocol string `json:"protocol,omitempty"`
	// +optional
	Ports []int32 `json:"ports,omitempty"`
	// Sources limits the rule to these networks.
	// +optional
	Sources []string `json:"sources,omitempty"`
	// +kubebuilder:validation:Enum=Accept;Drop
	Action string `json:"action"`
}

// +kubebuilder:object:root=true
type FirewallRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirewallRule `json:"items"`
}

// DHCPServer hands out addresses on a link and answers DNS there, from a
// cache in front of the router's own resolvers.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=dhcp
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Link,type=string,JSONPath=`.spec.link`
// +kubebuilder:printcolumn:name=Range,type=string,JSONPath=`.spec.rangeStart`
// +kubebuilder:printcolumn:name=Leases,type=integer,JSONPath=`.status.activeLeases`
// +kubebuilder:printcolumn:name=Applied,type=string,JSONPath=`.status.conditions[?(@.type=="Applied")].status`
type DHCPServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DHCPServerSpec   `json:"spec"`
	Status DHCPServerStatus `json:"status,omitempty"`
}

type DHCPServerSpec struct {
	Link       string `json:"link"`
	RangeStart string `json:"rangeStart"`
	RangeEnd   string `json:"rangeEnd"`
	// LeaseTime, such as 12h.
	// +kubebuilder:default="12h"
	LeaseTime string `json:"leaseTime,omitempty"`
	// Gateway handed to clients; the router's address on the link when unset.
	// +optional
	Gateway string `json:"gateway,omitempty"`
	// DNSServers handed to clients; the router itself when unset.
	// +optional
	DNSServers []string `json:"dnsServers,omitempty"`
	// Domain clients are told they are in, and their names resolve in.
	// +optional
	Domain string `json:"domain,omitempty"`
	// +optional
	StaticLeases []StaticLease `json:"staticLeases,omitempty"`
}

type StaticLease struct {
	// +kubebuilder:validation:Pattern=`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`
	MAC     string `json:"mac"`
	Address string `json:"address"`
	// +optional
	Hostname string `json:"hostname,omitempty"`
}

type DHCPServerStatus struct {
	Status       `json:",inline"`
	ActiveLeases int32   `json:"activeLeases,omitempty"`
	Leases       []Lease `json:"leases,omitempty"`
}

type Lease struct {
	MAC      string      `json:"mac"`
	Address  string      `json:"address"`
	Hostname string      `json:"hostname,omitempty"`
	Expires  metav1.Time `json:"expires,omitempty"`
}

// +kubebuilder:object:root=true
type DHCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DHCPServer `json:"items"`
}

// BGPRouter is the router's BGP speaker: its AS, its router ID and the
// networks it announces. There is one, named default.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="the BGP router is named default"
// +kubebuilder:printcolumn:name=ASN,type=integer,JSONPath=`.spec.asn`
// +kubebuilder:printcolumn:name=Router ID,type=string,JSONPath=`.spec.routerID`
// +kubebuilder:printcolumn:name=Applied,type=string,JSONPath=`.status.conditions[?(@.type=="Applied")].status`
type BGPRouter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BGPRouterSpec   `json:"spec"`
	Status BGPRouterStatus `json:"status,omitempty"`
}

type BGPRouterSpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4294967294
	ASN int64 `json:"asn"`
	// RouterID, an IPv4 address; the router's first address when unset.
	// +optional
	RouterID string `json:"routerID,omitempty"`
	// Announce lists the networks the router announces to its peers.
	// +optional
	Announce []string `json:"announce,omitempty"`
	// Import decides whether the routes peers announce go into the router's
	// routing table.
	// +kubebuilder:validation:Enum=All;None
	// +kubebuilder:default=All
	Import string `json:"import,omitempty"`
}

// BGPRouterStatus includes observed routes, not just the desired announcements.
type BGPRouterStatus struct {
	Status `json:",inline"`
	// Routes is a bounded snapshot of the kernel's main routing table,
	// refreshed with the BGP sessions. Protocol distinguishes routes from
	// kuberoot (107), BIRD and other writers. Multipath routes have one row
	// per next hop. At most 256 rows are reported.
	// +optional
	Routes []KernelRoute `json:"routes,omitempty"`
	// +optional
	RoutesTruncated bool `json:"routesTruncated,omitempty"`
	// RoutesError explains a failed read; routes are cleared on failure.
	// +optional
	RoutesError string `json:"routesError,omitempty"`
}

// KernelRoute is one observed route or next hop in the main routing table.
type KernelRoute struct {
	Destination string `json:"destination"`
	// +optional
	Gateway string `json:"gateway,omitempty"`
	// +optional
	Link     string `json:"link,omitempty"`
	Protocol int32  `json:"protocol"`
	Metric   int32  `json:"metric"`
	Type     int32  `json:"type"`
	// Weight is the relative weight of a multipath next hop.
	// +optional
	Weight int32 `json:"weight,omitempty"`
}

// +kubebuilder:object:root=true
type BGPRouterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BGPRouter `json:"items"`
}

// BGPPeer is a BGP session with a neighbour.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Address,type=string,JSONPath=`.spec.address`
// +kubebuilder:printcolumn:name=ASN,type=integer,JSONPath=`.spec.asn`
// +kubebuilder:printcolumn:name=State,type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name=Received,type=integer,JSONPath=`.status.routesReceived`
type BGPPeer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BGPPeerSpec   `json:"spec"`
	Status BGPPeerStatus `json:"status,omitempty"`
}

type BGPPeerSpec struct {
	Address string `json:"address"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4294967294
	ASN int64 `json:"asn"`
	// Multihop allows the neighbour to be this many hops away; 0 for a
	// directly connected one.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=255
	// +optional
	Multihop int32 `json:"multihop,omitempty"`
	// ImportPolicy restricts accepted prefixes. Unset preserves BGPRouter.import;
	// an empty policy rejects all. Prefixes match exactly, never more specifics.
	// +optional
	ImportPolicy *BGPPrefixPolicy `json:"importPolicy,omitempty"`
	// ExportPolicy restricts announced prefixes. Unset uses BGPRouter.announce;
	// an empty policy rejects all. This cannot enable transit export.
	// +optional
	ExportPolicy *BGPPrefixPolicy `json:"exportPolicy,omitempty"`
	// MaxPrefixes disables the session when accepted prefixes exceed this limit.
	// Zero leaves the limit disabled. Reconfiguration resets the BIRD limit.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	// +optional
	MaxPrefixes int32 `json:"maxPrefixes,omitempty"`
}

// BGPPrefixPolicy is an exact-prefix allow list with an implicit final reject.
type BGPPrefixPolicy struct {
	// +optional
	Prefixes []string `json:"prefixes,omitempty"`
}

type BGPPeerStatus struct {
	Status `json:",inline"`
	// State of the session: Established, Active, Connect, Idle...
	State string `json:"state,omitempty"`
	// Since is when the session entered its state.
	Since          string `json:"since,omitempty"`
	RoutesReceived int32  `json:"routesReceived,omitempty"`
}

// +kubebuilder:object:root=true
type BGPPeerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BGPPeer `json:"items"`
}

// Safeguard makes every change of the router's configuration a trial: the
// node runs it, and unless it is confirmed in time (spec.confirm set to the
// revision status.pending names), the node goes back to the last confirmed
// configuration. A change that cuts the administrators off undoes itself.
// There is one, named default; without it every change is final at once, so
// deleting it ends a trial, or a rollback, by making the resources final.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="the safeguard is named default"
// +kubebuilder:printcolumn:name=Running,type=string,JSONPath=`.status.running`
// +kubebuilder:printcolumn:name=Pending,type=string,JSONPath=`.status.pending`
// +kubebuilder:printcolumn:name=Deadline,type=date,JSONPath=`.status.deadline`
// +kubebuilder:printcolumn:name=Rolled Back,type=string,JSONPath=`.status.rolledBack`
type Safeguard struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SafeguardSpec   `json:"spec,omitempty"`
	Status SafeguardStatus `json:"status,omitempty"`
}

type SafeguardSpec struct {
	// ForwardingChecks are HTTP(S) health endpoints queried during OS boot
	// probation. Each must return 200 only while an independent observer can
	// forward traffic through this router. An API readiness endpoint alone
	// does not establish forwarding health. Redirects are rejected.
	// Unset preserves the service/API-only boot assessment.
	// +kubebuilder:validation:MaxItems=8
	// +optional
	ForwardingChecks []string `json:"forwardingChecks,omitempty"`

	// ConfirmWithin is how long a change runs on trial.
	// +kubebuilder:default="2m"
	ConfirmWithin metav1.Duration `json:"confirmWithin,omitempty"`
	// Confirm makes the pending revision final.
	// +optional
	Confirm string `json:"confirm,omitempty"`
}

type SafeguardStatus struct {
	Status `json:",inline"`
	// Running is the revision the node runs; Confirmed the last one confirmed.
	Running   string `json:"running,omitempty"`
	Confirmed string `json:"confirmed,omitempty"`
	// Pending is the revision on trial, until Deadline.
	Pending  string       `json:"pending,omitempty"`
	Deadline *metav1.Time `json:"deadline,omitempty"`
	// RolledBack is the last revision undone for want of confirmation.
	RolledBack string `json:"rolledBack,omitempty"`
}

// +kubebuilder:object:root=true
type SafeguardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Safeguard `json:"items"`
}
