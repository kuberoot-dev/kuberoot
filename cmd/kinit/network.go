package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// nodeInfo is what the rest of the boot needs to know about this machine.
type nodeInfo struct {
	name string
	ip   net.IP
}

// setupNetwork brings up loopback and configures the first ethernet link via DHCP.
func setupNetwork(cfg bootConfig) (nodeInfo, error) {
	var node nodeInfo
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return node, fmt.Errorf("loopback: %w", err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		return node, fmt.Errorf("loopback up: %w", err)
	}
	staticIface, staticAddr, _ := strings.Cut(cfg.static, ":")
	link, err := firstEthernet(staticIface)
	if err != nil {
		return node, err
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return node, fmt.Errorf("%s up: %w", link.Attrs().Name, err)
	}
	mac := link.Attrs().HardwareAddr
	node.name = keptNodeName(fmt.Sprintf("kuberoot-%02x%02x%02x", mac[3], mac[4], mac[5]))
	node.ip, err = dhcp(link, cfg.nameservers)
	if err != nil || cfg.static == "" {
		return node, err
	}
	ip, err := staticAddress(staticIface, staticAddr)
	if err != nil {
		return node, err
	}
	node.ip = ip
	return node, nil
}

// staticAddress brings up a cluster-facing interface with a fixed address.
func staticAddress(iface, cidr string) (net.IP, error) {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return nil, fmt.Errorf("static interface %s: %w", iface, err)
	}
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return nil, fmt.Errorf("static address %s: %w", cidr, err)
	}
	if err := netlink.AddrReplace(link, addr); err != nil {
		return nil, err
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return nil, err
	}
	log.Printf("%s: %s (static)", iface, cidr)
	return addr.IP, nil
}

func setHostname(node nodeInfo) error {
	if err := unix.Sethostname([]byte(node.name)); err != nil {
		return fmt.Errorf("hostname: %w", err)
	}
	hosts := fmt.Sprintf("127.0.0.1 localhost\n::1 localhost\n%s %s\n", node.ip, node.name)
	if err := os.WriteFile("/etc/hosts", []byte(hosts), 0o644); err != nil {
		return err
	}
	return os.WriteFile("/etc/hostname", []byte(node.name+"\n"), 0o644)
}

// firstEthernet picks the uplink for DHCP, skipping the static interface.
func firstEthernet(skip string) (netlink.Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.Type() == "device" && l.Attrs().Name != "lo" && l.Attrs().Name != skip && len(l.Attrs().HardwareAddr) == 6 {
			return l, nil
		}
	}
	return nil, fmt.Errorf("no ethernet link found")
}

func dhcp(link netlink.Link, nameservers []string) (net.IP, error) {
	name := link.Attrs().Name
	client, err := nclient4.New(name)
	if err != nil {
		return nil, fmt.Errorf("dhcp client on %s: %w", name, err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lease, err := client.Request(ctx)
	if err != nil {
		return nil, fmt.Errorf("dhcp on %s: %w", name, err)
	}
	ack := lease.ACK
	ipnet := &net.IPNet{IP: ack.YourIPAddr, Mask: ack.SubnetMask()}
	if err := netlink.AddrReplace(link, &netlink.Addr{IPNet: ipnet}); err != nil {
		return nil, fmt.Errorf("address %s: %w", ipnet, err)
	}
	if routers := ack.Router(); len(routers) > 0 {
		route := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: routers[0]}
		if err := netlink.RouteReplace(route); err != nil {
			return nil, fmt.Errorf("default route: %w", err)
		}
	}
	if len(nameservers) == 0 {
		for _, dns := range ack.DNS() {
			nameservers = append(nameservers, dns.String())
		}
	}
	var resolv strings.Builder
	for _, dns := range nameservers {
		fmt.Fprintf(&resolv, "nameserver %s\n", dns)
	}
	if err := os.WriteFile("/etc/resolv.conf", []byte(resolv.String()), 0o644); err != nil {
		return nil, fmt.Errorf("resolv.conf: %w", err)
	}
	log.Printf("%s: %s via %v", name, ipnet, ack.Router())
	return ack.YourIPAddr, nil
}

// nodeNameFile keeps the name a node first booted with. The name comes from
// the network card, and a node keeps it when the card changes: a control
// plane restored onto new hardware is the same node to the cluster.
var nodeNameFile = "/var/lib/kuberoot/node-name"

func keptNodeName(fromHardware string) string {
	if raw, err := os.ReadFile(nodeNameFile); err == nil {
		if name := strings.TrimSpace(string(raw)); name != "" {
			return name
		}
	}
	if err := os.MkdirAll(filepath.Dir(nodeNameFile), 0o755); err == nil {
		if err := atomicfile.WriteFile(nodeNameFile, []byte(fromHardware+"\n"), 0o644); err != nil {
			log.Printf("keep node name: %v", err)
		}
	}
	return fromHardware
}
