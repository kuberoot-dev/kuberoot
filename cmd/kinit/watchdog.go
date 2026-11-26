package main

import (
	"log"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/upgrade"
	"golang.org/x/sys/unix"
)

// Watch for crashes even when the node API cannot start. The eight-minute
// watchdog remains a backstop for a missing assessor, not its readiness timer.
func probationWatchdog() {
	if upgrade.CurrentSlot() == "" {
		return
	}
	start := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for now := range ticker.C {
		statuses := serviceStatuses()
		reason := upgrade.WatchdogFailure(now.Sub(start), statuses)
		if reason == "" {
			continue
		}
		reboot, err := upgrade.FailAttempt(upgrade.FailureDetails(reason, statuses))
		if err != nil {
			log.Printf("boot watchdog: %v", err)
			continue
		}
		if reboot {
			shutdown(unix.LINUX_REBOOT_CMD_RESTART, reason)
		}
		return // Good, or no known-good fallback: do not reboot forever.
	}
}

// Also used by alternatives: probation is resolved by slot rollback, not a
// per-service alternative switch.
func onProbation() bool {
	entries, err := upgrade.Entries()
	if err != nil {
		return false
	}
	on, _ := upgrade.Probation(entries)
	return on
}
