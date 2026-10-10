package router

import (
	"fmt"
	"net/netip"
	"strings"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
)

// Table is the router's nftables table; nothing else is touched.
const Table = "kuberoot_router"

// ManagementPorts are the router's API server and node API.
var ManagementPorts = []int{6443, 50000}

// Ruleset renders the router's table as an nftables script that replaces
// the table in one transaction: whatever the table held is gone, and no
// packet sees half of the new rules.
func Ruleset(c Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s\ndelete table inet %s\ntable inet %s {\n", Table, Table, Table)
	if len(c.Zones) > 0 {
		writeFilter(&b, c)
	}
	writeNAT(&b, c)
	b.WriteString("}\n")
	return b.String()
}

func writeFilter(b *strings.Builder, c Config) {
	links := map[string][]string{} // zone -> links
	for _, z := range c.Zones {
		links[z.Name] = z.Spec.Links
	}

	b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n")
	b.WriteString("\t\tct state established,related accept\n\t\tct state invalid drop\n\t\tiif \"lo\" accept\n")
	// Neighbour discovery and ping keep the network working and debuggable.
	b.WriteString("\t\tmeta l4proto ipv6-icmp accept\n\t\ticmp type echo-request accept\n")
	for _, z := range c.Zones {
		if z.Spec.Management {
			fmt.Fprintf(b, "\t\tiifname %s tcp dport %s accept\n", quoteSet(z.Spec.Links), portSet(ManagementPorts))
		}
	}
	// What the router serves on its own: DHCP and DNS where it hands out
	// addresses, BGP from its peers.
	for _, d := range c.DHCP {
		fmt.Fprintf(b, "\t\tiifname %q udp dport { 53, 67 } accept\n\t\tiifname %q tcp dport 53 accept\n", d.Spec.Link, d.Spec.Link)
	}
	for _, p := range c.Peers {
		fmt.Fprintf(b, "\t\t%s saddr %s tcp dport 179 accept\n", family(p.Spec.Address), p.Spec.Address)
	}
	for _, r := range c.Rules {
		if r.Spec.To == "self" {
			writeRule(b, r, "iifname "+quoteSet(links[r.Spec.From]))
		}
	}
	for _, z := range c.Zones {
		if z.Spec.Input == "Accept" {
			fmt.Fprintf(b, "\t\tiifname %s accept\n", quoteSet(z.Spec.Links))
		}
	}
	b.WriteString("\t}\n")

	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy drop;\n")
	b.WriteString("\t\tct state established,related accept\n\t\tct state invalid drop\n")
	// Ports forwarded by NAT rules are let through to their hosts.
	b.WriteString("\t\tct status dnat accept\n")
	for _, r := range c.Rules {
		if r.Spec.To != "self" {
			writeRule(b, r, "iifname "+quoteSet(links[r.Spec.From])+" oifname "+quoteSet(links[r.Spec.To]))
		}
	}
	for _, z := range c.Zones {
		for _, to := range z.Spec.ForwardTo {
			if len(links[to]) > 0 {
				fmt.Fprintf(b, "\t\tiifname %s oifname %s accept\n", quoteSet(z.Spec.Links), quoteSet(links[to]))
			}
		}
	}
	b.WriteString("\t}\n")
}

// writeRule writes a rule once per address family of its sources.
func writeRule(b *strings.Builder, r v1.FirewallRule, match string) {
	verdict := strings.ToLower(r.Spec.Action)
	proto := ""
	switch r.Spec.Protocol {
	case "tcp", "udp":
		proto = " meta l4proto " + r.Spec.Protocol
		if len(r.Spec.Ports) > 0 {
			ports := make([]int, len(r.Spec.Ports))
			for i, p := range r.Spec.Ports {
				ports[i] = int(p)
			}
			proto = " " + r.Spec.Protocol + " dport " + portSet(ports)
		}
	case "icmp":
		proto = " meta l4proto { icmp, ipv6-icmp }"
	}
	v4, v6 := splitFamilies(r.Spec.Sources)
	if len(r.Spec.Sources) == 0 {
		fmt.Fprintf(b, "\t\t%s%s %s comment %q\n", match, proto, verdict, "rule "+r.Name)
		return
	}
	if len(v4) > 0 {
		fmt.Fprintf(b, "\t\t%s ip saddr { %s }%s %s comment %q\n", match, strings.Join(v4, ", "), proto, verdict, "rule "+r.Name)
	}
	if len(v6) > 0 {
		fmt.Fprintf(b, "\t\t%s ip6 saddr { %s }%s %s comment %q\n", match, strings.Join(v6, ", "), proto, verdict, "rule "+r.Name)
	}
}

func writeNAT(b *strings.Builder, c Config) {
	var dnat, snat []string
	for _, n := range c.NAT {
		if m := n.Spec.Masquerade; m != nil {
			v4, v6 := splitFamilies(m.Sources)
			switch {
			case len(m.Sources) == 0:
				// IPv4 only: IPv6 has addresses enough to need no NAT.
				snat = append(snat, fmt.Sprintf("oifname %q meta nfproto ipv4 masquerade comment %q", m.OutLink, "nat "+n.Name))
			default:
				if len(v4) > 0 {
					snat = append(snat, fmt.Sprintf("oifname %q ip saddr { %s } masquerade comment %q", m.OutLink, strings.Join(v4, ", "), "nat "+n.Name))
				}
				if len(v6) > 0 {
					snat = append(snat, fmt.Sprintf("oifname %q ip6 saddr { %s } masquerade comment %q", m.OutLink, strings.Join(v6, ", "), "nat "+n.Name))
				}
			}
		}
		if f := n.Spec.PortForward; f != nil {
			to, _ := netip.ParseAddr(f.To)
			port := f.ToPort
			if port == 0 {
				port = f.Port
			}
			target := fmt.Sprintf("%s:%d", to, port)
			if to.Is6() {
				target = fmt.Sprintf("[%s]:%d", to, port)
			}
			dnat = append(dnat, fmt.Sprintf("iifname %q %s dport %d dnat %s to %s comment %q",
				f.InLink, f.Protocol, f.Port, family(f.To), target, "nat "+n.Name))
		}
	}
	if len(dnat) > 0 {
		b.WriteString("\tchain prerouting {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
		// No port forward takes the router's API away from a management zone.
		for _, z := range c.Zones {
			if z.Spec.Management {
				fmt.Fprintf(b, "\t\tiifname %s tcp dport %s return\n", quoteSet(z.Spec.Links), portSet(ManagementPorts))
			}
		}
		for _, r := range dnat {
			b.WriteString("\t\t" + r + "\n")
		}
		b.WriteString("\t}\n")
	}
	if len(snat) > 0 {
		b.WriteString("\tchain postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
		for _, r := range snat {
			b.WriteString("\t\t" + r + "\n")
		}
		b.WriteString("\t}\n")
	}
}

func portSet(ports []int) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = fmt.Sprint(p)
	}
	return "{ " + strings.Join(s, ", ") + " }"
}

// family is the nftables address family keyword of an address: ip or ip6.
func family(addr string) string {
	if a, err := netip.ParseAddr(addr); err == nil && a.Is6() {
		return "ip6"
	}
	return "ip"
}
