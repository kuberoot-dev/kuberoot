package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCPUEventRequiresMatchingKernelPath(t *testing.T) {
	valid := "add@/devices/system/cpu/cpu3\x00ACTION=add\x00DEVPATH=/devices/system/cpu/cpu3\x00SUBSYSTEM=cpu\x00SEQNUM=19\x00"
	if name, action := cpuEvent([]byte(valid)); name != "cpu3" || action != "add" {
		t.Fatalf("valid event: %q %q", name, action)
	}
	for _, event := range []string{
		"", valid[:len(valid)-1],
		valid + "ACTION=remove\x00",
		"online@/devices/system/cpu/cpu3\x00ACTION=add\x00DEVPATH=/devices/system/cpu/cpu3\x00SUBSYSTEM=cpu\x00",
		"add@/devices/system/cpu/../cpu3\x00ACTION=add\x00DEVPATH=/devices/system/cpu/../cpu3\x00SUBSYSTEM=cpu\x00",
		"add@/devices/system/cpu/cpu3/online\x00ACTION=add\x00DEVPATH=/devices/system/cpu/cpu3/online\x00SUBSYSTEM=cpu\x00",
		"add@/devices/system/cpu/cpu03\x00ACTION=add\x00DEVPATH=/devices/system/cpu/cpu03\x00SUBSYSTEM=cpu\x00",
		"add@/devices/system/cpu/cpu3\x00ACTION=add\x00DEVPATH=/devices/system/cpu/cpu3\x00SUBSYSTEM=memory\x00",
	} {
		if name, action := cpuEvent([]byte(event)); name != "" || action != "" {
			t.Errorf("accepted malformed event %q", event)
		}
	}
}

func TestCPUEventRejectsUserspaceAndMissingCredentials(t *testing.T) {
	kernel := &unix.SockaddrNetlink{}
	cred := unix.UnixCredentials(&unix.Ucred{})
	if !kernelCPUEvent(kernel, cred) {
		t.Fatal("kernel credentials rejected")
	}
	for _, tc := range []struct {
		from unix.Sockaddr
		oob  []byte
	}{
		{kernel, nil},
		{kernel, cred[:len(cred)/2]},
		{&unix.SockaddrNetlink{Pid: 42}, cred},
		{kernel, unix.UnixCredentials(&unix.Ucred{Pid: 42})},
		{kernel, unix.UnixCredentials(&unix.Ucred{Uid: 1000})},
		{kernel, unix.UnixCredentials(&unix.Ucred{Gid: 1000})},
		{kernel, append(append([]byte{}, cred...), cred...)},
	} {
		if kernelCPUEvent(tc.from, tc.oob) {
			t.Errorf("accepted non-kernel credentials: %+v", tc)
		}
	}
}

func TestCPUReconcileAddsPresentCPUsAndPreservesManualOffline(t *testing.T) {
	root := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		t.Helper()
		v, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(v)
	}
	for _, name := range []string{"cpu0", "cpu1", "cpu2"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// CPU0 has no online attribute; CPU2 is possible but not yet present.
	write("present", "0-1\n")
	write("cpu1/online", "0\n")
	write("cpu2/online", "0\n")
	seen := make(map[string]bool)
	reconcileCPUs(root, seen)
	if read("cpu1/online") != "1\n" || read("cpu2/online") != "0\n" || seen["cpu2"] {
		t.Fatal("initial scan did not distinguish present from possible CPUs")
	}
	if _, err := os.Stat(filepath.Join(root, "cpu0/online")); !os.IsNotExist(err) {
		t.Fatal("created a missing sysfs attribute")
	}
	write("cpu1/online", "0\n") // An explicit offline after the initial scan.
	write("present", "0-2\n")
	reconcileCPUs(root, seen) // Models a lost uevent followed by rescan.
	if read("cpu1/online") != "0\n" || read("cpu2/online") != "1\n" {
		t.Fatal("rescan reverted deliberate offline or missed a newly present CPU")
	}
	observeCPU(root, "cpu1", seen) // A duplicate add must also be harmless.
	if read("cpu1/online") != "0\n" {
		t.Fatal("duplicate add reversed deliberate offline")
	}
	write("cpu2/online", "garbage\n")
	if err := onlineCPU(filepath.Join(root, "cpu2/online")); err == nil {
		t.Fatal("unknown online state accepted")
	}
	delete(seen, "cpu2")
	reconcileCPUs(root, seen)
	if seen["cpu2"] {
		t.Fatal("failed online attempt suppresses future retries")
	}
	write("cpu2/online", "0\n")
	reconcileCPUs(root, seen)
	if !seen["cpu2"] || read("cpu2/online") != "1\n" {
		t.Fatal("reconciliation did not retry a failed online attempt")
	}
}

func TestCPUPresentList(t *testing.T) {
	for _, tc := range []struct {
		id   uint32
		list string
		want bool
	}{
		{0, "0", true}, {4, "0-1,4,7-8", true}, {8, "0-1,4,7-8", true},
		{2, "0-1,4,7-8", false}, {9, "0-1,4,7-8", false},
		{1, "", false}, {1, "2-1", false}, {1, "0-99999999999999", false},
	} {
		if got := cpuInList(tc.id, tc.list); got != tc.want {
			t.Errorf("cpuInList(%d, %q) = %v", tc.id, tc.list, got)
		}
	}
}
