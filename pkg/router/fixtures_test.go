package router

import (
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
)

// With ROUTER_FIXTURES set, the rendered configurations of a full router
// are written there, for checking with nft -c and bird -p.
func TestWriteFixtures(t *testing.T) {
	dir := os.Getenv("ROUTER_FIXTURES")
	if dir == "" {
		t.Skip("ROUTER_FIXTURES not set")
	}
	c := gateway()
	c.Rules = append(c.Rules, v1.FirewallRule{ObjectMeta: named("dual"), Spec: v1.FirewallRuleSpec{From: "wan", To: "self", Protocol: "udp", Ports: []int32{51820},
		Sources: []string{"198.51.100.0/24", "2001:db8:1::/48"}, Action: "Accept"}})
	c.NAT = append(c.NAT, v1.NATRule{ObjectMeta: named("v6fwd"), Spec: v1.NATRuleSpec{PortForward: &v1.PortForward{InLink: "eth0", Protocol: "udp", Port: 5353, To: "fd00:10::5"}}},
		v1.NATRule{ObjectMeta: named("all"), Spec: v1.NATRuleSpec{Masquerade: &v1.Masquerade{OutLink: "eth2"}}})
	c.BGP = []v1.BGPRouter{{ObjectMeta: named("default"), Spec: v1.BGPRouterSpec{ASN: 65010, Announce: []string{"192.168.10.0/24", "fd00:10::/64"}, Import: "All"}}}
	c.Peers = []v1.BGPPeer{{ObjectMeta: named("v4"), Spec: v1.BGPPeerSpec{Address: "10.255.0.3", ASN: 65020}},
		{ObjectMeta: named("v6"), Spec: v1.BGPPeerSpec{Address: "2001:db8::1", ASN: 64512, Multihop: 2}}}
	ok, problems := Check(c)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	for name, body := range map[string]string{"ruleset.nft": Ruleset(ok), "dnsmasq.conf": DnsmasqConfig(ok, "/tmp/leases") + "user=root\npid-file=\n", "bird.conf": BirdConfig(ok, "10.255.0.2")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
