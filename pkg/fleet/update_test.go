package fleet

import (
	"strings"
	"testing"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/fleet/v1alpha1"
	nodev1 "github.com/kuberoot-dev/kuberoot/pkg/apis/node/v1alpha1"
)

var v2 = v1.Release{Version: "0.2.0", URL: "http://releases/0.2.0.tar", Sha256: strings.Repeat("ab", 32)}

func entry(slot, release, state string, booted bool) nodev1.BootEntry {
	e := nodev1.BootEntry{Status: nodev1.BootEntryStatus{Release: release, State: state, Booted: booted}}
	e.Name = "slot-" + slot
	return e
}

func upgrade(name, phase string) nodev1.Upgrade {
	u := nodev1.Upgrade{Status: nodev1.UpgradeStatus{Phase: phase}}
	u.Name = name
	return u
}

func TestDecide(t *testing.T) {
	onV1 := []nodev1.BootEntry{entry("a", "0.1.0", "Good", true)}
	attempt := func(phase string) *v1.UpdateStatus { return &v1.UpdateStatus{Version: "0.2.0", Phase: phase} }
	for _, c := range []struct {
		name     string
		prev     *v1.UpdateStatus
		entries  []nodev1.BootEntry
		upgrades []nodev1.Upgrade
		phase    string
		create   bool
	}{
		{"nothing yet: stage it", nil, onV1, nil, v1.UpdateDownloading, true},
		{"downloading", attempt(v1.UpdateDownloading), onV1, []nodev1.Upgrade{upgrade(UpgradeName(v2), "Writing")}, v1.UpdateDownloading, false},
		{"staging failed", attempt(v1.UpdateDownloading), onV1, []nodev1.Upgrade{upgrade(UpgradeName(v2), "Failed")}, v1.UpdateFailed, false},
		{"another upgrade is not ours", nil, onV1, []nodev1.Upgrade{upgrade("by-hand", "Staged")}, v1.UpdateDownloading, true},
		{"staged, before the reboot", attempt(v1.UpdateDownloading),
			[]nodev1.BootEntry{entry("a", "0.1.0", "Good", true), entry("b", "0.2.0", "Trying", false)}, nil, v1.UpdateRebooting, false},
		{"booted on probation", attempt(v1.UpdateRebooting),
			[]nodev1.BootEntry{entry("a", "0.1.0", "Good", false), entry("b", "0.2.0", "Trying", true)}, nil, v1.UpdateVerifying, false},
		{"booted and good", attempt(v1.UpdateVerifying),
			[]nodev1.BootEntry{entry("a", "0.1.0", "Good", false), entry("b", "0.2.0", "Good", true)}, nil, v1.UpdateUpdated, false},
		{"ran out of attempts", attempt(v1.UpdateRebooting),
			[]nodev1.BootEntry{entry("a", "0.1.0", "Good", true), entry("b", "0.2.0", "Bad", false)}, nil, v1.UpdateRolledBack, false},
		{"rolled back, seen by a center that just started", nil,
			[]nodev1.BootEntry{entry("a", "0.1.0", "Good", true), entry("b", "0.2.0", "Bad", false)}, nil, v1.UpdateRolledBack, false},
		{"went back to the other slot by hand", attempt(v1.UpdateVerifying),
			[]nodev1.BootEntry{entry("a", "0.1.0", "Good", true), entry("b", "0.2.0", "Good", false)}, nil, v1.UpdateRolledBack, false},
		{"an old good slot of the release, never tried: stage it again", nil,
			[]nodev1.BootEntry{entry("a", "0.2.0", "Good", false), entry("b", "0.3.0", "Good", true)}, nil, v1.UpdateDownloading, true},
		{"a failure is final", attempt(v1.UpdateRolledBack), onV1, nil, v1.UpdateRolledBack, false},
	} {
		got := decide(v2, c.prev, c.entries, c.upgrades)
		if got.Phase != c.phase || got.Create != c.create {
			t.Errorf("%s: %s create=%v (%s), want %s create=%v", c.name, got.Phase, got.Create, got.Message, c.phase, c.create)
		}
	}
}

func TestUpgradeName(t *testing.T) {
	if n := UpgradeName(v2); n != "fleet-abababababab" {
		t.Errorf("UpgradeName = %s", n)
	}
}
