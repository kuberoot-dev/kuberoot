package nodeapi

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// echoServer answers each connection with its name.
func echoServer(t *testing.T, name string) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, name)
			c.Close()
		}
	}()
	return l.Addr().String()
}

func TestBalancerSkipsAControlPlaneThatIsDown(t *testing.T) {
	up := echoServer(t, "up")
	gone, _ := net.Listen("tcp", "127.0.0.1:0")
	down := gone.Addr().String()
	gone.Close()
	b := newAPIBalancer([]string{"https://" + down, "https://" + up})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	front, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := front.Addr().String()
	front.Close()
	go func() { _ = b.serve(ctx, addr) }()
	time.Sleep(100 * time.Millisecond)

	for i := range 4 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(c)
		c.Close()
		if string(got) != "up" {
			t.Fatalf("connection %d reached %q", i, got)
		}
	}
	// The one that refused waits its turn out.
	if o := b.order(); o[len(o)-1] != down {
		t.Fatalf("order %v: the control plane that is down goes last", o)
	}
}

func TestBalancerFollowsTheServersItIsGiven(t *testing.T) {
	b := newAPIBalancer([]string{"https://10.0.0.1:6443"})
	b.set([]string{"10.0.0.2:6443", "10.0.0.1:6443"})
	if o := b.order(); len(o) != 2 {
		t.Fatalf("order %v", o)
	}
	b.set(nil) // an empty list from a cluster that answered nothing keeps the known ones
	if o := b.order(); len(o) != 2 {
		t.Fatalf("order after an empty list: %v", o)
	}
}
