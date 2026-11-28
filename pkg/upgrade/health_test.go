package upgrade

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/bootdisk"
)

func TestHTTPHealthChecks(t *testing.T) {
	body := "application-v1"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	var redirected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, body)
		case "/wrong":
			fmt.Fprint(w, strings.Repeat("X", 1024))
		case "/error":
			w.WriteHeader(503)
		case "/redirect":
			http.Redirect(w, r, "/target", 302)
		case "/target":
			redirected.Store(true)
			fmt.Fprint(w, body)
		case "/large":
			fmt.Fprint(w, strings.Repeat("x", maxBody+1))
		}
	}))
	defer server.Close()
	for _, tt := range []struct {
		name, path, digest string
		code               int32
		wantOK             bool
	}{
		{"matching content", "/ok", sum, 0, true},
		{"wrong content despite HTTP 200", "/wrong", sum, 0, false},
		{"status only", "/ok", "", 0, true},
		{"explicit status", "/error", "", 503, true},
		{"unavailable app", "/error", "", 0, false},
		{"redirect is not followed", "/redirect", sum, 0, false},
		{"bounded response", "/large", sum, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckHealth(context.Background(), []HealthCheck{{URL: server.URL + tt.path, BodySHA256: tt.digest, StatusCode: tt.code}}, nil)
			if (err == nil) != tt.wantOK {
				t.Fatalf("error=%v, want success=%v", err, tt.wantOK)
			}
		})
	}
	if redirected.Load() {
		t.Fatal("health check followed a redirect")
	}
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer tlsServer.Close()
	if err := CheckHealth(context.Background(), []HealthCheck{{URL: tlsServer.URL}}, nil); err == nil {
		t.Fatal("untrusted TLS certificate was accepted")
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if err := CheckHealth(context.Background(), []HealthCheck{{URL: closed.URL}}, nil); err == nil {
		t.Fatal("unreachable application passed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckHealth(ctx, []HealthCheck{{URL: server.URL + "/ok"}}, nil); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestHealthCheckBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	started := time.Now()
	err := CheckHealth(context.Background(), []HealthCheck{{URL: server.URL}}, nil)
	if err == nil || time.Since(started) > healthBudget+2*time.Second {
		t.Fatalf("budget not enforced: elapsed=%s error=%v", time.Since(started), err)
	}
}

func TestPodsHealthChecks(t *testing.T) {
	counts := map[string][2]int{"app=web": {2, 2}, "app=db": {0, 1}, "app=gone": {0, 0}, "app=two": {1, 1}}
	var asked []string
	pods := func(_ context.Context, ns, sel string) (int, int, error) {
		asked = append(asked, ns+"/"+sel)
		if sel == "app=err" {
			return 0, 0, errors.New("forbidden")
		}
		c := counts[sel]
		return c[0], c[1], nil
	}
	for _, tt := range []struct {
		sel    string
		min    int32
		wantOK bool
	}{
		{"app=web", 0, true},
		{"app=web", 2, true},
		{"app=web", 3, false},
		{"app=db", 0, false},   // a selected pod is not Ready
		{"app=gone", 0, false}, // nothing to run is not healthy
		{"app=two", 2, false},
		{"app=err", 0, false},
	} {
		err := CheckHealth(context.Background(), []HealthCheck{{Pods: &PodsCheck{Namespace: "shop", Selector: tt.sel, MinReady: tt.min}}}, pods)
		if (err == nil) != tt.wantOK {
			t.Fatalf("%s min %d: error=%v, want success=%v", tt.sel, tt.min, err, tt.wantOK)
		}
	}
	if asked[0] != "shop/app=web" {
		t.Fatalf("asked %v", asked)
	}
	if err := CheckHealth(context.Background(), []HealthCheck{{Pods: &PodsCheck{Namespace: "shop", Selector: "app=web"}}}, nil); err == nil {
		t.Fatal("pods passed without a cluster client")
	}
}

func TestHealthPolicyValidation(t *testing.T) {
	pods := &PodsCheck{Namespace: "shop", Selector: "app=web"}
	for _, checks := range [][]HealthCheck{
		{{URL: "file:///etc/passwd"}}, {{URL: "http://user:secret@localhost"}}, {{URL: "http://localhost/#fragment"}},
		{{URL: "http://localhost/", BodySHA256: "abcd"}}, {{URL: "http://localhost/", StatusCode: 199}},
		{{}}, {{URL: "http://localhost/", Pods: pods}}, {{Pods: pods, StatusCode: 200}},
		{{Pods: &PodsCheck{Namespace: "Shop", Selector: "app=web"}}},
		{{Pods: &PodsCheck{Namespace: "shop", Selector: ""}}},
		{{Pods: &PodsCheck{Namespace: "shop", Selector: "app in ("}}},
		{{Pods: &PodsCheck{Namespace: "shop", Selector: "app=web", MinReady: -1}}},
		make([]HealthCheck, 9),
	} {
		if err := ValidateHealthChecks(checks); err == nil {
			t.Fatalf("accepted invalid checks: %+v", checks)
		}
	}
	if err := ValidateHealthChecks([]HealthCheck{{URL: "https://shop.example/healthz", StatusCode: 204}, {Pods: pods}}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		healthPrefix + `[{"url":"http://localhost","unknown":true}]`,
		healthPrefix + `[{"url":"http://localhost"}] trailing`,
		healthPrefix + `[{"url":"http://localhost"}]` + "\n" + healthPrefix + `[]`,
		strings.TrimSpace(healthPrefix), healthPrefix + `{`,
		healthPrefix + `[{"pods":{"namespace":"shop"}}]`,
	} {
		if _, err := healthPolicy(raw); err == nil {
			t.Fatalf("accepted malformed policy: %q", raw)
		}
	}
	if checks, err := healthPolicy("title kuberoot legacy\noptions root=legacy\n"); err != nil || len(checks) != 0 {
		t.Fatalf("legacy entry: %v %v", checks, err)
	}
}

func TestHealthPolicyFollowsSlotEntry(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "old", "1")
	checks := []HealthCheck{
		{URL: "http://127.0.0.1:30080/", BodySHA256: strings.Repeat("ab", 32)},
		{Pods: &PodsCheck{Namespace: "shop", Selector: "app=web", MinReady: 2}},
	}
	entry := bootdisk.Entry{ID: "kuberoot-b", Title: "kuberoot new (slot b)"}
	if err := addProbationEntry(dir, entry, "a", checks); err != nil {
		t.Fatal(err)
	}
	read := func(current string) []Entry {
		t.Helper()
		entries, err := readEntriesIn(dir, current)
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
	entries := read("b")
	if len(entries) != 2 || entries[0].Slot != "b" || entries[0].HealthCheckError != nil || !reflect.DeepEqual(entries[0].HealthChecks, checks) {
		t.Fatalf("persisted policy: %+v", entries)
	}
	if len(entries[1].HealthChecks) != 0 {
		t.Fatal("new policy leaked into fallback slot")
	}
	// systemd-boot renames the entry when it spends a boot attempt.
	if err := os.Rename(filepath.Join(dir, "kuberoot-b+3.conf"), filepath.Join(dir, "kuberoot-b+2-1.conf")); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read("b")[0].HealthChecks, checks) {
		t.Fatal("policy lost after boot-counter rename")
	}
	if _, err := failAttemptIn(dir, "b", "workloads unhealthy"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read("b")[0].HealthChecks, checks) {
		t.Fatal("policy lost when an attempt failed")
	}
	if err := os.Rename(filepath.Join(dir, "kuberoot-b+2-1.conf"), filepath.Join(dir, "kuberoot-b+1-2.conf")); err != nil {
		t.Fatal(err)
	}
	if _, err := markGoodIn(dir, "b"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read("b")[0].HealthChecks, checks) {
		t.Fatal("policy lost on MarkGood")
	}
	if err := preferIn(dir, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := preferIn(dir, "b", "a"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read("a")[0].HealthChecks, checks) {
		t.Fatal("policy lost on manual selection")
	}
	if err := retireSlot(dir, "b"); err != nil {
		t.Fatal(err)
	}
	if err := addProbationEntry(dir, entry, "a", nil); err != nil {
		t.Fatal(err)
	}
	if len(read("a")[0].HealthChecks) != 0 {
		t.Fatal("stale policy inherited by a later release")
	}
	if err := addProbationEntry(dir, entry, "a", []HealthCheck{{URL: "ftp://x"}}); err == nil {
		t.Fatal("invalid policy staged")
	}
}

func TestCorruptPolicyIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "old", "1")
	writeEntry(t, dir, "kuberoot-b+2-1.conf", "new", "2")
	f, err := os.OpenFile(filepath.Join(dir, "kuberoot-b+2-1.conf"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, healthPrefix+`[{"url":`)
	f.Close()
	entries, err := readEntriesIn(dir, "b")
	if err != nil || entries[0].HealthCheckError == nil {
		t.Fatalf("corrupt policy passed: %+v %v", entries, err)
	}
}
