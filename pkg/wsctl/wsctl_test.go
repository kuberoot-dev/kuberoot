package wsctl

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"sigs.k8s.io/yaml"
)

func TestUserData(t *testing.T) {
	data, err := UserData("anna-desk", "anna", "Secret123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(data, "#cloud-config\n") {
		t.Fatalf("not cloud-config: %.40s", data)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(data), &doc); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, data)
	}
	for _, want := range []string{"hostname: anna-desk", "name: anna", "password: Secret123", "tigervncserver :1", "-BlacklistThreshold 1000000", "User=anna", "enable --now xrdp"} {
		if !strings.Contains(data, want) {
			t.Errorf("user data lacks %q", want)
		}
	}
}

func TestPasswordAndToken(t *testing.T) {
	p, err := Password()
	if err != nil || len(p) != 12 || strings.ContainsAny(p, " :#'\"0O1lI") {
		t.Fatalf("password %q %v", p, err)
	}
	tok, err := Token()
	if err != nil || len(tok) != 48 {
		t.Fatalf("token %q %v", tok, err)
	}
}

func TestPhase(t *testing.T) {
	for _, c := range []struct {
		machine string
		answers bool
		want    string
	}{{"Running", true, "Ready"}, {"Running", false, "Provisioning"}, {"", false, "Pending"}, {"Stopped", false, "Stopped"}, {"Moving", false, "Moving"}} {
		if got := Phase(c.machine, c.answers); got != c.want {
			t.Errorf("Phase(%q, %v) = %q, want %q", c.machine, c.answers, got, c.want)
		}
	}
}

// fakeVNC answers each connection with a greeting and echoes what it gets.
func fakeVNC(t *testing.T) net.Listener {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = c.Write([]byte("RFB 003.008\n"))
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return l
}

func TestGatewayCarriesTheBrowserToTheDesktop(t *testing.T) {
	vnc := fakeVNC(t)
	defer vnc.Close()
	host, port, _ := net.SplitHostPort(vnc.Addr().String())
	p, _ := strconv.Atoi(port)
	g := &Gateway{DesktopVNCPort: p}
	g.SetRoutes(map[string]Route{"desk": {Address: host, Token: "t0k"}})
	srv := httptest.NewServer(g)
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/desk/vnc?token=t0k", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_, msg, err := ws.ReadMessage()
	if err != nil || !strings.HasPrefix(string(msg), "RFB") {
		t.Fatalf("greeting %q %v", msg, err)
	}
	// What the browser sends reaches the desktop, and its answer comes back.
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err = ws.ReadMessage(); err != nil || string(msg) != "hello" {
		t.Fatalf("echo %q %v", msg, err)
	}
}

func TestGatewayRefuses(t *testing.T) {
	g := &Gateway{NoVNC: t.TempDir()}
	g.SetRoutes(map[string]Route{"desk": {Address: "10.0.0.9", Token: "t0k"}})
	srv := httptest.NewServer(g)
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for path, want := range map[string]int{
		"/ws/desk/?token=t0k":   http.StatusFound,
		"/ws/desk/?token=wrong": http.StatusForbidden,
		"/ws/desk/":             http.StatusForbidden,
		"/ws/other/?token=t0k":  http.StatusForbidden,
		"/elsewhere":            http.StatusNotFound,
	} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
	resp, _ := client.Get(srv.URL + "/ws/desk/?token=t0k")
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "/novnc/vnc.html?") || !strings.Contains(loc, "path=ws%2Fdesk%2Fvnc%3Ftoken%3Dt0k") {
		t.Errorf("redirect to %s", loc)
	}
}
