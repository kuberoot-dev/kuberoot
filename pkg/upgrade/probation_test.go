package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
)

func TestAssessment(t *testing.T) {
	start := time.Unix(1000, 0)
	running := []supervisor.ServiceStatus{{Name: "kubelet", State: supervisor.StateRunning}}
	t.Run("third unrequested exit fails even if Ready", func(t *testing.T) {
		a := Assessment{Start: start}
		s := []supervisor.ServiceStatus{{Name: "broken", State: supervisor.StateRunning, UnrequestedExits: 3, Restarts: 3, LastExit: "code 1"}}
		good, why := a.Observe(start.Add(4*time.Second), true, s)
		if good || !strings.Contains(why, "broken: 3 unrequested exits; last exit: code 1") {
			t.Fatalf("%v %q", good, why)
		}
		if WatchdogFailure(4*time.Second, s) != why {
			t.Fatal("watchdog disagrees")
		}
	})
	t.Run("slow start and two transient exits recover", func(t *testing.T) {
		a := Assessment{Start: start}
		s := []supervisor.ServiceStatus{{Name: "kubelet", State: supervisor.StateWaiting, Restarts: 2, UnrequestedExits: 2}}
		if good, why := a.Observe(start.Add(4*time.Minute), false, s); good || why != "" {
			t.Fatalf("%v %q", good, why)
		}
		s[0].State = supervisor.StateRunning
		for _, seconds := range []int{245, 274, 275} {
			good, why := a.Observe(start.Add(time.Duration(seconds)*time.Second), true, s)
			if good != (seconds == 275) || why != "" {
				t.Fatalf("at %d: %v %q", seconds, good, why)
			}
		}
	})
	t.Run("requested restarts reset healthy window but do not fail", func(t *testing.T) {
		a := Assessment{Start: start}
		s := append([]supervisor.ServiceStatus(nil), running...)
		a.Observe(start, true, s)
		s[0].Restarts = 5
		if good, why := a.Observe(start.Add(30*time.Second), true, s); good || why != "" {
			t.Fatalf("%v %q", good, why)
		}
		a.Observe(start.Add(35*time.Second), true, s)
		if good, why := a.Observe(start.Add(65*time.Second), true, s); !good || why != "" {
			t.Fatalf("%v %q", good, why)
		}
	})
	t.Run("never Ready reaches exact ceiling", func(t *testing.T) {
		a := Assessment{Start: start}
		for _, elapsed := range []time.Duration{ProbationDeadline - time.Nanosecond, ProbationDeadline} {
			good, why := a.Observe(start.Add(elapsed), false, running)
			if good || (why != "") != (elapsed == ProbationDeadline) {
				t.Fatalf("at %v: %v %q", elapsed, good, why)
			}
		}
	})
	t.Run("watchdog leaves time for readiness assessor", func(t *testing.T) {
		if WatchdogFailure(ProbationDeadline, running) != "" {
			t.Fatal("watchdog preempted assessment")
		}
		if WatchdogFailure(WatchdogDeadline-time.Nanosecond, running) != "" {
			t.Fatal("early timeout")
		}
		if WatchdogFailure(WatchdogDeadline, running) == "" {
			t.Fatal("missing backstop")
		}
	})
}

func TestFailedAttemptPersistence(t *testing.T) {
	for _, file := range []string{"kuberoot-b+2-1.conf", "kuberoot-b+0-3.conf"} {
		t.Run(file, func(t *testing.T) {
			dir := t.TempDir()
			writeEntry(t, dir, "kuberoot-a.conf", "old", "1")
			writeEntry(t, dir, file, "new", "2")
			reason := "service broken: 3 unrequested exits; last exit: code 1\nsecond line"
			reboot, err := failAttemptIn(dir, "b", reason)
			if err != nil || !reboot {
				t.Fatalf("%v %v", reboot, err)
			}
			entries, err := readEntriesIn(dir, "a") // after reboot, reason is still visible
			if err != nil || !strings.Contains(entries[0].Failure, reason) {
				t.Fatalf("%+v %v", entries, err)
			}
			if entries[0].File != file {
				t.Fatal("failure spent an extra attempt")
			}
			if file == "kuberoot-b+0-3.conf" && (entries[0].State != StateBad || !entries[1].Default) {
				t.Fatalf("last attempt: %+v", entries)
			}
			if _, err := markGoodIn(dir, "b"); err == nil {
				t.Fatal("failed attempt was blessed by racing assessor")
			}
			if _, err := failAttemptIn(dir, "b", "watchdog deadline"); err != nil {
				t.Fatal(err)
			}
			entries, _ = readEntriesIn(dir, "b")
			if !strings.Contains(entries[0].Failure, reason) {
				t.Fatal("watchdog overwrote crash evidence")
			}
			if file == "kuberoot-b+2-1.conf" {
				if err := os.Rename(filepath.Join(dir, file), filepath.Join(dir, "kuberoot-b+1-2.conf")); err != nil {
					t.Fatal(err)
				}
				if good, err := markGoodIn(dir, "b"); err != nil || !good {
					t.Fatalf("next attempt cannot recover: %v %v", good, err)
				}
			}
		})
	}
}

func TestFailedAttemptDoesNotRebootGoodOrWithoutFallback(t *testing.T) {
	for _, file := range []string{"kuberoot-b.conf", "kuberoot-b+0-3.conf"} {
		dir := t.TempDir()
		writeEntry(t, dir, file, "new", "2")
		reboot, err := failAttemptIn(dir, "b", "failure")
		if reboot || err != nil {
			t.Fatalf("%s: %v %v", file, reboot, err)
		}
		entries, _ := readEntriesIn(dir, "b")
		if (entries[0].Failure != "") != (entries[0].State != StateGood) {
			t.Fatalf("%+v", entries)
		}
	}
}

func TestFailureLogIsBoundedAndSurvivesEncoding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 4096)+"\nlast line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	statuses := []supervisor.ServiceStatus{{Name: "broken", UnrequestedExits: 3, LogFile: path}}
	reason := CrashLoop(statuses)
	details := FailureDetails(reason, statuses)
	if len(details) > len(reason)+len("; log tail: ")+2048 || !strings.HasSuffix(details, "last line\n") {
		t.Fatalf("bad tail: len=%d", len(details))
	}
	writeEntry(t, dir, "kuberoot-b+2-1.conf", "new", "2")
	if _, err := failAttemptIn(dir, "b", details); err != nil {
		t.Fatal(err)
	}
	entries, err := readEntriesIn(dir, "b")
	if err != nil || !strings.Contains(entries[0].Failure, details) {
		t.Fatalf("lost logs: %+v %v", entries, err)
	}
	statuses[0].LogFile = filepath.Join(dir, "missing")
	if FailureDetails(reason, statuses) != reason {
		t.Fatal("missing log changed reason")
	}
}

func TestPreferStartsFreshAssessment(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "old", "1")
	writeEntry(t, dir, "kuberoot-b+2-1.conf", "new", "2")
	if _, err := failAttemptIn(dir, "b", "crash"); err != nil {
		t.Fatal(err)
	}
	if err := preferIn(dir, "b", "a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "kuberoot-b+3.conf"), filepath.Join(dir, "kuberoot-b+2-1.conf")); err != nil {
		t.Fatal(err)
	}
	if good, err := markGoodIn(dir, "b"); err != nil || !good {
		t.Fatalf("%v %v", good, err)
	}
}
