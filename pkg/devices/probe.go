package devices

import (
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	v1 "github.com/tym83/kuberoot/pkg/apis/devices/v1alpha1"
)

// probe checks a service from outside, as a client would: an HTTP request,
// a TCP connection, an ICMP echo. It reports up, how long it took, and the
// HTTP status; a service down is a reading too, with the error.
type probe struct {
	spec   v1.DeviceSpec
	client *http.Client
}

func (p *probe) Read() (Reading, error) {
	start := time.Now()
	r := Reading{"up": 0}
	var err error
	switch p.spec.Protocol {
	case "http":
		err = p.http(r)
	case "tcp":
		var c net.Conn
		if c, err = net.DialTimeout("tcp", p.spec.Address, timeout(p.spec)); err == nil {
			_ = c.Close()
		}
	case "icmp":
		err = ping(p.spec.Address, timeout(p.spec))
	}
	if err == nil {
		r["up"] = 1
		r["latency_seconds"] = time.Since(start).Seconds()
	}
	return pick(p.spec.Points, r), err
}

func (p *probe) http(r Reading) error {
	resp, err := p.client.Get(p.spec.Address)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	r["status"] = float64(resp.StatusCode)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s answered %s", p.spec.Address, resp.Status)
	}
	return nil
}

func (p *probe) Close() {}

// ping sends one ICMP echo and waits for its reply. kuberoot-devices runs
// as root, which may open raw ICMP sockets; IPv4 only.
func ping(host string, timeout time.Duration) error {
	addr, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return err
	}
	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("icmp: %w", err)
	}
	defer c.Close()
	id, seq := os.Getpid()&0xffff, int(rand.Uint32()&0xffff)
	msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("kuberoot")}}
	b, err := msg.Marshal(nil)
	if err != nil {
		return err
	}
	if _, err := c.WriteTo(b, addr); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	if err := c.SetReadDeadline(deadline); err != nil {
		return err
	}
	buf := make([]byte, 1500)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			return fmt.Errorf("no echo reply from %s: %w", host, err)
		}
		reply, err := icmp.ParseMessage(1, buf[:n])
		if err != nil || reply.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		// Replies to other pings arrive on the same raw socket.
		if e, ok := reply.Body.(*icmp.Echo); ok && e.ID == id && e.Seq == seq && from.String() == addr.String() {
			return nil
		}
	}
}
