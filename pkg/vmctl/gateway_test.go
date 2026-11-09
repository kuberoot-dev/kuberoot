package vmctl

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	routerv1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
	"github.com/kuberoot-dev/kuberoot/pkg/router"
)

func TestGatewayAnnouncement(t *testing.T) {
	mac, _ := net.ParseMAC("52:54:00:12:34:56")
	ip := netip.MustParseAddr("10.123.0.1").As4()
	// Ethernet/IPv4 ARP reply: sender is the new gateway, target is the
	// broadcast MAC at the same IP, so guests replace their cached MAC.
	want, _ := hex.DecodeString("00010800060400025254001234560a7b0001ffffffffffff0a7b0001")
	if got := gatewayARP(mac, ip); !bytes.Equal(got, want) {
		t.Fatalf("gratuitous ARP packet: %x, want %x", got, want)
	}
}

func TestGatewayCleanupRequiresLocalLock(t *testing.T) {
	dir := t.TempDir()
	cleanups := 0
	cleanup := func() error { cleanups++; return nil }
	release, err := lockGateway(dir, cleanup)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if second, err := lockGateway(dir, cleanup); err == nil {
		second()
		t.Fatal("second process could clean up a live controller's gateway")
	}
	if cleanups != 1 {
		t.Fatalf("cleanup happened without ownership: %d calls", cleanups)
	}
	release()
	third, err := lockGateway(dir, cleanup)
	if err != nil {
		t.Fatal(err)
	}
	defer third()
	if cleanups != 2 {
		t.Fatalf("replacement did not clean stale state: %d calls", cleanups)
	}
}

func TestFailedGatewayCleanupDoesNotKeepLocalLock(t *testing.T) {
	dir := t.TempDir()
	failure := errors.New("cleanup failed")
	if release, err := lockGateway(dir, func() error { return failure }); !errors.Is(err, failure) || release != nil {
		t.Fatalf("unexpected cleanup failure result: %v", err)
	}
	release, err := lockGateway(dir, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestStableGatewayUsesOnlyAPIBindings(t *testing.T) {
	cfg := router.Config{
		Interfaces: []routerv1.Interface{{Spec: routerv1.InterfaceSpec{Link: "vmbr0", Addresses: []string{"10.123.0.1/24"}}}},
		DHCP: []routerv1.DHCPServer{{ObjectMeta: metav1.ObjectMeta{Name: "machines"}, Spec: routerv1.DHCPServerSpec{
			Link: "vmbr0", RangeStart: "10.123.0.3", RangeEnd: "10.123.0.254", LeaseTime: "12h",
			StaticLeases: []routerv1.StaticLease{{MAC: "52:54:00:11:22:33", Address: "10.123.0.3"}},
		}}},
	}
	before := gatewayDNSConfig(cfg, "/old-node/dnsmasq.leases", true)
	if before != gatewayDNSConfig(cfg, "/new-node/missing.leases", true) {
		t.Fatal("leader-local lease path affected authoritative configuration")
	}
	for _, required := range []string{"leasefile-ro\n", ",static,", "dhcp-ignore=tag:!known\n", "dhcp-ignore-clid\n"} {
		if !strings.Contains(before, required) {
			t.Fatalf("HA configuration lacks %q:\n%s", required, before)
		}
	}
	if strings.Contains(before, "dhcp-leasefile=") {
		t.Fatal("HA config still consumes historical leases")
	}
	// gateway restarts dnsmasq whenever this configuration changes. Removing
	// the old MAC and assigning its IP to a new VM must change that config;
	// the new process starts with no obsolete in-memory or persistent leases.
	cfg.DHCP[0].Spec.StaticLeases[0].MAC = "52:54:00:44:55:66"
	after := gatewayDNSConfig(cfg, "/old-node/dnsmasq.leases", true)
	if before == after || strings.Contains(after, "52:54:00:11:22:33") || !strings.Contains(after, "dhcp-host=52:54:00:44:55:66,10.123.0.3\n") {
		t.Fatalf("changed binding did not replace authoritative config:\n%s", after)
	}
	wantLegacy := router.DnsmasqConfig(cfg, "legacy.leases") + "user=root\npid-file=\n"
	if got := gatewayDNSConfig(cfg, "legacy.leases", false); got != wantLegacy {
		t.Fatalf("non-HA gateway changed:\n%s", got)
	}
}
