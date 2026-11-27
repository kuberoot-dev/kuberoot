package router

import (
	"reflect"
	"testing"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
)

func iface(name, link string, addrs ...string) v1.Interface {
	return v1.Interface{ObjectMeta: named(name), Spec: v1.InterfaceSpec{Link: link, Addresses: addrs}}
}

func TestStaleAddressesOfRemovedInterfaces(t *testing.T) {
	previous := []v1.Interface{
		iface("lan", "eth3", "192.168.10.1/24"),
		iface("wan", "eth4", "198.18.0.1/24", "fd42:1::1/64"),
		iface("uplink", "eth0"), // DHCP: the router never set its addresses
		iface("transit", "eth2", "10.255.0.2/24"),
		iface("moved", "eth5", "10.9.0.1/24"),
	}
	current := []v1.Interface{
		iface("transit", "eth2", "10.255.0.2/24", "10.255.0.20/24"),
		iface("moved", "eth6", "10.9.0.1/24"),
		iface("lan", "eth3"), // keeps the link, no longer addresses it
	}
	want := map[string][]string{
		"eth3": {"192.168.10.1/24"},
		"eth4": {"198.18.0.1/24", "fd42:1::1/64"},
		"eth5": {"10.9.0.1/24"},
	}
	if got := StaleAddresses(previous, current); !reflect.DeepEqual(got, want) {
		t.Fatalf("stale addresses:\n got %v\nwant %v", got, want)
	}
}

func TestNoStaleAddressesWhenNothingWasRemoved(t *testing.T) {
	same := []v1.Interface{iface("lan", "eth3", "192.168.10.1/24"), iface("uplink", "eth0")}
	if got := StaleAddresses(same, same); len(got) != 0 {
		t.Fatalf("addresses still declared were called stale: %v", got)
	}
	if got := StaleAddresses(nil, same); len(got) != 0 {
		t.Fatalf("a first pass found stale addresses: %v", got)
	}
}
