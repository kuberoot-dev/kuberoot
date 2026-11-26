package upgrade

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
)

const (
	ProbationWindow   = 30 * time.Second
	ProbationDeadline = 5 * time.Minute
	// WatchdogDeadline allows the node API time to start and assess readiness.
	WatchdogDeadline = 8 * time.Minute
)

// CrashLoop counts unrequested exits, including exit zero and spawn failures:
// supervised services are expected to stay up. Explicit restarts do not count.
func CrashLoop(statuses []supervisor.ServiceStatus) string {
	for _, s := range statuses {
		if s.UnrequestedExits >= 3 {
			return fmt.Sprintf("service %s: %d unrequested exits; last exit: %s", s.Name, s.UnrequestedExits, s.LastExit)
		}
	}
	return ""
}

// Assessment uses caller-supplied times, so no clock or sleeps enter the policy.
type Assessment struct {
	Start, HealthySince time.Time
	Restarts            int
}

func (a *Assessment) Observe(now time.Time, ready bool, statuses []supervisor.ServiceStatus) (good bool, failure string) {
	if why := CrashLoop(statuses); why != "" {
		return false, why
	}
	if now.Sub(a.Start) >= ProbationDeadline {
		return false, "node did not become healthy within 5m0s"
	}
	total := 0
	for _, s := range statuses {
		total += s.Restarts
		ready = ready && s.State == supervisor.StateRunning
	}
	if !ready || total != a.Restarts {
		a.HealthySince = time.Time{}
		a.Restarts = total
		return false, ""
	}
	if a.HealthySince.IsZero() {
		a.HealthySince = now
	}
	return now.Sub(a.HealthySince) >= ProbationWindow, ""
}

// WatchdogFailure is independent of the node API, including when it crashes.
func WatchdogFailure(elapsed time.Duration, statuses []supervisor.ServiceStatus) string {
	if why := CrashLoop(statuses); why != "" {
		return why
	}
	if elapsed >= WatchdogDeadline {
		return "kinit watchdog: slot did not become Good within 8m0s"
	}
	return ""
}

const failurePrefix = "# kuberoot-failure "

type attemptFailure struct {
	Attempt int
	Message string
}

// FailAttempt records the failure before reboot. systemd-boot already consumed
// this attempt when it booted: never decrement the counter a second time.
// Recheck Good and fallback under the same lock as MarkGood.
func FailAttempt(reason string) (reboot bool, err error) {
	err = withESP(func() error {
		var err error
		reboot, err = failAttemptIn(entriesDir, CurrentSlot(), reason)
		return err
	})
	return
}

func failAttemptIn(dir, current, reason string) (bool, error) {
	entries, err := readEntriesIn(dir, current)
	if err != nil {
		return false, err
	}
	on, fallback := Probation(entries)
	if !on {
		return false, nil
	}
	for _, e := range entries {
		if !e.Booted {
			continue
		}
		path := filepath.Join(dir, e.File)
		raw, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		lines := []string{}
		for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			if !strings.HasPrefix(line, failurePrefix) {
				lines = append(lines, line)
			}
		}
		// Preserve the first conclusive reason if both assessors noticed failure.
		if e.Failure != "" && e.failedAttempt == e.Done {
			return fallback, nil
		}
		data, _ := json.Marshal(attemptFailure{e.Done, fmt.Sprintf("attempt %d: %s", e.Done, reason)})
		lines = append(lines, failurePrefix+string(data))
		return fallback, atomicfile.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}
	return false, nil
}

// FailureDetails adds at most 2 KiB of the failing service's local log. Missing
// or non-regular logs never delay a failure or turn it into an assessment error.
func FailureDetails(reason string, statuses []supervisor.ServiceStatus) string {
	for _, st := range statuses {
		if st.UnrequestedExits < 3 {
			continue
		}
		if st.LogFile == "" {
			return reason
		}
		f, err := os.OpenFile(st.LogFile, os.O_RDONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			return reason
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return reason
		}
		buf := make([]byte, min(info.Size(), 2048))
		n, _ := f.ReadAt(buf, max(0, info.Size()-2048))
		if n > 0 {
			return fmt.Sprintf("%s; log tail: %s", reason, strings.ToValidUTF8(string(buf[:n]), "?"))
		}
		return reason
	}
	return reason
}
