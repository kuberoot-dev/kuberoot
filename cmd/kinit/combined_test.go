package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCombinedControlPlane(t *testing.T) {
	old := activeProfile
	t.Cleanup(func() { activeProfile = old })
	var err error
	activeProfile, err = loadProfileFile("../../distros/slim/profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	services, err := renderServices(activeProfile.Spec.Roles[roleControlPlane], edgeFacts(false))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "components.json")
	got, err := combineServicesFile(services, path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var components map[string][]string
	if err := json.Unmarshal(raw, &components); err != nil {
		t.Fatal(err)
	}
	if len(components) != 6 {
		t.Fatalf("components: %v", components)
	}
	for _, s := range services {
		if argv, ok := components[s.name]; ok && !reflect.DeepEqual(argv, s.args[1:]) {
			t.Errorf("%s flags changed", s.name)
		}
	}
	for _, s := range got {
		if _, ok := components[s.name]; ok {
			t.Errorf("%s still starts separately", s.name)
		}
	}
	if got[len(got)-1].name != "kuberoot-control" {
		t.Fatal("no shared process")
	}
	workers, err := renderServices(activeProfile.Spec.Roles[roleWorker], edgeFacts(false))
	if err != nil {
		t.Fatal(err)
	}
	got, err = combineServicesFile(workers, path)
	if err != nil || len(got) != len(workers) {
		t.Fatalf("worker changed: %v", err)
	}
	for i := range workers {
		if got[i].name != workers[i].name || (got[i].after == nil) != (workers[i].after == nil) {
			t.Fatal("worker barrier changed")
		}
	}
}

func TestSlimHasOnlyBundledAddons(t *testing.T) {
	p, err := loadProfileFile("../../distros/slim/profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cp := p.Spec.Roles[roleControlPlane]
	if !cp.Addons || cp.Packages {
		t.Fatal("slim must apply bundled addons without a package operator")
	}
	for _, s := range cp.Services {
		if s.Name == "kubepkg-operator" || s.Name == "kuberoot-intents" {
			t.Errorf("unneeded slim service %s", s.Name)
		}
	}
}

func TestControlMemoryBudget(t *testing.T) {
	for _, c := range []struct {
		ram  uint64
		want string
	}{
		{0, "GOMEMLIMIT=300MiB"}, {512 << 20, "GOMEMLIMIT=204MiB"},
		{640 << 20, "GOMEMLIMIT=256MiB"}, {768 << 20, "GOMEMLIMIT=300MiB"},
		{1024 << 20, "GOMEMLIMIT=300MiB"},
	} {
		got := controlMemoryEnv(c.ram)
		if len(got) != 2 || got[0] != "GOGC=25" || got[1] != c.want {
			t.Errorf("RAM %d: %v", c.ram, got)
		}
	}
}
