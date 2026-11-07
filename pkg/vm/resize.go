package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

// ErrMachineSpec identifies a request that cannot describe a supported guest.
var ErrMachineSpec = errors.New("invalid machine specification")

// ErrMigrationCeilings protects bounded guests until both allocation and
// asynchronous migration completion can be verified before source shutdown.
var ErrMigrationCeilings = errors.New("live migration with explicit resource ceilings requires verified memory and completion contracts; send is disabled")

func validateCeilings(s node.MachineSpec) error {
	if s.MaxCPUs < 0 || (s.MaxCPUs > 0 && (s.CPUs <= 0 || s.CPUs > s.MaxCPUs)) {
		return fmt.Errorf("%w: maxCPUs must be zero or at least the positive cpus request", ErrMachineSpec)
	}
	if s.MaxMemoryMiB < 0 || (s.MaxMemoryMiB > 0 && (s.MemoryMiB <= 0 || s.MemoryMiB > s.MaxMemoryMiB)) {
		return fmt.Errorf("%w: maxMemoryMiB must be zero or at least the positive memoryMiB request", ErrMachineSpec)
	}
	if s.MaxMemoryMiB > 0 {
		// CH's virtio-mem region requires 128 MiB alignment. Applying the
		// same step to requests keeps shrink/restart boot shapes valid too.
		if s.MaxMemoryMiB > math.MaxInt64>>20 || s.MaxMemoryMiB%128 != 0 || s.MemoryMiB%128 != 0 {
			return fmt.Errorf("%w: bounded memoryMiB and maxMemoryMiB must be multiples of 128 MiB and fit in signed bytes", ErrMachineSpec)
		}
	}
	return nil
}

func memoryReservation(s node.MachineSpec) int64 {
	if s.MaxMemoryMiB > 0 {
		return s.MaxMemoryMiB
	}
	return s.MemoryMiB
}

type machineResources struct {
	CPUs         int32 `json:"cpus"`
	MemoryMiB    int64 `json:"memoryMiB"`
	MaxCPUs      int32 `json:"maxCPUs"`
	MaxMemoryMiB int64 `json:"maxMemoryMiB"`
	ACPIHotplug  bool  `json:"acpiHotplug,omitempty"`
}

// vmInfo intentionally omits memory_actual_size: ballooning and asynchronous
// guest onlining make that field unsuitable for resize decisions or accounting.
type vmInfo struct {
	State  string `json:"state"`
	Config struct {
		CPUs struct {
			Boot        int32 `json:"boot_vcpus"`
			Max         int32 `json:"max_vcpus"`
			ACPIHotplug bool  `json:"acpi_hotplug"`
		} `json:"cpus"`
		Memory struct {
			Size          uint64          `json:"size"`
			HotplugSize   uint64          `json:"hotplug_size"`
			Hotplugged    uint64          `json:"hotplugged_size"`
			HotplugMethod string          `json:"hotplug_method"`
			Zones         json.RawMessage `json:"zones"`
			File          *string         `json:"file"`
		} `json:"memory"`
	} `json:"config"`
}

func parseResources(raw []byte) (machineResources, error) {
	var info vmInfo
	var out machineResources
	if err := json.Unmarshal(raw, &info); err != nil {
		return out, fmt.Errorf("decode vm.info: %w", err)
	}
	if info.State != "Running" {
		return out, fmt.Errorf("vm.info state is %q, not Running", info.State)
	}
	cpu, mem := info.Config.CPUs, info.Config.Memory
	if cpu.Boot <= 0 || cpu.Max < cpu.Boot || mem.Size == 0 || mem.Size > math.MaxInt64 ||
		mem.HotplugSize > math.MaxInt64-mem.Size || mem.Hotplugged > mem.HotplugSize {
		return out, fmt.Errorf("vm.info has invalid CPU or memory bounds")
	}
	if mem.File != nil || (len(mem.Zones) > 0 && string(mem.Zones) != "null" && string(mem.Zones) != "[]") ||
		(mem.HotplugSize > 0 && mem.HotplugMethod != "VirtioMem") {
		return out, fmt.Errorf("vm.info uses unsupported memory zones, file backing or hotplug method")
	}
	requested, capacity := mem.Size+mem.Hotplugged, mem.Size+mem.HotplugSize
	if requested%(1<<20) != 0 || capacity%(1<<20) != 0 {
		return out, fmt.Errorf("vm.info memory is not a whole MiB")
	}
	out = machineResources{CPUs: cpu.Boot, MaxCPUs: cpu.Max, MemoryMiB: int64(requested >> 20), MaxMemoryMiB: int64(capacity >> 20), ACPIHotplug: cpu.ACPIHotplug}
	return out, nil
}

func (m *Machines) remoteCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if m.remote != nil {
		return m.remote(ctx, name, args...)
	}
	return exec.CommandContext(ctx, "/usr/bin/ch-remote", append([]string{"--api-socket", m.socket(name)}, args...)...).CombinedOutput()
}

func (m *Machines) currentResources(ctx context.Context, name string) (machineResources, error) {
	raw, err := m.remoteCommand(ctx, name, "info")
	if err != nil {
		return machineResources{}, fmt.Errorf("vm.info: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	return parseResources(raw)
}

func (m *Machines) launchPreflightFailed(ctx context.Context, name string, pid int, preflightErr error) error {
	actual, observeErr := m.currentResources(ctx, name)
	status, reason := metav1.ConditionFalse, "LaunchPreflightFailed"
	if observeErr != nil {
		status, reason = metav1.ConditionUnknown, "ObservationFailed"
	}
	return errors.Join(preflightErr, observeErr, m.recordResources(name, pid, actual, status, reason, preflightErr.Error()))
}

type resourceObservation struct {
	PID       int              `json:"pid"`
	Applied   machineResources `json:"applied"`
	Condition metav1.Condition `json:"condition"`
}

// recordResources keeps status tied to this monitor, including partial success.
// It is only an observation: reservation always uses the immutable capacity.
func (m *Machines) recordResources(name string, pid int, actual machineResources, status metav1.ConditionStatus, reason, message string) error {
	next := resourceObservation{PID: pid, Applied: actual, Condition: metav1.Condition{
		Type: "ResourcesReady", Status: status, Reason: reason, Message: message, LastTransitionTime: metav1.Now(),
	}}
	var previous resourceObservation
	if raw, err := os.ReadFile(m.mark(name, "resources")); err == nil && json.Unmarshal(raw, &previous) == nil && previous.PID == pid &&
		previous.Condition.Status == status {
		next.Condition.LastTransitionTime = previous.Condition.LastTransitionTime
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	return writeAtomic(m.mark(name, "resources"), raw)
}

func (m *Machines) resourceStatus(name string, pid int, desired node.MachineSpec, st *node.MachineStatus) {
	var observed resourceObservation
	if raw, err := os.ReadFile(m.mark(name, "resources")); err == nil && json.Unmarshal(raw, &observed) == nil && observed.PID == pid {
		st.CPUs, st.MemoryMiB = observed.Applied.CPUs, observed.Applied.MemoryMiB
		if observed.Condition.Status == metav1.ConditionTrue && (st.CPUs != desired.CPUs || st.MemoryMiB != desired.MemoryMiB) {
			observed.Condition.Status, observed.Condition.Reason, observed.Condition.Message = metav1.ConditionFalse, "Pending", "the requested resource size has not been applied yet"
		}
		st.Conditions = []metav1.Condition{observed.Condition}
		if observed.Condition.Status != metav1.ConditionTrue {
			st.Message = observed.Condition.Message
		}
	}
}

// reconcileResources never shrinks in place: a guest may refuse to release RAM
// indefinitely. It observes CH's committed requests after each attempt, so a
// successful CPU resize followed by a failed RAM resize is safely retried.
func (m *Machines) reconcileResources(ctx context.Context, name string, pid int, s node.MachineSpec) (restart bool, result error) {
	actual, err := m.currentResources(ctx, name)
	if err != nil {
		if recordErr := m.recordResources(name, pid, machineResources{}, metav1.ConditionUnknown, "ObservationFailed", err.Error()); recordErr != nil {
			return false, errors.Join(err, recordErr)
		}
		return false, err
	}
	defer func() {
		status, reason, message := metav1.ConditionTrue, "Applied", "the VMM accepted the requested CPU and memory size; guest onlining is asynchronous"
		if result != nil {
			status, reason, message = metav1.ConditionFalse, "ResizeFailed", result.Error()
		}
		if errors.Is(result, errCPUHotplugUnavailable) {
			reason = "CPUHotplugUnavailable"
		}
		if result != nil && actual.CPUs == 0 {
			status, reason = metav1.ConditionUnknown, "ObservationFailed"
		}
		if restart {
			status, reason, message = metav1.ConditionFalse, "RestartRequired", "shrinking requires a guest restart"
		}
		if err := m.recordResources(name, pid, actual, status, reason, message); err != nil {
			result = errors.Join(result, err)
		}
	}()
	if (s.MaxCPUs > 0 && actual.MaxCPUs != s.MaxCPUs) || (s.MaxMemoryMiB > 0 && actual.MaxMemoryMiB != s.MaxMemoryMiB) {
		return false, fmt.Errorf("running monitor capacity differs from its immutable ceilings; refusing live resize")
	}
	if s.CPUs < actual.CPUs || s.MemoryMiB < actual.MemoryMiB {
		return true, nil
	}
	if s.CPUs > actual.CPUs && s.MaxCPUs > 0 && m.architecture() == "arm64" && !actual.ACPIHotplug {
		return false, fmt.Errorf("%w: this running guest was started without ACPI CPU hotplug; an explicit maintenance restart is required before CPU growth", errCPUHotplugUnavailable)
	}
	resize := func(axis, value string) error {
		out, callErr := m.remoteCommand(ctx, name, "resize", axis, value)
		// Even an error may follow a committed change. Never report desired as
		// applied, and do not restart a guest just because a response was lost.
		observed, observeErr := m.currentResources(ctx, name)
		if observeErr == nil {
			actual = observed
		} else {
			actual = machineResources{}
		}
		if callErr != nil {
			callErr = fmt.Errorf("resize %s: %w: %s", axis, callErr, strings.TrimSpace(string(out)))
		}
		return errors.Join(callErr, observeErr)
	}
	if s.CPUs != actual.CPUs {
		if s.MaxCPUs == 0 {
			return true, nil
		}
		if err := resize("--cpus", strconv.FormatInt(int64(s.CPUs), 10)); err != nil {
			return false, err
		}
	}
	if s.MemoryMiB != actual.MemoryMiB {
		if s.MaxMemoryMiB == 0 {
			return true, nil
		}
		if err := resize("--memory", strconv.FormatInt(s.MemoryMiB<<20, 10)); err != nil {
			return false, err
		}
	}
	if actual.CPUs != s.CPUs || actual.MemoryMiB != s.MemoryMiB {
		return false, fmt.Errorf("VMM did not apply the requested size: observed %d CPUs/%d MiB, requested %d CPUs/%d MiB", actual.CPUs, actual.MemoryMiB, s.CPUs, s.MemoryMiB)
	}
	return false, nil
}
