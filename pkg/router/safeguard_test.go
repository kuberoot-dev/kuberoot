package router

import (
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
)

func withRoute(dst string) Config {
	return Config{Routes: []v1.Route{{ObjectMeta: named("r"), Spec: v1.RouteSpec{Destination: dst, Link: "eth1"}}}}
}

func TestRevisionFollowsTheSpecs(t *testing.T) {
	a, b := withRoute("10.0.0.0/8"), withRoute("10.0.0.0/8")
	b.Routes[0].Status.ObservedGeneration = 7 // status is not configuration
	if Revision(a) != Revision(b) {
		t.Error("the same specs give different revisions")
	}
	if Revision(a) == Revision(withRoute("10.1.0.0/16")) {
		t.Error("different specs give the same revision")
	}
}

func TestSafeguardTrialConfirmAndRollback(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	guard := &v1.Safeguard{ObjectMeta: named("default"), Spec: v1.SafeguardSpec{ConfirmWithin: metav1.Duration{Duration: 2 * time.Minute}}}
	good, bad := withRoute("10.0.0.0/8"), withRoute("0.0.0.0/0")
	goodRev, badRev := Revision(good), Revision(bad)
	var tr trial

	// The first configuration is the baseline.
	if _, rev, changed := tr.decide(good, goodRev, guard, now); rev != goodRev || !changed {
		t.Fatalf("baseline: running %s, changed %v", rev, changed)
	}
	// A change runs on trial.
	if _, rev, _ := tr.decide(bad, badRev, guard, now.Add(time.Minute)); rev != badRev || tr.Pending != badRev {
		t.Fatalf("trial: running %s, pending %s", rev, tr.Pending)
	}
	// Out of time: back to the confirmed one, and it stays back.
	if c, rev, _ := tr.decide(bad, badRev, guard, now.Add(3*time.Minute+time.Second)); rev != goodRev || Revision(c) != goodRev || tr.RolledBack != badRev {
		t.Fatalf("rollback: running %s, rolled back %s", rev, tr.RolledBack)
	}
	if _, rev, _ := tr.decide(bad, badRev, guard, now.Add(time.Hour)); rev != goodRev {
		t.Fatalf("the undone revision came back: running %s", rev)
	}
	// Changing the resources again starts a new trial; confirming makes it final.
	fixed := withRoute("192.168.0.0/16")
	fixedRev := Revision(fixed)
	if _, rev, _ := tr.decide(fixed, fixedRev, guard, now.Add(2*time.Hour)); rev != fixedRev || tr.Pending != fixedRev {
		t.Fatalf("new trial: running %s, pending %s", rev, tr.Pending)
	}
	guard.Spec.Confirm = fixedRev
	_, rev, changed := tr.decide(fixed, fixedRev, guard, now.Add(2*time.Hour+time.Minute))
	if rev != fixedRev || !changed || tr.ConfirmedRev != fixedRev || tr.Pending != "" || tr.RolledBack != "" {
		t.Fatalf("confirm: running %s, confirmed %s, pending %q", rev, tr.ConfirmedRev, tr.Pending)
	}

	// The confirmed configuration survives a restart of the controller.
	path := filepath.Join(t.TempDir(), "confirmed.json")
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	var again trial
	again.Load(path)
	if again.ConfirmedRev != fixedRev || Revision(again.Confirmed) != fixedRev {
		t.Errorf("after a restart: confirmed %s", again.ConfirmedRev)
	}
}

func TestWithoutSafeguardEveryChangeIsFinal(t *testing.T) {
	var tr trial
	a, b := withRoute("10.0.0.0/8"), withRoute("10.1.0.0/16")
	tr.decide(a, Revision(a), nil, time.Now())
	if _, rev, changed := tr.decide(b, Revision(b), nil, time.Now()); rev != Revision(b) || !changed || tr.Pending != "" {
		t.Errorf("running %s, changed %v, pending %q", rev, changed, tr.Pending)
	}
}

func TestRollbackSurvivesARestart(t *testing.T) {
	now := time.Now()
	guard := &v1.Safeguard{ObjectMeta: named("default"), Spec: v1.SafeguardSpec{ConfirmWithin: metav1.Duration{Duration: time.Minute}}}
	good, bad := withRoute("10.0.0.0/8"), withRoute("0.0.0.0/0")
	var tr trial
	tr.decide(good, Revision(good), guard, now)
	tr.decide(bad, Revision(bad), guard, now)
	if _, _, save := tr.decide(bad, Revision(bad), guard, now.Add(2*time.Minute)); !save {
		t.Fatal("a rollback is not kept on disk")
	}
	path := filepath.Join(t.TempDir(), "confirmed.json")
	if err := tr.Save(path); err != nil {
		t.Fatal(err)
	}
	var again trial
	again.Load(path)
	if _, rev, _ := again.decide(bad, Revision(bad), guard, now.Add(3*time.Minute)); rev != Revision(good) {
		t.Errorf("after a restart the undone revision runs again: %s", rev)
	}
}
