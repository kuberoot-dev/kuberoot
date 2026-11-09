package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

func budgetMachines(t *testing.T, budget int64) *Machines {
	t.Helper()
	oldState, oldRun := StateDir, RunDir
	StateDir, RunDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { StateDir, RunDir = oldState, oldRun })
	return &Machines{MemoryBudgetMiB: budget}
}

func guestMemory(memory int64) node.MachineSpec {
	return node.MachineSpec{CPUs: 1, MemoryMiB: memory, Running: true, Volumes: []string{"root"}}
}

func TestMemoryBudgetConcurrentAdmission(t *testing.T) {
	m := budgetMachines(t, 512)
	results := make(chan error, 20)
	for i := range 20 {
		go func() { results <- m.Save(fmt.Sprintf("vm-%02d", i), guestMemory(128)) }()
	}
	accepted := 0
	for range 20 {
		if err := <-results; err == nil {
			accepted++
		} else if !errors.Is(err, ErrMemoryBudget) {
			t.Fatal(err)
		}
	}
	if accepted != 4 || len(m.Names()) != 4 {
		t.Fatalf("accepted %d; persisted %v", accepted, m.Names())
	}
}

func TestMemoryBudgetPendingStart(t *testing.T) {
	m := budgetMachines(t, 1024)
	release, err := m.reserveMemory("starting", guestMemory(768))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := m.reserveMemory("starting", guestMemory(768)); !errors.Is(err, ErrWaiting) {
		t.Fatalf("duplicate operation: %v", err)
	}
	if err := m.Save("another", guestMemory(512)); !errors.Is(err, ErrMemoryBudget) {
		t.Fatalf("pending start overcommitted: %v", err)
	}
	release()
	if err := m.Save("another", guestMemory(512)); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryBudgetRefusesUnverifiedReceive(t *testing.T) {
	m := budgetMachines(t, 1024)
	receiver := guestMemory(512)
	receiver.Receive = "tcp:0.0.0.0:9001"
	if err := m.Save("arriving", receiver); !errors.Is(err, ErrMigrationMemory) {
		t.Fatalf("unverified receiver admitted: %v", err)
	}
	// A raw specification from an older node must not bypass admission
	// when it is reconciled after a restart.
	legacy := &Machines{}
	if err := legacy.Save("arriving", receiver); err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(context.Background(), "arriving", receiver); !errors.Is(err, ErrMigrationMemory) {
		t.Fatalf("restored receiver admitted: %v", err)
	}
	receiver.Running = false
	if err := m.Save("arriving", receiver); err != nil {
		t.Fatalf("existing receiver cannot be stopped: %v", err)
	}
	if err := m.Apply(context.Background(), "arriving", receiver); err != nil {
		t.Fatalf("receiver stop rejected: %v", err)
	}
	if err := m.MarkDeleting("arriving"); err != nil {
		t.Fatalf("receiver deletion rejected: %v", err)
	}
}

func TestSendRequiresRunningConfiguration(t *testing.T) {
	m := budgetMachines(t, 1024)
	running := guestMemory(512)
	if err := os.WriteFile(m.mark("source", "config"), []byte(configKey(running)), 0o600); err != nil {
		t.Fatal(err)
	}
	send := running
	send.MemoryMiB, send.SendTo = 256, "tcp:10.0.0.2:9001"
	if err := m.checkSendConfig("source", send); err == nil {
		t.Fatal("migration could send more RAM than declared at its target")
	}
	send.MemoryMiB = 512
	if err := m.checkSendConfig("source", send); err != nil {
		t.Fatalf("matching send rejected: %v", err)
	}
}

func TestMemoryBudgetRechecksPersistedIntent(t *testing.T) {
	m := budgetMachines(t, 1024)
	legacy := &Machines{}
	for _, name := range []string{"a", "b"} {
		if err := legacy.Save(name, guestMemory(768)); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Apply(context.Background(), "a", guestMemory(768)); !errors.Is(err, ErrMemoryBudget) {
		t.Fatalf("reboot admitted persisted overcommit: %v", err)
	}
	stopped := guestMemory(768)
	stopped.Running = false
	if err := m.Save("a", stopped); err != nil {
		t.Fatalf("cannot stop a machine to recover an overcommitted pool: %v", err)
	}
	release, err := m.reserveMemory("b", guestMemory(768))
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestMemoryBudgetCorruptSpecFailsClosed(t *testing.T) {
	m := budgetMachines(t, 1024)
	if err := m.Save("broken", guestMemory(128)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"{broken", "{}", "null"} {
		if err := os.WriteFile(m.specFile("broken"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := m.Save("new", guestMemory(128)); err == nil || errors.Is(err, ErrMemoryBudget) {
			t.Fatalf("corrupt persisted spec %q was skipped: %v", raw, err)
		}
	}
}

func TestMemoryBudgetLiveShrinkAndStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("live process verification uses /proc")
	}
	m := budgetMachines(t, 768)
	if err := m.Save("old", guestMemory(512)); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	cmd.Args[0] = m.socket("old")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for path, data := range map[string]string{m.pidFile("old"): strconv.Itoa(cmd.Process.Pid), m.mark("old", "memory-mib"): "512"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Wait for the spawned process to expose its expected identity in /proc
	// before testing the accounting of a live monitor.
	deadline := time.Now().Add(5 * time.Second)
	for m.pid("old") != cmd.Process.Pid {
		if time.Now().After(deadline) {
			cmdline, cmdlineErr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", cmd.Process.Pid))
			stat, statErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
			t.Fatalf("test monitor pid %d not recognized within 5s: cmdline=%q (%v), stat=%q (%v)",
				cmd.Process.Pid, cmdline, cmdlineErr, stat, statErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, running := range []bool{true, false} {
		small := guestMemory(256)
		small.Running = running
		if err := m.Save("old", small); err != nil {
			t.Fatal(err)
		}
		if err := m.Save("new", guestMemory(512)); !errors.Is(err, ErrMemoryBudget) {
			t.Fatalf("running=%v released live memory early: %v", running, err)
		}
	}
	// An administrator can reduce the configured pool below existing usage.
	// Stopping must remain possible without lending the guest's live RAM out.
	m.MemoryBudgetMiB = 256
	stopped := guestMemory(256)
	stopped.Running = false
	if err := m.Save("old", stopped); err != nil {
		t.Fatalf("over-budget live guest cannot be stopped: %v", err)
	}
	if err := m.Save("new", guestMemory(128)); !errors.Is(err, ErrMemoryBudget) {
		t.Fatalf("stop request released an over-budget live guest: %v", err)
	}
	m.MemoryBudgetMiB = 768
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err := m.Save("new", guestMemory(512)); err != nil {
		t.Fatalf("exited guest still charged: %v", err)
	}
}

func TestMemoryBudgetInterruptedLaunch(t *testing.T) {
	m := budgetMachines(t, 1024)
	clear, err := m.beginLaunch("orphan", 768)
	if err != nil {
		t.Fatal(err)
	}
	defer clear()
	// Simulate an API restart: no pending map and no published PID exist.
	restarted := &Machines{MemoryBudgetMiB: 1024}
	if err := restarted.Save("new", guestMemory(512)); !errors.Is(err, ErrMemoryBudget) {
		t.Fatalf("interrupted launch lost its reservation: %v", err)
	}
	if _, err := restarted.beginLaunch("orphan", 768); err == nil {
		t.Fatal("second monitor could start for an interrupted launch")
	}
	if err := restarted.Delete(context.Background(), "orphan"); err == nil {
		t.Fatal("delete released a possibly live untracked monitor")
	}
	clear()
	if err := restarted.Save("new", guestMemory(512)); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryBudgetLaunchIdentity(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("live process verification uses /proc")
	}
	m := budgetMachines(t, 768)
	if err := m.Save("starting", guestMemory(512)); err != nil {
		t.Fatal(err)
	}
	clear, err := m.beginLaunch("starting", 512)
	if err != nil {
		t.Fatal(err)
	}
	defer clear()
	cmd := exec.Command("sleep", "60")
	cmd.Args[0] = m.socket("starting")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	// Withhold identity publication to deterministically model an exec whose
	// cmdline is not visible yet. Cancellation must leave its reservation held.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.finishLaunch(canceled, "starting", cmd.Process.Pid, clear); !errors.Is(err, context.Canceled) {
		t.Fatalf("unrecognized launch completed: %v", err)
	}
	if !exists(m.mark("starting", "starting")) {
		t.Fatal("unrecognized launch lost its reservation")
	}
	stopped := guestMemory(128)
	stopped.Running = false
	if err := m.Save("starting", stopped); err != nil {
		t.Fatal(err)
	}
	restarted := &Machines{MemoryBudgetMiB: 768}
	if err := restarted.Save("another", guestMemory(512)); !errors.Is(err, ErrMemoryBudget) {
		t.Fatalf("unrecognized launch released RAM after an API restart: %v", err)
	}

	ready, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	var finishErr error
	go func() {
		finishErr = m.finishLaunch(ready, "starting", cmd.Process.Pid, clear)
		close(done)
	}()
	defer func() { cancel(); <-done }()
	if err := os.WriteFile(m.mark("starting", "memory-mib"), []byte("512"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.pidFile("starting"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	// The goroutine is joined by the defer, including assertion failures.
	select {
	case <-done:
		if finishErr != nil {
			t.Fatal(finishErr)
		}
	case <-ready.Done():
		t.Fatal("published monitor identity was not recognized")
	}
	if exists(m.mark("starting", "starting")) {
		t.Fatal("recognized launch retained its starting marker")
	}
	if err := restarted.Save("another", guestMemory(512)); !errors.Is(err, ErrMemoryBudget) {
		t.Fatalf("published monitor lost its live reservation: %v", err)
	}
}

func TestLiveMemoryBookkeepingCompatibility(t *testing.T) {
	m := budgetMachines(t, 1024)
	if err := os.WriteFile(m.mark("old", "config"), []byte(configKey(guestMemory(512))), 0o600); err != nil {
		t.Fatal(err)
	}
	if amount, err := m.runningMemoryMiB("old"); err != nil || amount != 512 {
		t.Fatalf("legacy launch memory: %d, %v", amount, err)
	}
	if err := os.WriteFile(m.mark("old", "memory-mib"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.runningMemoryMiB("old"); err == nil {
		t.Fatal("corrupt live metadata silently fell back to the fingerprint")
	}
}

func TestMemoryBudgetOptIn(t *testing.T) {
	m := budgetMachines(t, 0)
	if err := m.Save("legacy", guestMemory(1<<62)); err != nil {
		t.Fatalf("zero budget changed legacy admission: %v", err)
	}
	m.MemoryBudgetMiB = -1
	if err := m.Save("invalid", guestMemory(128)); err == nil {
		t.Fatal("negative budget admitted a machine")
	}
}
