package router

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
)

func named(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name} }

// gateway is a home router: WAN on eth0, LAN on eth1 with DHCP, NAT out of
// the WAN, a port forwarded to a server, LAN may go out, WAN may not come in.
func gateway() Config {
	return Config{
		Interfaces: []v1.Interface{
			{ObjectMeta: named("lan"), Spec: v1.InterfaceSpec{Link: "eth1", Addresses: []string{"192.168.10.1/24", "fd00:10::1/64"}}},
			{ObjectMeta: named("guests"), Spec: v1.InterfaceSpec{Link: "eth1.20", VLAN: &v1.VLAN{Parent: "eth1", ID: 20}, Addresses: []string{"192.168.20.1/24"}}},
		},
		Routes: []v1.Route{{ObjectMeta: named("lab"), Spec: v1.RouteSpec{Destination: "10.50.0.0/16", Gateway: "192.168.10.254"}}},
		NAT: []v1.NATRule{
			{ObjectMeta: named("out"), Spec: v1.NATRuleSpec{Masquerade: &v1.Masquerade{OutLink: "eth0", Sources: []string{"192.168.0.0/16"}}}},
			{ObjectMeta: named("web"), Spec: v1.NATRuleSpec{PortForward: &v1.PortForward{InLink: "eth0", Protocol: "tcp", Port: 8080, To: "192.168.10.5", ToPort: 80}}},
		},
		Zones: []v1.FirewallZone{
			{ObjectMeta: named("wan"), Spec: v1.FirewallZoneSpec{Links: []string{"eth0"}, Input: "Drop", Management: true}},
			{ObjectMeta: named("lan"), Spec: v1.FirewallZoneSpec{Links: []string{"eth1"}, Input: "Accept", ForwardTo: []string{"wan"}}},
			{ObjectMeta: named("guests"), Spec: v1.FirewallZoneSpec{Links: []string{"eth1.20"}, Input: "Drop", ForwardTo: []string{"wan"}}},
		},
		Rules: []v1.FirewallRule{
			{ObjectMeta: named("ssh-from-office"), Spec: v1.FirewallRuleSpec{From: "wan", To: "lan", Protocol: "tcp", Ports: []int32{22}, Sources: []string{"203.0.113.0/24"}, Action: "Accept"}},
			{ObjectMeta: named("no-printer"), Spec: v1.FirewallRuleSpec{Priority: -1, From: "guests", To: "self", Protocol: "any", Action: "Drop"}},
		},
		DHCP: []v1.DHCPServer{{ObjectMeta: named("lan"), Spec: v1.DHCPServerSpec{Link: "eth1", RangeStart: "192.168.10.100", RangeEnd: "192.168.10.199",
			Domain: "home.lan", StaticLeases: []v1.StaticLease{{MAC: "52:54:00:AA:BB:CC", Address: "192.168.10.5", Hostname: "server"}}}}},
	}
}

func TestCheckKeepsValidResourcesAndNamesTheRest(t *testing.T) {
	c := gateway()
	c.Interfaces = append(c.Interfaces, v1.Interface{ObjectMeta: named("zz-dup"), Spec: v1.InterfaceSpec{Link: "eth1"}})
	c.Routes = append(c.Routes, v1.Route{ObjectMeta: named("mixed"), Spec: v1.RouteSpec{Destination: "10.0.0.0/8", Gateway: "fd00::1"}})
	c.Rules = append(c.Rules, v1.FirewallRule{ObjectMeta: named("typo"), Spec: v1.FirewallRuleSpec{From: "lna", To: "wan", Action: "Accept"}})
	c.DHCP = append(c.DHCP, v1.DHCPServer{ObjectMeta: named("outside"), Spec: v1.DHCPServerSpec{Link: "eth1.20", RangeStart: "192.168.10.2", RangeEnd: "192.168.10.9"}})
	c.Peers = []v1.BGPPeer{{ObjectMeta: named("upstream"), Spec: v1.BGPPeerSpec{Address: "10.0.0.1", ASN: 65001}}}

	ok, problems := Check(c)
	want := map[Ref]string{
		{"Interface", "zz-dup"}:   "configured by another Interface",
		{"Route", "mixed"}:        "different families",
		{"FirewallRule", "typo"}:  "zone lna does not exist",
		{"DHCPServer", "outside"}: "outside 192.168.20.0/24",
		{"BGPPeer", "upstream"}:   "no BGPRouter",
	}
	for ref, msg := range want {
		if !strings.Contains(problems[ref], msg) {
			t.Errorf("%s: problem %q, want it to say %q", ref, problems[ref], msg)
		}
	}
	if len(problems) != len(want) {
		t.Errorf("problems = %v", problems)
	}
	if len(ok.Interfaces) != 2 || len(ok.Routes) != 1 || len(ok.Rules) != 2 || len(ok.DHCP) != 1 {
		t.Errorf("valid resources were dropped: %+v", ok)
	}
	if ok.Rules[0].Name != "no-printer" {
		t.Errorf("rules are not in priority order: %s first", ok.Rules[0].Name)
	}
}

func TestRulesetReplacesTheTableAndFilters(t *testing.T) {
	ok, _ := Check(gateway())
	got := Ruleset(ok)
	for _, want := range []string{
		"table inet kuberoot_router\ndelete table inet kuberoot_router\ntable inet kuberoot_router {",
		"chain input {\n\t\ttype filter hook input priority filter; policy drop;",
		`iifname { "eth0" } tcp dport { 6443, 50000 } accept`,
		`iifname "eth1" udp dport { 53, 67 } accept`,
		`iifname { "eth1.20" } drop comment "rule no-printer"`,
		`iifname { "eth1" } accept`,
		`iifname { "eth0" } oifname { "eth1" } ip saddr { 203.0.113.0/24 } tcp dport { 22 } accept comment "rule ssh-from-office"`,
		`iifname { "eth1" } oifname { "eth0" } accept`,
		`iifname { "eth1.20" } oifname { "eth0" } accept`,
		"ct status dnat accept",
		`iifname "eth0" tcp dport 8080 dnat ip to 192.168.10.5:80 comment "nat web"`,
		`oifname "eth0" ip saddr { 192.168.0.0/16 } masquerade comment "nat out"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ruleset lacks %q:\n%s", want, got)
		}
	}
	// The guests' drop comes before the zone's own policy.
	if strings.Index(got, `rule no-printer`) > strings.Index(got, `iifname { "eth1" } accept`) {
		t.Error("rules must come before zone input policies")
	}
}

func TestNoZonesNoFilter(t *testing.T) {
	c := gateway()
	c.Zones, c.Rules = nil, nil
	ok, _ := Check(c)
	got := Ruleset(ok)
	if strings.Contains(got, "hook input") || strings.Contains(got, "hook forward") {
		t.Errorf("filtered without any zone:\n%s", got)
	}
	if !strings.Contains(got, "masquerade") {
		t.Error("NAT is applied without zones as well")
	}
}

func TestDnsmasqConfig(t *testing.T) {
	ok, _ := Check(gateway())
	got := DnsmasqConfig(ok, "/var/lib/kuberoot/router/dnsmasq.leases")
	for _, want := range []string{
		"interface=eth1\n",
		"dhcp-range=set:s-lan,192.168.10.100,192.168.10.199,255.255.255.0,12h\n",
		"dhcp-option=tag:s-lan,option:router,192.168.10.1\n",
		"dhcp-option=tag:s-lan,option:dns-server,192.168.10.1\n",
		"domain=home.lan,192.168.10.100,192.168.10.199\n",
		"dhcp-host=52:54:00:aa:bb:cc,192.168.10.5,server\n",
		"dhcp-leasefile=/var/lib/kuberoot/router/dnsmasq.leases\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dnsmasq config lacks %q:\n%s", want, got)
		}
	}
	if DnsmasqConfig(Config{}, "x") != "" {
		t.Error("dnsmasq runs with nothing to serve")
	}
}

func TestParseLeases(t *testing.T) {
	leases := ParseLeases(strings.NewReader("1791500000 52:54:00:aa:bb:cc 192.168.10.5 server 01:52:54:00:aa:bb:cc\n0 52:54:00:11:22:33 192.168.10.100 * *\n"))
	if len(leases) != 2 || leases[0].Hostname != "server" || leases[1].Hostname != "" || leases[0].Expires.Unix() != 1791500000 {
		t.Errorf("leases = %+v", leases)
	}
}

func TestBirdConfig(t *testing.T) {
	c := Config{
		BGP: []v1.BGPRouter{{ObjectMeta: named("default"), Spec: v1.BGPRouterSpec{ASN: 65010, Announce: []string{"192.168.10.0/24", "fd00:10::/64"}, Import: "All"}}},
		Peers: []v1.BGPPeer{
			{ObjectMeta: named("r2"), Spec: v1.BGPPeerSpec{Address: "10.244.0.20", ASN: 65020}},
			{ObjectMeta: named("upstream-v6"), Spec: v1.BGPPeerSpec{Address: "2001:db8::1", ASN: 64512, Multihop: 2}},
		},
	}
	ok, problems := Check(c)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	got := BirdConfig(ok, "10.244.0.10")
	for _, want := range []string{
		"router id 10.244.0.10;",
		"protocol static announce_ipv4 {\n\tipv4;\n\troute 192.168.10.0/24 blackhole;",
		"route fd00:10::/64 blackhole;",
		"if net ~ [ 192.168.10.0/24 ] then accept;\n\tif net ~ [ fd00:10::/64 ] then accept;",
		"protocol bgp peer_r2 {\n\tlocal as 65010;\n\tneighbor 10.244.0.20 as 65020;\n\tipv4 { import all; export filter announce; };",
		"protocol bgp peer_upstream_v6 {",
		"multihop 2;",
		"ipv6 { import all; export filter announce; };",
		"export filter { if source = RTS_BGP then accept; reject; };",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("bird config lacks %q:\n%s", want, got)
		}
	}
	ok.BGP[0].Spec.Import = "None"
	if got := BirdConfig(ok, "10.244.0.10"); !strings.Contains(got, "import none; export filter announce") || strings.Contains(got, "RTS_BGP") {
		t.Errorf("with imports off:\n%s", got)
	}
}

func TestParseProtocols(t *testing.T) {
	out := `BIRD 2.15.1 ready.
Name       Proto      Table      State  Since         Info
device1    Device     ---        up     10:00:00.000
peer_r2    BGP        ---        up     10:21:34.123  Established
  BGP state:          Established
    Neighbor address: 10.244.0.20
  Channel ipv4
    State:          UP
    Routes:         3 imported, 1 exported, 3 preferred
peer_down  BGP        ---        start  10:22:00.000  Active        Socket: Connection refused
  BGP state:          Active
`
	got := ParseProtocols(strings.NewReader(out))
	if s := got["peer_r2"]; s.State != "Established" || s.RoutesReceived != 3 || s.Since != "10:21:34.123" {
		t.Errorf("peer_r2 = %+v", s)
	}
	if s := got["peer_down"]; s.State != "Active" || s.RoutesReceived != 0 {
		t.Errorf("peer_down = %+v", s)
	}
	if _, ok := got["device1"]; ok {
		t.Error("a non-BGP protocol was taken for a session")
	}
}

func TestCheckIsTheSameWhateverTheListOrder(t *testing.T) {
	a := v1.FirewallZone{ObjectMeta: named("a-mgmt"), Spec: v1.FirewallZoneSpec{Links: []string{"eth0"}, Management: true}}
	b := v1.FirewallZone{ObjectMeta: named("b-other"), Spec: v1.FirewallZoneSpec{Links: []string{"eth0"}}}
	for _, zones := range [][]v1.FirewallZone{{a, b}, {b, a}} {
		ok, problems := Check(Config{Zones: zones})
		if len(ok.Zones) != 1 || ok.Zones[0].Name != "a-mgmt" || problems[Ref{"FirewallZone", "b-other"}] == "" {
			t.Errorf("listed as %s, %s: kept %v, problems %v", zones[0].Name, zones[1].Name, ok.Zones, problems)
		}
	}
}

func TestCheckRefusesDuplicateRoutesAndZonedPeers(t *testing.T) {
	c := Config{
		Routes: []v1.Route{
			{ObjectMeta: named("a"), Spec: v1.RouteSpec{Destination: "10.0.0.0/8", Gateway: "192.168.10.254"}},
			{ObjectMeta: named("b"), Spec: v1.RouteSpec{Destination: "10.0.0.0/8", Gateway: "192.168.10.253"}},
		},
		BGP:   []v1.BGPRouter{{ObjectMeta: named("default"), Spec: v1.BGPRouterSpec{ASN: 65010}}},
		Peers: []v1.BGPPeer{{ObjectMeta: named("ll"), Spec: v1.BGPPeerSpec{Address: "fe80::1%eth0", ASN: 65020}}},
	}
	ok, problems := Check(c)
	if len(ok.Routes) != 1 || !strings.Contains(problems[Ref{"Route", "b"}], "is Route a's") {
		t.Errorf("duplicate route: kept %d, problem %q", len(ok.Routes), problems[Ref{"Route", "b"}])
	}
	if len(ok.Peers) != 0 || problems[Ref{"BGPPeer", "ll"}] == "" {
		t.Error("a peer address with a zone was accepted")
	}
}

func TestPortForwardsLeaveTheManagementPortsAlone(t *testing.T) {
	c := gateway()
	c.NAT = append(c.NAT, v1.NATRule{ObjectMeta: named("grab-api"), Spec: v1.NATRuleSpec{PortForward: &v1.PortForward{InLink: "eth0", Protocol: "tcp", Port: 6443, To: "192.168.10.5"}}})
	ok, _ := Check(c)
	got := Ruleset(ok)
	ret := strings.Index(got, `iifname { "eth0" } tcp dport { 6443, 50000 } return`)
	dnat := strings.Index(got, `dport 6443 dnat`)
	if ret < 0 || dnat < 0 || ret > dnat {
		t.Errorf("management ports are not kept from port forwards:\n%s", got)
	}
}

func TestMasqueradeWithoutSourcesIsIPv4(t *testing.T) {
	c := Config{NAT: []v1.NATRule{{ObjectMeta: named("all"), Spec: v1.NATRuleSpec{Masquerade: &v1.Masquerade{OutLink: "eth0"}}}}}
	ok, _ := Check(c)
	if got := Ruleset(ok); !strings.Contains(got, `oifname "eth0" meta nfproto ipv4 masquerade`) {
		t.Errorf("ruleset:\n%s", got)
	}
}

func TestIPv6RouteWithoutMetricMatchesTheKernel(t *testing.T) {
	r, err := kernelRoute(v1.RouteSpec{Destination: "2001:db8::/64", Gateway: "fd00::1"})
	if err != nil || r.Priority != 1024 {
		t.Errorf("priority %d, %v; the kernel keeps 1024", r.Priority, err)
	}
	if r, _ := kernelRoute(v1.RouteSpec{Destination: "10.0.0.0/8", Gateway: "192.168.10.1"}); r.Priority != 0 {
		t.Errorf("IPv4 priority %d", r.Priority)
	}
}
