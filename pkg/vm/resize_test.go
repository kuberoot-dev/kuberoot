package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

func boundedMachine() node.MachineSpec {
	return node.MachineSpec{CPUs: 1, MaxCPUs: 4, MemoryMiB: 1024, MaxMemoryMiB: 4096, Volumes: []string{"root"}, Running: true}
}

func TestMachineArgsCeilings(t *testing.T) {
	m := &Machines{}
	got := strings.Join(m.Args("bounded", boundedMachine(), nil), " ")
	for _, want := range []string{"--cpus boot=1,max=4", "--memory size=1024M,hotplug_method=virtio-mem,hotplug_size=3072M"} {
		if !strings.Contains(got, want) {
			t.Fatalf("args missing %q: %s", want, got)
		}
	}
	s := boundedMachine()
	s.MemoryMiB = s.MaxMemoryMiB
	if got := strings.Join(m.Args("full", s, nil), " "); strings.Contains(got, "hotplug_size=0") {
		t.Fatalf("empty virtio-mem region: %s", got)
	}
	s.MaxCPUs, s.MaxMemoryMiB = 0, 0
	got = strings.Join(m.Args("legacy", s, nil), " ")
	if strings.Contains(got, ",max=") || strings.Contains(got, "hotplug_") {
		t.Fatalf("legacy args changed: %s", got)
	}
}

func TestLaunchFingerprintSeparatesSize(t *testing.T) {
	s := boundedMachine()
	key := configKey(s)
	grown := s
	grown.CPUs, grown.MemoryMiB = 2, 2048
	if configKey(grown) != key {
		t.Fatal("bounded size changed the launch fingerprint")
	}
	for _, changed := range []node.MachineSpec{
		{CPUs: 1, MaxCPUs: 8, MemoryMiB: 1024, MaxMemoryMiB: 4096, Volumes: []string{"root"}},
		{CPUs: 1, MaxCPUs: 4, MemoryMiB: 1024, MaxMemoryMiB: 8192, Volumes: []string{"root"}},
		{CPUs: 1, MaxCPUs: 4, MemoryMiB: 1024, MaxMemoryMiB: 4096, Volumes: []string{"other"}},
	} {
		if configKey(changed) == key {
			t.Fatalf("changed shape has same fingerprint: %+v", changed)
		}
	}
	s.MaxCPUs, s.MaxMemoryMiB = 0, 0
	if configKey(s) != "1/1024/root//" {
		t.Fatalf("legacy bytes changed: %q", configKey(s))
	}
	old := configKey(s)
	s.MemoryMiB = 2048
	if configKey(s) == old {
		t.Fatal("legacy size no longer triggers restart")
	}
	s.MaxCPUs = 4
	old = configKey(s)
	s.CPUs = 2
	if configKey(s) != old {
		t.Fatal("CPU-only ceiling did not permit live CPU growth")
	}
	s.MemoryMiB = 3072
	if configKey(s) == old {
		t.Fatal("CPU ceiling silently enabled RAM hotplug")
	}
}

func TestMachineCeilingsValidationAndImmutability(t *testing.T) {
	m := budgetMachines(t, 4096)
	for _, change := range []func(*node.MachineSpec){
		func(s *node.MachineSpec) { s.CPUs = 5 },
		func(s *node.MachineSpec) { s.MemoryMiB = 8192 },
		func(s *node.MachineSpec) { s.MaxCPUs = -1 },
		func(s *node.MachineSpec) { s.MaxMemoryMiB = -1 },
		func(s *node.MachineSpec) { s.MemoryMiB = 1025 },
		func(s *node.MachineSpec) { s.MaxMemoryMiB = math.MaxInt64 },
	} {
		s := boundedMachine()
		change(&s)
		if err := m.Save("bad", s); !errors.Is(err, ErrMachineSpec) {
			t.Fatalf("accepted %+v: %v", s, err)
		}
	}
	s := boundedMachine()
	if err := m.Save("one", s); err != nil {
		t.Fatal(err)
	}
	s.CPUs, s.MemoryMiB = 2, 2048
	if err := m.Save("one", s); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*node.MachineSpec){
		func(s *node.MachineSpec) { s.MaxCPUs = 8 },
		func(s *node.MachineSpec) { s.MaxMemoryMiB = 8192 },
		func(s *node.MachineSpec) { s.MaxCPUs, s.MaxMemoryMiB = 0, 0 },
	} {
		bad := s
		change(&bad)
		if err := m.Save("one", bad); !errors.Is(err, ErrMachineSpec) {
			t.Fatalf("mutable ceiling: %v", err)
		}
	}
	legacy := guestMemory(17)
	legacy.Running = false
	if err := m.Save("legacy", legacy); err != nil {
		t.Fatal(err)
	}
	legacy.MaxCPUs = 4
	if err := m.Save("legacy", legacy); !errors.Is(err, ErrMachineSpec) {
		t.Fatalf("added ceiling to legacy spec: %v", err)
	}
}

func TestMemoryBudgetChargesCeiling(t *testing.T) {
	m := budgetMachines(t, 4096)
	s := boundedMachine()
	if err := m.Save("one", s); err != nil {
		t.Fatal(err)
	}
	if err := m.Save("two", guestMemory(128)); !errors.Is(err, ErrMemoryBudget) {
		t.Fatalf("ceiling not reserved: %v", err)
	}
	s.MemoryMiB = 512
	if err := m.Save("one", s); err != nil {
		t.Fatal(err)
	}
	restarted := &Machines{MemoryBudgetMiB: 4096}
	if err := restarted.Save("two", guestMemory(128)); !errors.Is(err, ErrMemoryBudget) {
		t.Fatalf("shrink/restart released ceiling: %v", err)
	}
	if err := os.Remove(m.specFile("one")); err != nil {
		t.Fatal(err)
	}
	release, err := m.reserveMemory("starting", s)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := m.Save("two", guestMemory(128)); !errors.Is(err, ErrMemoryBudget) {
		t.Fatalf("pending ceiling not charged: %v", err)
	}
}

type testMonitor struct {
	cpus       int32
	memoryMiB  int64
	failMemory bool
	calls      []string
}

func (f *testMonitor) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if args[0] == "info" {
		return []byte(fmt.Sprintf(`{"state":"Running","memory_actual_size":123,"config":{"cpus":{"boot_vcpus":%d,"max_vcpus":4,"acpi_hotplug":true},"memory":{"size":1073741824,"hotplug_size":3221225472,"hotplugged_size":%d,"hotplug_method":"VirtioMem"}}}`, f.cpus, (f.memoryMiB-1024)<<20)), nil
	}
	f.calls = append(f.calls, strings.Join(args, " "))
	n, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return nil, err
	}
	if args[1] == "--cpus" {
		f.cpus = int32(n)
	}
	if args[1] == "--memory" {
		if f.failMemory {
			f.failMemory = false
			return []byte("injected RAM failure"), errors.New("resize failed")
		}
		f.memoryMiB = n >> 20
	}
	return nil, nil
}

func TestMachineResizePartialSuccess(t *testing.T) {
	m := budgetMachines(t, 4096)
	f := &testMonitor{cpus: 1, memoryMiB: 1024, failMemory: true}
	m.remote = f.run
	s := boundedMachine()
	s.CPUs, s.MemoryMiB = 2, 2048
	restart, err := m.reconcileResources(context.Background(), "vm", 42, s)
	if restart || err == nil {
		t.Fatalf("partial failure: restart=%v err=%v", restart, err)
	}
	var observed resourceObservation
	raw, err := os.ReadFile(m.mark("vm", "resources"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Applied.CPUs != 2 || observed.Applied.MemoryMiB != 1024 || observed.Condition.Status != metav1.ConditionFalse {
		t.Fatalf("false applied status: %+v", observed)
	}
	// A new API process must derive its next operation from CH, not a local
	// previous desired size or memory_actual_size (deliberately 123 above).
	m = &Machines{remote: f.run}
	restart, err = m.reconcileResources(context.Background(), "vm", 42, s)
	if restart || err != nil {
		t.Fatalf("reconcile: %v %v", restart, err)
	}
	want := []string{"resize --cpus 2", "resize --memory 2147483648", "resize --memory 2147483648"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("commands=%v want=%v", f.calls, want)
	}
	var status node.MachineStatus
	m.resourceStatus("vm", 42, s, &status)
	if status.CPUs != 2 || status.MemoryMiB != 2048 || len(status.Conditions) != 1 || status.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("status=%+v", status)
	}
	s.MemoryMiB = 1536
	restart, err = m.reconcileResources(context.Background(), "vm", 42, s)
	if !restart || err != nil || len(f.calls) != 3 {
		t.Fatalf("shrink was hot-applied: %v %v %v", restart, err, f.calls)
	}
}

func TestParseResourcesRejectsUnsafeShape(t *testing.T) {
	f := &testMonitor{cpus: 1, memoryMiB: 2048}
	raw, _ := f.run(context.Background(), "vm", "info")
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["state"] = "Paused" },
		func(v map[string]any) {
			v["config"].(map[string]any)["memory"].(map[string]any)["hotplugged_size"] = 8 << 30
		},
		func(v map[string]any) {
			v["config"].(map[string]any)["memory"].(map[string]any)["size"] = uint64(math.MaxUint64)
		},
		func(v map[string]any) {
			v["config"].(map[string]any)["memory"].(map[string]any)["zones"] = []any{map[string]any{"size": 1024}}
		},
	} {
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		mutate(v)
		changed, _ := json.Marshal(v)
		if _, err := parseResources(changed); err == nil {
			t.Fatalf("accepted %s", changed)
		}
	}
}

func TestMachineResizeLostResponseAfterCommit(t *testing.T) {
	m := budgetMachines(t, 4096)
	f := &testMonitor{cpus: 2, memoryMiB: 1024}
	m.remote = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := f.run(ctx, name, args...)
		if args[0] == "resize" {
			return nil, errors.New("response lost after commit")
		}
		return out, err
	}
	s := boundedMachine()
	s.CPUs, s.MemoryMiB = 2, 2048
	restart, err := m.reconcileResources(context.Background(), "vm", 42, s)
	if restart || err == nil {
		t.Fatalf("lost response: restart=%v err=%v", restart, err)
	}
	var st node.MachineStatus
	m.resourceStatus("vm", 42, s, &st)
	if st.CPUs != 2 || st.MemoryMiB != 2048 || st.Conditions[0].Status != metav1.ConditionFalse {
		t.Fatalf("committed state lost: %+v", st)
	}
	m = &Machines{remote: f.run}
	if restart, err := m.reconcileResources(context.Background(), "vm", 42, s); restart || err != nil {
		t.Fatalf("recovery: %v %v", restart, err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("committed resize retried: %v", f.calls)
	}
}

func TestMachineResizeObservationFailure(t *testing.T) {
	m := budgetMachines(t, 4096)
	f := &testMonitor{cpus: 1, memoryMiB: 1024}
	failInfo := false
	m.remote = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "info" && failInfo {
			return nil, errors.New("info temporarily unavailable")
		}
		out, err := f.run(ctx, name, args...)
		if args[0] == "resize" {
			failInfo = true
		}
		return out, err
	}
	s := boundedMachine()
	s.CPUs, s.MemoryMiB = 2, 2048
	if restart, err := m.reconcileResources(context.Background(), "vm", 42, s); restart || err == nil {
		t.Fatalf("observation failure: %v %v", restart, err)
	}
	var st node.MachineStatus
	m.resourceStatus("vm", 42, s, &st)
	if st.CPUs != 0 || st.MemoryMiB != 0 || st.Conditions[0].Status != metav1.ConditionUnknown {
		t.Fatalf("invented applied state: %+v", st)
	}
	m = &Machines{remote: f.run}
	if restart, err := m.reconcileResources(context.Background(), "vm", 42, s); restart || err != nil {
		t.Fatalf("recovery: %v %v", restart, err)
	}
	want := []string{"resize --cpus 2", "resize --memory 2147483648"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("lost observation repeated CPU resize: %v", f.calls)
	}
}

func TestCeilingsRefuseUnverifiedReceiveWithoutPool(t *testing.T) {
	m := budgetMachines(t, 0)
	for _, axis := range []string{"CPU", "memory"} {
		s := boundedMachine()
		s.Receive = "tcp:0.0.0.0:9001"
		if axis == "CPU" {
			s.MaxMemoryMiB = 0
		} else {
			s.MaxCPUs = 0
		}
		if err := m.Save(axis, s); !errors.Is(err, ErrMigrationMemory) {
			t.Fatalf("%s ceiling receive admitted: %v", axis, err)
		}
		// A restored raw specification must pass the same gate before launch.
		if err := m.Apply(context.Background(), axis, s); !errors.Is(err, ErrMigrationMemory) {
			t.Fatalf("%s restored receiver launched: %v", axis, err)
		}
		s.Running = false
		if err := m.Save(axis, s); err != nil {
			t.Fatalf("receiver stop denied: %v", err)
		}
	}
}

func TestCeilingsRefuseAsynchronousSend(t *testing.T) {
	m := budgetMachines(t, 0)
	s := boundedMachine()
	s.SendTo = "tcp:10.0.0.2:9001"
	if err := m.Save("source", s); !errors.Is(err, ErrMigrationCeilings) {
		t.Fatalf("bounded send admitted: %v", err)
	}
	if err := m.Apply(context.Background(), "source", s); !errors.Is(err, ErrMigrationCeilings) {
		t.Fatalf("restored bounded sender applied: %v", err)
	}
	if err := m.checkSendConfig("source", s); !errors.Is(err, ErrMigrationCeilings) {
		t.Fatalf("send config guard bypassed: %v", err)
	}
	s.Running = false
	if err := m.Save("source", s); err != nil {
		t.Fatalf("cannot stop sender: %v", err)
	}
}
