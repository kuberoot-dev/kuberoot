// Package vmctl runs a hypervisor cluster's virtual machines: it places each
// on a node, keeps its disk replicated, starts it elsewhere when its node
// fails, and serves the machines' network its gateway.
package vmctl

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha1"
)

// FailoverAfter is how long a node is down before its machines start on
// another node. The node's heartbeat stops first (about 40 seconds); DRBD's
// quorum keeps a node that is only cut off from writing meanwhile.
var FailoverAfter = 30 * time.Second

// Node is a node of the cluster as the planner sees it.
type Node struct {
	Name, Address string
	Ready         bool
	// Since when it is not ready.
	DownSince time.Time
}

// Plan is where a machine's disk and the machine itself go.
type Plan struct {
	ReplicaNodes []string
	Node         string
	Minor, Port  int32
	MAC          string
	// Moved: the machine starts on a node other than the one it ran on.
	Moved bool
	// Waiting says why the machine cannot run now.
	Waiting string
}

// PlanFor decides a machine's placement. used are the minors other machines
// hold; load counts machines per node.
// upToDate names the nodes whose copy of this machine's disk is current: a
// machine moves only to one of them.
func PlanFor(vm v1.VirtualMachine, nodes []Node, used map[int32]bool, load map[string]int, upToDate map[string]bool, now time.Time) Plan {
	st := vm.Status
	p := Plan{ReplicaNodes: st.ReplicaNodes, Node: st.Node, Minor: st.Minor, Port: st.Port, MAC: st.MAC}
	if p.MAC == "" {
		p.MAC = MAC(vm.Name)
	}
	if p.Minor == 0 {
		for m := int32(100); ; m++ {
			if !used[m] {
				p.Minor = m
				break
			}
		}
		p.Port = 7700 + p.Minor
	}
	byName := map[string]Node{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	if len(p.ReplicaNodes) == 0 {
		want := int(vm.Spec.Replicas)
		if want == 0 {
			want = 3
		}
		var ready []Node
		for _, n := range nodes {
			if n.Ready {
				ready = append(ready, n)
			}
		}
		// The least loaded first; the asked-for node leads.
		sort.SliceStable(ready, func(i, j int) bool {
			if (ready[i].Name == vm.Spec.Node) != (ready[j].Name == vm.Spec.Node) {
				return ready[i].Name == vm.Spec.Node
			}
			if load[ready[i].Name] != load[ready[j].Name] {
				return load[ready[i].Name] < load[ready[j].Name]
			}
			return ready[i].Name < ready[j].Name
		})
		if len(ready) < want {
			p.Waiting = fmt.Sprintf("%d of %d nodes for replicas are ready", len(ready), want)
			return p
		}
		for _, n := range ready[:want] {
			p.ReplicaNodes = append(p.ReplicaNodes, n.Name)
		}
	}
	if p.Node == "" {
		p.Node = p.ReplicaNodes[0]
		return p
	}
	n, known := byName[p.Node]
	if known && n.Ready {
		return p
	}
	// A node gone from the cluster counts as down for good.
	if known && !n.DownSince.IsZero() && now.Sub(n.DownSince) < FailoverAfter {
		p.Waiting = fmt.Sprintf("node %s is down; moving after %s", p.Node, FailoverAfter)
		return p
	}
	// The node is gone: a replica with a current copy of the disk takes the
	// machine.
	for _, r := range p.ReplicaNodes {
		if r != p.Node && byName[r].Ready && upToDate[r] {
			p.Node, p.Moved = r, true
			return p
		}
	}
	p.Waiting = fmt.Sprintf("node %s is down and no other node has a current copy of the disk", p.Node)
	return p
}

// MAC is a machine's stable, locally administered address.
func MAC(name string) string {
	s := sha256.Sum256([]byte(name))
	return fmt.Sprintf("52:54:%02x:%02x:%02x:%02x", s[0], s[1], s[2], s[3])
}
