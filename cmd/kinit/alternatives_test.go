package main

import (
	"strings"
	"testing"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/alternatives"
	"github.com/kuberoot-dev/kuberoot/pkg/distro"
)

const alternativesProfile = `apiVersion: kuberoot.dev/v1alpha1
kind: Distribution
metadata: {name: alt}
spec:
  roles:
    controlPlane:
      generators: [node]
      services:
      - name: api
        args: [/usr/bin/api, '--name={{.Node.Name}}']
        alternatives:
          choices:
          - name: new
            program: /usr/bin/api.new
            args: [--emulate=old]
          - name: old
            program: /usr/bin/api.old
          switch: {crashes: 2, within: 10m, cooldown: 24h}
`

func renderAlternativesProfile(t *testing.T) service {
	t.Helper()
	p, err := distro.Parse([]byte(alternativesProfile), "test")
	if err != nil {
		t.Fatal(err)
	}
	services, err := renderServices(p.Spec.Roles[roleControlPlane], edgeFacts(false))
	if err != nil || len(services) != 1 || services[0].alt == nil {
		t.Fatalf("services %+v, %v", services, err)
	}
	return services[0]
}

func TestAlternativesRenderTheirOwnCommandLines(t *testing.T) {
	s := renderAlternativesProfile(t)
	if got := strings.Join(s.alt.args["new"], " "); got != "/usr/bin/api.new --name=node-1 --emulate=old" {
		t.Errorf("new: %s", got)
	}
	if got := strings.Join(s.alt.args["old"], " "); got != "/usr/bin/api.old --name=node-1" {
		t.Errorf("old: %s", got)
	}
}

func TestAlternativeSwitchesAfterCrashesAndIsKept(t *testing.T) {
	alternativesDir, bootCrashes = t.TempDir(), map[string]*alternatives.Crashes{}
	defer func() { alternativesDir = alternatives.Dir }()
	s := renderAlternativesProfile(t)
	choice := s.alt.choose(s.name)
	if choice != "new" && choice != "old" {
		t.Fatalf("chose %q", choice)
	}
	if again := s.alt.choose(s.name); again != choice {
		t.Fatalf("second boot chose %s after %s", again, choice)
	}
	now := time.Now()
	if got := s.alt.crashed(s.name, choice, now); got != choice {
		t.Fatalf("switched after one crash of two")
	}
	switched := s.alt.crashed(s.name, choice, now.Add(time.Second))
	if switched == choice {
		t.Fatalf("no switch after two crashes")
	}
	if kept := s.alt.choose(s.name); kept != switched {
		t.Errorf("the switch to %s was not kept: %s", switched, kept)
	}
	// Once per boot: two more crashes switch nothing.
	s.alt.crashed(s.name, switched, now.Add(2*time.Second))
	if got := s.alt.crashed(s.name, switched, now.Add(3*time.Second)); got != switched {
		t.Errorf("switched twice in one boot, to %s", got)
	}
}

func TestProfileRejectsBadAlternatives(t *testing.T) {
	for _, bad := range []string{
		strings.Replace(alternativesProfile, "name: old", "name: new", 1),
		strings.Replace(alternativesProfile, "name: old", "name: 'old one'", 1),
		strings.Replace(alternativesProfile, "within: 10m", "within: soon", 1),
		strings.Replace(alternativesProfile, "crashes: 2", "crashes: 0", 1),
	} {
		if _, err := distro.Parse([]byte(bad), "test"); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}
