package vmctl

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	routerv1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
	"github.com/kuberoot-dev/kuberoot/pkg/router"
	"github.com/kuberoot-dev/kuberoot/pkg/vm"
)

const (
	gatewayTable   = "kuberoot_vm_gateway"
	gatewayLockDir = "/run/kuberoot-vmctl"
)

// LockGateway excludes another controller process on this node and clears
// state left in the kernel after a crash. Call before joining the election
// and hold the returned lock for the whole process, including while it is
// a follower. Only the gateway IP and our dedicated nft table are touched.
func (c *Controller) LockGateway() (release func(), err error) {
	// Every process uses the same host lock, even if its DHCP state-dir or
	// election namespace differs. They all manage the same machine bridge.
	return lockGateway(gatewayLockDir, func() error {
		if !c.Network.IsValid() {
			return nil
		}
		c.gatewayOwned, c.gatewayRulesOwned = true, true
		return c.stopGateway()
	})
}

func lockGateway(stateDir string, cleanup func() error) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(stateDir, "controller.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("another vmctl process holds the gateway: %w", err)
	}
	if err := cleanup(); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return func() { _ = lock.Close() }, nil
}

// gateway runs only on the elected controller. The bridge itself belongs
// to the node: claim just the gateway address and our own nftables table.
func (c *Controller) gateway(ctx context.Context) error {
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
	if c.StableAddresses {
		reservations, err := c.reserveAddresses(ctx)
		if err != nil {
			return err
		}
		cfg.DHCP[0].Spec.StaticLeases = reservations
	}
	if out, err := uplink(); err == nil {
		cfg.NAT = []routerv1.NATRule{{ObjectMeta: metav1.ObjectMeta{Name: "machines"},
			Spec: routerv1.NATRuleSpec{Masquerade: &routerv1.Masquerade{OutLink: out, Sources: []string{c.Network.Masked().String()}}}}}
	}
	ok, problems := router.Check(cfg)
	if len(problems) > 0 {
		return fmt.Errorf("%v", problems)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	link, err := netlink.LinkByName(vm.Bridge)
	if err != nil {
		return err // the node's machines' network creates the bridge
	}
	a, err := netlink.ParseAddr(addr)
	if err != nil {
		return err
	}
	if err := netlink.AddrReplace(link, a); err != nil {
		return err
	}
	c.gatewayOwned = true
	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}
	// Repeat on the shared VXLAN segment after takeover and when the
	// bridge's automatic MAC changes as taps are attached or detached.
	if err := announceGateway(link, gw); err != nil {
		return err
	}
	rs := strings.ReplaceAll(router.Ruleset(ok), router.Table, gatewayTable)
	if rs != c.nft {
		c.gatewayRulesOwned = true // nft may commit just before cancellation
		if err := applyGatewayRules(ctx, rs); err != nil {
			return err
		}
		c.nft = rs
	}
	conf := gatewayDNSConfig(ok, c.leaseFile(), c.StableAddresses)
	if conf != c.conf || !c.dhcp.Running() {
		if err := os.WriteFile(filepath.Join(c.StateDir, "dnsmasq.conf"), []byte(conf), 0o644); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		c.dhcp.Restart()
		c.conf = conf
	}
	return nil
}

func gatewayDNSConfig(cfg router.Config, leaseFile string, stable bool) string {
	var conf string
	if stable {
		conf = router.DnsmasqStaticConfig(cfg)
	} else {
		conf = router.DnsmasqConfig(cfg, leaseFile)
	}
	conf += "user=root\npid-file=\n"
	if stable {
		// Only persisted MAC/IP bindings may be served. A full daemon
		// restart above clears obsolete in-memory leases when they change.
		conf += "dhcp-ignore=tag:!known\ndhcp-ignore-clid\n"
	}
	return conf
}

func applyGatewayRules(ctx context.Context, rules string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gateway nft: %w: %s", err, out)
	}
	return nil
}

// stopGateway must finish before releasing the Lease. Do not delete the
// shared bridge, its other addresses or another controller's rules.
func (c *Controller) stopGateway() error {
	var errs []error
	if c.gatewayOwned {
		link, err := netlink.LinkByName(vm.Bridge)
		var missing netlink.LinkNotFoundError
		if err == nil {
			addr, _ := netlink.ParseAddr(netip.PrefixFrom(c.Network.Masked().Addr().Next(), c.Network.Bits()).String())
			err = netlink.AddrDel(link, addr)
		}
		if err != nil && !errors.As(err, &missing) && !errors.Is(err, unix.EADDRNOTAVAIL) {
			errs = append(errs, fmt.Errorf("remove gateway address: %w", err))
		} else {
			c.gatewayOwned = false
		}
	}
	if c.gatewayRulesOwned {
		// Declare first so deletion is idempotent if the table went away.
		err := applyGatewayRules(context.Background(), "table inet "+gatewayTable+"\ndelete table inet "+gatewayTable+"\n")
		if err != nil {
			errs = append(errs, err)
		} else {
			c.nft = ""
			c.gatewayRulesOwned = false
		}
	}
	return errors.Join(errs...)
}

// announceGateway sends an unsolicited ARP reply with the bridge's MAC.
// AF_PACKET SOCK_DGRAM supplies the Ethernet header; no external arping
// binary or change to the shared bridge's MAC is needed.
func announceGateway(link netlink.Link, ip netip.Addr) error {
	mac := link.Attrs().HardwareAddr
	if len(mac) != 6 || !ip.Is4() {
		return fmt.Errorf("gateway requires Ethernet and IPv4")
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, int(htons(unix.ETH_P_ARP)))
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	packet := gatewayARP(mac, ip.As4())
	to := &unix.SockaddrLinklayer{Ifindex: link.Attrs().Index, Protocol: htons(unix.ETH_P_ARP), Halen: 6}
	copy(to.Addr[:], []byte{255, 255, 255, 255, 255, 255})
	return unix.Sendto(fd, packet, 0, to)
}

func htons(v uint16) uint16 {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return binary.NativeEndian.Uint16(b[:])
}

func gatewayARP(mac net.HardwareAddr, ip [4]byte) []byte {
	p := make([]byte, 28)
	binary.BigEndian.PutUint16(p[0:2], 1) // Ethernet
	binary.BigEndian.PutUint16(p[2:4], unix.ETH_P_IP)
	p[4], p[5] = 6, 4
	binary.BigEndian.PutUint16(p[6:8], 2) // ARP reply
	copy(p[8:14], mac)
	copy(p[14:18], ip[:])
	copy(p[18:24], []byte{255, 255, 255, 255, 255, 255})
	copy(p[24:28], ip[:])
	return p
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
