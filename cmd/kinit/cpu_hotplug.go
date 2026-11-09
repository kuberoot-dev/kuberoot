package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const cpuSysfs = "/sys/devices/system/cpu"

// kinit has no udev: online newly registered CPUs after ACPI makes them present.
// Remember observed CPUs so reconciliation never reverses a deliberate offline.
func watchCPUHotplug() {
	seen := make(map[string]bool)
	for {
		if err := receiveCPUHotplug(cpuSysfs, seen); err != nil {
			log.Printf("CPU hotplug listener: %v; retrying", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func receiveCPUHotplug(root string, seen map[string]bool) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return err
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 5}); err != nil {
		return err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: 1}); err != nil {
		return err
	}
	// Subscribe before scanning so a CPU added during the scan remains queued.
	reconcileCPUs(root, seen)
	data := make([]byte, 16*1024)
	control := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
	nextScan := time.Now().Add(5 * time.Second)
	for {
		// Unrelated uevents must not postpone a failed CPU's retry forever.
		if !time.Now().Before(nextScan) {
			reconcileCPUs(root, seen)
			nextScan = time.Now().Add(5 * time.Second)
		}
		n, oobn, flags, from, err := unix.Recvmsg(fd, data, control, 0)
		if err == unix.EINTR {
			continue
		}
		// Retry CPUs whose online write failed transiently, even if no further
		// uevent arrives. Successfully observed CPUs remain untouched.
		if err == unix.EAGAIN {
			continue
		}
		if err == unix.ENOBUFS {
			reconcileCPUs(root, seen)
			continue
		}
		if err != nil {
			return err
		}
		if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
			reconcileCPUs(root, seen)
			continue
		}
		if !kernelCPUEvent(from, control[:oobn]) {
			continue
		}
		name, action := cpuEvent(data[:n])
		switch action {
		case "add":
			observeCPU(root, name, seen)
		case "remove":
			delete(seen, name)
		}
	}
}

func kernelCPUEvent(from unix.Sockaddr, control []byte) bool {
	peer, ok := from.(*unix.SockaddrNetlink)
	if !ok || peer.Pid != 0 {
		return false
	}
	messages, err := unix.ParseSocketControlMessage(control)
	if err != nil || len(messages) != 1 {
		return false
	}
	cred, err := unix.ParseUnixCredentials(&messages[0])
	return err == nil && cred.Pid == 0 && cred.Uid == 0 && cred.Gid == 0
}

// Accept only the exact kernel CPU path; never construct a path from arbitrary
// DEVPATH data. Header and environment must agree, without duplicate keys.
func cpuEvent(data []byte) (string, string) {
	if len(data) == 0 || data[len(data)-1] != 0 {
		return "", ""
	}
	parts := bytes.Split(data[:len(data)-1], []byte{0})
	fields := make(map[string]string)
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(string(part), "=")
		if !ok {
			return "", ""
		}
		if _, duplicate := fields[key]; duplicate {
			return "", ""
		}
		fields[key] = value
	}
	action, path := fields["ACTION"], fields["DEVPATH"]
	if (action != "add" && action != "remove") || fields["SUBSYSTEM"] != "cpu" || string(parts[0]) != action+"@"+path {
		return "", ""
	}
	name, ok := strings.CutPrefix(path, "/devices/system/cpu/")
	if !ok || !cpuName(name) {
		return "", ""
	}
	return name, action
}

func cpuName(name string) bool {
	id, ok := strings.CutPrefix(name, "cpu")
	if !ok || id == "" {
		return false
	}
	n, err := strconv.ParseUint(id, 10, 31)
	return err == nil && strconv.FormatUint(n, 10) == id
}

func reconcileCPUs(root string, seen map[string]bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("CPU hotplug scan: %v", err)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && cpuName(entry.Name()) {
			observeCPU(root, entry.Name(), seen)
		}
	}
}

func observeCPU(root, name string, seen map[string]bool) {
	if seen[name] {
		return
	}
	// Possible but absent ARM CPUs must stay dormant until the ACPI add event.
	present, err := os.ReadFile(filepath.Join(root, "present"))
	if err != nil {
		log.Printf("CPU hotplug read present: %v", err)
		return
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(name, "cpu"), 10, 31)
	if err != nil || !cpuInList(uint32(id), strings.TrimSpace(string(present))) {
		return
	}
	if err := onlineCPU(filepath.Join(root, name, "online")); err != nil {
		log.Printf("CPU hotplug %s: %v", name, err)
		return
	}
	seen[name] = true
}

func cpuInList(id uint32, list string) bool {
	for _, item := range strings.Split(list, ",") {
		first, last, ranged := strings.Cut(item, "-")
		if !ranged {
			last = first
		}
		lo, err1 := strconv.ParseUint(first, 10, 31)
		hi, err2 := strconv.ParseUint(last, 10, 31)
		if err1 == nil && err2 == nil && lo <= uint64(id) && uint64(id) <= hi {
			return true
		}
	}
	return false
}

func onlineCPU(path string) error {
	value, err := os.ReadFile(path)
	// CPU0 and non-hotpluggable CPUs may have no online attribute.
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(value)) {
	case "1":
		return nil
	case "0":
	default:
		return fmt.Errorf("unexpected online state %q", value)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString("1\n")
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
