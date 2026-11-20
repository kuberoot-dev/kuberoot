package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/vishvananda/netlink"
)

// The MTUs kinit accepts, from DHCP or the kernel command line. 576 is the
// smallest datagram every IPv4 host must take (RFC 791) and the floor RFC
// 2132 sets for option 26; 9000 is a jumbo frame. A value outside is a
// misconfigured server, and the link keeps the MTU it has.
const (
	minMTU = 576
	maxMTU = 9000
)

// uplinkMTUKey is the key of kuberoot.mtu=<n> in bootConfig.mtu: the link
// DHCP runs on, whatever it is called.
const uplinkMTUKey = ""

// parseMTUArg reads the value of kuberoot.mtu=: "1400" for the uplink, or
// "eth1:9000" for a named interface.
func parseMTUArg(v string) (iface string, mtu int, err error) {
	iface, n, named := strings.Cut(v, ":")
	if !named {
		iface, n = uplinkMTUKey, v
	}
	mtu, err = strconv.Atoi(n)
	if err != nil {
		return "", 0, fmt.Errorf("kuberoot.mtu=%s: %w", v, err)
	}
	if !saneMTU(mtu) {
		return "", 0, fmt.Errorf("kuberoot.mtu=%s: outside %d-%d", v, minMTU, maxMTU)
	}
	return iface, mtu, nil
}

func saneMTU(mtu int) bool { return mtu >= minMTU && mtu <= maxMTU }

// dhcpRequest asks the server for the interface MTU (option 26), on top of
// the options the client always asks for. Servers such as KubeVirt's send
// only the options a client asks for.
var dhcpRequest = []dhcpv4.Modifier{dhcpv4.WithRequestedOptions(dhcpv4.OptionInterfaceMTU)}

// leaseMTU is the interface MTU a DHCP server offers (option 26), or 0 when
// it offers none or one outside the sane range.
func leaseMTU(reply *dhcpv4.DHCPv4) int {
	if reply == nil {
		return 0
	}
	mtu, err := dhcpv4.GetUint16(dhcpv4.OptionInterfaceMTU, reply.Options)
	if err != nil || !saneMTU(int(mtu)) {
		return 0
	}
	return int(mtu)
}

// uplinkMTU is the MTU the uplink gets: the one set on the kernel command
// line wins, since whoever set it knows the network better than its DHCP
// server; otherwise the lease's. 0 leaves the link as it is.
func uplinkMTU(explicit, lease int) int {
	if explicit > 0 {
		return explicit
	}
	return lease
}

// explicitUplinkMTU is the MTU the kernel command line sets for the uplink
// named name: kuberoot.mtu=<name>:<n>, else kuberoot.mtu=<n>; 0 for none.
func (c bootConfig) explicitUplinkMTU(name string) int {
	if mtu, ok := c.mtu[name]; ok {
		return mtu
	}
	return c.mtu[uplinkMTUKey]
}

// setMTU applies mtu to link; 0 or the MTU the link already has does nothing.
func setMTU(link netlink.Link, mtu int, source string) error {
	if mtu == 0 || link.Attrs().MTU == mtu {
		return nil
	}
	if err := netlink.LinkSetMTU(link, mtu); err != nil {
		return fmt.Errorf("%s: mtu %d: %w", link.Attrs().Name, mtu, err)
	}
	link.Attrs().MTU = mtu
	log.Printf("%s: mtu %d (%s)", link.Attrs().Name, mtu, source)
	return nil
}
