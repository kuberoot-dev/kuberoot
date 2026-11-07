package legacyvmctl

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	routerv1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha1"
)

// reserveAddresses checkpoints every assignment in the API before DHCP
// may advertise it. A leader on a different node needs no local lease
// file to recover the same assignments. Partial writes are safe: no new
// configuration is returned until all reservations have been persisted.
func (c *Controller) reserveAddresses(ctx context.Context) ([]routerv1.StaticLease, error) {
	list, err := c.Dynamic.Resource(vmsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var machines []v1.VirtualMachine
	for _, u := range list.Items {
		var m v1.VirtualMachine
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &m); err != nil {
			return nil, err
		}
		machines = append(machines, m)
	}
	reservations, err := addressReservations(c.Network, machines, c.leases())
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		u := &list.Items[i]
		m, want := &machines[i], reservations[u.GetName()]
		if want.Address == m.Status.Address && want.MAC == m.Status.MAC {
			continue
		}
		m.Status.Address, m.Status.MAC = want.Address, want.MAC
		u.Object["status"] = toMap(&m.Status)
		if _, err := c.Dynamic.Resource(vmsGVR).UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
			return nil, fmt.Errorf("reserve address of %s: %w", m.Name, err)
		}
	}
	var out []routerv1.StaticLease
	for _, r := range reservations {
		if r.Address != "" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MAC < out[j].MAC })
	return out, nil
}

func addressReservations(network netip.Prefix, machines []v1.VirtualMachine, local map[string]string) (map[string]routerv1.StaticLease, error) {
	if !network.IsValid() || !network.Addr().Is4() || network.Bits() > 29 {
		return nil, fmt.Errorf("machine network must be IPv4 with at least eight addresses")
	}
	first, last := network.Masked().Addr().Next().Next().Next(), lastHost(network)
	used, macs := map[netip.Addr]string{}, map[string]string{}
	assignments := map[string]routerv1.StaticLease{}
	// First reserve every existing address, including deleting machines
	// whose finalizer has not yet stopped their processes on the nodes.
	for _, m := range machines {
		mac := strings.ToLower(m.Status.MAC)
		if mac == "" {
			mac = MAC(m.Name)
		}
		parsedMAC, err := net.ParseMAC(mac)
		if err != nil || len(parsedMAC) != 6 {
			return nil, fmt.Errorf("machine %s has invalid MAC %q", m.Name, mac)
		}
		mac = parsedMAC.String()
		if other, exists := macs[mac]; exists {
			return nil, fmt.Errorf("machines %s and %s share MAC %s", other, m.Name, mac)
		}
		macs[mac] = m.Name
		address := m.Status.Address
		if address == "" {
			address = local[mac] // adopt a lease on the old, single gateway
		}
		if address == "" && m.Status.Node != "" {
			return nil, fmt.Errorf("machine %s was already placed without a persisted address; adopt its lease on the original gateway before enabling HA", m.Name)
		}
		if address != "" {
			ip, err := netip.ParseAddr(address)
			if err != nil || !ip.Is4() || ip.Compare(first) < 0 || ip.Compare(last) > 0 {
				return nil, fmt.Errorf("machine %s address %q is outside the DHCP range", m.Name, address)
			}
			if other, exists := used[ip]; exists {
				return nil, fmt.Errorf("machines %s and %s share address %s", other, m.Name, ip)
			}
			used[ip] = m.Name
		}
		assignments[m.Name] = routerv1.StaticLease{MAC: mac, Address: address}
	}
	// Sort separately; callers still pair machines with their API objects.
	var names []string
	for _, m := range machines {
		if m.DeletionTimestamp == nil && assignments[m.Name].Address == "" {
			names = append(names, m.Name)
		}
	}
	sort.Strings(names)
	next := first
	for _, name := range names {
		for next.IsValid() && next.Compare(last) <= 0 && used[next] != "" {
			next = next.Next()
		}
		if !next.IsValid() || next.Compare(last) > 0 {
			return nil, fmt.Errorf("machine network %s has no free address for %s", network, name)
		}
		r := assignments[name]
		r.Address = next.String()
		assignments[name], used[next] = r, name
	}
	return assignments, nil
}
