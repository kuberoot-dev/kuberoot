package router

import (
	"github.com/vishvananda/netlink"
	"net"
	"testing"
)

func TestObservedRouteKeepsKernelProtocolAndNextHops(t *testing.T) {
	_, dst, _ := net.ParseCIDR("192.168.20.0/24")
	r := netlink.Route{Dst: dst, Family: netlink.FAMILY_V4, Protocol: RouteProtocol, Priority: 42, Type: 1,
		MultiPath: []*netlink.NexthopInfo{{LinkIndex: 2, Gw: net.ParseIP("10.255.0.2"), Hops: 0}, {LinkIndex: 3, Gw: net.ParseIP("10.255.1.2"), Hops: 2}}}
	rows := routeRows(r, map[int]string{2: "eth2", 3: "eth3"})
	if len(rows) != 2 || rows[0].Destination != "192.168.20.0/24" || rows[0].Protocol != 107 || rows[0].Metric != 42 || rows[0].Gateway != "10.255.0.2" || rows[1].Link != "eth3" || rows[1].Weight != 3 {
		t.Fatalf("lost observed route fields: %+v", rows)
	}
	// A route from another writer retains its actual protocol, not kuberoot's.
	r.Protocol = 192
	if got := routeRows(r, nil)[0].Protocol; got != 192 {
		t.Fatalf("protocol=%d", got)
	}
}

func TestObservedDefaultRoutesAndMissingGateway(t *testing.T) {
	for _, tc := range []struct {
		family int
		dst    string
	}{{netlink.FAMILY_V4, "0.0.0.0/0"}, {netlink.FAMILY_V6, "::/0"}} {
		rows := routeRows(netlink.Route{Family: tc.family, Protocol: 2}, nil)
		if len(rows) != 1 || rows[0].Destination != tc.dst || rows[0].Gateway != "" {
			t.Fatalf("route=%+v", rows)
		}
	}
}
