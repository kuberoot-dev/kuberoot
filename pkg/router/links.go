package router

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
)

// RouteProtocol marks the router's routes; routes of any other protocol
// (the kernel's, DHCP's, bird's) are not the controller's to remove.
const RouteProtocol netlink.RouteProtocol = 0x6b

// linkAlias marks VLAN links the router created, so it removes only those.
const linkAlias = "kuberoot-router"

// ApplyInterfaces makes the links match the Interfaces: VLAN links exist,
// MTUs and addresses are set, links are up. Links the router created for
// Interfaces that are gone are removed. Errors are per Interface name.
func ApplyInterfaces(ifs []v1.Interface) map[string]error {
	errs := map[string]error{}
	wanted := map[string]bool{}
	for _, i := range ifs {
		wanted[i.Spec.Link] = true
		if err := applyInterface(i.Spec); err != nil {
			errs[i.Name] = err
		}
	}
	links, _ := netlink.LinkList()
	for _, l := range links {
		if l.Attrs().Alias == linkAlias && !wanted[l.Attrs().Name] {
			_ = netlink.LinkDel(l)
		}
	}
	return errs
}

func applyInterface(s v1.InterfaceSpec) error {
	link, err := netlink.LinkByName(s.Link)
	if s.VLAN != nil {
		if link, err = ensureVLAN(s.Link, *s.VLAN, link, err); err != nil {
			return err
		}
	}
	if err != nil {
		return fmt.Errorf("link %s: %w", s.Link, err)
	}
	if s.MTU > 0 && link.Attrs().MTU != int(s.MTU) {
		if err := netlink.LinkSetMTU(link, int(s.MTU)); err != nil {
			return fmt.Errorf("mtu: %w", err)
		}
	}
	// No addresses: the link's addresses are someone else's (DHCP on the
	// uplink), not the router's to remove.
	if len(s.Addresses) > 0 {
		if err := syncAddresses(link, s.Addresses); err != nil {
			return err
		}
	}
	return netlink.LinkSetUp(link)
}

func ensureVLAN(name string, v v1.VLAN, existing netlink.Link, lookupErr error) (netlink.Link, error) {
	parent, err := netlink.LinkByName(v.Parent)
	if err != nil {
		return nil, fmt.Errorf("parent link %s: %w", v.Parent, err)
	}
	if lookupErr == nil {
		if vl, ok := existing.(*netlink.Vlan); ok && vl.VlanId == int(v.ID) && vl.ParentIndex == parent.Attrs().Index {
			return existing, nil
		}
		if existing.Attrs().Alias != linkAlias {
			return nil, fmt.Errorf("link %s exists and is not the router's VLAN %d on %s", name, v.ID, v.Parent)
		}
		if err := netlink.LinkDel(existing); err != nil {
			return nil, err
		}
	}
	vl := &netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: name, ParentIndex: parent.Attrs().Index}, VlanId: int(v.ID)}
	if err := netlink.LinkAdd(vl); err != nil {
		return nil, fmt.Errorf("create VLAN %s: %w", name, err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, err
	}
	_ = netlink.LinkSetAlias(link, linkAlias)
	_ = netlink.LinkSetUp(parent)
	return link, nil
}

func syncAddresses(link netlink.Link, addresses []string) error {
	want := map[string]*netlink.Addr{}
	for _, a := range addresses {
		addr, err := netlink.ParseAddr(a)
		if err != nil {
			return fmt.Errorf("address %s: %w", a, err)
		}
		want[addr.IPNet.String()] = addr
	}
	have, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return err
	}
	for _, a := range have {
		if a.IP.IsLinkLocalUnicast() {
			continue // IPv6 needs its link-local address
		}
		if _, ok := want[a.IPNet.String()]; !ok {
			if err := netlink.AddrDel(link, &a); err != nil && !errors.Is(err, unix.EADDRNOTAVAIL) {
				return fmt.Errorf("remove %s: %w", a.IPNet, err)
			}
		}
	}
	for _, a := range want {
		if err := netlink.AddrReplace(link, a); err != nil {
			return fmt.Errorf("add %s: %w", a.IPNet, err)
		}
	}
	return nil
}

// ApplyRoutes makes the router's routes the Routes: missing ones added,
// changed ones replaced, ones no Route asks for removed. Errors are per
// Route name.
func ApplyRoutes(routes []v1.Route) map[string]error {
	errs := map[string]error{}
	want := map[string]bool{}
	for _, r := range routes {
		nr, err := kernelRoute(r.Spec)
		if err != nil {
			errs[r.Name] = err
			continue
		}
		if err := netlink.RouteReplace(nr); err != nil {
			errs[r.Name] = fmt.Errorf("route %s: %w", r.Spec.Destination, err)
			continue
		}
		want[routeKey(nr.Dst, nr.Priority)] = true
	}
	for _, fam := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		have, err := netlink.RouteListFiltered(fam, &netlink.Route{Protocol: RouteProtocol}, netlink.RT_FILTER_PROTOCOL)
		if err != nil {
			continue
		}
		for _, r := range have {
			if !want[routeKey(r.Dst, r.Priority)] {
				_ = netlink.RouteDel(&r)
			}
		}
	}
	return errs
}

func kernelRoute(s v1.RouteSpec) (*netlink.Route, error) {
	p, err := netip.ParsePrefix(s.Destination)
	if err != nil {
		return nil, err
	}
	dst := &net.IPNet{IP: p.Masked().Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
	r := &netlink.Route{Dst: dst, Protocol: RouteProtocol, Priority: int(s.Metric)}
	// The kernel stores an IPv6 route of metric 0 as 1024; asked for as
	// such, it is found again by the same key.
	if p.Addr().Is6() && r.Priority == 0 {
		r.Priority = 1024
	}
	if s.Gateway != "" {
		r.Gw = net.ParseIP(s.Gateway)
	}
	if s.Link != "" {
		l, err := netlink.LinkByName(s.Link)
		if err != nil {
			return nil, fmt.Errorf("link %s: %w", s.Link, err)
		}
		r.LinkIndex = l.Attrs().Index
	}
	return r, nil
}

func routeKey(dst *net.IPNet, metric int) string {
	if dst == nil {
		return fmt.Sprintf("default/%d", metric)
	}
	return fmt.Sprintf("%s/%d", dst, metric)
}

// LinkState is what the kernel says about a link.
type LinkState struct {
	MAC, OperState string
	Addresses      []string
}

// ReadLink reports a link's MAC, state and addresses.
func ReadLink(name string) (LinkState, error) {
	l, err := netlink.LinkByName(name)
	if err != nil {
		return LinkState{}, err
	}
	st := LinkState{MAC: l.Attrs().HardwareAddr.String(), OperState: strings.ToLower(l.Attrs().OperState.String())}
	addrs, _ := netlink.AddrList(l, netlink.FAMILY_ALL)
	for _, a := range addrs {
		if !a.IP.IsLinkLocalUnicast() {
			st.Addresses = append(st.Addresses, a.IPNet.String())
		}
	}
	return st, nil
}

// FirstAddress4 is the first IPv4 address of any link but loopback: the
// router's ID for BGP when none is given.
func FirstAddress4() string {
	links, _ := netlink.LinkList()
	for _, l := range links {
		if l.Attrs().Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := netlink.AddrList(l, netlink.FAMILY_V4)
		for _, a := range addrs {
			return a.IP.String()
		}
	}
	return ""
}
