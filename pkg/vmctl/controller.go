package vmctl

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	nodev1 "github.com/tym83/kuberoot/pkg/apis/node/v1alpha1"
	routerv1 "github.com/tym83/kuberoot/pkg/apis/router/v1alpha1"
	v1 "github.com/tym83/kuberoot/pkg/apis/vm/v1alpha1"
	"github.com/tym83/kuberoot/pkg/router"
	"github.com/tym83/kuberoot/pkg/vm"
)

const finalizer = "vm.kuberoot.dev/cleanup"

var (
	vmsGVR      = v1.GroupVersion.WithResource("virtualmachines")
	volumesGVR  = schema.GroupVersionResource{Group: "node.kuberoot.dev", Version: "v1alpha1", Resource: "volumes"}
	machinesGVR = schema.GroupVersionResource{Group: "node.kuberoot.dev", Version: "v1alpha1", Resource: "machines"}
)

// Controller runs the cluster's virtual machines through the node APIs of
// its nodes, and serves the machines' network its gateway.
type Controller struct {
	Dynamic dynamic.Interface
	Kube    kubernetes.Interface
	// Network of the machines; the gateway takes its first address.
	Network  netip.Prefix
	StateDir string

	dhcp *router.Daemon
	conf string
	nft  string
}

// Run reconciles every few seconds until ctx ends.
func (c *Controller) Run(ctx context.Context) error {
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return err
	}
	c.dhcp = &router.Daemon{Name: "dnsmasq", Args: []string{"/usr/sbin/dnsmasq", "--conf-file=" + filepath.Join(c.StateDir, "dnsmasq.conf")}}
	defer c.dhcp.Stop()
	if c.Network.IsValid() {
		go c.serveSeeds(ctx, c.Network.Masked().Addr().Next().String())
	}
	for {
		if err := c.gateway(); err != nil {
			klog.Errorf("gateway: %v", err)
		}
		c.reconcile(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *Controller) nodes(ctx context.Context) ([]Node, error) {
	list, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Node
	for _, n := range list.Items {
		node := Node{Name: n.Name}
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				node.Address = a.Address
			}
		}
		for _, cond := range n.Status.Conditions {
			if cond.Type == corev1.NodeReady {
				node.Ready = cond.Status == corev1.ConditionTrue
				if !node.Ready {
					node.DownSince = cond.LastTransitionTime.Time
				}
			}
		}
		out = append(out, node)
	}
	return out, nil
}

func (c *Controller) reconcile(ctx context.Context) {
	nodes, err := c.nodes(ctx)
	if err != nil {
		klog.Errorf("nodes: %v", err)
		return
	}
	list, err := c.Dynamic.Resource(vmsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("virtual machines: %v", err)
		return
	}
	var vms []v1.VirtualMachine
	used, load := map[int32]bool{}, map[string]int{}
	for _, u := range list.Items {
		var m v1.VirtualMachine
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &m); err != nil {
			continue
		}
		vms = append(vms, m)
		if m.Status.Minor != 0 {
			used[m.Status.Minor] = true
		}
		if m.Status.Node != "" {
			load[m.Status.Node]++
		}
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].CreationTimestamp.Before(&vms[j].CreationTimestamp) })
	disks := c.listVolumes(ctx)
	for _, d := range disks {
		if d.minor != 0 {
			used[d.minor] = true // minors of volumes still on nodes, orphans too
		}
	}
	c.sweep(ctx, vms, nodes, disks)
	leases := c.leases()
	for i := range vms {
		m := &vms[i]
		if m.DeletionTimestamp != nil {
			c.cleanup(ctx, m, nodes)
			continue
		}
		if err := c.ensureFinalizer(ctx, m); err != nil {
			klog.Errorf("%s: %v", m.Name, err)
			continue
		}
		mine := map[string]disk{}
		upToDate := map[string]bool{}
		for _, d := range disks {
			if d.vm == m.Name {
				mine[d.node] = d
				upToDate[d.node] = d.diskState == "UpToDate"
			}
		}
		p := PlanFor(*m, nodes, used, load, upToDate, time.Now())
		// The old node is back, still running the machine with its disk:
		// the machine stays there rather than moving.
		if old := m.Status.Node; old != "" && p.Node != old && ready(nodes, old) && mine[old].role == "Primary" {
			p.Node, p.Moved = old, false
		}
		used[p.Minor] = true
		st := c.apply(ctx, m, p, nodes, mine)
		st.Address = leases[strings.ToLower(p.MAC)]
		c.writeStatus(ctx, m, st)
	}
}

// disk is a volume as a node reports it.
type disk struct {
	node, vm               string
	minor                  int32
	role, diskState, phase string
	imageWritten           bool
}

// listVolumes lists the volumes of every node through the cluster's view
// of the node APIs, where each is named <node>.<machine>.
func (c *Controller) listVolumes(ctx context.Context) []disk {
	list, err := c.Dynamic.Resource(volumesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("volumes: %v", err)
		return nil
	}
	var out []disk
	for _, u := range list.Items {
		node, vmName, ok := strings.Cut(u.GetName(), ".")
		if !ok {
			continue
		}
		d := disk{node: node, vm: vmName}
		minor, _, _ := unstructured.NestedInt64(u.Object, "spec", "minor")
		d.minor = int32(minor)
		d.role, _, _ = unstructured.NestedString(u.Object, "status", "role")
		d.diskState, _, _ = unstructured.NestedString(u.Object, "status", "diskState")
		d.phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
		d.imageWritten, _, _ = unstructured.NestedBool(u.Object, "status", "imageWritten")
		out = append(out, d)
	}
	return out
}

// sweep removes, from nodes that are up, volumes and machines no machine of
// the cluster accounts for: left by a deletion while their node was down.
func (c *Controller) sweep(ctx context.Context, vms []v1.VirtualMachine, nodes []Node, disks []disk) {
	known := map[string]bool{}
	for _, m := range vms {
		for _, r := range m.Status.ReplicaNodes {
			known[r+"."+m.Name] = true
		}
	}
	if machines, err := c.Dynamic.Resource(machinesGVR).List(ctx, metav1.ListOptions{}); err == nil {
		for _, u := range machines.Items {
			node, _, _ := strings.Cut(u.GetName(), ".")
			if !known[u.GetName()] && ready(nodes, node) {
				klog.Infof("removing machine %s: no virtual machine has it", u.GetName())
				_ = c.Dynamic.Resource(machinesGVR).Delete(ctx, u.GetName(), metav1.DeleteOptions{})
			}
		}
	}
	for _, d := range disks {
		name := d.node + "." + d.vm
		if !known[name] && ready(nodes, d.node) {
			klog.Infof("removing volume %s: no virtual machine has it", name)
			_ = c.Dynamic.Resource(volumesGVR).Delete(ctx, name, metav1.DeleteOptions{})
		}
	}
}

func ready(nodes []Node, name string) bool {
	for _, n := range nodes {
		if n.Name == name {
			return n.Ready
		}
	}
	return false
}

// apply makes the nodes run the plan: the disk's volume on each replica
// node, primary where the machine runs; the machine there and nowhere else.
func (c *Controller) apply(ctx context.Context, m *v1.VirtualMachine, p Plan, nodes []Node, mine map[string]disk) v1.VirtualMachineStatus {
	st := v1.VirtualMachineStatus{Node: p.Node, ReplicaNodes: p.ReplicaNodes, MAC: p.MAC, Minor: p.Minor, Port: p.Port,
		Moves: m.Status.Moves, Phase: "Pending", ImageWritten: m.Status.ImageWritten, ReplicaAddresses: map[string]string{}}
	if m.Status.FailedMigration == m.Spec.Node {
		st.FailedMigration = m.Status.FailedMigration
	}
	for k, v := range m.Status.ReplicaAddresses {
		st.ReplicaAddresses[k] = v
	}
	for _, n := range nodes {
		for _, r := range p.ReplicaNodes {
			if n.Name == r && n.Address != "" {
				st.ReplicaAddresses[r] = n.Address
			}
		}
	}
	if len(p.ReplicaNodes) > 0 && mine[p.ReplicaNodes[0]].imageWritten {
		st.ImageWritten = true
	}
	if p.Moved {
		st.Moves++
		klog.Infof("%s: node %s is down, starting it on %s", m.Name, m.Status.Node, p.Node)
	}
	if p.Waiting != "" {
		st.Message = p.Waiting
		if m.Status.Node != "" {
			st.Phase = "Moving"
		}
		return st
	}
	running := m.Spec.Running == nil || *m.Spec.Running
	byName := map[string]Node{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	if c.migrating(m, p, running, byName, mine) {
		return c.migrate(ctx, m, p, st, byName, mine)
	}
	// Machines first leave the nodes they no longer run on, so the volume
	// there can step down.
	for _, r := range p.ReplicaNodes {
		if r != p.Node && byName[r].Ready {
			_ = c.Dynamic.Resource(machinesGVR).Delete(ctx, r+"."+m.Name, metav1.DeleteOptions{})
		}
	}
	primary := map[string]bool{}
	if running {
		primary[p.Node] = true
	}
	if err := c.ensureVolumes(ctx, m, p, &st, byName, primary, bothPrimary(mine)); err != nil {
		st.Message = err.Error()
		return st
	}
	vol, err := c.Dynamic.Resource(volumesGVR).Get(ctx, p.Node+"."+m.Name, metav1.GetOptions{})
	if err != nil {
		st.Message = err.Error()
		return st
	}
	role, _, _ := unstructured.NestedString(vol.Object, "status", "role")
	phase, _, _ := unstructured.NestedString(vol.Object, "status", "phase")
	if msg, _, _ := unstructured.NestedString(vol.Object, "status", "message"); msg != "" {
		st.Message = "volume: " + msg
	}
	if running && (role != "Primary" || phase != "Ready") {
		st.Phase = "Starting"
		if st.Message == "" {
			st.Message = "waiting for the disk on " + p.Node
		}
		return st
	}
	machine := machineSpec(m, p, running)
	if err := c.ensure(ctx, machinesGVR, p.Node+"."+m.Name, map[string]any{"spec": toMap(&machine)}); err != nil {
		st.Message = fmt.Sprintf("machine on %s: %v", p.Node, err)
		return st
	}
	got, err := c.Dynamic.Resource(machinesGVR).Get(ctx, p.Node+"."+m.Name, metav1.GetOptions{})
	if err != nil {
		st.Message = err.Error()
		return st
	}
	mphase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	msg, _, _ := unstructured.NestedString(got.Object, "status", "message")
	st.Phase, st.Message = mphase, msg
	if mphase == "" {
		st.Phase = "Starting"
	}
	return st
}

// ensure creates a node object, or updates its spec when it differs.
func (c *Controller) ensure(ctx context.Context, gvr schema.GroupVersionResource, name string, want map[string]any) error {
	r := c.Dynamic.Resource(gvr)
	cur, err := r.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		kind := "Volume"
		if gvr.Resource == "machines" {
			kind = "Machine"
		}
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "node.kuberoot.dev/v1alpha1", "kind": kind,
			"metadata": map[string]any{"name": name}, "spec": want["spec"]}}
		_, err = r.Create(ctx, obj, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if spec, _, _ := unstructured.NestedMap(cur.Object, "spec"); reflect.DeepEqual(normalize(spec), normalize(want["spec"].(map[string]any))) {
		return nil
	}
	cur.Object["spec"] = want["spec"]
	_, err = r.Update(ctx, cur, metav1.UpdateOptions{})
	return err
}

// cleanup removes a deleted machine from every node, then lets it go.
func (c *Controller) cleanup(ctx context.Context, m *v1.VirtualMachine, nodes []Node) {
	exists, isReady := map[string]bool{}, map[string]bool{}
	for _, n := range nodes {
		exists[n.Name], isReady[n.Name] = true, n.Ready
	}
	for _, r := range m.Status.ReplicaNodes {
		if !exists[r] {
			continue // the node left the cluster, and its copy with it
		}
		if !isReady[r] {
			// Kept until the node is back: its copy may still run.
			c.writeStatus(ctx, m, v1.VirtualMachineStatus{Phase: "Deleting", Message: "waiting for node " + r + " to remove its copy",
				Node: m.Status.Node, ReplicaNodes: m.Status.ReplicaNodes, Minor: m.Status.Minor, Port: m.Status.Port, MAC: m.Status.MAC})
			return
		}
		_ = c.Dynamic.Resource(machinesGVR).Delete(ctx, r+"."+m.Name, metav1.DeleteOptions{})
		if err := c.Dynamic.Resource(volumesGVR).Delete(ctx, r+"."+m.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			klog.Errorf("%s: volume on %s: %v", m.Name, r, err)
			return
		}
	}
	u, err := c.Dynamic.Resource(vmsGVR).Get(ctx, m.Name, metav1.GetOptions{})
	if err != nil {
		return
	}
	var keep []string
	for _, f := range u.GetFinalizers() {
		if f != finalizer {
			keep = append(keep, f)
		}
	}
	u.SetFinalizers(keep)
	_, _ = c.Dynamic.Resource(vmsGVR).Update(ctx, u, metav1.UpdateOptions{})
}

func (c *Controller) ensureFinalizer(ctx context.Context, m *v1.VirtualMachine) error {
	for _, f := range m.Finalizers {
		if f == finalizer {
			return nil
		}
	}
	u, err := c.Dynamic.Resource(vmsGVR).Get(ctx, m.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	u.SetFinalizers(append(u.GetFinalizers(), finalizer))
	_, err = c.Dynamic.Resource(vmsGVR).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

func (c *Controller) writeStatus(ctx context.Context, m *v1.VirtualMachine, st v1.VirtualMachineStatus) {
	if reflect.DeepEqual(m.Status, st) {
		return
	}
	u, err := c.Dynamic.Resource(vmsGVR).Get(ctx, m.Name, metav1.GetOptions{})
	if err != nil {
		return
	}
	u.Object["status"] = toMap(&st)
	if _, err := c.Dynamic.Resource(vmsGVR).UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
		klog.V(2).Infof("status of %s: %v", m.Name, err)
	}
}

// gateway gives the machines' network its gateway on this node: the
// network's first address on the bridge, NAT out of the node's other links,
// and DHCP and DNS.
func (c *Controller) gateway() error {
	if !c.Network.IsValid() {
		return nil
	}
	gw := c.Network.Masked().Addr().Next()
	addr := netip.PrefixFrom(gw, c.Network.Bits()).String()
	start, end := gw.Next().Next(), lastHost(c.Network)
	cfg := router.Config{
		Interfaces: []routerv1.Interface{{ObjectMeta: metav1.ObjectMeta{Name: "machines"}, Spec: routerv1.InterfaceSpec{Link: vm.Bridge, Addresses: []string{addr}}}},
		DHCP: []routerv1.DHCPServer{{ObjectMeta: metav1.ObjectMeta{Name: "machines"}, Spec: routerv1.DHCPServerSpec{
			Link: vm.Bridge, RangeStart: start.String(), RangeEnd: end.String(), LeaseTime: "12h"}}},
	}
	if out, err := uplink(); err == nil {
		cfg.NAT = []routerv1.NATRule{{ObjectMeta: metav1.ObjectMeta{Name: "machines"},
			Spec: routerv1.NATRuleSpec{Masquerade: &routerv1.Masquerade{OutLink: out, Sources: []string{c.Network.Masked().String()}}}}}
	}
	ok, problems := router.Check(cfg)
	if len(problems) > 0 {
		return fmt.Errorf("%v", problems)
	}
	for _, err := range router.ApplyInterfaces(ok.Interfaces) {
		return err // the bridge comes with the node's machines' network
	}
	if rs := router.Ruleset(ok); rs != c.nft {
		cmd := exec.Command("nft", "-f", "-")
		cmd.Stdin = strings.NewReader(rs)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("nft: %v: %s", err, out)
		}
		c.nft = rs
	}
	conf := router.DnsmasqConfig(ok, c.leaseFile()) + "user=root\npid-file=\n"
	if conf != c.conf || !c.dhcp.Running() {
		if err := os.WriteFile(filepath.Join(c.StateDir, "dnsmasq.conf"), []byte(conf), 0o644); err != nil {
			return err
		}
		c.dhcp.Restart()
		c.conf = conf
	}
	return nil
}

func (c *Controller) leaseFile() string { return filepath.Join(c.StateDir, "dnsmasq.leases") }

// leases maps the machines' MACs to the addresses they took.
func (c *Controller) leases() map[string]string {
	f, err := os.Open(c.leaseFile())
	if err != nil {
		return map[string]string{}
	}
	defer f.Close()
	return newestLeases(router.ParseLeases(f))
}

// newestLeases maps each MAC to the address of its newest lease. A machine
// reinstalled under the same MAC asks with another client identifier and
// gets another address while its old lease lasts; the newest is the one in
// use.
func newestLeases(leases []routerv1.Lease) map[string]string {
	out := map[string]string{}
	newest := map[string]time.Time{}
	for _, l := range leases {
		mac := strings.ToLower(l.MAC)
		if t, ok := newest[mac]; ok && !l.Expires.Time.After(t) {
			continue
		}
		newest[mac], out[mac] = l.Expires.Time, l.Address
	}
	return out
}

// uplink is the link of the default route.
func uplink() (string, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return "", err
	}
	for _, r := range routes {
		if r.Dst == nil || r.Dst.IP.IsUnspecified() {
			l, err := netlink.LinkByIndex(r.LinkIndex)
			if err != nil {
				return "", err
			}
			return l.Attrs().Name, nil
		}
	}
	return "", fmt.Errorf("no default route")
}

func lastHost(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 2
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	v += host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// toMap converts a pointer to a struct into its unstructured form.
func toMap(ptr any) map[string]any {
	m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(ptr)
	return m
}

// normalize drops empty values, as the server does when it stores a spec.
func normalize(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		switch x := v.(type) {
		case nil:
		case string:
			if x != "" {
				out[k] = x
			}
		case bool:
			if x {
				out[k] = x
			}
		case int64:
			if x != 0 {
				out[k] = x
			}
		case []any:
			if len(x) > 0 {
				out[k] = fmt.Sprint(x)
			}
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

// machineSpec is a machine as its node runs it.
func machineSpec(m *v1.VirtualMachine, p Plan, running bool) nodev1.MachineSpec {
	mem := m.Spec.Memory.Value() >> 20
	if mem == 0 {
		mem = 512
	}
	cpus := m.Spec.CPUs
	if cpus == 0 {
		cpus = 1
	}
	return nodev1.MachineSpec{CPUs: cpus, MemoryMiB: mem, Volumes: []string{m.Name}, MAC: p.MAC, Running: running,
		SystemSerial: systemSerial(m)}
}

// bothPrimary: the disk is still writable on two nodes, after a live move or
// one abandoned; it stays allowed to be until one of them steps down.
func bothPrimary(mine map[string]disk) bool {
	n := 0
	for _, d := range mine {
		if d.role == "Primary" {
			n++
		}
	}
	return n > 1
}

// ensureVolumes keeps the machine's disk on every replica node that is up,
// primary on the nodes named, writable on two at once while it moves.
func (c *Controller) ensureVolumes(ctx context.Context, m *v1.VirtualMachine, p Plan, st *v1.VirtualMachineStatus,
	byName map[string]Node, primary map[string]bool, twoPrimaries bool) error {
	size := m.Spec.Disk.Size.Value()
	if size == 0 {
		size = 4 << 30
	}
	for i, r := range p.ReplicaNodes {
		if !byName[r].Ready {
			continue
		}
		spec := nodev1.VolumeSpec{SizeBytes: size, Minor: p.Minor, Port: p.Port, NodeID: int32(i),
			Primary: primary[r], AllowTwoPrimaries: twoPrimaries}
		// The image goes to the first replica, and only until it is in: a
		// replica that comes back empty later syncs, never re-images.
		if i == 0 && !st.ImageWritten {
			spec.Image = m.Spec.Disk.Image
		}
		for j, peer := range p.ReplicaNodes {
			if peer != r {
				spec.Peers = append(spec.Peers, nodev1.VolumePeer{Node: peer, Address: st.ReplicaAddresses[peer], NodeID: int32(j)})
			}
		}
		if err := c.ensure(ctx, volumesGVR, r+"."+m.Name, map[string]any{"spec": toMap(&spec)}); err != nil {
			return fmt.Errorf("volume on %s: %w", r, err)
		}
	}
	return nil
}
