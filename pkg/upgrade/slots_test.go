package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuberoot-dev/kuberoot/pkg/bootdisk"
)

func writeEntry(t *testing.T, dir, file, release, version string) {
	t.Helper()
	body := "title kuberoot " + release + " (slot x)\nsort-key kuberoot\nversion " + version + "\nlinux /k\ninitrd /i\noptions o\n"
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func files(t *testing.T, dir string) string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}

func TestReadEntriesStatesAndDefault(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "0.1.0", "1")
	writeEntry(t, dir, "kuberoot-b+0-3.conf", "0.1.1", "2") // ran out of attempts
	writeEntry(t, dir, "loader.conf", "x", "9")             // not a slot entry
	entries, err := readEntriesIn(dir, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	b, a := entries[0], entries[1]
	if b.Slot != "b" || b.State != StateBad || b.Default {
		t.Errorf("slot b = %+v; want Bad, not default", b)
	}
	if a.State != StateGood || !a.Default || !a.Booted || a.Release != "0.1.0" {
		t.Errorf("slot a = %+v; want Good, default, booted, release 0.1.0", a)
	}
}

func TestTryingEntryCountsAttempts(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-b+2-1.conf", "0.1.1", "3")
	entries, _ := readEntriesIn(dir, "b")
	if e := entries[0]; e.State != StateTrying || e.Left != 2 || e.Done != 1 {
		t.Errorf("entry = %+v; want Trying with 2 left, 1 done", e)
	}
}

func TestMarkGoodDropsTheCounterOfTheBootedSlot(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "0.1.0", "1")
	writeEntry(t, dir, "kuberoot-b+2-1.conf", "0.1.1", "2")
	marked, err := markGoodIn(dir, "b")
	if err != nil || !marked {
		t.Fatalf("marked = %v, %v", marked, err)
	}
	if got := files(t, dir); got != "kuberoot-a.conf kuberoot-b.conf" {
		t.Errorf("files = %q", got)
	}
	if marked, _ := markGoodIn(dir, "b"); marked {
		t.Error("a good slot was marked again")
	}
}

func TestPreferPutsAnotherSlotOnProbation(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "0.1.0", "1")
	writeEntry(t, dir, "kuberoot-b.conf", "0.1.1", "2")
	if err := preferIn(dir, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); got != "kuberoot-a+3.conf kuberoot-b.conf" {
		t.Errorf("files = %q", got)
	}
	entries, _ := readEntriesIn(dir, "b")
	if entries[0].Slot != "a" || !entries[0].Default || entries[0].Version != 3 {
		t.Errorf("first entry = %+v; want slot a, default, version 3", entries[0])
	}
}

func TestPreferKeepsTheRunningSlotTrusted(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "0.1.0", "1")
	writeEntry(t, dir, "kuberoot-b+3.conf", "0.1.1", "2")
	if err := preferIn(dir, "a", "a"); err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); got != "kuberoot-a.conf kuberoot-b+3.conf" {
		t.Errorf("files = %q", got)
	}
	entries, _ := readEntriesIn(dir, "a")
	if entries[0].Slot != "a" || !entries[0].Default {
		t.Errorf("first entry = %+v; want slot a as default", entries[0])
	}
}

func TestPreferRefusesABadSlot(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "0.1.0", "1")
	writeEntry(t, dir, "kuberoot-b+0-3.conf", "0.1.2", "2")
	if err := preferIn(dir, "b", "a"); err == nil {
		t.Fatal("a slot with no attempts left was preferred")
	}
	if got := files(t, dir); got != "kuberoot-a.conf kuberoot-b+0-3.conf" {
		t.Errorf("files changed: %q", got)
	}
}

func TestStagingRetiresTheOldEntryAndAddsOneOnProbation(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "0.1.0", "4")
	writeEntry(t, dir, "kuberoot-b.conf", "0.1.1", "7")
	if err := retireSlot(dir, "b"); err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); got != "kuberoot-a.conf" {
		t.Fatalf("after retiring: %q", got)
	}
	a := bootdisk.Artifacts{Version: "0.1.2"}
	if err := addProbationEntry(dir, bootdisk.SlotEntry(a, "b", ""), "a"); err != nil {
		t.Fatal(err)
	}
	entries, _ := readEntriesIn(dir, "a")
	if e := entries[0]; e.File != "kuberoot-b+3.conf" || e.Version != 5 || e.State != StateTrying || e.Release != "0.1.2" {
		t.Errorf("new entry = %+v; want kuberoot-b+3.conf, version 5, Trying, 0.1.2", e)
	}
}

func TestTheLastAttemptIsStillOnProbation(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "0.1.0", "1")
	writeEntry(t, dir, "kuberoot-b+0-3.conf", "0.1.1", "2") // booted on its last attempt
	entries, _ := readEntriesIn(dir, "b")
	if on, fallback := Probation(entries); !on || !fallback {
		t.Errorf("Probation = %v, %v; want on probation with slot a to fall back to", on, fallback)
	}
	entries, _ = readEntriesIn(dir, "a")
	if on, _ := Probation(entries); on {
		t.Error("a good running slot is not on probation")
	}
}

func TestNoFallbackWithoutAnotherGoodSlot(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a+0-3.conf", "0.1.0", "1")
	writeEntry(t, dir, "kuberoot-b+0-3.conf", "0.1.1", "2")
	entries, _ := readEntriesIn(dir, "b")
	if on, fallback := Probation(entries); !on || fallback {
		t.Errorf("Probation = %v, %v; want on probation, nothing to fall back to", on, fallback)
	}
}
