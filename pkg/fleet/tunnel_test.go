package fleet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/pki"
)

type testFleet struct {
	hub       *Hub
	addr      string
	centerPin string
	centerCA  *pki.Authority
	centerPEM []byte
	nodeDir   string
	connected chan string
}

func writeAuthority(t *testing.T, dir, name string) (*pki.Authority, []byte) {
	t.Helper()
	ca, certPEM, keyPEM, err := pki.NewAuthority(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".crt"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return ca, certPEM
}

// newTestFleet starts a center hub and a node API, as the agent finds them:
// a node CA, a serving CA and an API that answers with the path it was asked.
func newTestFleet(t *testing.T, ctx context.Context) (*testFleet, string) {
	t.Helper()
	dir := t.TempDir()
	pkiDir := filepath.Join(dir, "pki")
	if err := os.MkdirAll(pkiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	nodeCA, _ := writeAuthority(t, pkiDir, "node-ca")
	serving, _ := writeAuthority(t, pkiDir, "node-serving-ca")
	certPEM, keyPEM, err := serving.Issue(pki.Spec{CommonName: "localhost", DNSNames: []string{"localhost"}, IPs: []net.IP{net.ParseIP("127.0.0.1")}, Server: true})
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := tls.X509KeyPair(certPEM, keyPEM)
	clients := x509.NewCertPool()
	clients.AddCert(nodeCA.Certificate())
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := r.TLS.PeerCertificates[0].Subject
		if !slices.Contains(peer.Organization, "admins") {
			http.Error(w, "not an admin", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, peer.CommonName+" "+r.URL.Path)
	}))
	api.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clients}
	api.StartTLS()
	t.Cleanup(api.Close)

	centerCA, centerPEM, _, err := pki.NewAuthority("center")
	if err != nil {
		t.Fatal(err)
	}
	servingCert, err := ServingCert(centerCA, centerPEM)
	if err != nil {
		t.Fatal(err)
	}
	f := &testFleet{centerCA: centerCA, centerPEM: centerPEM, centerPin: Hash(centerCA.Certificate()), nodeDir: dir, connected: make(chan string, 10)}
	var mu sync.Mutex
	pins := map[string]string{}
	f.hub = &Hub{
		Cert: servingCert,
		Identity: func(_ context.Context, name string) string {
			mu.Lock()
			defer mu.Unlock()
			return pins[name]
		},
		Enroll: func(_ context.Context, req EnrollRequest, _ string) error {
			if req.Name != "shop-1" || req.Token != "t0k3n" {
				return errors.New("token refused")
			}
			pin, err := HashPEM([]byte(req.CA))
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			pins[req.Name] = pin
			return nil
		},
		Changed: func(name string) { f.connected <- name },
		Authorize: func(_ context.Context, user string, _ []string, _ string) error {
			if user != "alice" {
				return errors.New("not alice")
			}
			return nil
		},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.addr = ln.Addr().String()
	go func() { _ = f.hub.ServeListener(ctx, ln) }()
	return f, api.URL
}

func (f *testFleet) agent(api string) *Agent {
	return &Agent{StateDir: filepath.Join(f.nodeDir, "fleet"), PKIDir: filepath.Join(f.nodeDir, "pki"), LocalAPI: api, AdminGroup: "admins"}
}

func waitConnected(t *testing.T, f *testFleet, name string) {
	t.Helper()
	select {
	case got := <-f.connected:
		if got != name {
			t.Fatalf("connected %s, want %s", got, name)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("%s did not connect", name)
	}
}

func fetch(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// A node enrolls with its token, connects out, and the center reaches the
// node API through the connection, nothing else of the node.
func TestEnrollAndReachNodeAPI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, api := newTestFleet(t, ctx)
	a := f.agent(api)
	if err := a.Save(&Enrollment{Name: "shop-1", Servers: []string{f.addr}, CAHash: f.centerPin, Token: "t0k3n"}); err != nil {
		t.Fatal(err)
	}
	go a.Run(ctx)
	waitConnected(t, f, "shop-1")

	e, _ := a.Load()
	if !e.Enrolled || e.Token != "" {
		t.Errorf("after enrolling: %+v, want enrolled and the token gone", e)
	}
	client, err := f.hub.Client("shop-1")
	if err != nil {
		t.Fatal(err)
	}
	code, body := fetch(t, client, "https://node/apis/node.kuberoot.dev/v1alpha1/bootentries")
	if code != http.StatusOK || body != "kuberoot:fleet-center /apis/node.kuberoot.dev/v1alpha1/bootentries" {
		t.Errorf("node API: %d %q", code, body)
	}
	if code, _ := fetch(t, client, "https://node/api/v1/secrets"); code != http.StatusForbidden {
		t.Errorf("outside the node API: %d, want 403", code)
	}
	if a.Status().Phase != AgentConnected {
		t.Errorf("agent %+v", a.Status())
	}

	// Users of the center reach it too, if allowed.
	userCA, _, _, _ := pki.NewAuthority("users")
	f.hub.UserCAs = x509.NewCertPool()
	f.hub.UserCAs.AddCert(userCA.Certificate())
	roots := x509.NewCertPool()
	roots.AddCert(f.centerCA.Certificate())
	as := func(user string) *http.Client {
		certPEM, keyPEM, _ := userCA.Issue(pki.Spec{CommonName: user})
		cert, _ := tls.X509KeyPair(certPEM, keyPEM)
		return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: ServerName}}}
	}
	url := "https://" + f.addr + "/nodes/shop-1/apis/node.kuberoot.dev/v1alpha1/nodeservices"
	if code, body := fetch(t, as("alice"), url); code != http.StatusOK || !strings.HasSuffix(body, "/apis/node.kuberoot.dev/v1alpha1/nodeservices") {
		t.Errorf("alice through the center: %d %q", code, body)
	}
	if code, _ := fetch(t, as("bob"), url); code != http.StatusForbidden {
		t.Errorf("bob through the center: %d, want 403", code)
	}

	// Leaving the fleet closes the connection.
	if err := a.Remove(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(40 * time.Second)
	for {
		if _, ok := f.hub.Connected("shop-1"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("still connected after leaving the fleet")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// The node talks to no center but the pinned one, and the center to no node
// but one whose own authority it pinned.
func TestPinsBothWays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, _ := newTestFleet(t, ctx)
	nodeCA, nodePEM, _, _ := pki.NewAuthority("node")
	identity, err := issueChain(nodeCA, nodePEM, pki.Spec{CommonName: "shop-1"})
	if err != nil {
		t.Fatal(err)
	}
	other, _, _, _ := pki.NewAuthority("impostor")
	if _, err := dialCenter(ctx, f.addr, Hash(other.Certificate()), &identity); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Errorf("a center with another authority: %v", err)
	}
	// Not enrolled yet: refused.
	if _, err := dialCenter(ctx, f.addr, f.centerPin, &identity); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a node that never enrolled: %v", err)
	}
	a := &Agent{}
	if err := a.enroll(ctx, f.addr, &Enrollment{Name: "shop-1", CAHash: f.centerPin, Token: "wrong"}, nodePEM); err == nil {
		t.Error("a wrong token enrolled")
	}
	if err := a.enroll(ctx, f.addr, &Enrollment{Name: "shop-1", CAHash: f.centerPin, Token: "t0k3n"}, nodePEM); err != nil {
		t.Fatal(err)
	}
	// Enrolled with nodeCA: a certificate of another authority for the same
	// name does not open the connection; one of nodeCA does.
	otherCA, otherPEM, _, _ := pki.NewAuthority("other")
	stolen, _ := issueChain(otherCA, otherPEM, pki.Spec{CommonName: "shop-1"})
	if _, err := dialCenter(ctx, f.addr, f.centerPin, &stolen); err == nil {
		t.Error("a certificate of another authority opened shop-1's connection")
	}
	conn, err := dialCenter(ctx, f.addr, f.centerPin, &identity)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

// A node told of a second center replica stays connected; when its center
// goes away it connects to the other one.
func TestFailoverToAnotherReplica(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstCtx, stopFirst := context.WithCancel(ctx)
	f, api := newTestFleet(t, firstCtx)
	first := f.addr
	a := f.agent(api)
	if err := a.Save(&Enrollment{Name: "shop-1", Servers: []string{first}, CAHash: f.centerPin, Token: "t0k3n"}); err != nil {
		t.Fatal(err)
	}
	go a.Run(ctx)
	waitConnected(t, f, "shop-1")

	// The second replica: the same authority and pins, as replicas share them.
	second := &Hub{Cert: f.hub.Cert, Identity: f.hub.Identity, Enroll: f.hub.Enroll, Changed: f.hub.Changed}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = second.ServeListener(ctx, ln) }()

	e, _ := a.Load()
	e.Servers = []string{first, ln.Addr().String()}
	if err := a.Save(e); err != nil {
		t.Fatal(err)
	}
	select {
	case name := <-f.connected:
		t.Fatalf("%s reconnected for new addresses", name)
	case <-time.After(2 * time.Second):
	}
	if _, ok := f.hub.Connected("shop-1"); !ok {
		t.Fatal("not connected to the first replica any more")
	}

	stopFirst()
	<-f.connected // the first replica saw it go
	select {
	case <-f.connected:
	case <-time.After(30 * time.Second):
		t.Fatal("the node did not connect to the second replica")
	}
	if _, ok := second.Connected("shop-1"); !ok {
		t.Fatal("not connected to the second replica")
	}
	// The replica counts the node connected before the agent has written
	// down where it is connected to.
	deadline := time.Now().Add(5 * time.Second)
	for a.Status().Server != ln.Addr().String() {
		if time.Now().After(deadline) {
			t.Fatalf("agent %+v", a.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
