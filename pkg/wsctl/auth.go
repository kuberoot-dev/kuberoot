package wsctl

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig is how the gateway signs people in: the Secret
// workstation/gateway-oidc, one key a field. With no Secret the gateway
// admits a browser by the workspace's token, as before.
type OIDCConfig struct {
	// IssuerURL is the identity provider's issuer, discovered from
	// <issuer>/.well-known/openid-configuration.
	IssuerURL string
	ClientID  string
	// ClientSecret is empty for a public client: the code is bound to the
	// login by PKCE either way.
	ClientSecret string
	// RedirectURL is where the provider sends the browser back to:
	// https://<advertise>:<port>/callback when empty.
	RedirectURL string
	// UsernameClaim names the person (preferred_username when empty) and
	// GroupsClaim their groups (groups when empty).
	UsernameClaim string
	GroupsClaim   string
	// UsernamePrefix and GroupsPrefix are put before the names when the
	// gateway asks the cluster whether someone may open a workspace, as
	// the API server's --oidc-username-prefix does.
	UsernamePrefix string
	GroupsPrefix   string
	// CA, in PEM, is trusted for the provider besides the system's roots.
	CA []byte
}

// configFromSecret reads an OIDCConfig from a Secret's data.
func configFromSecret(data map[string][]byte) (OIDCConfig, error) {
	c := OIDCConfig{
		IssuerURL: string(data["issuerURL"]), ClientID: string(data["clientID"]),
		ClientSecret: string(data["clientSecret"]), RedirectURL: string(data["redirectURL"]),
		UsernameClaim: string(data["usernameClaim"]), GroupsClaim: string(data["groupsClaim"]),
		UsernamePrefix: string(data["usernamePrefix"]), GroupsPrefix: string(data["groupsPrefix"]),
		CA: data["ca.crt"],
	}
	if c.IssuerURL == "" || c.ClientID == "" {
		return c, errors.New("issuerURL and clientID are required")
	}
	if !strings.HasPrefix(c.IssuerURL, "https://") {
		return c, errors.New("issuerURL must be https")
	}
	if c.UsernameClaim == "" {
		c.UsernameClaim = "preferred_username"
	}
	if c.GroupsClaim == "" {
		c.GroupsClaim = "groups"
	}
	return c, nil
}

// Person is who a session or a ticket stands for.
type Person struct {
	User   string   `json:"u"`
	Groups []string `json:"g,omitempty"`
}

// AccessFunc tells whether a person may open a workspace.
type AccessFunc func(ctx context.Context, p Person, workspace string) (bool, error)

// Auth signs people in with OIDC (authorization code with PKCE), keeps
// their session in a signed cookie and issues the short-lived tickets a
// desktop connection is opened with.
type Auth struct {
	Config OIDCConfig
	// Access decides who may open which workspace.
	Access AccessFunc
	// SessionTTL is how long a sign-in lasts; TicketTTL how long a ticket
	// opens a connection (the connection itself stays).
	SessionTTL time.Duration
	TicketTTL  time.Duration

	key []byte

	mu       sync.Mutex
	provider *oidc.Provider
	client   *http.Client
}

const (
	sessionCookie = "kuberoot_ws_session"
	loginCookie   = "kuberoot_ws_login"
)

// NewAuth makes an Auth with a fresh signing key: sessions and tickets do
// not outlive the process.
func NewAuth(cfg OIDCConfig, access AccessFunc) (*Auth, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	a := &Auth{Config: cfg, Access: access, SessionTTL: 8 * time.Hour, TicketTTL: 5 * time.Minute, key: key}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if len(cfg.CA) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(cfg.CA) {
			return nil, errors.New("ca.crt: no certificates")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	a.client = &http.Client{Transport: tr, Timeout: 15 * time.Second}
	return a, nil
}

// oauth is the provider's endpoints, discovered once it answers.
func (a *Auth) oauth(ctx context.Context, r *http.Request) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx = oidc.ClientContext(ctx, a.client)
	if a.provider == nil {
		p, err := oidc.NewProvider(ctx, a.Config.IssuerURL)
		if err != nil {
			return nil, nil, fmt.Errorf("identity provider: %w", err)
		}
		a.provider = p
	}
	redirect := a.Config.RedirectURL
	if redirect == "" {
		redirect = "https://" + r.Host + "/callback"
	}
	cfg := &oauth2.Config{ClientID: a.Config.ClientID, ClientSecret: a.Config.ClientSecret,
		Endpoint: a.provider.Endpoint(), RedirectURL: redirect, Scopes: []string{oidc.ScopeOpenID, "profile", "email", "groups"}}
	return cfg, a.provider.Verifier(&oidc.Config{ClientID: a.Config.ClientID}), nil
}

// seal and open sign a value into a compact string and check it back: the
// kind keeps a ticket from passing for a session and the other way round.
func (a *Auth) seal(kind string, ttl time.Duration, v any) (string, error) {
	body, err := json.Marshal(struct {
		Kind string `json:"k"`
		Exp  int64  `json:"e"`
		V    any    `json:"v"`
	}{kind, time.Now().Add(ttl).Unix(), v})
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(body)
	return p + "." + a.mac(p), nil
}

func (a *Auth) open(kind, token string, v any) error {
	p, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(a.mac(p))) {
		return errors.New("bad signature")
	}
	body, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return err
	}
	var env struct {
		Kind string          `json:"k"`
		Exp  int64           `json:"e"`
		V    json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return err
	}
	if env.Kind != kind {
		return errors.New("wrong kind")
	}
	if time.Now().Unix() > env.Exp {
		return errors.New("expired")
	}
	return json.Unmarshal(env.V, v)
}

func (a *Auth) mac(p string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(p))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Ticket opens a connection to one workspace's desktop for a short while.
type Ticket struct {
	Workspace string `json:"w"`
	Person
}

func (a *Auth) Ticket(workspace string, p Person) (string, error) {
	return a.seal("ticket", a.TicketTTL, Ticket{Workspace: workspace, Person: p})
}

// CheckTicket returns who a ticket was issued to, if it is for workspace
// and still valid.
func (a *Auth) CheckTicket(workspace, token string) (Person, error) {
	var t Ticket
	if err := a.open("ticket", token, &t); err != nil {
		return Person{}, err
	}
	if t.Workspace != workspace {
		return Person{}, errors.New("ticket for another workspace")
	}
	return t.Person, nil
}

// Session is who the browser signed in as, if anyone.
func (a *Auth) Session(r *http.Request) (Person, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Person{}, false
	}
	var p Person
	if err := a.open("session", c.Value, &p); err != nil || p.User == "" {
		return Person{}, false
	}
	return p, true
}

type login struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Nonce    string `json:"n"`
	Next     string `json:"x"`
}

// Login sends the browser to the identity provider.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := a.oauth(r.Context(), r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	l := login{State: random(), Verifier: oauth2.GenerateVerifier(), Nonce: random(), Next: localPath(r.URL.Query().Get("next"))}
	v, err := a.seal("login", 10*time.Minute, l)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: v, Path: "/callback", MaxAge: 600,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, cfg.AuthCodeURL(l.State, oauth2.S256ChallengeOption(l.Verifier), oidc.Nonce(l.Nonce)), http.StatusFound)
}

// Callback takes the provider's answer, checks the ID token and starts a
// session.
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(loginCookie)
	var l login
	if err != nil || a.open("login", c.Value, &l) != nil || r.URL.Query().Get("state") != l.State {
		http.Error(w, "sign-in expired or not started here; try again", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Path: "/callback", MaxAge: -1, Secure: true, HttpOnly: true})
	if e := r.URL.Query().Get("error"); e != "" {
		http.Error(w, "sign-in refused: "+e, http.StatusForbidden)
		return
	}
	cfg, verifier, err := a.oauth(r.Context(), r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	ctx := oidc.ClientContext(r.Context(), a.client)
	tok, err := cfg.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(l.Verifier))
	if err != nil {
		http.Error(w, "sign-in failed", http.StatusForbidden)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	id, err := verifier.Verify(ctx, raw)
	if err != nil || id.Nonce != l.Nonce {
		http.Error(w, "sign-in failed: the ID token does not check", http.StatusForbidden)
		return
	}
	var claims map[string]any
	if err := id.Claims(&claims); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	p, err := a.person(claims)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	v, err := a.seal("session", a.SessionTTL, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: v, Path: "/", MaxAge: int(a.SessionTTL.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, l.Next, http.StatusFound)
}

// Logout ends the browser's session.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, Secure: true, HttpOnly: true})
	http.Redirect(w, r, "/", http.StatusFound)
}

// person is who the claims name, with the configured prefixes.
func (a *Auth) person(claims map[string]any) (Person, error) {
	user, _ := claims[a.Config.UsernameClaim].(string)
	if user == "" {
		return Person{}, fmt.Errorf("the ID token has no %q claim", a.Config.UsernameClaim)
	}
	p := Person{User: a.Config.UsernamePrefix + user}
	switch g := claims[a.Config.GroupsClaim].(type) {
	case string:
		p.Groups = []string{a.Config.GroupsPrefix + g}
	case []any:
		for _, x := range g {
			if s, ok := x.(string); ok {
				p.Groups = append(p.Groups, a.Config.GroupsPrefix+s)
			}
		}
	}
	return p, nil
}

// localPath keeps redirects after sign-in on the gateway.
func localPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}

func random() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
