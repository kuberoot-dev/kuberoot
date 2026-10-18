package main

import (
	"log"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kuberoot-dev/kuberoot/pkg/upgrade"
)

// probationDeadline is how long a slot on probation may take to be declared
// good. The node API usually decides within minutes; this covers the case
// where it never even starts.
const probationDeadline = 8 * time.Minute

// probationWatchdog reboots a node whose boot slot is still on probation after
// the deadline, so systemd-boot spends an attempt and eventually falls back.
func probationWatchdog() {
	if upgrade.CurrentSlot() == "" || !onProbation() {
		return
	}
	log.Printf("slot %s is on probation; rebooting unless it is good within %s", upgrade.CurrentSlot(), probationDeadline)
	time.Sleep(probationDeadline)
	if onProbation() {
		log.Printf("slot %s did not become good in time", upgrade.CurrentSlot())
		shutdown(unix.LINUX_REBOOT_CMD_RESTART, "probation deadline")
	}
}

func onProbation() bool {
	entries, err := upgrade.Entries()
	if err != nil {
		// The EFI partition may be busy with the node API; when in doubt, do not reboot.
		return false
	}
	for _, e := range entries {
		if e.Booted {
			// Bad: systemd-boot spent the last attempt on this boot. It is
			// still on probation, and the reboot lands on the other slot.
			return e.State != upgrade.StateGood
		}
	}
	return false
}
