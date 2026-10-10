package wsctl

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
}

// Gateway opens desktops: in a browser, noVNC over a WebSocket to the
// machine's VNC server; for RDP clients, a port of its own for each
// workspace, passed through to the machine's xrdp. A browser needs the
// workspace's token, then the desktop asks for the owner's password.
type Gateway struct {
	// NoVNC is the directory noVNC is served from.
	NoVNC string
	// DesktopVNCPort is the desktops' VNC port; VNCPort when 0.
	DesktopVNCPort int

	mu        sync.Mutex
	routes    map[string]Route
	listeners map[int32]net.Listener
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
			upstream, err := net.DialTimeout("tcp", net.JoinHostPort(r.Address, fmt.Sprint(RDPPort)), 5*time.Second)
			if err != nil {
				return
			}
			defer upstream.Close()
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

var upgrader = websocket.Upgrader{
	Subprotocols:    []string{"binary"},
	ReadBufferSize:  64 << 10,
	WriteBufferSize: 64 << 10,
	// The token in the URL is what admits a browser, from any origin.
	CheckOrigin: func(*http.Request) bool { return true },
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(r.URL.Path, "/novnc/"):
		http.StripPrefix("/novnc/", http.FileServer(http.Dir(g.NoVNC))).ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, "/ws/"):
		g.workspace(w, r)
	default:
		http.NotFound(w, r)
	}
}

// workspace handles /ws/<name>/, which opens noVNC on the desktop, and
// /ws/<name>/vnc, the WebSocket noVNC connects to.
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
		path := fmt.Sprintf("ws/%s/vnc?token=%s", name, url.QueryEscape(token))
		q := url.Values{"autoconnect": {"true"}, "reconnect": {"true"}, "resize": {"remote"}, "path": {path}}
		http.Redirect(w, r, "/novnc/vnc.html?"+q.Encode(), http.StatusFound)
	case "vnc":
		if route.Address == "" {
			http.Error(w, "the desktop has no address yet", http.StatusServiceUnavailable)
			return
		}
		g.vnc(w, r, route.Address)
	default:
		http.NotFound(w, r)
	}
}

// vnc carries a browser's WebSocket to the desktop's VNC server.
func (g *Gateway) vnc(w http.ResponseWriter, r *http.Request, address string) {
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
