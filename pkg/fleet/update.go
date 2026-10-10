package fleet

import (
	"fmt"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/fleet/v1alpha1"
	nodev1 "github.com/kuberoot-dev/kuberoot/pkg/apis/node/v1alpha1"
)

// UpgradeName is the Upgrade the center creates on a node for a release.
func UpgradeName(r v1.Release) string {
	sum := r.Sha256
	if len(sum) > 12 {
		sum = sum[:12]
	}
	return "fleet-" + sum
}

// step is what a node's update is at, and whether to start it.
type step struct {
	Phase, Message string
	// Create: stage the release on the node now.
	Create bool
}

func terminal(phase string) bool {
	return phase == v1.UpdateUpdated || phase == v1.UpdateRolledBack || phase == v1.UpdateFailed
}

// failure: the release does not run on the node, and will not be tried again.
func failure(phase string) bool {
	return phase == v1.UpdateRolledBack || phase == v1.UpdateFailed
}

// decide reads where a node is on its way to a release from the node's
// boot entries and upgrades, and what the center last saw (prev). The boot
// entries outlive reboots, so it holds across them and across restarts of
// the center: a slot of the release that ran out of attempts means the node
// rolled the release back.
func decide(desired v1.Release, prev *v1.UpdateStatus, entries []nodev1.BootEntry, upgrades []nodev1.Upgrade) step {
	attempted := prev != nil && prev.Version == desired.Version && prev.Phase != ""
	if attempted && failure(prev.Phase) {
		return step{Phase: prev.Phase, Message: prev.Message}
	}
	booted := "?"
	bootedRelease := ""
	for _, e := range entries {
		if e.Status.Booted {
			booted, bootedRelease = slotOf(e.Name), e.Status.Release
		}
	}
	for _, e := range entries {
		if e.Status.Release != desired.Version {
			continue
		}
		slot := slotOf(e.Name)
		switch {
		case e.Status.State == "Bad":
			return step{Phase: v1.UpdateRolledBack, Message: fmt.Sprintf(
				"slot %s with %s ran out of boot attempts; the node went back to slot %s with %s", slot, desired.Version, booted, bootedRelease)}
		case e.Status.Booted && e.Status.State == "Good":
			return step{Phase: v1.UpdateUpdated, Message: fmt.Sprintf("slot %s runs %s", slot, desired.Version)}
		case e.Status.Booted:
			return step{Phase: v1.UpdateVerifying, Message: fmt.Sprintf("slot %s on probation", slot)}
		case e.Status.State == "Trying":
			return step{Phase: v1.UpdateRebooting, Message: fmt.Sprintf("slot %s holds %s and boots next", slot, desired.Version)}
		case attempted && (prev.Phase == v1.UpdateRebooting || prev.Phase == v1.UpdateVerifying):
			// Good but not booted, after the center saw it staged: the
			// node went back to the other slot.
			return step{Phase: v1.UpdateRolledBack, Message: fmt.Sprintf(
				"the node went back from slot %s to slot %s with %s", slot, booted, bootedRelease)}
		}
	}
	for _, u := range upgrades {
		if u.Name != UpgradeName(desired) {
			continue
		}
		switch u.Status.Phase {
		case "Failed":
			return step{Phase: v1.UpdateFailed, Message: u.Status.Message}
		case "Staged":
			return step{Phase: v1.UpdateRebooting, Message: u.Status.Message}
		default:
			return step{Phase: v1.UpdateDownloading, Message: fmt.Sprintf("%s %d%%: %s", u.Status.Phase, u.Status.Progress, u.Status.Message)}
		}
	}
	return step{Phase: v1.UpdateDownloading, Message: "upgrade " + UpgradeName(desired) + " created", Create: true}
}

func slotOf(entry string) string {
	if len(entry) > len("slot-") {
		return entry[len("slot-"):]
	}
	return entry
}
