package main

import (
	"log"
	"sync"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/alternatives"
	"github.com/kuberoot-dev/kuberoot/pkg/distro"
	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
)

// alternativesDir is where the node keeps its choices; tests move it.
var alternativesDir = alternatives.Dir

// serviceAlternatives are the rendered alternatives of one service.
type serviceAlternatives struct {
	names  []string
	args   map[string][]string
	policy *distro.AlternativeSwitch // nil: the choice never changes
}

// bootCrashes counts crashes per service for the whole boot, across role
// switches: "once per boot" means once per kinit.
var (
	bootCrashesMu sync.Mutex
	bootCrashes   = map[string]*alternatives.Crashes{}
)

// choose returns the alternative the node runs for the service, choosing
// it on the node's first boot, and shows it in the service's status.
func (a *serviceAlternatives) choose(service string) string {
	st, why, err := alternatives.Choose(alternativesDir, service, a.names, alternatives.Random, time.Now())
	switch {
	case st.Choice == "":
		// The state partition cannot be read: run the first, keep nothing.
		st.Choice = a.names[0]
		log.Printf("%s: alternatives: %v; running %s this boot", service, err, st.Choice)
	case err != nil:
		log.Printf("%s: chose %s (%s), but could not keep the choice: %v", service, st.Choice, why, err)
	case why != "":
		log.Printf("%s: chose %s at random of %v (%s); later boots keep it", service, st.Choice, a.names, why)
	default:
		log.Printf("%s: runs %s, chosen %s", service, st.Choice, st.ChosenAt.Format(time.RFC3339))
	}
	setStatus(service, func(s *supervisor.ServiceStatus) { s.Alternative = st.Choice })
	return st.Choice
}

// crashed counts a crash of the service running choice and returns the
// choice to run next: the next alternative once the profile's switch is due.
func (a *serviceAlternatives) crashed(service, choice string, now time.Time) string {
	if a.policy == nil {
		return choice
	}
	within, cooldown, err := a.policy.Durations()
	if err != nil { // checked when the profile was loaded
		return choice
	}
	bootCrashesMu.Lock()
	c, ok := bootCrashes[service]
	if !ok {
		c = &alternatives.Crashes{Crashes: a.policy.Crashes, Within: within, Cooldown: cooldown}
		bootCrashes[service] = c
	}
	bootCrashesMu.Unlock()

	st, _, _ := alternatives.Choose(alternativesDir, service, a.names, alternatives.Random, now)
	switchNow, why := c.Record(now, st, func() string {
		if onProbation() {
			return "the slot this node booted is on probation: a release that keeps failing is rolled back to the other slot instead"
		}
		return ""
	})
	if !switchNow {
		log.Printf("%s: crashed on %s: %s; no switch", service, choice, why)
		return choice
	}
	to := alternatives.Next(a.names, choice)
	next := alternatives.State{Choice: to, ChosenAt: st.ChosenAt, SwitchedAt: now.UTC(), SwitchedFrom: choice}
	if err := alternatives.Save(alternativesDir, service, next); err != nil {
		log.Printf("%s: crashed on %s: %s, but the switch to %s cannot be kept: %v; no switch", service, choice, why, to, err)
		return choice
	}
	log.Printf("%s: crashed on %s: %s; switched to %s", service, choice, why, to)
	setStatus(service, func(s *supervisor.ServiceStatus) { s.Alternative = to })
	return to
}
