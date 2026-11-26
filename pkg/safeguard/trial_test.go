package safeguard

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHealthyTrialConfirmsItself(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	g := &Guard{ConfirmWithin: time.Minute, AutoConfirm: true}
	var tr Trial[string]
	tr.Decide("a", "a", g, "", now)
	if c, rev, _ := tr.Decide("b", "b", g, "", now.Add(10*time.Second)); c != "b" || rev != "b" || tr.Pending != "b" {
		t.Fatalf("not on trial: %s %s pending %q", c, rev, tr.Pending)
	}
	if _, rev, changed := tr.Decide("b", "b", g, "", now.Add(2*time.Minute)); rev != "b" || !changed || tr.ConfirmedRev != "b" {
		t.Fatalf("healthy trial not confirmed: %s", rev)
	}
}

func TestUnhealthyTrialUndoesAtOnce(t *testing.T) {
	now := time.Now()
	g := &Guard{ConfirmWithin: time.Hour, AutoConfirm: true}
	var tr Trial[string]
	tr.Decide("a", "a", g, "", now)
	tr.Decide("b", "b", g, "", now)
	c, rev, save := tr.Decide("b", "b", g, "device press-7 lost", now.Add(time.Second))
	if c != "a" || rev != "a" || !save || tr.RolledBack != "b" || tr.Why != "device press-7 lost" {
		t.Fatalf("broken change kept: %s %s, rolled back %q why %q", c, rev, tr.RolledBack, tr.Why)
	}
	// It stays undone, healthy or not, until the resources change.
	if _, rev, _ := tr.Decide("b", "b", g, "", now.Add(2*time.Hour)); rev != "a" {
		t.Fatalf("undone revision came back: %s", rev)
	}
	path := filepath.Join(t.TempDir(), "confirmed.json")
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	var again Trial[string]
	again.Load(path)
	if again.Confirmed != "a" || again.RolledBack != "b" || again.Why == "" {
		t.Fatalf("after a restart: %+v", again)
	}
}

func TestWithoutAutoConfirmOnlyConfirmKeepsAChange(t *testing.T) {
	now := time.Now()
	g := &Guard{ConfirmWithin: time.Minute}
	var tr Trial[string]
	tr.Decide("a", "a", g, "", now)
	tr.Decide("b", "b", g, "", now)
	if _, rev, _ := tr.Decide("b", "b", g, "", now.Add(2*time.Minute)); rev != "a" || tr.Why != "not confirmed in time" {
		t.Fatalf("unconfirmed change kept: %s (%s)", rev, tr.Why)
	}
	tr.Decide("c", "c", g, "", now.Add(3*time.Minute))
	g.Confirm = "c"
	if _, rev, _ := tr.Decide("c", "c", g, "", now.Add(3*time.Minute+time.Second)); rev != "c" || tr.ConfirmedRev != "c" {
		t.Fatalf("confirmed change not kept: %s", rev)
	}
}

func TestPendingDeadlineSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	guard := &Guard{ConfirmWithin: time.Minute}
	var tr Trial[string]
	tr.Decide("a", "a", guard, "", now)
	if _, _, save := tr.Decide("b", "b", guard, "", now); !save {
		t.Fatal("starting a trial must save its deadline")
	}
	path := filepath.Join(t.TempDir(), "confirmed.json")
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	var again Trial[string]
	again.Load(path)
	if _, rev, save := again.Decide("b", "b", guard, "", now.Add(30*time.Second)); rev != "b" || save || !again.Deadline.Equal(now.Add(time.Minute)) {
		t.Fatalf("restart changed the trial: %+v, save=%v", again, save)
	}
	if _, rev, save := again.Decide("b", "b", guard, "", now.Add(time.Minute)); rev != "a" || !save || again.RolledBack != "b" {
		t.Fatalf("restart extended the deadline: %+v", again)
	}
}

func TestReturningToBaselineClearsPersistedTrialAndRollback(t *testing.T) {
	for _, rolledBack := range []bool{false, true} {
		now := time.Now()
		guard := &Guard{ConfirmWithin: time.Minute}
		var tr Trial[string]
		tr.Decide("a", "a", guard, "", now)
		tr.Decide("b", "b", guard, "", now)
		if rolledBack {
			tr.Decide("b", "b", guard, "", now.Add(time.Minute))
		}
		if _, _, save := tr.Decide("a", "a", guard, "", now.Add(2*time.Minute)); !save {
			t.Fatalf("returning to baseline did not save cleared state (rolledBack=%v)", rolledBack)
		}
		path := filepath.Join(t.TempDir(), "confirmed.json")
		if err := tr.Save(path); err != nil {
			t.Fatal(err)
		}
		var again Trial[string]
		again.Load(path)
		if again.Pending != "" || !again.Deadline.IsZero() || again.RolledBack != "" || again.Why != "" {
			t.Fatalf("stale trial survived baseline restore: %+v", again)
		}
		if _, rev, save := again.Decide("b", "b", guard, "", now.Add(3*time.Minute)); rev != "b" || !save || again.Pending != "b" {
			t.Fatalf("old rollback suppressed a new trial: %+v", again)
		}
	}
}
