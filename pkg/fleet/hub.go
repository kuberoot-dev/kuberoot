package fleet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"k8s.io/klog/v2"
)

// ErrNotConnected: the node has no open connection to the center.
var ErrNotConnected = errors.New("not connected")

// Hub is the center's side: it takes the nodes' connections, enrolls nodes,
// and lends out a client per connected node. It also proxies the node API of
// a connected node to the center's users at /nodes/<name>/.
type Hub struct {
	// Cert is the center's serving certificate, issued for ServerName, with
	// the authority nodes pin.
	Cert tls.Certificate

	// Identity returns the pinned authority of an enrolled node, "" if the
	// node is unknown or not enrolled.
	Identity func(ctx context.Context, name string) string
	// Enroll checks a node's one-time token and pins its authority.
	Enroll func(ctx context.Context, req EnrollRequest, from string) error
	// Changed is told when a node connects or its connection ends.
	Changed func(name string)

	// UserCAs verify the client certificates of the center's users, for the
	// node API proxy; Authorize decides whether a user may reach a node.
	UserCAs   *x509.CertPool
	Authorize func(ctx context.Context, user string, groups []string, node string) error

	mu    sync.Mutex
	conns map[string]*nodeConn
}

type nodeConn struct {
	cc    *http2.ClientConn
	from  string
	since time.Time
	ended chan struct{}
}

// Conn is a node's connection as the hub sees it.
type Conn struct {
	From  string
	Since time.Time
}

// Connected returns a node's open connection, if it has one.
func (h *Hub) Connected(name string) (Conn, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.conns[name]
	if !ok {
		return Conn{}, false
	}
	return Conn{From: c.from, Since: c.since}, true
}

// Client reaches a connected node's API.
func (h *Hub) Client(name string) (*http.Client, error) {
	h.mu.Lock()
	c, ok := h.conns[name]
	h.mu.Unlock()
	if !ok {
		return nil, ErrNotConnected
	}
	return &http.Client{Transport: c.cc, Timeout: 30 * time.Second}, nil
}

// Serve takes connections on addr until ctx ends.
func (h *Hub) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return h.ServeListener(ctx, ln)
}

// ServeListener takes connections on ln until ctx ends.
func (h *Hub) ServeListener(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/enroll", h.enroll)
	mux.HandleFunc("GET /v1/connect", h.connect)
	mux.HandleFunc("/nodes/", h.proxy)
	srv := &http.Server{
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{h.Cert},
			// Nodes and users present certificates of different authorities,
			// each checked by the handler that serves them.
			ClientAuth: tls.RequestClientCert,
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
		},
		// The node's connection is taken over after an HTTP/1.1 upgrade.
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		h.mu.Lock()
		for _, c := range h.conns {
			_ = c.cc.Close()
		}
		h.mu.Unlock()
	}()
	err := srv.ServeTLS(ln, "", "")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (h *Hub) enroll(w http.ResponseWriter, r *http.Request) {
	var req EnrollRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.Enroll(r.Context(), req, r.RemoteAddr); err != nil {
		klog.Warningf("fleet: enrollment of %q from %s refused: %v", req.Name, r.RemoteAddr, err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	klog.Infof("fleet: %s enrolled from %s", req.Name, r.RemoteAddr)
	_, _ = io.WriteString(w, "enrolled\n")
}

// nodeIdentity checks a node's client certificate against the authority
// the node pinned when it enrolled, and returns the node's name.
func (h *Hub) nodeIdentity(r *http.Request) (string, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", errors.New("no client certificate")
	}
	name := r.TLS.PeerCertificates[0].Subject.CommonName
	pin := h.Identity(r.Context(), name)
	if pin == "" {
		return "", fmt.Errorf("%q is not an enrolled node", name)
	}
	var raw [][]byte
	for _, c := range r.TLS.PeerCertificates {
		raw = append(raw, c.Raw)
	}
	if _, err := verifyPinned(raw, pin, x509.ExtKeyUsageClientAuth); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return name, nil
}

func (h *Hub) connect(w http.ResponseWriter, r *http.Request) {
	name, err := h.nodeIdentity(r)
	if err != nil {
		klog.Warningf("fleet: connection from %s refused: %v", r.RemoteAddr, err)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), TunnelProto) {
		http.Error(w, "upgrade to "+TunnelProto, http.StatusUpgradeRequired)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot take the connection over", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	// The server's header timeout would otherwise end the connection.
	_ = conn.SetDeadline(time.Time{})
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + TunnelProto + "\r\n\r\n")
	if err := rw.Flush(); err != nil {
		conn.Close()
		return
	}
	var c net.Conn = conn
	if rw.Reader.Buffered() > 0 {
		c = &bufferedConn{Conn: conn, r: rw.Reader}
	}
	ended := &endedConn{Conn: c, ended: make(chan struct{})}
	t := &http2.Transport{ReadIdleTimeout: 30 * time.Second, PingTimeout: 15 * time.Second}
	cc, err := t.NewClientConn(ended)
	if err != nil {
		conn.Close()
		return
	}
	nc := &nodeConn{cc: cc, from: r.RemoteAddr, since: time.Now(), ended: ended.ended}
	h.mu.Lock()
	if h.conns == nil {
		h.conns = map[string]*nodeConn{}
	}
	if old, ok := h.conns[name]; ok {
		// The node came back before the old connection was found dead.
		_ = old.cc.Close()
	}
	h.conns[name] = nc
	h.mu.Unlock()
	klog.Infof("fleet: %s connected from %s", name, r.RemoteAddr)
	h.notify(name)
	go h.watch(name, nc)
}

// endedConn tells when reading the connection fails: the node closed it.
type endedConn struct {
	net.Conn
	ended chan struct{}
	once  sync.Once
}

func (c *endedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.once.Do(func() { close(c.ended) })
	}
	return n, err
}

// watch pings a node's connection and drops it once it stops answering.
func (h *Hub) watch(name string, nc *nodeConn) {
	for {
		select {
		case <-time.After(15 * time.Second):
		case <-nc.ended:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := nc.cc.Ping(ctx)
		cancel()
		if err == nil && !nc.cc.State().Closed {
			continue
		}
		_ = nc.cc.Close()
		h.mu.Lock()
		current := h.conns[name] == nc
		if current {
			delete(h.conns, name)
		}
		h.mu.Unlock()
		if current {
			klog.Infof("fleet: %s disconnected (%v)", name, err)
			h.notify(name)
		}
		return
	}
}

// Names lists the connected nodes.
func (h *Hub) Names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.conns))
	for name := range h.conns {
		out = append(out, name)
	}
	return out
}

// Drop closes a node's connection, as when the node is removed.
func (h *Hub) Drop(name string) {
	h.mu.Lock()
	c, ok := h.conns[name]
	delete(h.conns, name)
	h.mu.Unlock()
	if ok {
		_ = c.cc.Close()
	}
}

func (h *Hub) notify(name string) {
	if h.Changed != nil {
		h.Changed(name)
	}
}

// proxy serves /nodes/<name>/... to the center's users: the node's API
// through its connection, for whoever may reach that node.
func (h *Hub) proxy(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/nodes/")
	name, path, _ := strings.Cut(rest, "/")
	user, groups, err := h.user(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if err := h.Authorize(r.Context(), user, groups, name); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	h.mu.Lock()
	c, ok := h.conns[name]
	h.mu.Unlock()
	if !ok {
		http.Error(w, "node "+name+" is not connected", http.StatusServiceUnavailable)
		return
	}
	p := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host, pr.Out.Host = "https", name, name
			pr.Out.URL.Path, pr.Out.URL.RawPath = "/"+path, ""
			pr.Out.Header.Del("Authorization")
		},
		Transport:     c.cc,
		FlushInterval: -1,
	}
	p.ServeHTTP(w, r)
}

// user is the center user a client certificate names.
func (h *Hub) user(r *http.Request) (string, []string, error) {
	if h.UserCAs == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", nil, errors.New("a client certificate of the center's cluster is required")
	}
	leaf := r.TLS.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range r.TLS.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: h.UserCAs, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return "", nil, err
	}
	return leaf.Subject.CommonName, leaf.Subject.Organization, nil
}
