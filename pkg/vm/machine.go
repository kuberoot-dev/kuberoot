package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

// ErrWaiting is a machine waiting for something to be ready, not failing.
var ErrWaiting = errors.New("waiting")

// Bridge joins the machines of a node to the machines' network.
const Bridge = "vmbr0"

// Firmware boots the machines: UEFI for cloud-hypervisor.
var Firmware = "/usr/share/kuberoot/CLOUDHV.fd"

// Machines runs the virtual machines of a node.
type Machines struct {
	Volumes *Volumes
	// MemoryBudgetMiB limits guest RAM; the distribution reserves additional
	// memory outside pod allocatable for the OS and monitor processes.
	MemoryBudgetMiB   int64
	budgetMu          sync.Mutex
	pendingMemory     map[string]int64
	remote            func(context.Context, string, ...string) ([]byte, error)
	arch              string
	probeCapabilities func(context.Context) ([]byte, error)
}

func (m *Machines) dir() string              { return filepath.Join(StateDir, "machines") }
func (m *Machines) specFile(n string) string { return filepath.Join(m.dir(), n+".json") }
func (m *Machines) pidFile(n string) string  { return filepath.Join(RunDir, n+".pid") }
func (m *Machines) socket(n string) string   { return filepath.Join(RunDir, n+".sock") }
func (m *Machines) consoleLog(n string) string {
	return filepath.Join("/var/log/kuberoot/vm", n+".console")
}

// Save keeps a machine's spec; Apply then makes the node match it.
func (m *Machines) Save(name string, s node.MachineSpec) error {
	m.budgetMu.Lock()
	defer m.budgetMu.Unlock()
	if err := validateCeilings(s); err != nil {
		return err
	}
	if s.Running && s.SendTo != "" && (s.MaxCPUs > 0 || s.MaxMemoryMiB > 0) {
		return ErrMigrationCeilings
	}
	old, err := m.Load(name)
	if err == nil && (old.MaxCPUs != s.MaxCPUs || old.MaxMemoryMiB != s.MaxMemoryMiB) {
		return fmt.Errorf("%w: maxCPUs and maxMemoryMiB are immutable; create a new machine to change its ceilings", ErrMachineSpec)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if s.Running && s.Receive != "" && (m.MemoryBudgetMiB > 0 || s.MaxCPUs > 0 || s.MaxMemoryMiB > 0) {
		return ErrMigrationMemory
	}
	if err := m.checkMemoryBudget(name, s); err != nil && (s.Running || !errors.Is(err, ErrMemoryBudget)) {
		return err
	}
	if err := os.MkdirAll(m.dir(), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeAtomic(m.specFile(name), raw)
}

// Load reads a machine's spec.
func (m *Machines) Load(name string) (node.MachineSpec, error) {
	var s node.MachineSpec
	raw, err := os.ReadFile(m.specFile(name))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(raw, &s)
}

// Names lists the machines of the node.
func (m *Machines) Names() []string {
	files, _ := filepath.Glob(filepath.Join(m.dir(), "*.json"))
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimSuffix(filepath.Base(f), ".json"))
	}
	sort.Strings(out)
	return out
}

// TapName is a machine's network interface on the node; link names are
// short, so it is made from a hash of the machine's name.
func TapName(machine string) string {
	sum := sha256.Sum256([]byte(machine))
	return "vmt" + hex.EncodeToString(sum[:])[:10]
}

// Args are cloud-hypervisor's arguments for a machine with these disks.
func (m *Machines) Args(name string, s node.MachineSpec, disks []string) []string {
	return m.launchArgs(name, s, disks, false)
}

func (m *Machines) launchArgs(name string, s node.MachineSpec, disks []string, armCPUHotplug bool) []string {
	cpus := fmt.Sprintf("boot=%d", s.CPUs)
	if s.MaxCPUs > 0 {
		cpus += fmt.Sprintf(",max=%d", s.MaxCPUs)
	}
	if armCPUHotplug {
		cpus += ",acpi_hotplug=on"
	}
	memory := fmt.Sprintf("size=%dM", s.MemoryMiB)
	if s.MaxMemoryMiB > s.MemoryMiB {
		memory += fmt.Sprintf(",hotplug_method=virtio-mem,hotplug_size=%dM", s.MaxMemoryMiB-s.MemoryMiB)
	}
	args := []string{"--api-socket", "path=" + m.socket(name),
		"--cpus", cpus,
		"--memory", memory,
		"--firmware", Firmware,
		"--serial", "file=" + m.consoleLog(name), "--console", "off"}
	for _, d := range disks {
		// Volumes expose raw block devices. Format autodetection disables
		// sector-zero writes, preventing the guest from growing partitions.
		args = append(args, "--disk", "path="+d+",image_type=raw")
	}
	if s.MAC != "" {
		args = append(args, "--net", fmt.Sprintf("tap=%s,mac=%s", TapName(name), s.MAC))
	}
	if s.SystemSerial != "" {
		args = append(args, "--platform", "system_serial_number="+s.SystemSerial)
	}
	return args
}

// Apply starts a machine meant to run and stops one that is not; one that
// changes its size live only within explicit ceilings. A smaller size or a
// changed launch configuration needs a restart; legacy sizes still do too.
func (m *Machines) Apply(ctx context.Context, name string, s node.MachineSpec) error {
	if s.Running {
		if s.SendTo != "" && (s.MaxCPUs > 0 || s.MaxMemoryMiB > 0) {
			return ErrMigrationCeilings
		}
		if err := validateCeilings(s); err != nil {
			return err
		}
	}
	release, err := m.reserveMemory(name, s)
	if err != nil {
		return err
	}
	defer release()
	pid := m.pid(name)
	switch {
	case exists(m.mark(name, "sent")):
		return nil // the machine went on to another node
	case exists(m.mark(name, "receiving")):
		return nil // arriving
	case s.Receive != "" && exists(m.mark(name, "failed")):
		return nil // left for the controller to see the move failed
	case s.Running && pid == 0 && s.Receive != "":
		return m.receive(name, s)
	case s.Running && pid == 0:
		plan, err := m.prepareLaunch(ctx, s)
		if err != nil {
			return err
		}
		defer plan.close()
		return m.start(name, s, plan)
	case !s.Running && pid != 0:
		return m.stop(ctx, name, pid)
	case s.Running && s.SendTo != "":
		if err := m.checkSendConfig(name, s); err != nil {
			return err
		}
		return m.send(ctx, name, pid, s.SendTo)
	case s.Running && m.startedWith(name) != configKey(s):
		plan, err := m.prepareLaunch(ctx, s)
		if err != nil {
			return m.launchPreflightFailed(ctx, name, pid, err)
		}
		defer plan.close()
		if err := m.stop(ctx, name, pid); err != nil {
			return err
		}
		return m.start(name, s, plan)
	case s.Running && (s.MaxCPUs > 0 || s.MaxMemoryMiB > 0):
		restart, err := m.reconcileResources(ctx, name, pid, s)
		if err != nil || !restart {
			return err
		}
		plan, err := m.prepareLaunch(ctx, s)
		if err != nil {
			return m.launchPreflightFailed(ctx, name, pid, err)
		}
		defer plan.close()
		if err := m.stop(ctx, name, pid); err != nil {
			return err
		}
		return m.start(name, s, plan)
	}
	return nil
}

func (m *Machines) checkSendConfig(name string, s node.MachineSpec) error {
	if s.MaxCPUs > 0 || s.MaxMemoryMiB > 0 {
		return ErrMigrationCeilings
	}
	if m.startedWith(name) != configKey(s) {
		return fmt.Errorf("cannot migrate machine %s while its requested CPU, memory, disk or network configuration differs from the running guest", name)
	}

	return nil
}

func (m *Machines) mark(name, what string) string { return m.pidFile(name) + "." + what }

// receive starts an empty monitor that takes a running machine arriving
// from another node, and goes on running it.
func (m *Machines) receive(name string, s node.MachineSpec) error {
	if _, err := m.disks(s); err != nil {
		return err
	}
	if s.MAC != "" {
		if err := ensureTap(TapName(name)); err != nil {
			return err
		}
	}
	if err := m.launch(name, []string{"--api-socket", "path=" + m.socket(name)}, s); err != nil {
		return err
	}
	for i := 0; i < 50 && !exists(m.socket(name)); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.Remove(m.mark(name, "failed"))
	if err := os.WriteFile(m.mark(name, "receiving"), nil, 0o600); err != nil {
		return err
	}
	go func() {
		out, err := exec.Command("/usr/bin/ch-remote", "--api-socket", m.socket(name), "receive-migration", "receiver_url="+s.Receive).CombinedOutput()
		if err != nil {
			_ = os.WriteFile(m.mark(name, "failed"), out, 0o600)
			if pid := m.pid(name); pid != 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		_ = os.Remove(m.mark(name, "receiving"))
	}()
	return nil
}

// send moves the running machine, alive, to a node receiving it; this
// node's monitor stops once the machine runs there.
func (m *Machines) send(ctx context.Context, name string, pid int, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/ch-remote", "--api-socket", m.socket(name), "send-migration", "destination_url="+url).CombinedOutput()
	if err != nil {
		return fmt.Errorf("send-migration: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.WriteFile(m.mark(name, "sent"), nil, 0o600); err != nil {
		return err
	}
	for i := 0; i < 10 && m.pid(name) != 0; i++ {
		time.Sleep(time.Second)
	}
	if m.pid(name) != 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	return nil
}

// configKey identifies the immutable launch shape. Preserve the exact legacy
// encoding when no ceiling was requested, so upgrading does not reboot guests.
// An unbounded axis still includes its size because changing it needs a restart.
func configKey(s node.MachineSpec) string {
	cpus, memory := s.CPUs, s.MemoryMiB
	if s.MaxCPUs > 0 {
		cpus = 0
	}
	if s.MaxMemoryMiB > 0 {
		memory = 0
	}
	key := fmt.Sprintf("%d/%d/%s/%s/%s", cpus, memory, strings.Join(s.Volumes, ","), s.MAC, s.SystemSerial)
	if s.MaxCPUs > 0 || s.MaxMemoryMiB > 0 {
		key += fmt.Sprintf("/ceilings=%d,%d", s.MaxCPUs, s.MaxMemoryMiB)
	}
	return key
}

func (m *Machines) startedWith(name string) string {
	raw, err := os.ReadFile(m.pidFile(name) + ".config")
	if err != nil {
		return ""
	}
	return string(raw)
}

func (m *Machines) start(name string, s node.MachineSpec, plan *launchPlan) error {
	disks, err := m.disks(s)
	if err != nil {
		return err
	}
	if s.MAC != "" {
		if err := ensureTap(TapName(name)); err != nil {
			return err
		}
	}
	return m.launchPinned(name, m.launchArgs(name, s, disks, plan.armCPUHotplug), s, plan.executable)
}

// disks are a machine's volumes as devices, once they are ready here.
func (m *Machines) disks(s node.MachineSpec) ([]string, error) {
	var disks []string
	for _, v := range s.Volumes {
		vs, err := m.Volumes.Load(v)
		if err != nil {
			return nil, fmt.Errorf("volume %s: %w", v, err)
		}
		// The disk is this node's to write once DRBD made it primary here,
		// and holds the machine's data once its image is in.
		st := m.Volumes.Status(context.Background(), v)
		if !vs.Primary || st.Role != "Primary" || (vs.Image != "" && !m.Volumes.ImageWritten(v)) {
			return nil, fmt.Errorf("%w: volume %s is not ready here", ErrWaiting, v)
		}
		disks = append(disks, Device(vs))
	}
	return disks, nil
}

// launch runs cloud-hypervisor for a machine, in a session of its own.
func (m *Machines) launch(name string, args []string, s node.MachineSpec) error {
	return m.launchPinned(name, args, s, nil)
}

func (m *Machines) launchPinned(name string, args []string, s node.MachineSpec, executable *os.File) error {
	for _, d := range []string{RunDir, filepath.Dir(m.consoleLog(name))} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	_ = os.Remove(m.socket(name))
	_ = os.Remove(m.mark(name, "resources"))
	log, err := os.OpenFile(m.consoleLog(name)+".vmm", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	clearLaunch, err := m.beginLaunch(name, memoryReservation(s))
	if err != nil {
		return err
	}
	cmd := monitorCommand(nil, executable, args...)
	cmd.Stdout, cmd.Stderr = log, log
	// A session of its own: the machine outlives the node API.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		clearLaunch()
		return fmt.Errorf("start cloud-hypervisor: %w", err)
	}
	// A monitor outlives the API process, so publish its allocation together
	// with its PID. Failed bookkeeping must not leave an unaccounted guest.
	rollback := func(err error) error {
		_ = cmd.Process.Kill()
		waitErr := cmd.Wait()
		if cmd.ProcessState == nil {
			return fmt.Errorf("%w; monitor exit was not confirmed: %v", err, waitErr)
		}
		clearLaunch()
		return err
	}
	if err := os.WriteFile(m.pidFile(name)+".config", []byte(configKey(s)), 0o600); err != nil {
		return rollback(err)
	}
	if err := os.WriteFile(m.mark(name, "memory-mib"), []byte(strconv.FormatInt(memoryReservation(s), 10)), 0o600); err != nil {
		return rollback(err)
	}
	if err := os.WriteFile(m.pidFile(name), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		return rollback(err)
	}
	ready, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.finishLaunch(ready, name, cmd.Process.Pid, clearLaunch); err != nil {
		return rollback(err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// finishLaunch keeps the starting reservation until accounting can recognize
// the exact monitor. Start's close-on-exec handshake can finish before Linux
// publishes the new process's cmdline in /proc.
func (m *Machines) finishLaunch(ctx context.Context, name string, pid int, clearLaunch func()) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if pid > 0 && m.pid(name) == pid {
			clearLaunch()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("monitor %s pid %d was not recognized: %w", name, pid, ctx.Err())
		case <-ticker.C:
		}
	}
}

// stop presses the power button, and after a minute stops the monitor.
func (m *Machines) stop(ctx context.Context, name string, pid int) error {
	// A monitor waiting for a machine to arrive has none to power off, and
	// answers nothing until one does.
	if !exists(m.mark(name, "receiving")) {
		press, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = exec.CommandContext(press, "/usr/bin/ch-remote", "--api-socket", m.socket(name), "power-button").Run()
		cancel()
		for i := 0; i < 60 && m.pid(name) != 0; i++ {
			time.Sleep(time.Second)
		}
	}
	if m.pid(name) != 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		time.Sleep(3 * time.Second)
		if m.pid(name) != 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			for i := 0; i < 10 && m.pid(name) != 0; i++ {
				time.Sleep(time.Second)
			}
		}
	}
	if m.pid(name) != 0 {
		return fmt.Errorf("the monitor of %s does not exit", name)
	}
	_ = os.Remove(m.pidFile(name))
	return nil
}

// pid is the machine's monitor process when it runs, 0 otherwise.
func (m *Machines) pid(name string) int {
	raw, err := os.ReadFile(m.pidFile(name))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || !strings.Contains(string(cmdline), m.socket(name)) {
		return 0
	}
	// A monitor that exited and was not reaped is gone as well.
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil && strings.Contains(string(stat), ") Z ") {
		return 0
	}
	return pid
}

// Status reports whether a machine runs.
func (m *Machines) Status(name string, s node.MachineSpec) node.MachineStatus {
	pid := m.pid(name)
	if exists(m.mark(name, "sent")) {
		return node.MachineStatus{Phase: "Sent", Message: "the machine runs on the node it was sent to"}
	}
	if pid == 0 && s.Receive != "" && exists(m.mark(name, "failed")) {
		return node.MachineStatus{Phase: "Failed", Message: "receiving: " + lastLine(m.mark(name, "failed"))}
	}
	if pid == 0 {
		st := node.MachineStatus{Phase: "Stopped"}
		if s.Running {
			// Not started yet, or exited and about to be started again;
			// what the monitor last said tells which.
			st.Phase, st.Message = "Starting", lastLine(m.consoleLog(name)+".vmm")
		}
		return st
	}
	st := node.MachineStatus{Phase: "Running", PID: int32(pid)}
	if s.MaxCPUs > 0 || s.MaxMemoryMiB > 0 {
		m.resourceStatus(name, pid, s, &st)
	}
	switch {
	case exists(m.mark(name, "receiving")):
		st.Phase = "Receiving"
	case s.SendTo != "":
		st.Phase = "Sending"
	}
	if info, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
		t := metav1.NewTime(info.ModTime())
		st.StartedAt = &t
	}
	return st
}

// MarkDeleting asks for a machine to be stopped and forgotten on the next
// pass; until then it is still listed, as Deleting.
func (m *Machines) MarkDeleting(name string) error {
	return os.WriteFile(m.specFile(name)+".deleting", nil, 0o600)
}

// Deleting reports a machine on its way out.
func (m *Machines) Deleting(name string) bool {
	_, err := os.Stat(m.specFile(name) + ".deleting")
	return err == nil
}

// Delete stops a machine and forgets it.
func (m *Machines) Delete(ctx context.Context, name string) error {
	if exists(m.mark(name, "starting")) && m.pid(name) == 0 {
		return fmt.Errorf("machine %s has an interrupted launch; recover its monitor before deleting its reservation", name)
	}
	if pid := m.pid(name); pid != 0 {
		if err := m.stop(ctx, name, pid); err != nil {
			return err
		}
	}
	if l, err := netlink.LinkByName(TapName(name)); err == nil {
		_ = netlink.LinkDel(l)
	}
	_ = os.Remove(m.specFile(name))
	_ = os.Remove(m.specFile(name) + ".deleting")
	for _, f := range []string{"config", "memory-mib", "starting", "resources", "sent", "receiving", "failed"} {
		_ = os.Remove(m.mark(name, f))
	}
	return nil
}

// ConsoleFile is where a machine's serial console is written.
func (m *Machines) ConsoleFile(name string) string { return m.consoleLog(name) }

// Console is the end of a machine's serial console.
func (m *Machines) Console(name string, max int64) ([]byte, error) {
	f, err := os.Open(m.consoleLog(name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > max {
		if _, err := f.Seek(info.Size()-max, 0); err != nil {
			return nil, err
		}
	}
	buf := make([]byte, min(info.Size(), max))
	n, _ := f.Read(buf)
	return buf[:n], nil
}

// ensureTap creates a machine's tap, persistent, in the machines' bridge.
func ensureTap(name string) error {
	br, err := netlink.LinkByName(Bridge)
	if err != nil {
		return fmt.Errorf("bridge %s: %w", Bridge, err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		tap := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: name, MTU: br.Attrs().MTU}, Mode: netlink.TUNTAP_MODE_TAP,
			Flags: netlink.TUNTAP_NO_PI | netlink.TUNTAP_VNET_HDR}
		if err := netlink.LinkAdd(tap); err != nil {
			return fmt.Errorf("tap %s: %w", name, err)
		}
		if link, err = netlink.LinkByName(name); err != nil {
			return err
		}
	}
	if err := netlink.LinkSetMaster(link, br); err != nil {
		return err
	}
	return netlink.LinkSetUp(link)
}

func lastLine(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	return lines[len(lines)-1]
}
