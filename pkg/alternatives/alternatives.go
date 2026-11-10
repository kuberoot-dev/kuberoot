// Package alternatives keeps which of a service's interchangeable builds a
// node runs (distro.Alternatives): chosen at random on the node's first boot,
// kept on the state partition, and switched to the next one when the chosen
// build keeps crashing.
package alternatives

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
)

// Dir keeps one file per service with alternatives.
const Dir = "/var/lib/kuberoot/alternatives"

// LabelPrefix names the Node labels that show what a node chose:
// alternative.kuberoot.dev/<service>=<choice>.
const LabelPrefix = "alternative.kuberoot.dev/"

// State is what a node keeps about one service.
type State struct {
	Choice   string    `json:"choice"`
	ChosenAt time.Time `json:"chosenAt"`
	// SwitchedAt and SwitchedFrom record the last switch, if any.
	SwitchedAt   time.Time `json:"switchedAt,omitzero"`
	SwitchedFrom string    `json:"switchedFrom,omitempty"`
}

func path(dir, service string) string { return filepath.Join(dir, service+".json") }

// Random picks one of n, uniformly.
func Random(n int) int {
	i, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err) // the kernel's random source does not fail once it is ready
	}
	return int(i.Int64())
}

// Choose returns the choice the node keeps for service. A node with none, or
// one that keeps a choice the profile no longer offers or a file it cannot
// read, picks one at random and keeps it; why says what happened then, and is
// empty when the kept choice stands.
func Choose(dir, service string, choices []string, pick func(int) int, now time.Time) (st State, why string, err error) {
	raw, err := os.ReadFile(path(dir, service))
	switch {
	case err == nil:
		if json.Unmarshal(raw, &st) == nil && slices.Contains(choices, st.Choice) {
			return st, "", nil
		}
		why = fmt.Sprintf("kept choice %q is not one of %v; choosing again", st.Choice, choices)
		if st.Choice == "" {
			why = "kept choice is unreadable; choosing again"
		}
	case errors.Is(err, fs.ErrNotExist):
		why = "first boot"
	default:
		return State{}, "", err
	}
	st = State{Choice: choices[pick(len(choices))], ChosenAt: now.UTC()}
	return st, why, Save(dir, service, st)
}

// Save keeps st for service.
func Save(dir, service string, st State) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path(dir, service), append(raw, '\n'), 0o644)
}

// Next is the choice after current, in the profile's order, wrapping around.
func Next(choices []string, current string) string {
	i := slices.Index(choices, current)
	return choices[(i+1)%len(choices)]
}

// Crashes counts one service's crashes during a boot and decides when it
// switches. Crashes, Within and Cooldown come from the profile.
type Crashes struct {
	Crashes  int
	Within   time.Duration
	Cooldown time.Duration

	times []time.Time
	// switched: the service switched during this boot already.
	switched bool
}

// Record notes a crash at now and says whether the service switches now. A
// switch is held back, with the reason, when it already switched this boot,
// when the last switch was less than Cooldown ago, or when blocked, asked
// only then, gives a reason (the slot is on probation). A switch starts the
// count anew.
func (c *Crashes) Record(now time.Time, st State, blocked func() string) (switchNow bool, why string) {
	kept := c.times[:0]
	for _, t := range c.times {
		if now.Sub(t) < c.Within {
			kept = append(kept, t)
		}
	}
	c.times = append(kept, now)
	count := fmt.Sprintf("%d of %d crashes within %s", len(c.times), c.Crashes, c.Within)
	switch {
	case len(c.times) < c.Crashes:
		return false, count
	case c.switched:
		return false, count + "; it switched once this boot already"
	case !st.SwitchedAt.IsZero() && now.Sub(st.SwitchedAt) < c.Cooldown:
		return false, fmt.Sprintf("%s; it switched %s ago, less than %s", count, now.Sub(st.SwitchedAt).Round(time.Second), c.Cooldown)
	}
	if reason := blocked(); reason != "" {
		return false, count + "; " + reason
	}
	c.switched, c.times = true, nil
	return true, count
}
