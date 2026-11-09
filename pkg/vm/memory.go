package vm

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

// ErrMemoryBudget means accepting a machine would exceed the node's guest pool.
var ErrMemoryBudget = errors.New("machine memory budget exceeded")

// ErrMigrationMemory means the receiver cannot verify the incoming guest's
// actual allocation before it accepts the migration stream.
var ErrMigrationMemory = errors.New("live migration requires a verified source memory allocation; receiving is disabled with a machine memory budget or explicit resource ceilings")

// checkMemoryBudget runs under budgetMu, including the caller's subsequent
// write: two admissions cannot spend the same remaining memory.
func (m *Machines) checkMemoryBudget(name string, candidate node.MachineSpec) error {
	if m.MemoryBudgetMiB == 0 {
		return nil
	}
	if m.MemoryBudgetMiB < 0 {
		return fmt.Errorf("machine memory budget must not be negative")
	}
	names := map[string]bool{name: true}
	for _, dir := range []struct{ path, suffix string }{{m.dir(), ".json"}, {RunDir, ".pid"}, {RunDir, ".pid.starting"}} {
		entries, err := os.ReadDir(dir.path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read machine reservations: %w", err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), dir.suffix) {
				names[strings.TrimSuffix(entry.Name(), dir.suffix)] = true
			}
		}
	}
	for n := range m.pendingMemory {
		names[n] = true
	}
	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)
	remaining := m.MemoryBudgetMiB
	var exceeded error
	for _, n := range ordered {
		s, err := m.Load(n)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read reservation for machine %s: %w", n, err)
		}
		if n == name {
			s = candidate
		}
		if err == nil || n == name {
			if s.MemoryMiB <= 0 {
				return fmt.Errorf("machine %s has invalid memoryMiB %d", n, s.MemoryMiB)
			}
			if err := validateCeilings(s); err != nil {
				return fmt.Errorf("machine %s: %w", n, err)
			}
		}
		reserved := int64(0)
		if s.Running {
			reserved = memoryReservation(s)
		}
		if m.pid(n) != 0 {
			live, err := m.runningMemoryMiB(n)
			if err != nil {
				return err
			}
			reserved = max(reserved, live)
		}
		if raw, err := os.ReadFile(m.mark(n, "starting")); err == nil {
			starting, err := parseMemoryMiB(n, raw)
			if err != nil {
				return err
			}
			reserved = max(reserved, starting)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read starting reservation for %s: %w", n, err)
		}
		reserved = max(reserved, m.pendingMemory[n])
		if reserved > remaining {
			exceeded = fmt.Errorf("%w: machine %s reserves %d MiB; the %d MiB pool has %d MiB remaining", ErrMemoryBudget, n, reserved, m.MemoryBudgetMiB, remaining)
			remaining = 0
			continue
		}
		remaining -= reserved
	}
	return exceeded
}

// reserveMemory keeps an in-flight start or migration receiver accounted until
// its PID is recorded. Persisted intent is rechecked after a node reboot too.
func (m *Machines) reserveMemory(name string, s node.MachineSpec) (func(), error) {
	m.budgetMu.Lock()
	defer m.budgetMu.Unlock()
	if !s.Running {
		return func() {}, nil
	}
	if s.Receive != "" && m.pid(name) == 0 && (m.MemoryBudgetMiB > 0 || s.MaxCPUs > 0 || s.MaxMemoryMiB > 0) {
		return nil, ErrMigrationMemory
	}
	if m.MemoryBudgetMiB == 0 {
		return func() {}, nil
	}
	if _, ok := m.pendingMemory[name]; ok {
		return nil, fmt.Errorf("%w: machine %s already has an operation in progress", ErrWaiting, name)
	}
	if m.pendingMemory == nil {
		m.pendingMemory = make(map[string]int64)
	}
	m.pendingMemory[name] = memoryReservation(s)
	// Keep any newer persisted intent as well as this operation's allocation.
	desired, err := m.Load(name)
	if os.IsNotExist(err) {
		desired, err = s, nil
	}
	if err == nil {
		err = m.checkMemoryBudget(name, desired)
	}
	if err != nil {
		delete(m.pendingMemory, name)
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			m.budgetMu.Lock()
			defer m.budgetMu.Unlock()
			delete(m.pendingMemory, name)
		})
	}, nil
}

// runningMemoryMiB uses launch-time bookkeeping, not the desired spec: a stop
// or shrink is asynchronous and cannot make the old guest's RAM free early.
func (m *Machines) runningMemoryMiB(name string) (int64, error) {
	raw, err := os.ReadFile(m.mark(name, "memory-mib"))
	if os.IsNotExist(err) {
		// Monitors started before separate allocation bookkeeping carry CPU and
		// memory as the first two fields of their original launch fingerprint.
		fields := strings.SplitN(m.startedWith(name), "/", 3)
		if len(fields) != 3 {
			return 0, fmt.Errorf("machine %s has no readable live memory reservation", name)
		}
		raw, err = []byte(fields[1]), nil
	}
	if err != nil {
		return 0, fmt.Errorf("read live memory reservation for %s: %w", name, err)
	}
	return parseMemoryMiB(name, raw)
}

func parseMemoryMiB(name string, raw []byte) (int64, error) {
	amount, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("machine %s has invalid live memory reservation %q", name, raw)
	}
	return amount, nil
}

// beginLaunch records a reservation before fork: a monitor can survive an API
// crash before its PID is written. An interrupted launch remains charged until
// an operator recovers it, or a host reboot clears /run and the old process.
func (m *Machines) beginLaunch(name string, memoryMiB int64) (func(), error) {
	if m.MemoryBudgetMiB == 0 {
		return func() {}, nil
	}
	f, err := os.OpenFile(m.mark(name, "starting"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("record launch reservation for %s (an interrupted launch needs recovery): %w", name, err)
	}
	clear := func() { _ = os.Remove(m.mark(name, "starting")) }
	_, err = f.WriteString(strconv.FormatInt(memoryMiB, 10))
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		clear()
		return nil, err
	}
	return clear, nil
}
