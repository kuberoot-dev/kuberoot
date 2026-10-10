package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
	"github.com/kuberoot-dev/kuberoot/pkg/safeguard"
)

// Revision names a configuration by what it asks of the node: the specs of
// its resources. The same resources make the same revision.
func Revision(c Config) string {
	type entry struct {
		Kind, Name string
		Spec       any
	}
	var all []entry
	for _, x := range c.Interfaces {
		all = append(all, entry{"Interface", x.Name, x.Spec})
	}
	for _, x := range c.Routes {
		all = append(all, entry{"Route", x.Name, x.Spec})
	}
	for _, x := range c.NAT {
		all = append(all, entry{"NATRule", x.Name, x.Spec})
	}
	for _, x := range c.Zones {
		all = append(all, entry{"FirewallZone", x.Name, x.Spec})
	}
	for _, x := range c.Rules {
		all = append(all, entry{"FirewallRule", x.Name, x.Spec})
	}
	for _, x := range c.DHCP {
		all = append(all, entry{"DHCPServer", x.Name, x.Spec})
	}
	for _, x := range c.BGP {
		all = append(all, entry{"BGPRouter", x.Name, x.Spec})
	}
	for _, x := range c.Peers {
		all = append(all, entry{"BGPPeer", x.Name, x.Spec})
	}
	raw, _ := json.Marshal(all)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:12]
}

// trial is the router's configuration under its Safeguard.
type trial struct{ safeguard.Trial[Config] }

// decide returns the configuration to run and its revision. It reports
// whether the confirmed configuration changed, to be kept on disk.
func (t *trial) decide(candidate Config, rev string, guard *v1.Safeguard, now time.Time) (Config, string, bool) {
	var g *safeguard.Guard
	if guard != nil {
		g = &safeguard.Guard{ConfirmWithin: guard.Spec.ConfirmWithin.Duration, Confirm: guard.Spec.Confirm}
	}
	return t.Decide(candidate, rev, g, "", now)
}
