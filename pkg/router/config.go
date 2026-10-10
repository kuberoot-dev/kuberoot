// Package router turns router.kuberoot.dev resources into the node's
// configuration: links, routes, an nftables table, dnsmasq and bird.
package router

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
)

// Config is every router resource, as listed from the cluster.
type Config struct {
	Interfaces []v1.Interface
	Routes     []v1.Route
	NAT        []v1.NATRule
	Zones      []v1.FirewallZone
	Rules      []v1.FirewallRule
	DHCP       []v1.DHCPServer
	BGP        []v1.BGPRouter
	Peers      []v1.BGPPeer
	// Safeguards decide how changes apply; not part of the configuration.
	Safeguards []v1.Safeguard `json:"-"`
}

// Ref names a resource: its kind and name.
type Ref struct{ Kind, Name string }

func (r Ref) String() string { return r.Kind + "/" + r.Name }

// Problems are what is wrong with resources; a resource with a problem is
// left out of the configuration, and the rest still applies.
type Problems map[Ref]string

// Check finds the problems of each resource and returns the configuration
// without them. Problems that depend on another resource are found against
// the resources that are themselves valid.
func Check(c Config) (Config, Problems) {
	// Resources are taken by name, so of two that claim the same link the
	// same one wins every time, whatever order they were listed in.
	sortConfig(&c)
	p := Problems{}
	var ok Config
	links := map[string]bool{}
	for _, i := range c.Interfaces {
		if err := checkInterface(i.Spec); err != nil {
			p[Ref{"Interface", i.Name}] = err.Error()
			continue
		}
		if links[i.Spec.Link] {
			p[Ref{"Interface", i.Name}] = fmt.Sprintf("link %s is configured by another Interface", i.Spec.Link)
			continue
		}
		links[i.Spec.Link] = true
		ok.Interfaces = append(ok.Interfaces, i)
	}
	routeOf := map[string]string{}
	for _, r := range c.Routes {
		if err := checkRoute(r.Spec); err != nil {
			p[Ref{"Route", r.Name}] = err.Error()
			continue
		}
		key := fmt.Sprintf("%s/%d", mustPrefix(r.Spec.Destination), r.Spec.Metric)
		if other, ok := routeOf[key]; ok {
			p[Ref{"Route", r.Name}] = fmt.Sprintf("route %s, metric %d, is Route %s's", r.Spec.Destination, r.Spec.Metric, other)
			continue
		}
		routeOf[key] = r.Name
		ok.Routes = append(ok.Routes, r)
	}
	for _, n := range c.NAT {
		if err := checkNAT(n.Spec); err != nil {
			p[Ref{"NATRule", n.Name}] = err.Error()
			continue
		}
		ok.NAT = append(ok.NAT, n)
	}
	zones := map[string]bool{}
	zoneOf := map[string]string{}
	for _, z := range c.Zones {
		if err := checkZone(z, zoneOf); err != nil {
			p[Ref{"FirewallZone", z.Name}] = err.Error()
			continue
		}
		for _, l := range z.Spec.Links {
			zoneOf[l] = z.Name
		}
		zones[z.Name] = true
		ok.Zones = append(ok.Zones, z)
	}
	for _, z := range ok.Zones {
		for _, to := range z.Spec.ForwardTo {
			if !zones[to] {
				p[Ref{"FirewallZone", z.Name}] = fmt.Sprintf("forwards to zone %s, which does not exist", to)
			}
		}
	}
	for _, r := range c.Rules {
		if err := checkRule(r.Spec, zones); err != nil {
			p[Ref{"FirewallRule", r.Name}] = err.Error()
			continue
		}
		ok.Rules = append(ok.Rules, r)
	}
	served := map[string]bool{}
	for _, d := range c.DHCP {
		if err := checkDHCP(d.Spec, ok.Interfaces); err != nil {
			p[Ref{"DHCPServer", d.Name}] = err.Error()
			continue
		}
		if served[d.Spec.Link] {
			p[Ref{"DHCPServer", d.Name}] = fmt.Sprintf("link %s is served by another DHCPServer", d.Spec.Link)
			continue
		}
		served[d.Spec.Link] = true
		ok.DHCP = append(ok.DHCP, d)
	}
	for _, b := range c.BGP {
		if err := checkBGP(b); err != nil {
			p[Ref{"BGPRouter", b.Name}] = err.Error()
			continue
		}
		ok.BGP = append(ok.BGP, b)
	}
	for _, peer := range c.Peers {
		if a, err := netip.ParseAddr(peer.Spec.Address); err != nil || a.Zone() != "" {
			p[Ref{"BGPPeer", peer.Name}] = fmt.Sprintf("address %q: an IPv4 or IPv6 address without a zone", peer.Spec.Address)
			continue
		}
		if len(ok.BGP) == 0 {
			p[Ref{"BGPPeer", peer.Name}] = "no BGPRouter named default"
			continue
		}
		ok.Peers = append(ok.Peers, peer)
	}
	sortConfig(&ok)
	return ok, p
}

func checkInterface(s v1.InterfaceSpec) error {
	for _, a := range s.Addresses {
		if _, err := netip.ParsePrefix(a); err != nil {
			return fmt.Errorf("address %q: %v", a, err)
		}
	}
	if s.VLAN != nil && s.VLAN.Parent == s.Link {
		return fmt.Errorf("a VLAN link cannot be its own parent")
	}
	return nil
}

func checkRoute(s v1.RouteSpec) error {
	dst, err := netip.ParsePrefix(s.Destination)
	if err != nil {
		return fmt.Errorf("destination %q: %v", s.Destination, err)
	}
	if s.Gateway == "" && s.Link == "" {
		return fmt.Errorf("a route needs a gateway, a link or both")
	}
	if s.Gateway != "" {
		gw, err := netip.ParseAddr(s.Gateway)
		if err != nil {
			return fmt.Errorf("gateway %q: %v", s.Gateway, err)
		}
		if gw.Is4() != dst.Addr().Is4() {
			return fmt.Errorf("gateway %s and destination %s are of different families", gw, dst)
		}
	}
	return nil
}

func checkNAT(s v1.NATRuleSpec) error {
	switch {
	case (s.Masquerade == nil) == (s.PortForward == nil):
		return fmt.Errorf("exactly one of masquerade and portForward")
	case s.Masquerade != nil:
		for _, src := range s.Masquerade.Sources {
			if _, err := netip.ParsePrefix(src); err != nil {
				return fmt.Errorf("source %q: %v", src, err)
			}
		}
	default:
		if _, err := netip.ParseAddr(s.PortForward.To); err != nil {
			return fmt.Errorf("to %q: %v", s.PortForward.To, err)
		}
	}
	return nil
}

func checkZone(z v1.FirewallZone, zoneOf map[string]string) error {
	for _, l := range z.Spec.Links {
		if other, ok := zoneOf[l]; ok {
			return fmt.Errorf("link %s is already in zone %s", l, other)
		}
	}
	return nil
}

func checkRule(s v1.FirewallRuleSpec, zones map[string]bool) error {
	if !zones[s.From] {
		return fmt.Errorf("zone %s does not exist", s.From)
	}
	if s.To != "self" && !zones[s.To] {
		return fmt.Errorf("zone %s does not exist", s.To)
	}
	if len(s.Ports) > 0 && s.Protocol != "tcp" && s.Protocol != "udp" {
		return fmt.Errorf("ports need protocol tcp or udp")
	}
	for _, src := range s.Sources {
		if _, err := netip.ParsePrefix(src); err != nil {
			return fmt.Errorf("source %q: %v", src, err)
		}
	}
	return nil
}

func checkDHCP(s v1.DHCPServerSpec, ifs []v1.Interface) error {
	start, err := netip.ParseAddr(s.RangeStart)
	if err != nil || !start.Is4() {
		return fmt.Errorf("rangeStart %q: an IPv4 address", s.RangeStart)
	}
	end, err := netip.ParseAddr(s.RangeEnd)
	if err != nil || !end.Is4() || end.Less(start) {
		return fmt.Errorf("rangeEnd %q: an IPv4 address not before rangeStart", s.RangeEnd)
	}
	subnet := linkNet4(ifs, s.Link)
	if !subnet.IsValid() {
		return fmt.Errorf("link %s has no Interface with an IPv4 address", s.Link)
	}
	if !subnet.Contains(start) || !subnet.Contains(end) {
		return fmt.Errorf("range %s-%s is outside %s, the network of link %s", start, end, subnet, s.Link)
	}
	for _, l := range s.StaticLeases {
		a, err := netip.ParseAddr(l.Address)
		if err != nil || !subnet.Contains(a) {
			return fmt.Errorf("static lease %s: %q is not an address in %s", l.MAC, l.Address, subnet)
		}
	}
	return nil
}

func checkBGP(b v1.BGPRouter) error {
	if b.Name != "default" {
		return fmt.Errorf("the BGP router is named default")
	}
	if b.Spec.RouterID != "" {
		if a, err := netip.ParseAddr(b.Spec.RouterID); err != nil || !a.Is4() {
			return fmt.Errorf("routerID %q: an IPv4 address", b.Spec.RouterID)
		}
	}
	for _, a := range b.Spec.Announce {
		if _, err := netip.ParsePrefix(a); err != nil {
			return fmt.Errorf("announce %q: %v", a, err)
		}
	}
	return nil
}

func mustPrefix(s string) netip.Prefix {
	p, _ := netip.ParsePrefix(s)
	return p.Masked()
}

// linkNet4 is the first IPv4 network an Interface gives a link.
func linkNet4(ifs []v1.Interface, link string) netip.Prefix {
	if a := linkAddr4(ifs, link); a.IsValid() {
		return a.Masked()
	}
	return netip.Prefix{}
}

// linkAddr4 is the first IPv4 address an Interface gives a link, with its prefix.
func linkAddr4(ifs []v1.Interface, link string) netip.Prefix {
	for _, i := range ifs {
		if i.Spec.Link != link {
			continue
		}
		for _, a := range i.Spec.Addresses {
			if p, err := netip.ParsePrefix(a); err == nil && p.Addr().Is4() {
				return p
			}
		}
	}
	return netip.Prefix{}
}

// sortConfig orders resources by name, rules by priority then name, so the
// same resources always render the same configuration.
func sortConfig(c *Config) {
	sort.Slice(c.Interfaces, func(i, j int) bool { return c.Interfaces[i].Name < c.Interfaces[j].Name })
	sort.Slice(c.Routes, func(i, j int) bool { return c.Routes[i].Name < c.Routes[j].Name })
	sort.Slice(c.NAT, func(i, j int) bool { return c.NAT[i].Name < c.NAT[j].Name })
	sort.Slice(c.Zones, func(i, j int) bool { return c.Zones[i].Name < c.Zones[j].Name })
	sort.Slice(c.Rules, func(i, j int) bool {
		a, b := c.Rules[i], c.Rules[j]
		if a.Spec.Priority != b.Spec.Priority {
			return a.Spec.Priority < b.Spec.Priority
		}
		return a.Name < b.Name
	})
	sort.Slice(c.DHCP, func(i, j int) bool { return c.DHCP[i].Name < c.DHCP[j].Name })
	sort.Slice(c.Peers, func(i, j int) bool { return c.Peers[i].Name < c.Peers[j].Name })
}

// quoteSet is an nftables set of quoted names: { "eth0", "eth1" }.
func quoteSet(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return "{ " + strings.Join(q, ", ") + " }"
}

// splitFamilies separates CIDRs into IPv4 and IPv6.
func splitFamilies(cidrs []string) (v4, v6 []string) {
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil {
			if p.Addr().Is4() {
				v4 = append(v4, p.Masked().String())
			} else {
				v6 = append(v6, p.Masked().String())
			}
		}
	}
	return v4, v6
}
