package nodeapi

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

// Pod networks between nodes.
const (
	// PodNetworkVXLAN carries pod traffic between nodes in VXLAN: the network
	// under the nodes sees only node addresses, which clouds and virtualized
	// networks with address filtering let through.
	PodNetworkVXLAN = "vxlan"
	// PodNetworkHostGW routes pod subnets through the nodes themselves: no
	// encapsulation, but only on a plain layer-2 network that forwards
	// packets addressed to pods.
	PodNetworkHostGW = "host-gw"
	// PodNetworkNone leaves pod traffic between nodes to a CNI package.
	PodNetworkNone = "none"

	vxlanDevice = "kuberoot.vx"
	vxlanID     = 1
	vxlanPort   = 8472
	// VXLANOverhead is what encapsulation adds to every packet.
	VXLANOverhead = 50
)

// peer is another node's pod subnet and the node address it is reached at.
type peer struct {
	subnet *net.IPNet
	node   net.IP
}

// syncRoutes keeps every other node's pod subnet reachable, through that node
// (host-gw) or through a VXLAN tunnel to it. Subnets come from the Node spec,
// which the control plane assigns and a node cannot change; only subnets inside
// the cluster's pod range, claimed by exactly one node, are routed. Routes into
// the pod range that no node claims any more are removed.
func syncRoutes(ctx context.Context, kubeconfig, self string, podRange *net.IPNet, mode, cniTemplate string) {
	if podRange != nil && podRange.IP.To4() == nil {
		klog.Errorf("pod network %s: only an IPv4 pod range is routed between nodes; use a CNI package", podRange)
		return
	}
	var client kubernetes.Interface
	for ctx.Err() == nil {
		if client == nil {
			client = clientFrom(kubeconfig)
		}
		if client != nil && podRange != nil {
			if nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
				own, peers := desiredPeers(nodes.Items, self, podRange)
				var err error
				if mode == PodNetworkHostGW {
					err = applyHostGW(peers, podRange)
				} else {
					err = applyVXLAN(own, peers, podRange)
				}
				if err != nil {
					klog.Errorf("pod network: %v", err)
				}
				if cniTemplate != "" && own != nil {
					if err := writeCNIConfig(cniTemplate, cniConfigPath, own.subnet); err != nil {
						klog.Errorf("pod network: CNI configuration: %v", err)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

// desiredPeers returns this node's own subnet and the other nodes' subnets
// with their addresses.
func desiredPeers(nodes []corev1.Node, self string, podRange *net.IPNet) (*peer, map[string]peer) {
	claims := map[string]int{}
	peers := map[string]peer{}
	var own *peer
	for _, n := range nodes {
		_, subnet, err := net.ParseCIDR(n.Spec.PodCIDR)
		if err != nil || !within(subnet, podRange) {
			continue
		}
		claims[subnet.String()]++
		addr := internalIP(n, podRange)
		if addr == nil {
			continue
		}
		if n.Name == self {
			own = &peer{subnet: subnet, node: addr}
			continue
		}
		peers[subnet.String()] = peer{subnet: subnet, node: addr}
	}
	for subnet, count := range claims {
		if count > 1 {
			klog.Errorf("pod subnet %s is claimed by %d nodes; not routing it", subnet, count)
			delete(peers, subnet)
			if own != nil && own.subnet.String() == subnet {
				own = nil
			}
		}
	}
	return own, peers
}

func internalIP(n corev1.Node, podRange *net.IPNet) net.IP {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			if ip := net.ParseIP(a.Address).To4(); ip != nil && !podRange.Contains(ip) {
				return ip
			}
		}
	}
	return nil
}

func within(subnet, outer *net.IPNet) bool {
	inner, _ := subnet.Mask.Size()
	outerOnes, _ := outer.Mask.Size()
	return outer.Contains(subnet.IP) && inner >= outerOnes
}

// applyHostGW routes each peer subnet through the peer node.
func applyHostGW(peers map[string]peer, podRange *net.IPNet) error {
	desired := map[string]netlink.Route{}
	for key, p := range peers {
		desired[key] = netlink.Route{Dst: p.subnet, Gw: p.node}
	}
	return reconcileRoutes(desired, podRange)
}

// vtep is the tunnel endpoint of a pod subnet: the subnet's first address
// and a MAC made from it, so every node computes every other node's endpoint
// from the Node objects alone.
func vtep(subnet *net.IPNet) (net.IP, net.HardwareAddr) {
	ip := subnet.IP.To4()
	return ip, net.HardwareAddr{0x0e, 0x6b, ip[0], ip[1], ip[2], ip[3]}
}

// applyVXLAN keeps the tunnel device, and for every peer a route to its
// subnet through its endpoint, the endpoint's MAC, and where to send frames
// for that MAC: the peer node.
func applyVXLAN(own *peer, peers map[string]peer, podRange *net.IPNet) error {
	if own == nil {
		return nil // no subnet assigned yet
	}
	link, err := ensureVXLAN(own)
	if err != nil {
		return err
	}
	idx := link.Attrs().Index
	desired := map[string]netlink.Route{}
	_, ownMAC := vtep(own.subnet)
	wantNeigh := map[string]bool{ownMAC.String(): true}
	for key, p := range peers {
		ip, mac := vtep(p.subnet)
		desired[key] = netlink.Route{LinkIndex: idx, Dst: p.subnet, Gw: ip, Flags: int(netlink.FLAG_ONLINK)}
		wantNeigh[mac.String()] = true
		if err := netlink.NeighSet(&netlink.Neigh{LinkIndex: idx, State: netlink.NUD_PERMANENT, Type: unix.RTN_UNICAST, IP: ip, HardwareAddr: mac}); err != nil {
			return fmt.Errorf("neighbour %s: %w", ip, err)
		}
		if err := netlink.NeighSet(&netlink.Neigh{LinkIndex: idx, Family: unix.AF_BRIDGE, State: netlink.NUD_PERMANENT, Flags: netlink.NTF_SELF, IP: p.node, HardwareAddr: mac}); err != nil {
			return fmt.Errorf("forwarding entry for %s: %w", p.node, err)
		}
	}
	// Entries of nodes that left.
	for _, family := range []int{netlink.FAMILY_V4, unix.AF_BRIDGE} {
		neighs, err := netlink.NeighList(idx, family)
		if err != nil {
			continue
		}
		for _, n := range neighs {
			if n.HardwareAddr != nil && n.State&netlink.NUD_PERMANENT != 0 && !wantNeigh[n.HardwareAddr.String()] {
				_ = netlink.NeighDel(&n)
			}
		}
	}
	return reconcileRoutes(desired, podRange)
}

// ensureVXLAN creates the tunnel device on the node's address, with the
// node's endpoint address and MAC, and an MTU that leaves room for the
// encapsulation.
func ensureVXLAN(own *peer) (netlink.Link, error) {
	parent, mtu, err := linkWithAddress(own.node)
	if err != nil {
		return nil, err
	}
	ip, mac := vtep(own.subnet)
	if l, err := netlink.LinkByName(vxlanDevice); err == nil {
		vx, ok := l.(*netlink.Vxlan)
		if ok && vx.VxlanId == vxlanID && vx.SrcAddr.Equal(own.node) && vx.HardwareAddr.String() == mac.String() && vx.MTU == mtu-VXLANOverhead {
			return l, ensureUp(l, ip)
		}
		if err := netlink.LinkDel(l); err != nil {
			return nil, fmt.Errorf("replace %s: %w", vxlanDevice, err)
		}
	}
	vx := &netlink.Vxlan{
		LinkAttrs:    netlink.LinkAttrs{Name: vxlanDevice, MTU: mtu - VXLANOverhead, HardwareAddr: mac},
		VxlanId:      vxlanID,
		VtepDevIndex: parent,
		SrcAddr:      own.node,
		Port:         vxlanPort,
		Learning:     false,
	}
	if err := netlink.LinkAdd(vx); err != nil {
		return nil, fmt.Errorf("create %s: %w", vxlanDevice, err)
	}
	l, err := netlink.LinkByName(vxlanDevice)
	if err != nil {
		return nil, err
	}
	return l, ensureUp(l, ip)
}

func ensureUp(l netlink.Link, ip net.IP) error {
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}}
	if err := netlink.AddrReplace(l, addr); err != nil {
		return fmt.Errorf("address on %s: %w", vxlanDevice, err)
	}
	return netlink.LinkSetUp(l)
}

// linkWithAddress finds the interface holding ip and its MTU.
func linkWithAddress(ip net.IP) (index, mtu int, err error) {
	links, err := netlink.LinkList()
	if err != nil {
		return 0, 0, err
	}
	for _, l := range links {
		addrs, _ := netlink.AddrList(l, netlink.FAMILY_V4)
		for _, a := range addrs {
			if a.IP.Equal(ip) {
				return l.Attrs().Index, l.Attrs().MTU, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("no interface has the node address %s", ip)
}

// reconcileRoutes makes the routes into the pod range exactly the desired ones.
func reconcileRoutes(desired map[string]netlink.Route, podRange *net.IPNet) error {
	existing, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	for _, r := range existing {
		if r.Dst == nil || r.Gw == nil || !within(r.Dst, podRange) {
			continue
		}
		if want, ok := desired[r.Dst.String()]; !ok || !want.Gw.Equal(r.Gw) || (want.LinkIndex != 0 && want.LinkIndex != r.LinkIndex) {
			if err := netlink.RouteDel(&r); err != nil {
				klog.V(2).Infof("remove route %s: %v", r.Dst, err)
			}
		}
	}
	for _, r := range desired {
		r := r
		if err := netlink.RouteReplace(&r); err != nil {
			klog.V(2).Infof("route %s via %s: %v", r.Dst, r.Gw, err)
		}
	}
	return nil
}

func clientFrom(kubeconfig string) kubernetes.Interface {
	if _, err := os.Stat(kubeconfig); err != nil {
		return nil // a joining worker gets its kubeconfig once the kubelet bootstraps
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil
	}
	return client
}

// cniConfigPath is where the runtime reads the pod network's configuration.
const cniConfigPath = "/etc/cni/net.d/10-kuberoot.conflist"

// writeCNIConfig fills the node's subnet into the CNI template and writes
// it where the runtime reads it, when it changed.
func writeCNIConfig(template, path string, subnet *net.IPNet) error {
	raw, err := os.ReadFile(template)
	if err != nil {
		return err
	}
	want := strings.ReplaceAll(string(raw), "{{.PodCIDR}}", subnet.String())
	if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(want), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
