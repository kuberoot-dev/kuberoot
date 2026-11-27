package router

import (
	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
	"strings"
	"testing"
)

func TestPeerPolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		valid  bool
	}{
		{"192.0.2.0/24", true}, {"0.0.0.0/0", true}, {"192.0.2.1/24", false},
		{"::/0", false}, {"192.0.2.0/24+", false}, {"0.0.0.0/0 ]; accept; #", false},
	} {
		s := v1.BGPPeerSpec{Address: "198.18.0.2", ImportPolicy: &v1.BGPPrefixPolicy{Prefixes: []string{tc.prefix}}}
		if got := checkPeerPolicy(s) == nil; got != tc.valid {
			t.Errorf("prefix %q valid=%v", tc.prefix, got)
		}
	}
	if checkPeerPolicy(v1.BGPPeerSpec{MaxPrefixes: -1}) == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestPeerPoliciesAreIsolatedAndFailClosed(t *testing.T) {
	c := Config{BGP: []v1.BGPRouter{{Spec: v1.BGPRouterSpec{ASN: 65000, Announce: []string{"192.0.2.0/24"}}}}, Peers: []v1.BGPPeer{
		{ObjectMeta: named("customer"), Spec: v1.BGPPeerSpec{Address: "198.18.0.2", ASN: 65001, ImportPolicy: &v1.BGPPrefixPolicy{Prefixes: []string{"203.0.113.0/24"}}, ExportPolicy: &v1.BGPPrefixPolicy{}, MaxPrefixes: 1000}},
		{ObjectMeta: named("observer"), Spec: v1.BGPPeerSpec{Address: "198.18.0.3", ASN: 65002, ImportPolicy: &v1.BGPPrefixPolicy{}, ExportPolicy: &v1.BGPPrefixPolicy{Prefixes: []string{"192.0.2.0/24", "203.0.113.0/24"}}}},
	}}
	got := BirdConfig(c, "198.18.0.1")
	for _, want := range []string{"import filter { if net ~ [ 203.0.113.0/24 ] then accept; reject; }; export none; import limit 1000 action disable;", "import none; export filter { if source != RTS_STATIC then reject; if net ~ [ 192.0.2.0/24 ] then accept; reject; };"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	c.BGP[0].Spec.Import = "None"
	if strings.Contains(BirdConfig(c, "198.18.0.1"), "import filter") {
		t.Fatal("peer policy bypassed global import None")
	}
}

func TestPeerPolicyDeepCopy(t *testing.T) {
	p := v1.BGPPeer{Spec: v1.BGPPeerSpec{ImportPolicy: &v1.BGPPrefixPolicy{Prefixes: []string{"192.0.2.0/24"}}, ExportPolicy: &v1.BGPPrefixPolicy{Prefixes: []string{"198.51.100.0/24"}}}}
	q := p.DeepCopy()
	q.Spec.ImportPolicy.Prefixes[0] = "0.0.0.0/0"
	q.Spec.ExportPolicy.Prefixes[0] = "0.0.0.0/0"
	if p.Spec.ImportPolicy.Prefixes[0] != "192.0.2.0/24" || p.Spec.ExportPolicy.Prefixes[0] != "198.51.100.0/24" {
		t.Fatal("DeepCopy aliases policy slices")
	}
}
