package router

import (
	"bufio"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
)

// DnsmasqConfig renders dnsmasq's configuration for the DHCP servers: DHCP
// on their links and a DNS cache there in front of the router's resolvers.
// It is empty when there is nothing to serve.
func DnsmasqConfig(c Config, leaseFile string) string {
	if len(c.DHCP) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Written by kuberoot-router from the DHCPServer resources.\n")
	b.WriteString("keep-in-foreground\nno-hosts\nbind-dynamic\ndhcp-authoritative\nresolv-file=/etc/resolv.conf\n")
	fmt.Fprintf(&b, "dhcp-leasefile=%s\n", leaseFile)
	for _, d := range c.DHCP {
		s := d.Spec
		tag := "s-" + d.Name
		own := linkAddr4(c.Interfaces, s.Link).Addr()
		gateway := s.Gateway
		if gateway == "" {
			gateway = own.String()
		}
		dns := s.DNSServers
		if len(dns) == 0 {
			dns = []string{own.String()}
		}
		lease := s.LeaseTime
		if lease == "" {
			lease = "12h"
		}
		fmt.Fprintf(&b, "\n# DHCPServer %s\ninterface=%s\n", d.Name, s.Link)
		fmt.Fprintf(&b, "dhcp-range=set:%s,%s,%s,%s,%s\n", tag, s.RangeStart, s.RangeEnd, netmask(linkNet4(c.Interfaces, s.Link)), lease)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,option:router,%s\n", tag, gateway)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,option:dns-server,%s\n", tag, strings.Join(dns, ","))
		if s.Domain != "" {
			fmt.Fprintf(&b, "dhcp-option=tag:%s,option:domain-name,%s\n", tag, s.Domain)
			fmt.Fprintf(&b, "domain=%s,%s,%s\nlocal=/%s/\n", s.Domain, s.RangeStart, s.RangeEnd, s.Domain)
		}
		for _, l := range s.StaticLeases {
			if l.Hostname != "" {
				fmt.Fprintf(&b, "dhcp-host=%s,%s,%s\n", strings.ToLower(l.MAC), l.Address, l.Hostname)
			} else {
				fmt.Fprintf(&b, "dhcp-host=%s,%s\n", strings.ToLower(l.MAC), l.Address)
			}
		}
	}
	return b.String()
}

func netmask(p netip.Prefix) string {
	bits := p.Bits()
	m := uint32(0xffffffff) << (32 - bits)
	return fmt.Sprintf("%d.%d.%d.%d", byte(m>>24), byte(m>>16), byte(m>>8), byte(m))
}

// ParseLeases reads dnsmasq's lease file: expiry, MAC, address, hostname and
// client ID per line.
func ParseLeases(r io.Reader) []v1.Lease {
	var out []v1.Lease
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		l := v1.Lease{MAC: f[1], Address: f[2]}
		if f[3] != "*" {
			l.Hostname = f[3]
		}
		if exp, err := strconv.ParseInt(f[0], 10, 64); err == nil && exp > 0 {
			l.Expires.Time = time.Unix(exp, 0).UTC()
		}
		out = append(out, l)
	}
	return out
}

// BirdConfig renders bird's configuration: the networks to announce, a
// session per peer, and, unless imports are off, the routes peers announce
// written into the kernel's table. It is empty without a BGP router.
func BirdConfig(c Config, routerID string) string {
	if len(c.BGP) == 0 {
		return ""
	}
	bgp := c.BGP[0].Spec
	if bgp.RouterID != "" {
		routerID = bgp.RouterID
	}
	v4, v6 := splitFamilies(bgp.Announce)
	var b strings.Builder
	b.WriteString("# Written by kuberoot-router from the BGPRouter and BGPPeer resources.\n")
	fmt.Fprintf(&b, "log stderr all;\nrouter id %s;\n\nprotocol device {}\n", routerID)
	kernelExport := "export filter { if source = RTS_BGP then accept; reject; };"
	if bgp.Import == "None" {
		kernelExport = "export none;"
	}
	for _, fam := range []string{"ipv4", "ipv6"} {
		fmt.Fprintf(&b, "\nprotocol kernel kernel_%s {\n\t%s { import none; %s };\n\tmerge paths on;\n}\n", fam, fam, kernelExport)
	}
	// The announced networks exist for BGP only: kept out of the kernel.
	for _, f := range []struct {
		fam  string
		nets []string
	}{{"ipv4", v4}, {"ipv6", v6}} {
		fam, nets := f.fam, f.nets
		if len(nets) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\nprotocol static announce_%s {\n\t%s;\n", fam, fam)
		for _, n := range nets {
			fmt.Fprintf(&b, "\troute %s blackhole;\n", n)
		}
		b.WriteString("}\n")
	}
	// A prefix set holds one family.
	b.WriteString("\nfilter announce {\n")
	for _, nets := range [][]string{v4, v6} {
		if len(nets) > 0 {
			fmt.Fprintf(&b, "\tif net ~ [ %s ] then accept;\n", strings.Join(nets, ", "))
		}
	}
	b.WriteString("\treject;\n}\n")
	imp := "import all;"
	if bgp.Import == "None" {
		imp = "import none;"
	}
	for _, p := range c.Peers {
		fam := "ipv4"
		if a, _ := netip.ParseAddr(p.Spec.Address); a.Is6() {
			fam = "ipv6"
		}
		fmt.Fprintf(&b, "\nprotocol bgp %s {\n\tlocal as %d;\n\tneighbor %s as %d;\n", PeerProtocol(p.Name), bgp.ASN, p.Spec.Address, p.Spec.ASN)
		if p.Spec.Multihop > 0 {
			fmt.Fprintf(&b, "\tmultihop %d;\n", p.Spec.Multihop)
		}
		fmt.Fprintf(&b, "\t%s { %s export filter announce; };\n}\n", fam, imp)
	}
	return b.String()
}

// PeerProtocol is the name of a peer's session in bird.
func PeerProtocol(peer string) string {
	return "peer_" + strings.NewReplacer("-", "_", ".", "_").Replace(peer)
}

// PeerState is a session as bird reports it.
type PeerState struct {
	State, Since   string
	RoutesReceived int32
}

var routesLine = regexp.MustCompile(`Routes:\s+(\d+) imported`)

// ParseProtocols reads `birdc show protocols all`: each BGP session's state,
// since when, and how many routes it imported.
func ParseProtocols(r io.Reader) map[string]PeerState {
	out := map[string]PeerState{}
	current := ""
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		f := strings.Fields(line)
		switch {
		case len(f) >= 5 && !strings.HasPrefix(line, " ") && f[1] == "BGP":
			// name, protocol, table, state, since, info
			current = f[0]
			st := PeerState{Since: f[4], State: "Down"}
			if f[3] != "down" {
				st.State = "Connecting"
			}
			out[current] = st
		case len(f) > 0 && !strings.HasPrefix(line, " "):
			current = "" // another protocol
		case current != "" && strings.HasPrefix(strings.TrimSpace(line), "BGP state:"):
			st := out[current]
			st.State = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "BGP state:"))
			out[current] = st
		case current != "":
			if m := routesLine.FindStringSubmatch(line); m != nil {
				st := out[current]
				n, _ := strconv.Atoi(m[1])
				st.RoutesReceived = int32(n)
				out[current] = st
			}
		}
	}
	return out
}
