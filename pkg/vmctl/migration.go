package vmctl

import (
	"context"
	"fmt"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha1"
)

// MigrationTimeout ends a live move that has not finished, leaving the
// machine where it was.
var MigrationTimeout = 10 * time.Minute

// migrating: a live move is under way, or is asked for: the machine runs,
// spec.node names another replica node with a current copy of the disk,
// every replica node is up, and the last move there did not fail.
func (c *Controller) migrating(m *v1.VirtualMachine, p Plan, running bool, byName map[string]Node, mine map[string]disk) bool {
	if m.Status.Migration != nil {
		return true
	}
	t := m.Spec.Node
	return running && t != "" && t != p.Node && m.Status.Phase == "Running" && m.Status.FailedMigration != t &&
		slices.Contains(p.ReplicaNodes, t) && allReady(p.ReplicaNodes, byName) && mine[t].diskState == "UpToDate"
}

// allReady: the disk is writable on two nodes only while every copy of it
// is in reach; a copy that comes back to two writers may not tell which
// one it follows.
func allReady(nodes []string, byName map[string]Node) bool {
	for _, n := range nodes {
		if !byName[n].Ready {
			return false
		}
	}
	return true
}

// migrationPort is where the target node receives the machine.
func migrationPort(p Plan) int32 { return 9000 + p.Minor - 100 }

// migrate takes a live move one step further: the disk writable on both
// nodes, the target waiting for the machine, the machine sent, and once it
// runs there, the source cleared and the disk single-writer again.
func (c *Controller) migrate(ctx context.Context, m *v1.VirtualMachine, p Plan, st v1.VirtualMachineStatus,
	byName map[string]Node, mine map[string]disk) v1.VirtualMachineStatus {
	mg := v1.Migration{Target: m.Spec.Node, Phase: "Preparing", StartedAt: metav1.Now()}
	if m.Status.Migration != nil {
		mg = *m.Status.Migration
	}
	src, dst := p.Node, mg.Target
	st.Phase, st.Migration, st.FailedMigration = "Migrating", &mg, m.Status.FailedMigration
	st.Message = fmt.Sprintf("moving alive from %s to %s: %s", src, dst, mg.Phase)
	port := migrationPort(p)
	abort := func(reason string) v1.VirtualMachineStatus {
		klog.Errorf("%s: live move to %s failed: %s", m.Name, dst, reason)
		_ = c.Dynamic.Resource(machinesGVR).Delete(ctx, dst+"."+m.Name, metav1.DeleteOptions{})
		_ = c.ensure(ctx, machinesGVR, src+"."+m.Name, map[string]any{"spec": toMap(ptr(machineSpec(m, p, true)))})
		_ = c.ensureVolumes(ctx, m, p, &st, byName, map[string]bool{src: true}, bothPrimary(mine))
		st.Migration, st.FailedMigration, st.Phase = nil, dst, "Running"
		st.Message = "live move to " + dst + " failed: " + reason
		return st
	}
	if time.Since(mg.StartedAt.Time) > MigrationTimeout {
		return abort("not finished in " + MigrationTimeout.String())
	}
	if !allReady(p.ReplicaNodes, byName) {
		return abort("a node went down")
	}
	switch mg.Phase {
	case "Preparing":
		if phase, _ := c.machinePhase(ctx, dst+"."+m.Name); phase == "Deleting" {
			st.Message = "waiting for the machine left on " + dst + " to go"
			return st
		}
		if err := c.ensureVolumes(ctx, m, p, &st, byName, map[string]bool{src: true, dst: true}, true); err != nil {
			st.Message = err.Error()
			return st
		}
		if mine[dst].role == "Primary" {
			recv := machineSpec(m, p, true)
			recv.Receive = fmt.Sprintf("tcp:0.0.0.0:%d", port)
			if err := c.ensure(ctx, machinesGVR, dst+"."+m.Name, map[string]any{"spec": toMap(&recv)}); err != nil {
				return abort(err.Error())
			}
			mg.Phase = "Receiving"
		}
	case "Receiving":
		switch phase, msg := c.machinePhase(ctx, dst+"."+m.Name); phase {
		case "Receiving":
			send := machineSpec(m, p, true)
			send.SendTo = fmt.Sprintf("tcp:%s:%d", st.ReplicaAddresses[dst], port)
			if err := c.ensure(ctx, machinesGVR, src+"."+m.Name, map[string]any{"spec": toMap(&send)}); err != nil {
				return abort(err.Error())
			}
			mg.Phase = "Sending"
		case "Failed":
			return abort("the target could not receive: " + msg)
		}
	case "Sending":
		srcPhase, srcMsg := c.machinePhase(ctx, src+"."+m.Name)
		dstPhase, dstMsg := c.machinePhase(ctx, dst+"."+m.Name)
		switch {
		case srcPhase == "Sent" && dstPhase == "Running":
			// The machine runs on the target: it is the machine's node now.
			klog.Infof("%s: moved alive from %s to %s", m.Name, src, dst)
			if err := c.ensure(ctx, machinesGVR, dst+"."+m.Name, map[string]any{"spec": toMap(ptr(machineSpec(m, p, true)))}); err != nil {
				st.Message = err.Error()
				return st
			}
			_ = c.Dynamic.Resource(machinesGVR).Delete(ctx, src+"."+m.Name, metav1.DeleteOptions{})
			st.Node, st.Migration, st.Phase, st.Message = dst, nil, "Running", "moved alive from "+src
			p.Node = dst
			_ = c.ensureVolumes(ctx, m, p, &st, byName, map[string]bool{dst: true}, true)
		case srcPhase == "Failed":
			return abort("the source could not send: " + srcMsg)
		case dstPhase == "Failed":
			return abort("the target failed: " + dstMsg)
		}
	}
	return st
}

func (c *Controller) machinePhase(ctx context.Context, name string) (string, string) {
	u, err := c.Dynamic.Resource(machinesGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err.Error()
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	msg, _, _ := unstructured.NestedString(u.Object, "status", "message")
	return phase, msg
}

func ptr[T any](v T) *T { return &v }
