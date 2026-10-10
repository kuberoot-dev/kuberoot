package vmctl

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	routerv1 "github.com/tym83/kuberoot/pkg/apis/router/v1alpha1"
	v1 "github.com/tym83/kuberoot/pkg/apis/vm/v1alpha1"
)

func cluster(down string, since time.Time) []Node {
	var out []Node
	for _, n := range []string{"a", "b", "c"} {
		node := Node{Name: n, Address: "10.0.0." + string(rune('1'+n[0]-'a')), Ready: n != down}
		if n == down {
			node.DownSince = since
		}
		out = append(out, node)
	}
	return out
}

func TestPlanPlacesReplicasAndPicksTheLeastLoaded(t *testing.T) {
	now := time.Now()
	m := v1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "web"}, Spec: v1.VirtualMachineSpec{Replicas: 2}}
	p := PlanFor(m, cluster("", now), map[int32]bool{100: true}, map[string]int{"a": 2, "b": 0, "c": 1}, nil, now)
	if p.Waiting != "" || strings.Join(p.ReplicaNodes, ",") != "b,c" || p.Node != "b" {
		t.Errorf("plan = %+v", p)
	}
	if p.Minor != 101 || p.Port != 7801 || p.MAC != MAC("web") {
		t.Errorf("minor %d, port %d, mac %s", p.Minor, p.Port, p.MAC)
	}
	m.Spec.Node = "a"
	if p := PlanFor(m, cluster("", now), nil, map[string]int{"a": 5}, nil, now); p.Node != "a" {
		t.Errorf("the asked-for node was not taken: %+v", p)
	}
}

func TestPlanWaitsForEnoughNodes(t *testing.T) {
	now := time.Now()
	m := v1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "db"}, Spec: v1.VirtualMachineSpec{Replicas: 3}}
	if p := PlanFor(m, cluster("c", now), nil, nil, nil, now); p.Waiting == "" || len(p.ReplicaNodes) != 0 {
		t.Errorf("planned on too few nodes: %+v", p)
	}
}

func TestPlanMovesOffAFailedNodeAfterTheGrace(t *testing.T) {
	now := time.Now()
	m := v1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "db"},
		Status: v1.VirtualMachineStatus{Node: "a", ReplicaNodes: []string{"a", "b", "c"}, Minor: 100, Port: 7800, MAC: "52:54:00:00:00:01"}}
	current := map[string]bool{"a": true, "b": true, "c": true}
	if p := PlanFor(m, cluster("a", now.Add(-10*time.Second)), nil, nil, current, now); p.Node != "a" || p.Moved || p.Waiting == "" {
		t.Errorf("moved within the grace: %+v", p)
	}
	p := PlanFor(m, cluster("a", now.Add(-time.Minute)), nil, nil, current, now)
	if p.Node != "b" || !p.Moved || p.Minor != 100 || p.MAC != "52:54:00:00:00:01" {
		t.Errorf("after the grace: %+v", p)
	}
	// A node removed from the cluster is down for good.
	if p := PlanFor(m, cluster("", now)[1:], nil, nil, current, now); p.Node != "b" || !p.Moved {
		t.Errorf("node gone: %+v", p)
	}
	// Up again: stays where it is.
	if p := PlanFor(m, cluster("", now), nil, nil, current, now); p.Node != "a" || p.Moved {
		t.Errorf("node ready: %+v", p)
	}
}

func TestMACIsStableAndLocal(t *testing.T) {
	if MAC("a") != MAC("a") || MAC("a") == MAC("b") || !strings.HasPrefix(MAC("a"), "52:54:") {
		t.Error("MAC is not stable, distinct and locally administered")
	}
}

func TestPlanMovesOnlyToACurrentCopy(t *testing.T) {
	now := time.Now()
	m := v1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "db"},
		Status: v1.VirtualMachineStatus{Node: "a", ReplicaNodes: []string{"a", "b", "c"}, Minor: 100, Port: 7800}}
	// b is resyncing: c takes the machine.
	if p := PlanFor(m, cluster("a", now.Add(-time.Minute)), nil, nil, map[string]bool{"c": true}, now); p.Node != "c" {
		t.Errorf("moved to %s, want the current copy on c", p.Node)
	}
	if p := PlanFor(m, cluster("a", now.Add(-time.Minute)), nil, nil, map[string]bool{}, now); p.Moved || p.Waiting == "" {
		t.Errorf("moved without a current copy: %+v", p)
	}
}

func TestNewestLeaseWins(t *testing.T) {
	now := time.Now()
	leases := []routerv1.Lease{
		{MAC: "52:54:00:aa:bb:cc", Address: "10.123.0.242", Expires: metav1.NewTime(now.Add(12 * time.Hour))},
		{MAC: "52:54:00:AA:BB:CC", Address: "10.123.0.241", Expires: metav1.NewTime(now.Add(2 * time.Hour))},
		{MAC: "52:54:00:00:00:01", Address: "10.123.0.10", Expires: metav1.NewTime(now.Add(time.Hour))},
	}
	got := newestLeases(leases)
	if got["52:54:00:aa:bb:cc"] != "10.123.0.242" || got["52:54:00:00:00:01"] != "10.123.0.10" {
		t.Fatalf("got %v", got)
	}
}

func TestSystemSerialPointsToTheSeed(t *testing.T) {
	m := &v1.VirtualMachine{}
	m.Name = "desk"
	if got := systemSerial(m); got != "" {
		t.Fatalf("a machine without user data got %q", got)
	}
	seedBase.Store("http://10.123.0.1:8091")
	defer seedBase.Store("")
	m.Spec.UserData = "#cloud-config\n"
	if got := systemSerial(m); got != "ds=nocloud;s=http://10.123.0.1:8091/desk/" {
		t.Fatalf("got %q", got)
	}
}
