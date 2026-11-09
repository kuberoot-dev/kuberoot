package vm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

const testARMCapabilities = `{"schema_version":1,"architecture":"aarch64","backend":"kvm","preferred_gic":2,"capabilities":["arm-gicv2-acpi-cpu-hotadd-v1"],"maximum_hotadd_vcpus":8}`

func TestARMCPUCapabilityContract(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
	}{
		{"schema", `"schema_version":1`, `"schema_version":2`},
		{"architecture", `"aarch64"`, `"x86_64"`},
		{"backend", `"kvm"`, `"mshv"`},
		{"v3 preferred", `"preferred_gic":2`, `"preferred_gic":3`},
		{"no gic", `"preferred_gic":2`, `"preferred_gic":null`},
		{"no capability", `"arm-gicv2-acpi-cpu-hotadd-v1"`, `"other"`},
		{"invalid maximum", `"maximum_hotadd_vcpus":8`, `"maximum_hotadd_vcpus":9`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateARMCPUCapability([]byte(strings.Replace(testARMCapabilities, tc.old, tc.replacement, 1)), 4); err == nil {
				t.Fatal("accepted unsupported host contract")
			}
		})
	}
	for _, max := range []int32{0, -1, 9} {
		if err := validateARMCPUCapability([]byte(testARMCapabilities), max); err == nil {
			t.Fatalf("accepted ceiling %d", max)
		}
	}
	for _, raw := range []string{"", "null", "{}", "{", testARMCapabilities + "{}", strings.Repeat(" ", maxCapabilityOutput+1)} {
		if err := validateARMCPUCapability([]byte(raw), 4); err == nil {
			t.Fatalf("accepted invalid payload of length %d", len(raw))
		}
	}
	if err := validateARMCPUCapability([]byte(testARMCapabilities), 8); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchCapabilitiesAreFreshAndArchitectureSpecific(t *testing.T) {
	m := &Machines{arch: "arm64"}
	calls := 0
	m.probeCapabilities = func(context.Context) ([]byte, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("replacement binary has no verified probe")
		}
		return []byte(testARMCapabilities), nil
	}
	s := boundedMachine()
	plan, err := m.prepareLaunch(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.close()
	args := strings.Join(m.launchArgs("vm", s, nil, plan.armCPUHotplug), " ")
	if !strings.Contains(args, "--cpus boot=1,max=4,acpi_hotplug=on") || !strings.Contains(args, "--firmware "+Firmware) {
		t.Fatalf("unverified launch mode: %s", args)
	}
	if _, err := m.prepareLaunch(context.Background(), s); !errors.Is(err, errCPUHotplugUnavailable) || calls != 2 {
		t.Fatalf("cached capabilities survived a binary change: calls=%d err=%v", calls, err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		m.arch = arch
		if arch == "arm64" {
			s.MaxCPUs = 0
		}
		plan, err := m.prepareLaunch(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		plan.close()
		if plan.armCPUHotplug || calls != 2 || strings.Contains(strings.Join(m.launchArgs("vm", s, nil, plan.armCPUHotplug), " "), "acpi_hotplug") {
			t.Fatalf("changed legacy launch for %s", arch)
		}
	}
}

func TestARMRunningGuestUsesActualMode(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name, arch string
		mode       *bool
		cpu        int32
		memory     int64
		wantError  bool
	}{
		{"legacy unchanged", "arm64", nil, 1, 1024, false},
		{"legacy RAM growth", "arm64", nil, 1, 2048, false},
		{"missing CPU mode", "arm64", nil, 2, 1024, true},
		{"disabled CPU mode", "arm64", &no, 2, 1024, true},
		{"enabled CPU mode", "arm64", &yes, 2, 2048, false},
		{"x86 old mode", "amd64", nil, 2, 2048, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := budgetMachines(t, 4096)
			m.arch = tc.arch
			m.probeCapabilities = func(context.Context) ([]byte, error) {
				t.Fatal("running VM must not probe the installed executable")
				return nil, nil
			}
			f := &testMonitor{cpus: 1, memoryMiB: 1024}
			m.remote = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				raw, err := f.run(ctx, name, args...)
				if err != nil || args[0] != "info" {
					return raw, err
				}
				var info map[string]any
				if err := json.Unmarshal(raw, &info); err != nil {
					t.Fatal(err)
				}
				cpus := info["config"].(map[string]any)["cpus"].(map[string]any)
				if tc.mode == nil {
					delete(cpus, "acpi_hotplug")
				} else {
					cpus["acpi_hotplug"] = *tc.mode
				}
				return json.Marshal(info)
			}
			s := boundedMachine()
			s.CPUs, s.MemoryMiB = tc.cpu, tc.memory
			restart, err := m.reconcileResources(context.Background(), "vm", 42, s)
			if restart || errors.Is(err, errCPUHotplugUnavailable) != tc.wantError || (err != nil && !tc.wantError) {
				t.Fatalf("restart=%v error=%v", restart, err)
			}
			if tc.wantError {
				var status node.MachineStatus
				m.resourceStatus("vm", 42, s, &status)
				if len(f.calls) != 0 || status.CPUs != 1 || status.MemoryMiB != 1024 || len(status.Conditions) != 1 || status.Conditions[0].Status != metav1.ConditionFalse || status.Conditions[0].Reason != "CPUHotplugUnavailable" {
					t.Fatalf("legacy VM was mutated or its actual resources lost: %+v calls=%v", status, f.calls)
				}
			} else if f.cpus != tc.cpu || f.memoryMiB != tc.memory {
				t.Fatalf("resources not applied: %+v", f)
			}
		})
	}
}

func TestFailedARMPreflightPreservesRunningProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process identity uses Linux procfs")
	}
	for _, path := range []string{"configuration restart", "shrink restart"} {
		t.Run(path, func(t *testing.T) {
			m := budgetMachines(t, 4096)
			m.arch = "arm64"
			m.probeCapabilities = func(context.Context) ([]byte, error) { return nil, errors.New("probe denied") }
			f := &testMonitor{cpus: 2, memoryMiB: 2048}
			m.remote = f.run
			old := boundedMachine()
			old.CPUs, old.MemoryMiB = 2, 2048
			if err := m.Save("vm", old); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sleep", "120")
			cmd.Args[0] = m.socket("vm")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			for filename, data := range map[string]string{m.pidFile("vm"): strconv.Itoa(cmd.Process.Pid), m.mark("vm", "memory-mib"): "4096", m.mark("vm", "config"): configKey(old)} {
				if err := os.WriteFile(filename, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for m.pid("vm") != cmd.Process.Pid {
				if time.Now().After(deadline) {
					t.Fatal("fixture process identity did not become visible")
				}
				time.Sleep(time.Millisecond)
			}
			desired := old
			if path == "configuration restart" {
				desired.SystemSerial = "explicit new boot configuration"
			} else {
				desired.CPUs, desired.MemoryMiB = 1, 1024
			}
			if err := m.Apply(context.Background(), "vm", desired); !errors.Is(err, errCPUHotplugUnavailable) {
				t.Fatalf("unexpected preflight result: %v", err)
			}
			status := m.Status("vm", desired)
			if m.pid("vm") != cmd.Process.Pid || len(f.calls) != 0 || len(status.Conditions) != 1 || status.Conditions[0].Reason != "LaunchPreflightFailed" || status.CPUs != 2 || status.MemoryMiB != 2048 {
				t.Fatalf("failed preflight changed running guest: %+v calls=%v", status, f.calls)
			}
		})
	}
}

func TestMonitorLaunchRetainsProbedExecutable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor execution uses Linux procfs")
	}
	path := filepath.Join(t.TempDir(), "vmm")
	first := "#!/bin/sh\nprintf '%s' '" + testARMCapabilities + "'\n"
	if err := os.WriteFile(path, []byte(first), 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()
	if raw, err := probeHostCapabilities(context.Background(), executable); err != nil || string(raw) != testARMCapabilities {
		t.Fatalf("initial probe: %s %v", raw, err)
	}
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\nprintf replacement\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if raw, err := monitorCommand(context.Background(), executable).Output(); err != nil || string(raw) != testARMCapabilities {
		t.Fatalf("launch resolved replacement binary: %s %v", raw, err)
	}
}

func TestCapabilityOutputRemainsBoundedThroughCopy(t *testing.T) {
	var out capabilityOutput
	if _, err := io.Copy(&out, strings.NewReader(strings.Repeat("x", maxCapabilityOutput+1))); !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("output bound was bypassed: %v", err)
	}
	if out.buffer.Len() > maxCapabilityOutput {
		t.Fatal("probe retained oversized output")
	}
}
