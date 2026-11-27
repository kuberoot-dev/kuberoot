package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
)

func zoned(link string) Config {
	return Config{Zones: []v1.FirewallZone{{ObjectMeta: named("mgmt"), Spec: v1.FirewallZoneSpec{Links: []string{link}, Management: true, Input: "Drop"}}}}
}

func bootRuleset(t *testing.T, c *Controller) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(c.StateDir, BootRulesetFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBootRulesetIsTheConfirmedOne(t *testing.T) {
	c := &Controller{StateDir: t.TempDir()}
	now := time.Date(2026, 11, 27, 2, 0, 0, 0, time.UTC)
	guard := &v1.Safeguard{ObjectMeta: named("default"), Spec: v1.SafeguardSpec{ConfirmWithin: metav1.Duration{Duration: time.Minute}}}
	good, trial := zoned("eth0"), zoned("eth9")

	c.trial.decide(good, Revision(good), guard, now)
	if err := c.keepBootRuleset(); err != nil {
		t.Fatal(err)
	}
	if got := bootRuleset(t, c); got != Ruleset(good) || !strings.Contains(got, `iifname { "eth0" } tcp dport { 6443, 50000 } accept`) {
		t.Fatalf("boot ruleset is not the confirmed configuration's:\n%s", got)
	}
	// A configuration on trial does not reach the boot ruleset: a reboot
	// during the trial comes back to the confirmed configuration.
	c.trial.decide(trial, Revision(trial), guard, now.Add(10*time.Second))
	if err := c.keepBootRuleset(); err != nil {
		t.Fatal(err)
	}
	if got := bootRuleset(t, c); got != Ruleset(good) {
		t.Fatalf("a trial reached the boot ruleset:\n%s", got)
	}
	// Once confirmed, it does.
	guard.Spec.Confirm = Revision(trial)
	c.trial.decide(trial, Revision(trial), guard, now.Add(20*time.Second))
	if err := c.keepBootRuleset(); err != nil {
		t.Fatal(err)
	}
	if got := bootRuleset(t, c); got != Ruleset(trial) {
		t.Fatalf("the confirmed trial did not reach the boot ruleset:\n%s", got)
	}
}

func TestBootRulesetOfAnUnconfiguredRouterFiltersNothing(t *testing.T) {
	c := &Controller{StateDir: t.TempDir()}
	if err := c.keepBootRuleset(); err != nil {
		t.Fatal(err)
	}
	if got := bootRuleset(t, c); strings.Contains(got, "policy drop") {
		t.Fatalf("a router without zones would boot closed, before its first configuration:\n%s", got)
	}
}

func TestRouterProfileLoadsTheBootRuleset(t *testing.T) {
	raw, err := os.ReadFile("../../distros/router/profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bootRuleset: /var/lib/kuberoot/router/" + BootRulesetFile; !strings.Contains(string(raw), want) {
		t.Fatalf("the router profile does not load the file kuberoot-router keeps (%s)", want)
	}
}
