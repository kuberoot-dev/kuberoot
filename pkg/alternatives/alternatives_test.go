package alternatives

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func TestChooseKeepsTheFirstBootsChoice(t *testing.T) {
	dir := t.TempDir()
	choices := []string{"a", "b"}
	st, why, err := Choose(dir, "svc", choices, func(int) int { return 1 }, t0)
	if err != nil || st.Choice != "b" || why != "first boot" {
		t.Fatalf("first boot: %+v, %q, %v", st, why, err)
	}
	// Later boots keep it, whatever the random source would say.
	st, why, err = Choose(dir, "svc", choices, func(int) int { return 0 }, t0.Add(time.Hour))
	if err != nil || st.Choice != "b" || why != "" || !st.ChosenAt.Equal(t0) {
		t.Fatalf("second boot: %+v, %q, %v", st, why, err)
	}
}

func TestChooseAgainWhenTheKeptChoiceIsGoneOrUnreadable(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "svc", State{Choice: "old", ChosenAt: t0}); err != nil {
		t.Fatal(err)
	}
	st, why, err := Choose(dir, "svc", []string{"a", "b"}, func(int) int { return 0 }, t0)
	if err != nil || st.Choice != "a" || !strings.Contains(why, `"old"`) {
		t.Fatalf("dropped choice: %+v, %q, %v", st, why, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "svc.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, why, err = Choose(dir, "svc", []string{"a", "b"}, func(int) int { return 1 }, t0)
	if err != nil || st.Choice != "b" || !strings.Contains(why, "unreadable") {
		t.Fatalf("unreadable: %+v, %q, %v", st, why, err)
	}
	// What was chosen again is kept.
	if st, why, _ := Choose(dir, "svc", []string{"a", "b"}, func(int) int { return 0 }, t0); st.Choice != "b" || why != "" {
		t.Fatalf("not kept: %+v, %q", st, why)
	}
}

func TestNextWrapsAround(t *testing.T) {
	c := []string{"a", "b", "c"}
	if Next(c, "a") != "b" || Next(c, "c") != "a" {
		t.Fatal("Next does not follow the profile's order")
	}
}

func none() string { return "" }

func crashes() *Crashes {
	return &Crashes{Crashes: 3, Within: 10 * time.Minute, Cooldown: 24 * time.Hour}
}

func TestSwitchAfterEnoughCrashesWithinTheWindow(t *testing.T) {
	c := crashes()
	st := State{Choice: "a"}
	for i, at := range []time.Duration{0, 5 * time.Minute} {
		if sw, why := c.Record(t0.Add(at), st, none); sw {
			t.Fatalf("crash %d switched: %s", i+1, why)
		}
	}
	// The first crash has left the window by the fourth.
	if sw, _ := c.Record(t0.Add(11*time.Minute), st, none); sw {
		t.Fatal("crashes outside the window were counted")
	}
	if sw, why := c.Record(t0.Add(12*time.Minute), st, none); !sw || why != "3 of 3 crashes within 10m0s" {
		t.Fatalf("no switch on the third crash within the window: %v, %s", sw, why)
	}
}

func TestSwitchHeldBack(t *testing.T) {
	st := State{Choice: "a"}
	c := crashes()
	for range 3 {
		c.Record(t0, st, none)
	}
	// Once per boot.
	var sw bool
	var why string
	for range 3 {
		if sw, why = c.Record(t0.Add(time.Minute), st, none); sw {
			t.Fatal("switched twice in a boot")
		}
	}
	if !strings.Contains(why, "once this boot") {
		t.Errorf("held back for the wrong reason: %s", why)
	}
	// Within the cooldown of a switch made in an earlier boot.
	c = crashes()
	recent := State{Choice: "b", SwitchedAt: t0.Add(-time.Hour), SwitchedFrom: "a"}
	for range 3 {
		if sw, why := c.Record(t0, recent, none); sw || (len(c.times) == 3 && !strings.Contains(why, "less than 24h0m0s")) {
			t.Fatalf("switched within the cooldown: %v, %s", sw, why)
		}
	}
	// Blocked, such as on probation; it switches once it no longer is.
	c = crashes()
	for range 3 {
		if sw, _ := c.Record(t0, st, func() string { return "the slot is on probation" }); sw {
			t.Fatal("switched while blocked")
		}
	}
	if sw, _ := c.Record(t0, st, none); !sw {
		t.Fatal("no switch once unblocked")
	}
}
