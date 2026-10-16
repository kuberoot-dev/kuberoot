package fleet

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
	"github.com/kuberoot-dev/kuberoot/pkg/pki"
)

// Enrollment is what a node keeps of its fleet: where the center is, how to
// know it, and, until it is used, the one-time token.
type Enrollment struct {
	Name     string   `json:"name"`
	Servers  []string `json:"servers"`
	CAHash   string   `json:"caHash"`
	Token    string   `json:"token,omitempty"`
	Enrolled bool     `json:"enrolled,omitempty"`
}

// Agent phases, as FleetEnrollment reports them.
const (
	AgentEnrolling    = "Enrolling"
	AgentConnected    = "Connected"
	AgentDisconnected = "Disconnected"
)

// AgentStatus is where the agent is.
type AgentStatus struct {
	Phase, Message, Server string
	ConnectedSince         time.Time
}

// Agent is the node's side: it enrolls once, then keeps a connection to
// the center open and serves the node API over it to the center. Nothing on
// the node depends on the center: without one, the node goes on as it is.
type Agent struct {
	// StateDir keeps the enrollment, PKIDir the node's own authority.
	StateDir, PKIDir string
	// LocalAPI is the node API, reached with an admin identity of the node's
	// own authority in AdminGroup.
	LocalAPI   string
	AdminGroup string

	mu     sync.Mutex
	status AgentStatus
	poke   chan struct{}
	// broken is the server whose connection ended last: tried last next
	// time, since a center that went away is likely the one still gone.
	broken string
}

func (a *Agent) file() string { return filepath.Join(a.StateDir, "enrollment.json") }

// Load returns the enrollment, nil when the node is in no fleet.
func (a *Agent) Load() (*Enrollment, error) {
	raw, err := os.ReadFile(a.file())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e := &Enrollment{}
	return e, json.Unmarshal(raw, e)
}

// Save replaces the enrollment and has the agent act on it at once. New
// addresses of the same fleet, the server connected to among them, keep the
// connection: they are used when it ends.
func (a *Agent) Save(e *Enrollment) error {
	if err := os.MkdirAll(a.StateDir, 0o700); err != nil {
		return err
	}
	prev, _ := a.Load()
	raw, _ := json.Marshal(e)
	if err := atomicfile.WriteFile(a.file(), raw, 0o600); err != nil {
		return err
	}
	st := a.Status()
	if prev != nil && prev.Enrolled && e.Enrolled && prev.Name == e.Name && prev.CAHash == e.CAHash &&
		st.Phase == AgentConnected && slices.Contains(e.Servers, st.Server) {
		return nil
	}
	a.changed()
	return nil
}

// Remove leaves the fleet.
func (a *Agent) Remove() error {
	if err := os.Remove(a.file()); err != nil && !os.IsNotExist(err) {
		return err
	}
	a.changed()
	return nil
}

func (a *Agent) Status() AgentStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

func (a *Agent) setStatus(s AgentStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.Message != a.status.Message || s.Phase != a.status.Phase {
		klog.Infof("fleet: %s %s %s", s.Phase, s.Server, s.Message)
	}
	a.status = s
}

func (a *Agent) pokes() chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.poke == nil {
		a.poke = make(chan struct{}, 1)
	}
	return a.poke
}

func (a *Agent) changed() {
	select {
	case a.pokes() <- struct{}{}:
	default:
	}
}

// Run enrolls and connects until ctx ends, starting over whenever the
// enrollment changes. Failures back off up to a minute, with jitter, so a
// fleet that lost its center does not call it all at once when it is back.
func (a *Agent) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		// What changed before now is read below.
		select {
		case <-a.pokes():
		default:
		}
		e, err := a.Load()
		if err != nil || e == nil {
			if err != nil {
				klog.Errorf("fleet: enrollment: %v", err)
			}
			a.setStatus(AgentStatus{})
			select {
			case <-ctx.Done():
			case <-a.pokes():
			}
			continue
		}
		connCtx, cancel := context.WithCancel(ctx)
		go func() {
			// A changed or removed enrollment ends the connection.
			select {
			case <-connCtx.Done():
			case <-a.pokes():
				cancel()
			}
		}()
		connected, err := a.once(connCtx, e)
		cancel()
		if connected {
			backoff = time.Second
		}
		if err != nil && ctx.Err() == nil {
			st := a.Status()
			st.Phase, st.Message, st.ConnectedSince = AgentDisconnected, err.Error(), time.Time{}
			if !e.Enrolled {
				st.Phase = AgentEnrolling
			}
			a.setStatus(st)
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		backoff = min(2*backoff, time.Minute)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		case <-a.pokes():
			backoff = time.Second
		}
	}
}

// once enrolls if the node has not, then serves one connection until it
// ends. It reports whether the connection was made.
func (a *Agent) once(ctx context.Context, e *Enrollment) (bool, error) {
	caPEM, err := os.ReadFile(filepath.Join(a.PKIDir, "node-ca.crt"))
	if err != nil {
		return false, err
	}
	authority, err := pki.LoadAuthority(filepath.Join(a.PKIDir, "node-ca.crt"), filepath.Join(a.PKIDir, "node-ca.key"))
	if err != nil {
		return false, fmt.Errorf("the node's authority: %w", err)
	}
	var lastErr error
	for _, server := range a.order(e.Servers) {
		if !e.Enrolled {
			if err := a.enroll(ctx, server, e, caPEM); err != nil {
				lastErr = fmt.Errorf("enroll at %s: %w", server, err)
				continue
			}
			e.Enrolled, e.Token = true, ""
			if err := a.saveQuiet(e); err != nil {
				return false, err
			}
		}
		identity, err := issueChain(authority, caPEM, pki.Spec{CommonName: e.Name})
		if err != nil {
			return false, err
		}
		local, err := a.localProxy(authority, caPEM)
		if err != nil {
			return false, err
		}
		conn, err := dialCenter(ctx, server, e.CAHash, &identity)
		if err != nil {
			lastErr = fmt.Errorf("connect to %s: %w", server, err)
			continue
		}
		since := time.Now()
		a.setStatus(AgentStatus{Phase: AgentConnected, Server: server, ConnectedSince: since})
		serveTunnel(ctx, conn, local)
		a.mu.Lock()
		a.broken = server
		a.mu.Unlock()
		return true, fmt.Errorf("connection to %s closed after %s", server, time.Since(since).Round(time.Second))
	}
	if lastErr == nil {
		lastErr = errors.New("no server to connect to")
	}
	return false, lastErr
}

// order puts the server whose connection ended last at the end.
func (a *Agent) order(servers []string) []string {
	a.mu.Lock()
	broken := a.broken
	a.mu.Unlock()
	i := slices.Index(servers, broken)
	if i < 0 || len(servers) < 2 {
		return servers
	}
	out := append(slices.Clone(servers[:i]), servers[i+1:]...)
	return append(out, broken)
}

// saveQuiet records the enrollment without restarting the agent.
func (a *Agent) saveQuiet(e *Enrollment) error {
	raw, _ := json.Marshal(e)
	return atomicfile.WriteFile(a.file(), raw, 0o600)
}

// centerTLS trusts only a center vouched for by the pinned authority.
func centerTLS(pin string, cert *tls.Certificate) *tls.Config {
	cfg := &tls.Config{
		ServerName:         ServerName,
		InsecureSkipVerify: true, // verified against the pin instead, below
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{"http/1.1"},
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			leaf, err := verifyPinned(raw, pin, x509.ExtKeyUsageServerAuth)
			if err != nil {
				return fmt.Errorf("the center: %w", err)
			}
			return leaf.VerifyHostname(ServerName)
		},
	}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	return cfg
}

// EnrollRequest is what a node sends to enroll.
type EnrollRequest struct {
	Name  string `json:"name"`
	Token string `json:"token"`
	// CA is the node's own certificate authority, PEM: the center pins it.
	CA string `json:"ca"`
}

func (a *Agent) enroll(ctx context.Context, server string, e *Enrollment, caPEM []byte) error {
	body, _ := json.Marshal(EnrollRequest{Name: e.Name, Token: e.Token, CA: string(caPEM)})
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: centerTLS(e.CAHash, nil)}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+server+"/v1/enroll", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// issueChain issues a client certificate and returns it with its issuer,
// so the other end can check it against the pinned authority.
func issueChain(authority *pki.Authority, caPEM []byte, spec pki.Spec) (tls.Certificate, error) {
	certPEM, keyPEM, err := authority.Issue(spec)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(append(certPEM, caPEM...), keyPEM)
}

// dialCenter opens the node's connection and upgrades it to the tunnel.
func dialCenter(ctx context.Context, server, pin string, identity *tls.Certificate) (net.Conn, error) {
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}, Config: centerTLS(pin, identity)}
	conn, err := dialer.DialContext(ctx, "tcp", server)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	req, _ := http.NewRequest(http.MethodGet, "https://"+ServerName+"/v1/connect", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", TunnelProto)
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		conn.Close()
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufferedConn{Conn: conn, r: br}, nil
}

// bufferedConn reads what the handshake's reader already took first.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// serveTunnel serves HTTP/2 on the node's connection: the center is the
// client, the node the server, though the node dialed.
func serveTunnel(ctx context.Context, conn net.Conn, h http.Handler) {
	// The center pings every 15 seconds: a center that stops, without
	// closing the connection, is found gone in under 40.
	srv := &http2.Server{ReadIdleTimeout: 20 * time.Second, PingTimeout: 15 * time.Second}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	srv.ServeConn(conn, &http2.ServeConnOpts{Context: ctx, Handler: h})
	close(done)
	conn.Close()
}

// tunnelPaths are what the center may reach on the node: the node API and
// the discovery kubectl needs to use it.
var tunnelPaths = []string{"/apis/node.kuberoot.dev/", "/openapi/"}

func allowedPath(p string) bool {
	switch p {
	case "/api", "/apis", "/version", "/apis/node.kuberoot.dev":
		return true
	}
	for _, prefix := range tunnelPaths {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// localProxy forwards the center's requests to the node API with an
// identity of the node's own authority: the center acts on the node as a
// node admin, and the node API's own checks still apply.
func (a *Agent) localProxy(authority *pki.Authority, caPEM []byte) (http.Handler, error) {
	identity, err := issueChain(authority, caPEM, pki.Spec{CommonName: "kuberoot:fleet-center", Orgs: []string{a.AdminGroup}})
	if err != nil {
		return nil, err
	}
	servingCA, err := os.ReadFile(filepath.Join(a.PKIDir, "node-serving-ca.crt"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(servingCA)
	target, err := url.Parse(a.LocalAPI)
	if err != nil {
		return nil, err
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			for name := range r.Out.Header {
				if strings.HasPrefix(name, "Impersonate-") || name == "Authorization" {
					r.Out.Header.Del(name)
				}
			}
		},
		FlushInterval: -1,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{identity}, RootCAs: pool, ServerName: "localhost"},
			ForceAttemptHTTP2: true},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedPath(r.URL.Path) {
			http.Error(w, "the fleet reaches the node API only", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}

// PEMCert parses the first certificate of a PEM bundle.
func PEMCert(raw []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}
