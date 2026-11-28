package main

import (
	"os"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/kuberoot-dev/kuberoot/pkg/distro"
)

func TestOnlyNodesWithAKubeletAreLabelled(t *testing.T) {
	for name, want := range map[string]bool{"router": false, "edge": true} {
		raw, err := os.ReadFile("../../distros/" + name + "/profile.yaml")
		if err != nil {
			t.Fatal(err)
		}
		var p distro.Profile
		if err := yaml.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if got := runsKubelet(p.Spec.Roles["controlPlane"]); got != want {
			t.Errorf("%s control plane runs a kubelet: %v, want %v", name, got, want)
		}
	}
}
