package main

import (
	"sigs.k8s.io/yaml"
	"strings"
	"testing"
)

func TestTimingCmdline(t *testing.T) {
	for _, tc := range []struct {
		args string
		want int
	}{
		{"", 1}, {"kuberoot.profile=emulator", 60},
		{"kuberoot.profile=emulator kuberoot.timescale=1", 1},
		{"kuberoot.timescale=7 kuberoot.profile=emulator", 7},
		{"kuberoot.timescale=0", 1}, {"kuberoot.timescale=-1", 1},
		{"kuberoot.timescale=1001", 1}, {"kuberoot.timescale=NaN", 1},
		{"kuberoot.profile=emulator kuberoot.timescale=bad", 60},
	} {
		if got := parseCmdline(tc.args).timeScale(); got != tc.want {
			t.Errorf("%q: %d != %d", tc.args, got, tc.want)
		}
	}
}

func TestTimingConfig(t *testing.T) {
	cfg := "nodeStatusUpdateFrequency: 3s\nnodeLeaseDurationSeconds: 50\n"
	out, err := scaledKubeletConfig(cfg, 60)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["nodeStatusUpdateFrequency"] != "3m0s" || fields["nodeLeaseDurationSeconds"] != float64(3000) || fields["runtimeRequestTimeout"] != "2h0m0s" {
		t.Fatal(fields)
	}
	if out, err := scaledKubeletConfig(cfg, 1); err != nil || out != cfg {
		t.Fatal("default configuration changed")
	}
}

func TestTimingPreservesUserProbes(t *testing.T) {
	raw := "kind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n      - name: dns\n        readinessProbe:\n          httpGet: {path: /ready, port: 8181}\n"
	out, err := scaledAddon(raw, 60)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "timeoutSeconds: 60") || !strings.Contains(out, "periodSeconds: 600") {
		t.Fatal(out)
	}
	if got, _ := scaledAddon(raw, 1); got != raw {
		t.Fatal("default probes changed")
	}
}

func TestTimingServices(t *testing.T) {
	for _, distro := range []string{"edge", "slim"} {
		p, err := loadProfileFile("../../distros/" + distro + "/profile.yaml")
		if err != nil {
			t.Fatal(err)
		}
		services, err := renderServices(p.Spec.Roles[roleControlPlane], edgeFacts(false))
		if err != nil {
			t.Fatal(err)
		}
		scaled := scaleServices(services, 60)
		for _, svc := range scaled {
			if svc.name == "kube-apiserver" && !strings.Contains(strings.Join(svc.args, " "), "--request-timeout=1h0m0s") {
				t.Fatal(svc.args)
			}
		}
	}
}
