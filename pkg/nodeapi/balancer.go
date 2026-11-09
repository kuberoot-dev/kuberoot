package nodeapi

import (
	"context"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// apiBalancer gives a worker of several control planes one address for the
// cluster API: it forwards each connection to a control plane that accepts
// it, trying the others when one does not, and learns the control planes
// from the cluster's own kubernetes Service as they come and go. No virtual
// address, no load balancer outside the cluster: it works on any network.
type apiBalancer struct {
	mu      sync.Mutex
	servers []string // host:port
	next    int
	down    map[string]time.Time
}

// backoff keeps a control plane that refused a connection out of turn.
const backoff = 10 * time.Second

// localAPIServer is the API server a control plane runs itself, on the
// secure port every distribution profile gives it. A control plane points
// the balancer here, so that the one address --api-balancer offers answers
// on every node of the cluster.
const localAPIServer = "https://127.0.0.1:6443"

func newAPIBalancer(urls []string) *apiBalancer {
	b := &apiBalancer{down: map[string]time.Time{}}
	b.set(hostPorts(urls))
	return b
}

func hostPorts(urls []string) []string {
	var out []string
	for _, u := range urls {
		if p, err := url.Parse(u); err == nil && p.Host != "" {
			out = append(out, p.Host)
		}
	}
	return out
}

func (b *apiBalancer) set(servers []string) {
	if len(servers) == 0 {
		return
	}
	sort.Strings(servers)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.servers = servers
}

// order is the control planes to try for a connection: in turn, those that
// refused lately last.
func (b *apiBalancer) order() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(b.servers)
	if n == 0 {
		return nil
	}
	var up, down []string
	for i := range n {
		s := b.servers[(b.next+i)%n]
		if until, ok := b.down[s]; ok && time.Now().Before(until) {
			down = append(down, s)
		} else {
			up = append(up, s)
		}
	}
	b.next = (b.next + 1) % n
	return append(up, down...)
}

func (b *apiBalancer) markDown(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.down[s] = time.Now().Add(backoff)
}

// dial connects to the first control plane that accepts.
func (b *apiBalancer) dial(ctx context.Context) (net.Conn, error) {
	var last error
	d := net.Dialer{Timeout: 3 * time.Second}
	for _, s := range b.order() {
		c, err := d.DialContext(ctx, "tcp", s)
		if err == nil {
			return c, nil
		}
		last = err
		b.markDown(s)
	}
	if last == nil {
		last = io.ErrUnexpectedEOF
	}
	return nil, last
}

// serve forwards connections on addr until ctx ends.
func (b *apiBalancer) serve(ctx context.Context, addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		in, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		go func() {
			defer in.Close()
			out, err := b.dial(ctx)
			if err != nil {
				klog.V(2).Infof("API balancer: no control plane answered: %v", err)
				return
			}
			defer out.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(out, in); done <- struct{}{} }()
			go func() { _, _ = io.Copy(in, out); done <- struct{}{} }()
			<-done
		}()
	}
}

// follow keeps the control planes as the cluster lists them: the API
// servers' own endpoints of the kubernetes Service, read through the
// balancer itself.
func (b *apiBalancer) follow(ctx context.Context, kubeconfig string) {
	for ctx.Err() == nil {
		if c := clientFrom(kubeconfig); c != nil {
			slices, err := c.DiscoveryV1().EndpointSlices("default").List(ctx,
				metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=kubernetes"})
			if err == nil {
				var servers []string
				for _, sl := range slices.Items {
					port := "6443"
					for _, p := range sl.Ports {
						if p.Port != nil {
							port = strconv.Itoa(int(*p.Port))
						}
					}
					for _, e := range sl.Endpoints {
						for _, a := range e.Addresses {
							servers = append(servers, net.JoinHostPort(a, port))
						}
					}
				}
				b.set(servers)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
	}
}

// balancerTargets is what the balancer forwards to, and the kubeconfig it
// follows the cluster's control planes with, if any.
//
// A control plane serves the cluster API itself and never needed a balancer
// for its own sake. The cluster around it does: a CNI that replaces
// kube-proxy reaches the API server through the Service it programs itself,
// so an entry left without endpoints leaves it unable to read the endpoints
// that would refill it -- a circle it cannot leave on its own. One address
// that answers on every node, control plane or worker, breaks that circle,
// and unlike a control plane's own address it survives that control plane
// being replaced.
func balancerTargets(o Options) (servers []string, follow string) {
	if len(o.APIServers) > 0 {
		return o.APIServers, o.APIKubeconfig
	}
	if o.AdminKubeconfig == "" {
		return nil, ""
	}
	// A node that runs an API server: the local one is right for as long as
	// it does, so there is nothing to follow. The cluster's own list would
	// only replace it with addresses that can go away.
	return []string{localAPIServer}, ""
}
