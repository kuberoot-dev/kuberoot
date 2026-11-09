package main

import (
	"testing"

	"github.com/kuberoot-dev/kuberoot/pkg/distro"
)

// A distribution that installs a CNI must not raise a tunnel of its own: both
// take UDP 8472, the CNI's device never comes up, and every pod is refused an
// address with nothing in the cluster naming the reason. The cloud profile
// says "none" for exactly that, and a node must honour it without anyone
// remembering to pass a boot argument.
func TestPodNetworkComesFromTheDistributionWhenBootSaysNothing(t *testing.T) {
	cloud := &distro.Profile{}
	cloud.Spec.PodNetwork = "none"
	if got := podNetworkDefault("", cloud); got != "none" {
		t.Errorf("the distribution's choice must stand, got %q", got)
	}
}

// A boot argument is the operator speaking about this node, and it wins.
func TestBootArgumentOverridesTheDistribution(t *testing.T) {
	cloud := &distro.Profile{}
	cloud.Spec.PodNetwork = "none"
	if got := podNetworkDefault("host-gw", cloud); got != "host-gw" {
		t.Errorf("the boot argument must win, got %q", got)
	}
}

// Everything else keeps the behaviour it had.
func TestPodNetworkFallsBackToVxlan(t *testing.T) {
	for name, profile := range map[string]*distro.Profile{
		"no profile":     nil,
		"silent profile": {},
	} {
		if got := podNetworkDefault("", profile); got != "vxlan" {
			t.Errorf("%s: expected vxlan, got %q", name, got)
		}
	}
	bad := &distro.Profile{}
	bad.Spec.PodNetwork = "nonsense"
	if got := podNetworkDefault("", bad); got != "vxlan" {
		t.Errorf("an unknown value must not be taken, got %q", got)
	}
}
