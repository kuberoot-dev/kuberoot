// Package upgrade moves a node between its A and B root slots: it stages a new
// release into the inactive slot, boots it on probation and keeps it only once
// the node comes up healthy. systemd-boot counts the attempts and falls back.
package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
	"github.com/kuberoot-dev/kuberoot/pkg/bootdisk"
	"github.com/kuberoot-dev/kuberoot/pkg/installer"
)

const (
	efiMount   = "/run/kuberoot/efi"
	entriesDir = efiMount + "/loader/entries"
	// Tries is how many boots a freshly staged slot gets to prove itself.
	Tries = 3
)

// Entry states.
const (
	StateGood   = "Good"   // booted successfully at least once
	StateTrying = "Trying" // staged, boot attempts left
	StateBad    = "Bad"    // ran out of attempts; systemd-boot skips it
)

// Entry is the boot entry of one slot as found on the EFI partition.
type Entry struct {
	Slot    string
	File    string // file name, including any boot counter
	Version int    // sort version: the highest good-or-trying entry boots by default
	Release string // kuberoot release in the title
	State   string
	Left    int
	Done    int
	Booted  bool
	Default bool
}

var counter = regexp.MustCompile(`^kuberoot-([ab])(?:\+(\d+)(?:-(\d+))?)?\.conf$`)

// mu serialises everything that touches the EFI partition.
var mu sync.Mutex

// CurrentSlot is the slot the running system booted from.
func CurrentSlot() string {
	raw, _ := os.ReadFile("/proc/cmdline")
	for _, f := range strings.Fields(string(raw)) {
		if v, ok := strings.CutPrefix(f, "kuberoot.root=PARTLABEL="+bootdisk.RootLabel("")); ok {
			return v
		}
	}
	return ""
}

func otherSlot(slot string) string {
	if slot == "a" {
		return "b"
	}
	return "a"
}

// withESP mounts the EFI partition of the system disk for the duration of fn.
func withESP(fn func() error) error {
	mu.Lock()
	defer mu.Unlock()
	dev := installer.PartitionOn("", bootdisk.EFILabel)
	if dev == "" {
		return fmt.Errorf("no EFI partition: not an installed system")
	}
	if err := os.MkdirAll(efiMount, 0o755); err != nil {
		return err
	}
	if err := unix.Mount(dev, efiMount, "vfat", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "umask=0077"); err != nil {
		return fmt.Errorf("mount EFI partition: %w", err)
	}
	defer func() {
		unix.Sync()
		_ = unix.Unmount(efiMount, 0)
	}()
	return fn()
}

// Entries lists the slot entries, newest first.
func Entries() ([]Entry, error) {
	var out []Entry
	err := withESP(func() error {
		var err error
		out, err = readEntries()
		return err
	})
	return out, err
}

func readEntries() ([]Entry, error) { return readEntriesIn(entriesDir, CurrentSlot()) }

// readEntriesIn reads the slot entries of a loader entries directory, newest first.
func readEntriesIn(dir, current string) ([]Entry, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, f := range files {
		m := counter.FindStringSubmatch(f.Name())
		if m == nil {
			continue
		}
		e := Entry{Slot: m[1], File: f.Name(), State: StateGood, Booted: m[1] == current}
		if m[2] != "" {
			e.Left, _ = strconv.Atoi(m[2])
			e.Done, _ = strconv.Atoi(m[3])
			e.State = StateTrying
			if e.Left == 0 {
				e.State = StateBad
			}
		}
		raw, _ := os.ReadFile(filepath.Join(dir, f.Name()))
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(line, "version "); ok {
				e.Version, _ = strconv.Atoi(strings.TrimSpace(v))
			}
			if v, ok := strings.CutPrefix(line, "title "); ok {
				if words := strings.Fields(v); len(words) > 1 {
					e.Release = words[1]
				}
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	for i := range out {
		if out[i].State != StateBad {
			out[i].Default = true
			break
		}
	}
	return out, nil
}

// Stage writes a release into the inactive slot and makes it the next boot,
// on probation: it gets Tries boots to come up healthy. The slot's old entry
// goes first, so a write that breaks off halfway never leaves an entry that
// points at a half-written root.
func Stage(a bootdisk.Artifacts, report func(done, total int64)) (string, error) {
	current := CurrentSlot()
	if current == "" {
		return "", fmt.Errorf("not booted from an installed slot")
	}
	target := otherSlot(current)
	dev := installer.PartitionOn("", bootdisk.RootLabel(target))
	if dev == "" {
		return "", fmt.Errorf("no root partition for slot %s", target)
	}
	if err := fits(filepath.Join(a.Dir, "rootfs.squashfs"), dev); err != nil {
		return "", err
	}
	if err := withESP(func() error { return retireSlot(entriesDir, target) }); err != nil {
		return "", fmt.Errorf("retire slot %s: %w", target, err)
	}
	if err := installer.WriteRoot(nil, a, dev, report); err != nil {
		return "", fmt.Errorf("write slot %s: %w", target, err)
	}
	return target, withESP(func() error {
		slotDir := filepath.Join(efiMount, "kuberoot", target)
		if err := os.MkdirAll(slotDir, 0o755); err != nil {
			return err
		}
		for _, f := range []string{"vmlinuz.efi", "initrd.cpio"} {
			if err := copyFile(filepath.Join(a.Dir, f), filepath.Join(slotDir, f)); err != nil {
				return err
			}
		}
		return addProbationEntry(entriesDir, bootdisk.SlotEntry(a, target, ""), CurrentSlot())
	})
}

// fits refuses an image larger than the partition it is meant for.
func fits(image, dev string) error {
	st, err := os.Stat(image)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join("/sys/class/block", filepath.Base(dev), "size"))
	if err != nil {
		return err
	}
	sectors, _ := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if st.Size() > sectors*512 {
		return fmt.Errorf("root image of %d MiB does not fit the %d MiB slot", st.Size()>>20, sectors*512>>20)
	}
	return nil
}

// retireSlot removes every entry of a slot.
func retireSlot(dir, slot string) error {
	entries, err := readEntriesIn(dir, "")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Slot == slot {
			if err := os.Remove(filepath.Join(dir, e.File)); err != nil {
				return err
			}
		}
	}
	return nil
}

// addProbationEntry writes a slot's entry above every other one, with Tries
// boot attempts.
func addProbationEntry(dir string, entry bootdisk.Entry, current string) error {
	entries, err := readEntriesIn(dir, current)
	if err != nil {
		return err
	}
	top := 0
	for _, e := range entries {
		top = max(top, e.Version)
	}
	entry.Version = strconv.Itoa(top + 1)
	name := fmt.Sprintf("%s+%d.conf", entry.ID, Tries)
	return atomicfile.WriteFile(filepath.Join(dir, name), []byte(entry.String()), 0o644)
}

// Probation reports whether the running slot is on probation and whether
// another slot that came up healthy before can take over should it fail. The
// running slot is on probation while it carries a boot counter: Trying, or
// Bad when systemd-boot has just spent its last attempt on it.
func Probation(entries []Entry) (onProbation, fallback bool) {
	for _, e := range entries {
		if e.Booted && e.State != StateGood {
			onProbation = true
		}
		if !e.Booted && e.State == StateGood {
			fallback = true
		}
	}
	return onProbation, fallback
}

// MarkGood ends the probation of the running slot by dropping its boot counter.
func MarkGood() (bool, error) {
	var marked bool
	err := withESP(func() error {
		var err error
		marked, err = markGoodIn(entriesDir, CurrentSlot())
		return err
	})
	return marked, err
}

func markGoodIn(dir, current string) (bool, error) {
	entries, err := readEntriesIn(dir, current)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Booted && e.State != StateGood {
			return true, os.Rename(filepath.Join(dir, e.File), filepath.Join(dir, "kuberoot-"+e.Slot+".conf"))
		}
	}
	return false, nil
}

// Prefer makes slot the default for the next boot: a manual rollback or
// roll-forward. Another slot than the running one boots on probation, as a
// fresh upgrade would, so a bad choice still falls back on its own; a slot
// that already ran out of attempts is refused.
func Prefer(slot string) error {
	return withESP(func() error { return preferIn(entriesDir, slot, CurrentSlot()) })
}

func preferIn(dir, slot, current string) error {
	entries, err := readEntriesIn(dir, current)
	if err != nil {
		return err
	}
	top := 0
	var chosen *Entry
	for i, e := range entries {
		top = max(top, e.Version)
		if e.Slot == slot {
			chosen = &entries[i]
		}
	}
	if chosen == nil {
		return fmt.Errorf("slot %s has no boot entry", slot)
	}
	if chosen.State == StateBad {
		return fmt.Errorf("slot %s ran out of boot attempts; upgrade it to a working release instead", slot)
	}
	path := filepath.Join(dir, chosen.File)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "version ") {
			lines[i] = "version " + strconv.Itoa(top+1)
		}
	}
	name := "kuberoot-" + slot + ".conf"
	if !chosen.Booted {
		name = fmt.Sprintf("kuberoot-%s+%d.conf", slot, Tries)
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return err
	}
	if name != chosen.File {
		return os.Remove(path)
	}
	return nil
}

func copyFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(dst, raw, 0o644)
}
