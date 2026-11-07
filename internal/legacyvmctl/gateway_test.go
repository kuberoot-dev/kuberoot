package legacyvmctl

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"testing"
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
