// Command kuberoot-store runs a member of the resource store lab on the
// etcd v3 API, replicated by Raft. It takes etcd's flags, so that it can
// stand in for etcd where a member is run as etcd is. See
// docs/resource-store.md.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/resourcestore"
)

type flags struct {
	name, dataDir                            string
	listenClient, advertiseClient            string
	listenPeer, advertisePeer                string
	initialCluster, initialState, token      string
	certFile, keyFile, trustedCA             string
	clientCertAuth                           bool
	peerCertFile, peerKeyFile, peerTrustedCA string
	peerClientCertAuth                       bool
	listenMetrics                            string
	snapshotCount                            uint64
	heartbeatMs, electionMs                  int
}

func main() {
	// kuberoot-store snapshot restore FILE --data-dir DIR, as etcdutl's.
	if len(os.Args) > 2 && os.Args[1] == "snapshot" && os.Args[2] == "restore" {
		if err := restore(os.Args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	var f flags
	flag.StringVar(&f.name, "name", "default", "this member's name in --initial-cluster")
	flag.StringVar(&f.dataDir, "data-dir", "/var/lib/kuberoot-store", "the member's data: its state and Raft log")
	flag.StringVar(&f.listenClient, "listen-client-urls", "http://127.0.0.1:2379", "URLs the etcd v3 API is served on")
	flag.StringVar(&f.advertiseClient, "advertise-client-urls", "", "client URLs published to the other members (default: --listen-client-urls)")
	flag.StringVar(&f.listenPeer, "listen-peer-urls", "http://127.0.0.1:2380", "URLs the other members are served on")
	flag.StringVar(&f.advertisePeer, "initial-advertise-peer-urls", "", "where the other members reach this one (default: --listen-peer-urls)")
	flag.StringVar(&f.initialCluster, "initial-cluster", "", "the store's members, name=peer-URL,... (default: this member alone)")
	flag.StringVar(&f.initialState, "initial-cluster-state", "new", "new: start the store; existing: join the store this member was added to")
	flag.StringVar(&f.token, "initial-cluster-token", "etcd-cluster", "the store's token, part of member IDs")
	flag.StringVar(&f.certFile, "cert-file", "", "the client API's certificate")
	flag.StringVar(&f.keyFile, "key-file", "", "the client API's key")
	flag.StringVar(&f.trustedCA, "trusted-ca-file", "", "the authority of clients' certificates")
	flag.BoolVar(&f.clientCertAuth, "client-cert-auth", false, "require clients' certificates")
	flag.StringVar(&f.peerCertFile, "peer-cert-file", "", "the certificate between members, both ways")
	flag.StringVar(&f.peerKeyFile, "peer-key-file", "", "its key")
	flag.StringVar(&f.peerTrustedCA, "peer-trusted-ca-file", "", "the authority of members' certificates")
	flag.BoolVar(&f.peerClientCertAuth, "peer-client-cert-auth", false, "require members' certificates")
	flag.StringVar(&f.listenMetrics, "listen-metrics-urls", "", "URLs /health and /metrics are served on, plain HTTP")
	flag.Uint64Var(&f.snapshotCount, "snapshot-count", 10000, "entries applied between snapshots of the state")
	flag.IntVar(&f.heartbeatMs, "heartbeat-interval", 100, "milliseconds between the leader's heartbeats")
	flag.IntVar(&f.electionMs, "election-timeout", 1000, "milliseconds without a leader before an election")
	klog.InitFlags(nil)
	flag.Parse()
	if err := run(f); err != nil {
		klog.Fatal(err)
	}
}

func splitURLs(s string) ([]string, error) {
	var out []string
	for _, u := range strings.Split(s, ",") {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, err := url.Parse(u); err != nil {
			return nil, fmt.Errorf("%q: %w", u, err)
		}
		out = append(out, u)
	}
	return out, nil
}

func parseCluster(s, name string, self []string) (map[string][]string, error) {
	out := map[string][]string{}
	if s == "" {
		out[name] = self
		return out, nil
	}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return nil, fmt.Errorf("--initial-cluster: %q is not name=URL", part)
		}
		out[k] = append(out[k], v)
	}
	return out, nil
}

// tlsConfig is a server's or a client's TLS; nil when no certificate is
// given.
func tlsConfig(cert, key, ca string, requireClient bool) (*tls.Config, error) {
	if cert == "" {
		return nil, nil
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	if ca != "" {
		raw, err := os.ReadFile(ca)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("%s holds no certificate", ca)
		}
		cfg.ClientCAs, cfg.RootCAs = pool, pool
	}
	if requireClient {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

func listen(u string, cfg *tls.Config, protos ...string) (net.Listener, error) {
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", parsed.Host)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "https" {
		if cfg == nil {
			l.Close()
			return nil, fmt.Errorf("%s: https without a certificate", u)
		}
		c := cfg.Clone()
		c.NextProtos = protos
		return tls.NewListener(l, c), nil
	}
	return l, nil
}

func run(f flags) error {
	clientURLs, err := splitURLs(f.listenClient)
	if err != nil {
		return err
	}
	advClient := clientURLs
	if f.advertiseClient != "" {
		if advClient, err = splitURLs(f.advertiseClient); err != nil {
			return err
		}
	}
	peerURLs, err := splitURLs(f.listenPeer)
	if err != nil {
		return err
	}
	advPeer := peerURLs
	if f.advertisePeer != "" {
		if advPeer, err = splitURLs(f.advertisePeer); err != nil {
			return err
		}
	}
	cluster, err := parseCluster(f.initialCluster, f.name, advPeer)
	if err != nil {
		return err
	}
	clientTLS, err := tlsConfig(f.certFile, f.keyFile, f.trustedCA, f.clientCertAuth)
	if err != nil {
		return fmt.Errorf("client TLS: %w", err)
	}
	peerTLS, err := tlsConfig(f.peerCertFile, f.peerKeyFile, f.peerTrustedCA, f.peerClientCertAuth)
	if err != nil {
		return fmt.Errorf("peer TLS: %w", err)
	}
	var peerClientTLS *tls.Config
	if peerTLS != nil {
		peerClientTLS = &tls.Config{Certificates: peerTLS.Certificates, RootCAs: peerTLS.RootCAs, MinVersion: tls.VersionTLS12}
	}
	ticks := f.electionMs / f.heartbeatMs
	if ticks < 3 {
		ticks = 3
	}
	// The peers' listener goes first: a member being joined asks for the
	// member list before Raft starts.
	var peerLs []net.Listener
	for _, u := range peerURLs {
		l, err := listen(u, peerTLS, "http/1.1")
		if err != nil {
			return fmt.Errorf("peer listener: %w", err)
		}
		peerLs = append(peerLs, l)
	}
	n, err := resourcestore.StartNode(resourcestore.Config{
		Name: f.name, DataDir: f.dataDir, InitialCluster: cluster, InitialClusterState: f.initialState,
		ClusterToken: f.token, PeerURLs: advPeer, ClientURLs: advClient, PeerClientTLS: peerClientTLS,
		SnapshotCount: f.snapshotCount, TickMs: f.heartbeatMs, ElectionTicks: ticks,
	})
	if err != nil {
		return err
	}
	peerSrv := &http.Server{Handler: n.PeerHandler(), ReadHeaderTimeout: 10 * time.Second}
	for _, l := range peerLs {
		go func(l net.Listener) {
			if err := peerSrv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				klog.Fatalf("peer server: %v", err)
			}
		}(l)
	}
	// -v=4 logs every call and how it ended, for finding what a client
	// asked that the store does not answer as etcd would.
	// Clients ping idle connections, as the API server's etcd client does
	// every 30 seconds; etcd allows pings every 5 seconds.
	g := grpc.NewServer(grpc.MaxRecvMsgSize(16<<20),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Second, PermitWithoutStream: true}),
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			resp, err := h(ctx, req)
			klog.V(4).Infof("%s %v -> err=%v", info.FullMethod, req, err)
			return resp, err
		}))
	(&resourcestore.Server{Node: n}).Register(g)
	for _, u := range clientURLs {
		l, err := listen(u, clientTLS, "h2")
		if err != nil {
			return fmt.Errorf("client listener: %w", err)
		}
		go func() {
			if err := g.Serve(l); err != nil {
				klog.Fatalf("client server: %v", err)
			}
		}()
	}
	if f.listenMetrics != "" {
		urls, err := splitURLs(f.listenMetrics)
		if err != nil {
			return err
		}
		for _, u := range urls {
			l, err := listen(u, nil)
			if err != nil {
				return fmt.Errorf("metrics listener: %w", err)
			}
			go func() { _ = http.Serve(l, metrics(n)) }()
		}
	}
	klog.Infof("member %s (%x) of store %x: clients on %v, peers on %v, data in %s",
		f.name, n.ID(), n.ClusterID(), clientURLs, peerURLs, f.dataDir)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
	case <-n.Removed:
		// As etcd: a member removed from the store stops.
	}
	g.Stop()
	_ = peerSrv.Close()
	n.Stop()
	select {
	case <-n.Removed:
		return errors.New("this member was removed from the store")
	default:
	}
	return nil
}

// restore makes a data directory from a snapshot. It takes etcdutl's
// flags; the member's name and peers are those it is started with.
func restore(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: kuberoot-store snapshot restore FILE --data-dir DIR")
	}
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "the member's data directory to create")
	fs.String("name", "", "ignored: the member is named when it starts")
	fs.String("initial-cluster", "", "ignored: the member starts a store of its own")
	fs.String("initial-advertise-peer-urls", "", "ignored: taken when the member starts")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *dataDir == "" {
		return errors.New("--data-dir is required")
	}
	return resourcestore.Restore(args[0], *dataDir)
}

func metrics(n *resourcestore.Node) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if n.Leader() == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"health":"false","reason":"no leader"}`)
			return
		}
		_, _ = io.WriteString(w, `{"health":"true"}`)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		cur, compacted := n.Store().Revisions()
		leader := 0
		if n.Leader() == n.ID() {
			leader = 1
		}
		hasLeader := 0
		if n.Leader() != 0 {
			hasLeader = 1
		}
		fmt.Fprintf(w, "kuberoot_store_has_leader %d\nkuberoot_store_is_leader %d\nkuberoot_store_term %d\n", hasLeader, leader, n.Term())
		fmt.Fprintf(w, "kuberoot_store_applied_index %d\nkuberoot_store_revision %d\nkuberoot_store_compacted_revision %d\n", n.Applied(), cur, compacted)
		fmt.Fprintf(w, "kuberoot_store_db_size_bytes %d\n", n.Store().DBSize())
	})
	return mux
}
