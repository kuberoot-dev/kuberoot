package upgrade

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// A busy mount point used to be permanent: the unmount error was dropped, the
// partition stayed mounted, and every later call failed at the mount with
// EBUSY on a node with no shell to clear it. Detaching is what keeps one open
// file from costing the node its slots until it reboots.
func TestBusyUnmountDetachesInsteadOfLeakingTheMount(t *testing.T) {
	var flags []int
	unmount := func(_ string, flag int) error {
		flags = append(flags, flag)
		if flag == 0 {
			return unix.EBUSY
		}
		return nil
	}
	if err := unmountESP("/run/kuberoot/efi", unmount); err != nil {
		t.Fatalf("a busy mount point must be detached, got: %v", err)
	}
	if len(flags) != 2 || flags[0] != 0 || flags[1] != unix.MNT_DETACH {
		t.Errorf("expected a plain unmount and then a detach, got %v", flags)
	}
}

// Something else having taken the mount down is the state this runs towards,
// not a failure to report.
func TestUnmountOfSomethingAlreadyGoneIsNotAnError(t *testing.T) {
	unmount := func(string, int) error { return unix.EINVAL }
	if err := unmountESP("/run/kuberoot/efi", unmount); err != nil {
		t.Errorf("EINVAL means it is already unmounted: %v", err)
	}
}

// Any other failure has to reach the caller. Dropping it is what made the
// original defect invisible until the call after it.
func TestOtherUnmountFailuresAreReported(t *testing.T) {
	unmount := func(string, int) error { return unix.EPERM }
	err := unmountESP("/run/kuberoot/efi", unmount)
	if !errors.Is(err, unix.EPERM) {
		t.Errorf("expected the unmount failure to be returned, got: %v", err)
	}
}

// A detach that fails too leaves the mount point occupied, and the caller must
// learn that rather than go on to a mount that cannot succeed.
func TestFailedDetachIsReported(t *testing.T) {
	unmount := func(_ string, flag int) error {
		if flag == 0 {
			return unix.EBUSY
		}
		return unix.EPERM
	}
	if err := unmountESP("/run/kuberoot/efi", unmount); !errors.Is(err, unix.EPERM) {
		t.Errorf("expected the detach failure to be returned, got: %v", err)
	}
}

// An ordinary directory is not a mount point; a missing one is not an error,
// because the first call makes it.
func TestIsMountPointOnPlainDirectories(t *testing.T) {
	dir := t.TempDir()
	if got, err := isMountPoint(dir); err != nil || got {
		t.Errorf("a plain directory is not a mount point: got %v, %v", got, err)
	}
	if got, err := isMountPoint(dir + "/missing"); err != nil || got {
		t.Errorf("a missing path is not a mount point and not an error: got %v, %v", got, err)
	}
}
