package wsctl

import (
	"context"
	"crypto/subtle"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/klog/v2"
)

// Route is where a workspace's desktop is, and who may open it.
type Route struct {
	Address string
	Token   string
	RDPPort int32
	// User is the person the workspace is for, as the identity provider
	// names them (spec.user, or spec.owner when unset).
	User string
	// Owner is the account in the desktop.
	Owner string
}

// Gateway opens desktops: in a browser, noVNC over a WebSocket to the
// machine's VNC server; for RDP clients, a port of its own for each
// workspace, passed through to the machine's xrdp. Without single sign-on a
// browser needs the workspace's token; with it, a person signs in at the
// identity provider and opens the workspaces they may, each connection
// with a short-lived ticket. The desktop then asks for the owner's
// password.
type Gateway struct {
	// NoVNC is the directory noVNC is served from.
	NoVNC string
	// DesktopVNCPort is the desktops' VNC port; VNCPort when 0.
	DesktopVNCPort int
	// Advertise is the address an RDP client reaches the gateway at.
	Advertise string

	mu        sync.Mutex
	routes    map[string]Route
	listeners map[int32]net.Listener
	auth      *Auth
}

// SetAuth turns single sign-on on, or off given nil.
func (g *Gateway) SetAuth(a *Auth) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.auth = a
}

func (g *Gateway) currentAuth() *Auth {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.auth
}

// SetRoutes replaces the desktops' routes, opening and closing RDP ports.
func (g *Gateway) SetRoutes(routes map[string]Route) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes = routes
	if g.listeners == nil {
		g.listeners = map[int32]net.Listener{}
	}
	want := map[int32]string{}
	for name, r := range routes {
		if r.RDPPort != 0 {
			want[r.RDPPort] = name
		}
	}
	for port, l := range g.listeners {
		if _, ok := want[port]; !ok {
			_ = l.Close()
			delete(g.listeners, port)
		}
	}
	for port, name := range want {
		if _, ok := g.listeners[port]; ok {
			continue
		}
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			klog.Errorf("RDP port %d of %s: %v", port, name, err)
			continue
		}
		g.listeners[port] = l
		go g.serveRDP(l, name)
	}
}

// rdpDesktopPort is where the gateway reaches the desktops' xrdp.
var rdpDesktopPort = RDPPort

func (g *Gateway) route(name string) (Route, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.routes[name]
	return r, ok
}

// serveRDP passes RDP connections on to the desktop wherever it runs now.
func (g *Gateway) serveRDP(l net.Listener, name string) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			r, ok := g.route(name)
			if !ok || r.Address == "" {
				return
			}
			var first []byte
			if a := g.currentAuth(); a != nil {
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				pkt, err := readConnectionRequest(conn)
				if err != nil {
					return
				}
				ticket, rest, err := takeTicket(pkt)
				if err == nil {
					_, err = a.CheckTicket(name, ticket)
				}
				if err != nil {
					klog.V(1).Infof("RDP to %s from %s refused: %v", name, conn.RemoteAddr(), err)
					return
				}
				_ = conn.SetReadDeadline(time.Time{})
				first = rest
			}
			upstream, err := net.DialTimeout("tcp", net.JoinHostPort(r.Address, fmt.Sprint(rdpDesktopPort)), 5*time.Second)
			if err != nil {
				return
			}
			defer upstream.Close()
			if _, err := upstream.Write(first); err != nil {
				return
			}
			pipe(conn, upstream)
		}()
	}
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}

// tokenUpgrader admits a browser by the token or ticket in the URL, from
// any origin; sessionUpgrader, admitting it by its cookie, only from the
// gateway's own pages.
var (
	tokenUpgrader = websocket.Upgrader{
		Subprotocols:    []string{"binary"},
		ReadBufferSize:  64 << 10,
		WriteBufferSize: 64 << 10,
		CheckOrigin:     func(*http.Request) bool { return true },
	}
	sessionUpgrader = websocket.Upgrader{
		Subprotocols:    []string{"binary"},
		ReadBufferSize:  64 << 10,
		WriteBufferSize: 64 << 10,
	}
)

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a := g.currentAuth()
	switch {
	case r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(r.URL.Path, "/novnc/"):
		http.StripPrefix("/novnc/", http.FileServer(http.Dir(g.NoVNC))).ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, "/ws/"):
		if a == nil {
			g.workspace(w, r)
		} else {
			g.signedIn(w, r, a)
		}
	case a != nil && r.URL.Path == "/login":
		a.Login(w, r)
	case a != nil && r.URL.Path == "/callback":
		a.Callback(w, r)
	case a != nil && r.URL.Path == "/logout":
		a.Logout(w, r)
	case a != nil && r.URL.Path == "/":
		g.index(w, r, a)
	default:
		http.NotFound(w, r)
	}
}

// workspace handles /ws/<name>/, which opens noVNC on the desktop, and
// /ws/<name>/vnc, the WebSocket noVNC connects to, for a browser that
// brings the workspace's token.
func (g *Gateway) workspace(w http.ResponseWriter, r *http.Request) {
	name, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/ws/"), "/")
	route, ok := g.route(name)
	token := r.URL.Query().Get("token")
	if !ok || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(route.Token)) != 1 {
		http.Error(w, "no such workspace, or not your token", http.StatusForbidden)
		return
	}
	switch rest {
	case "":
		redirectNoVNC(w, r, name, "token", token)
	case "vnc":
		g.vnc(w, r, route.Address, &tokenUpgrader)
	default:
		http.NotFound(w, r)
	}
}

func redirectNoVNC(w http.ResponseWriter, r *http.Request, name, param, value string) {
	path := fmt.Sprintf("ws/%s/vnc?%s=%s", name, param, url.QueryEscape(value))
	q := url.Values{"autoconnect": {"true"}, "reconnect": {"true"}, "resize": {"remote"}, "path": {path}}
	http.Redirect(w, r, "/novnc/vnc.html?"+q.Encode(), http.StatusFound)
}

// mayOpen tells whether a person may open a workspace: the one it is for
// may, and whoever the cluster lets get workspaces/desktop.
func (g *Gateway) mayOpen(r *http.Request, a *Auth, p Person, name string, route Route) bool {
	if route.User != "" && p.User == a.Config.UsernamePrefix+route.User {
		return true
	}
	if a.Access == nil {
		return false
	}
	ok, err := a.Access(r.Context(), p, name)
	if err != nil {
		klog.Errorf("access of %s to %s: %v", p.User, name, err)
	}
	return ok
}

// signedIn handles /ws/<name>/ (noVNC on the desktop), /ws/<name>/vnc
// (its WebSocket, admitted by a ticket or the session) and /ws/<name>/rdp
// (an .rdp file with a ticket) for people signed in with OIDC.
func (g *Gateway) signedIn(w http.ResponseWriter, r *http.Request, a *Auth) {
	name, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/ws/"), "/")
	route, known := g.route(name)
	if rest == "vnc" {
		if t := r.URL.Query().Get("ticket"); t != "" {
			if _, err := a.CheckTicket(name, t); err != nil || !known {
				http.Error(w, "ticket refused", http.StatusForbidden)
				return
			}
			g.vnc(w, r, route.Address, &tokenUpgrader)
			return
		}
	}
	p, ok := a.Session(r)
	if !ok {
		if rest == "vnc" {
			http.Error(w, "sign in first", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
		return
	}
	if !known || !g.mayOpen(r, a, p, name, route) {
		http.Error(w, "no such workspace, or not yours", http.StatusForbidden)
		return
	}
	switch rest {
	case "":
		t, err := a.Ticket(name, p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		redirectNoVNC(w, r, name, "ticket", t)
	case "vnc":
		g.vnc(w, r, route.Address, &sessionUpgrader)
	case "rdp":
		t, err := a.Ticket(name, p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-rdp")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".rdp"))
		fmt.Fprintf(w, "full address:s:%s\r\nusername:s:%s\r\nloadbalanceinfo:s:Cookie: msts=%s\r\nprompt for credentials:i:1\r\n",
			net.JoinHostPort(g.advertise(r), fmt.Sprint(route.RDPPort)), route.Owner, t)
	default:
		http.NotFound(w, r)
	}
}

// advertise is the host RDP clients reach the gateway at: the one the
// browser came to unless an address is set.
func (g *Gateway) advertise(r *http.Request) string {
	if g.Advertise != "" {
		return g.Advertise
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		return r.Host
	}
	return host
}

var indexPage = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Workspaces</title>
<style>
body{font-family:system-ui,sans-serif;margin:2rem auto;max-width:42rem;padding:0 1rem;color:#1d1d1f;background:#fff}
li{margin:.6rem 0}a{color:#0b57d0}.who{color:#555}
@media (prefers-color-scheme:dark){body{background:#151515;color:#eee}a{color:#8ab4f8}.who{color:#aaa}}
</style></head><body>
<h1>Workspaces</h1>
<p class="who">Signed in as {{.User}} · <a href="/logout">sign out</a></p>
{{if .Spaces}}<ul>{{range .Spaces}}<li><b>{{.}}</b> — <a href="/ws/{{.}}/">open in the browser</a> · <a href="/ws/{{.}}/rdp">RDP file</a></li>{{end}}</ul>
{{else}}<p>No workspaces for you.</p>{{end}}
</body></html>
`))

// index lists the workspaces the person may open.
func (g *Gateway) index(w http.ResponseWriter, r *http.Request, a *Auth) {
	p, ok := a.Session(r)
	if !ok {
		http.Redirect(w, r, "/login?next=/", http.StatusFound)
		return
	}
	g.mu.Lock()
	routes := make(map[string]Route, len(g.routes))
	for k, v := range g.routes {
		routes[k] = v
	}
	g.mu.Unlock()
	var spaces []string
	for name, route := range routes {
		if g.mayOpen(r, a, p, name, route) {
			spaces = append(spaces, name)
		}
	}
	sort.Strings(spaces)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexPage.Execute(w, struct {
		User   string
		Spaces []string
	}{p.User, spaces})
}

// vnc carries a browser's WebSocket to the desktop's VNC server.
func (g *Gateway) vnc(w http.ResponseWriter, r *http.Request, address string, upgrader *websocket.Upgrader) {
	if address == "" {
		http.Error(w, "the desktop has no address yet", http.StatusServiceUnavailable)
		return
	}
	port := g.DesktopVNCPort
	if port == 0 {
		port = VNCPort
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(address, fmt.Sprint(port)), 5*time.Second)
	if err != nil {
		http.Error(w, "the desktop does not answer yet", http.StatusServiceUnavailable)
		return
	}
	defer upstream.Close()
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		defer cancel()
		buf := make([]byte, 64<<10)
		for {
			n, err := upstream.Read(buf)
			if err != nil {
				return
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return
			}
		}
	}()
	go func() {
		defer cancel()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if _, err := upstream.Write(data); err != nil {
				return
			}
		}
	}()
	<-ctx.Done()
}
