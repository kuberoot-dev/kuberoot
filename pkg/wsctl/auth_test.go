package wsctl

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/gorilla/websocket"
)

// fakeProvider is an OpenID provider that signs in whoever it is told to,
// checking the PKCE verifier against the challenge as a real one does.
type fakeProvider struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	user      string
	groups    []string
	challenge map[string]string // code → challenge
	nonce     map[string]string // code → nonce
}

func newFakeProvider(t *testing.T) *fakeProvider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{key: key, challenge: map[string]string{}, nonce: map[string]string{}}
	mux := http.NewServeMux()
	p.srv = httptest.NewTLSServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/auth", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "PKCE required", http.StatusBadRequest)
			return
		}
		code := random()
		p.challenge[code], p.nonce[code] = q.Get("code_challenge"), q.Get("nonce")
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := r.PostForm.Get("code")
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if p.challenge[code] == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge[code] {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k"}}, nil)
		claims, _ := json.Marshal(map[string]any{"iss": p.srv.URL, "aud": "wsctl", "sub": "1", "nonce": p.nonce[code],
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "preferred_username": p.user, "groups": p.groups})
		jws, _ := signer.Sign(claims)
		idt, _ := jws.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "token_type": "Bearer", "id_token": idt})
	})
	return p
}

func (p *fakeProvider) caPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.srv.Certificate().Raw})
}

// signedInGateway serves a gateway with single sign-on over TLS and returns
// a browser-like client whose cookies persist.
func signedInGateway(t *testing.T, p *fakeProvider, access AccessFunc, routes map[string]Route) (*Gateway, *httptest.Server, *http.Client) {
	cfg, err := configFromSecret(map[string][]byte{"issuerURL": []byte(p.srv.URL), "clientID": []byte("wsctl"), "ca.crt": p.caPEM()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAuth(cfg, access)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{NoVNC: t.TempDir()}
	g.SetAuth(a)
	g.SetRoutes(routes)
	srv := httptest.NewTLSServer(g)
	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar
	// The client trusts both the gateway and the provider.
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs.AddCert(p.srv.Certificate())
	return g, srv, client
}

func TestSignInOpensOnlyYourWorkspaces(t *testing.T) {
	vnc := fakeVNC(t)
	defer vnc.Close()
	host, port, _ := net.SplitHostPort(vnc.Addr().String())
	p := newFakeProvider(t)
	defer p.srv.Close()
	p.user, p.groups = "anna", []string{"design"}
	shared := func(_ context.Context, who Person, ws string) (bool, error) {
		return ws == "shared" && len(who.Groups) == 1 && who.Groups[0] == "design", nil
	}
	g, srv, client := signedInGateway(t, p, shared, map[string]Route{
		"anna":   {Address: host, User: "anna", Owner: "anna", RDPPort: 33890},
		"boris":  {Address: host, User: "boris", Owner: "boris"},
		"shared": {Address: host, User: "carol", Owner: "carol"}})
	defer srv.Close()
	pn, _ := strconv.Atoi(port)
	g.DesktopVNCPort = pn

	// Not signed in: off to the provider and back, then the list.
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Signed in as anna") {
		t.Fatalf("index after sign-in: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "/ws/anna/") || !strings.Contains(string(body), "/ws/shared/") || strings.Contains(string(body), "/ws/boris/") {
		t.Fatalf("index lists the wrong workspaces: %s", body)
	}

	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for path, want := range map[string]int{
		"/ws/anna/": http.StatusFound, "/ws/shared/": http.StatusFound,
		"/ws/boris/": http.StatusForbidden, "/ws/boris/vnc": http.StatusForbidden, "/ws/nobody/": http.StatusForbidden,
		// The workspace's token no longer opens it.
		"/ws/boris/?token=t0k": http.StatusForbidden,
	} {
		resp, err := noRedirect.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}

	// The browser's noVNC connects with the ticket the gateway handed it.
	resp, _ = noRedirect.Get(srv.URL + "/ws/anna/")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	wsPath := loc.Query().Get("path")
	if !strings.HasPrefix(wsPath, "ws/anna/vnc?ticket=") {
		t.Fatalf("noVNC path %q", wsPath)
	}
	dialer := websocket.Dialer{TLSClientConfig: client.Transport.(*http.Transport).TLSClientConfig}
	ws, _, err := dialer.Dial("wss"+strings.TrimPrefix(srv.URL, "https")+"/"+wsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, msg, err := ws.ReadMessage(); err != nil || !strings.HasPrefix(string(msg), "RFB") {
		t.Fatalf("greeting %q %v", msg, err)
	}
	ws.Close()
	// That ticket is anna's workspace's only.
	ticket := strings.TrimPrefix(wsPath, "ws/anna/vnc?ticket=")
	if _, resp, err := dialer.Dial("wss"+strings.TrimPrefix(srv.URL, "https")+"/ws/boris/vnc?ticket="+ticket, nil); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anna's ticket opened boris's desktop: %v", err)
	}

	// The RDP file carries a ticket as the routing token.
	resp, _ = client.Get(srv.URL + "/ws/anna/rdp")
	rdp, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(rdp), "loadbalanceinfo:s:Cookie: msts=") || !strings.Contains(string(rdp), "username:s:anna") {
		t.Fatalf("rdp file:\n%s", rdp)
	}
}

func TestTicketsExpireAndKeepToTheirKind(t *testing.T) {
	a, err := NewAuth(OIDCConfig{IssuerURL: "https://x", ClientID: "c"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.TicketTTL = -time.Second
	old, _ := a.Ticket("desk", Person{User: "anna"})
	if _, err := a.CheckTicket("desk", old); err == nil {
		t.Error("an expired ticket opened the desktop")
	}
	a.TicketTTL = time.Minute
	tok, _ := a.Ticket("desk", Person{User: "anna"})
	if p, err := a.CheckTicket("desk", tok); err != nil || p.User != "anna" {
		t.Errorf("fresh ticket: %v %v", p, err)
	}
	if _, err := a.CheckTicket("desk", tok[:len(tok)-2]+"xx"); err == nil {
		t.Error("a forged ticket opened the desktop")
	}
	// A ticket is no session, and a session is no ticket.
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	if _, ok := a.Session(r); ok {
		t.Error("a ticket passed for a session")
	}
	sess, _ := a.seal("session", time.Minute, Person{User: "anna"})
	if _, err := a.CheckTicket("desk", sess); err == nil {
		t.Error("a session passed for a ticket")
	}
}

func TestLocalPath(t *testing.T) {
	for in, want := range map[string]string{"/ws/a/": "/ws/a/", "": "/", "https://evil": "/", "//evil": "/", "/\\evil": "/"} {
		if got := localPath(in); got != want {
			t.Errorf("localPath(%q) = %q", in, got)
		}
	}
}

// connectionRequest is an RDP client's first packet, with a line before
// the negotiation request as mstsc and FreeRDP send it.
func connectionRequest(line string) []byte {
	neg := []byte{1, 0, 8, 0, 3, 0, 0, 0}
	data := append([]byte(line), neg...)
	pkt := make([]byte, 11, 11+len(data))
	pkt[0] = 3
	pkt[5] = 0xe0
	pkt = append(pkt, data...)
	binary.BigEndian.PutUint16(pkt[2:], uint16(len(pkt)))
	pkt[4] = byte(len(pkt) - 5)
	return pkt
}

func TestRDPTicketIsTakenOut(t *testing.T) {
	for _, line := range []string{"Cookie: msts=T1.x\r\n", "Cookie: mstshash=T1.x\r\n", "T1.x\r\n"} {
		pkt, err := readConnectionRequest(strings.NewReader(string(connectionRequest(line))))
		if err != nil {
			t.Fatal(err)
		}
		ticket, rest, err := takeTicket(pkt)
		if err != nil || ticket != "T1.x" {
			t.Fatalf("%q: ticket %q %v", line, ticket, err)
		}
		if want := connectionRequest(""); string(rest) != string(want) {
			t.Errorf("%q: passed on %x, want %x", line, rest, want)
		}
	}
	if _, _, err := takeTicket(connectionRequest("")); err == nil {
		t.Error("a request without a ticket passed")
	}
}

func TestRDPNeedsATicket(t *testing.T) {
	// The desktop answers whatever request reaches it with what it got.
	desk, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer desk.Close()
	got := make(chan []byte, 4)
	go func() {
		for {
			c, err := desk.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				pkt, err := readConnectionRequest(c)
				if err == nil {
					got <- pkt
					_, _ = c.Write([]byte("CC"))
				}
			}()
		}
	}()
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	rdpPort := int32(free.Addr().(*net.TCPAddr).Port)
	free.Close()
	a, _ := NewAuth(OIDCConfig{IssuerURL: "https://x", ClientID: "c"}, nil)
	_, deskPort, _ := net.SplitHostPort(desk.Addr().String())
	g := &Gateway{}
	g.SetAuth(a)
	g.SetRoutes(map[string]Route{"desk": {Address: "127.0.0.1", RDPPort: rdpPort}})
	defer g.SetRoutes(nil)
	dp, _ := strconv.Atoi(deskPort)
	rdpDesktopPort = dp
	defer func() { rdpDesktopPort = RDPPort }()

	try := func(line string) string {
		c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(rdpPort))))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = c.Write(connectionRequest(line))
		b, _ := io.ReadAll(c)
		return string(b)
	}
	if out := try("Cookie: mstshash=anna\r\n"); out != "" {
		t.Errorf("no ticket, yet the desktop answered %q", out)
	}
	other, _ := a.Ticket("other", Person{User: "anna"})
	if out := try("Cookie: msts=" + other + "\r\n"); out != "" {
		t.Errorf("another workspace's ticket, yet the desktop answered %q", out)
	}
	tok, _ := a.Ticket("desk", Person{User: "anna"})
	if out := try("Cookie: msts=" + tok + "\r\n"); out != "CC" {
		t.Fatalf("with a ticket the desktop answered %q", out)
	}
	if pkt := <-got; string(pkt) != string(connectionRequest("")) {
		t.Errorf("the desktop got %x", pkt)
	}
}
