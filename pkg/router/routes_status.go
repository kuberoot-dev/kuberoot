package router

import (
	"cmp"
	"slices"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// ReadRoutes observes the kernel's main table, without claiming ownership of
// other writers' routes. Bound status size even on a full-table BGP router.
func ReadRoutes() ([]v1.KernelRoute, bool, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, false, err
	}
	names := map[int]string{}
	for _, link := range links {
		names[link.Attrs().Index] = link.Attrs().Name
	}
	var rows []v1.KernelRoute
	truncated := false
	err = netlink.RouteListFilteredIter(netlink.FAMILY_ALL, &netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE, func(r netlink.Route) bool {
		for _, row := range routeRows(r, names) {
			if len(rows) == 256 {
				truncated = true
				return false
			}
			rows = append(rows, row)
		}
		return true
	})
	if err != nil {
		return nil, false, err
	}
	slices.SortFunc(rows, func(a, b v1.KernelRoute) int {
		for _, pair := range [][2]string{{a.Destination, b.Destination}, {a.Gateway, b.Gateway}, {a.Link, b.Link}} {
			if c := cmp.Compare(pair[0], pair[1]); c != 0 {
				return c
			}
		}
		for _, pair := range [][2]int32{{a.Protocol, b.Protocol}, {a.Metric, b.Metric}, {a.Type, b.Type}, {a.Weight, b.Weight}} {
			if c := cmp.Compare(pair[0], pair[1]); c != 0 {
				return c
			}
		}
		return 0
	})
	return rows, truncated, nil
}

func routeRows(r netlink.Route, names map[int]string) []v1.KernelRoute {
	dst := "0.0.0.0/0"
	if r.Family == netlink.FAMILY_V6 {
		dst = "::/0"
	}
	if r.Dst != nil {
		dst = r.Dst.String()
	}
	row := v1.KernelRoute{Destination: dst, Link: names[r.LinkIndex], Protocol: int32(r.Protocol), Metric: int32(r.Priority), Type: int32(r.Type)}
	if len(r.Gw) > 0 {
		row.Gateway = r.Gw.String()
	}
	if len(r.MultiPath) == 0 {
		return []v1.KernelRoute{row}
	}
	rows := make([]v1.KernelRoute, 0, len(r.MultiPath))
	for _, hop := range r.MultiPath {
		next := row
		next.Link, next.Gateway, next.Weight = names[hop.LinkIndex], "", int32(hop.Hops+1)
		if len(hop.Gw) > 0 {
			next.Gateway = hop.Gw.String()
		}
		rows = append(rows, next)
	}
	return rows
}
