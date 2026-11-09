package nodeapi

import (
	"context"
	"io"
	"net"
	"slices"
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

// Every node must offer the cluster API on the balancer's address, control
// plane included. A CNI that replaces kube-proxy programs the kubernetes
// Service itself and reaches the API server through it; should that entry
// ever lose its endpoints, the CNI can no longer read the endpoints that
// would refill it, and the node stays cut off until something outside it
// intervenes. The balancer is that address, and it must not depend on which
// node happens to be the control plane today.
func TestEveryNodeOffersTheClusterAPIOnTheBalancer(t *testing.T) {
	for _, c := range []struct {
		name    string
		opts    Options
		servers []string
		follow  string
	}{{
		name:    "a worker of several control planes balances between them and follows the cluster",
		opts:    Options{APIServers: []string{"https://10.0.0.1:6443", "https://10.0.0.2:6443"}, APIKubeconfig: "/var/lib/kubelet/kubeconfig"},
		servers: []string{"https://10.0.0.1:6443", "https://10.0.0.2:6443"},
		follow:  "/var/lib/kubelet/kubeconfig",
	}, {
		name:    "a control plane offers its own API server, and has nothing to follow",
		opts:    Options{AdminKubeconfig: "/etc/kubernetes/admin.kubeconfig"},
		servers: []string{localAPIServer},
	}, {
		name:    "a control plane given control planes to balance between keeps them",
		opts:    Options{APIServers: []string{"https://10.0.0.2:6443"}, AdminKubeconfig: "/etc/kubernetes/admin.kubeconfig"},
		servers: []string{"https://10.0.0.2:6443"},
	}, {
		name: "a worker of a single control plane reaches it directly, as it always has",
		opts: Options{},
	}} {
		t.Run(c.name, func(t *testing.T) {
			servers, follow := balancerTargets(c.opts)
			if !slices.Equal(servers, c.servers) {
				t.Errorf("servers %v, want %v", servers, c.servers)
			}
			if follow != c.follow {
				t.Errorf("follow %q, want %q", follow, c.follow)
			}
		})
	}
}
