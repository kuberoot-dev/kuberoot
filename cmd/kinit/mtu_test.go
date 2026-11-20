package main

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

func reply(t *testing.T, opts ...dhcpv4.Option) *dhcpv4.DHCPv4 {
	t.Helper()
	mods := []dhcpv4.Modifier{dhcpv4.WithMessageType(dhcpv4.MessageTypeAck)}
	for _, o := range opts {
		mods = append(mods, dhcpv4.WithOption(o))
	}
	r, err := dhcpv4.New(mods...)
	if err != nil {
		t.Fatal(err)
	}
	// Through the wire format, as the lease arrives.
	parsed, err := dhcpv4.FromBytes(r.ToBytes())
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func mtuOption(raw []byte) dhcpv4.Option {
	return dhcpv4.Option{Code: dhcpv4.OptionInterfaceMTU, Value: dhcpv4.OptionGeneric{Data: raw}}
}

func be16(n uint16) []byte { return binary.BigEndian.AppendUint16(nil, n) }

func TestLeaseMTU(t *testing.T) {
	for name, c := range map[string]struct {
		opts []dhcpv4.Option
		want int
	}{
		"offered":            {[]dhcpv4.Option{mtuOption(be16(1400))}, 1400},
		"jumbo":              {[]dhcpv4.Option{mtuOption(be16(9000))}, 9000},
		"the floor":          {[]dhcpv4.Option{mtuOption(be16(576))}, 576},
		"not offered":        {nil, 0},
		"below the floor":    {[]dhcpv4.Option{mtuOption(be16(575))}, 0},
		"above jumbo":        {[]dhcpv4.Option{mtuOption(be16(9001))}, 0},
		"zero":               {[]dhcpv4.Option{mtuOption(be16(0))}, 0},
		"one byte":           {[]dhcpv4.Option{mtuOption([]byte{5})}, 0},
		"four bytes":         {[]dhcpv4.Option{mtuOption([]byte{0, 0, 5, 120})}, 0},
		"beside other stuff": {[]dhcpv4.Option{dhcpv4.OptRouter(net.IPv4(10, 0, 0, 1)), mtuOption(be16(1450)), dhcpv4.OptDNS(net.IPv4(1, 1, 1, 1))}, 1450},
	} {
		if got := leaseMTU(reply(t, c.opts...)); got != c.want {
			t.Errorf("%s: leaseMTU = %d, want %d", name, got, c.want)
		}
	}
	if got := leaseMTU(nil); got != 0 {
		t.Errorf("no lease: %d", got)
	}
}

func TestDHCPRequestAsksForTheInterfaceMTU(t *testing.T) {
	d, err := dhcpv4.NewDiscovery(net.HardwareAddr{2, 0, 0, 0, 0, 1}, dhcpRequest...)
	if err != nil {
		t.Fatal(err)
	}
	if !d.ParameterRequestList().Has(dhcpv4.OptionInterfaceMTU) {
		t.Errorf("option 26 is not asked for: %v", d.ParameterRequestList())
	}
	// Asking for it keeps what the client asks for anyway.
	for _, o := range []dhcpv4.OptionCode{dhcpv4.OptionSubnetMask, dhcpv4.OptionRouter, dhcpv4.OptionDomainNameServer} {
		if !d.ParameterRequestList().Has(o) {
			t.Errorf("%v is no longer asked for", o)
		}
	}
}

func TestMTUFromCmdline(t *testing.T) {
	cfg := parseCmdline("kuberoot.mtu=1400 kuberoot.mtu=eth1:9000")
	if cfg.mtu[uplinkMTUKey] != 1400 || cfg.mtu["eth1"] != 9000 {
		t.Errorf("mtu = %v", cfg.mtu)
	}
	for _, bad := range []string{"kuberoot.mtu=", "kuberoot.mtu=big", "kuberoot.mtu=100", "kuberoot.mtu=65000", "kuberoot.mtu=eth1:", "kuberoot.mtu=eth1:1"} {
		if got := parseCmdline(bad).mtu; len(got) != 0 {
			t.Errorf("%q accepted: %v", bad, got)
		}
	}
	if got := parseCmdline("").mtu; len(got) != 0 {
		t.Errorf("no argument: %v", got)
	}
}

func TestExplicitMTUWinsOverTheLease(t *testing.T) {
	for _, c := range []struct{ explicit, lease, want int }{
		{0, 0, 0},       // nothing said: the link keeps its MTU
		{0, 1400, 1400}, // the lease alone
		{1300, 0, 1300}, // the command line alone
		{1300, 1400, 1300},
		{9000, 1400, 9000},
	} {
		if got := uplinkMTU(c.explicit, c.lease); got != c.want {
			t.Errorf("uplinkMTU(%d, %d) = %d, want %d", c.explicit, c.lease, got, c.want)
		}
	}
	// A value for the uplink by name wins over the unnamed one.
	cfg := parseCmdline("kuberoot.mtu=1400 kuberoot.mtu=eth0:1380")
	if got := cfg.explicitUplinkMTU("eth0"); got != 1380 {
		t.Errorf("eth0: %d", got)
	}
	if got := cfg.explicitUplinkMTU("ens3"); got != 1400 {
		t.Errorf("ens3: %d", got)
	}
	if got := parseCmdline("").explicitUplinkMTU("eth0"); got != 0 {
		t.Errorf("none set: %d", got)
	}
}
