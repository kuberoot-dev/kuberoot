package upgrade

import (
	"context"
	"crypto/sha256"
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

func TestApplicationHealthChecks(t *testing.T) {
	body := "application-v1"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	var redirected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, body)
		case "/wrong":
			fmt.Fprint(w, "application-v2")
		case "/error":
			w.WriteHeader(503)
		case "/redirect":
			http.Redirect(w, r, "/target", 302)
		case "/target":
			redirected.Store(true)
			fmt.Fprint(w, body)
		case "/large":
			fmt.Fprint(w, strings.Repeat("x", (1<<20)+1))
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
			err := CheckHealth(context.Background(), []HealthCheck{{URL: server.URL + tt.path, BodySHA256: tt.digest, StatusCode: tt.code}})
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
	if err := CheckHealth(context.Background(), []HealthCheck{{URL: tlsServer.URL}}); err == nil {
		t.Fatal("untrusted TLS certificate was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckHealth(ctx, []HealthCheck{{URL: server.URL}}); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestHealthPolicyValidation(t *testing.T) {
	for _, checks := range [][]HealthCheck{
		{{URL: "file:///etc/passwd"}}, {{URL: "http://user:secret@localhost"}}, {{URL: "http://localhost/#fragment"}},
		{{URL: "http://localhost/", BodySHA256: "abcd"}}, {{URL: "http://localhost/", StatusCode: 199}},
		make([]HealthCheck, 9),
	} {
		if err := ValidateHealthChecks(checks); err == nil {
			t.Fatalf("accepted invalid checks: %+v", checks)
		}
	}
	for _, raw := range []string{
		healthPrefix + `[{"url":"http://localhost","unknown":true}]`,
		healthPrefix + `[{"url":"http://localhost"}] trailing`,
		healthPrefix + `[{"url":"http://localhost"}]` + "\n" + healthPrefix + `[]`,
		strings.TrimSpace(healthPrefix), healthPrefix + `{`,
	} {
		if _, err := healthPolicy(raw); err == nil {
			t.Fatalf("accepted malformed policy: %q", raw)
		}
	}
	if checks, err := healthPolicy("title kuberoot legacy\noptions root=legacy\n"); err != nil || len(checks) != 0 {
		t.Fatalf("legacy entry: %v %v", checks, err)
	}
}

func TestApplicationPolicyFollowsSlotEntry(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "kuberoot-a.conf", "old", "1")
	checks := []HealthCheck{{URL: "http://127.0.0.1:30080/", BodySHA256: strings.Repeat("ab", 32)}}
	entry := bootdisk.Entry{ID: "kuberoot-b"}
	if err := addProbationEntryWithHealthChecks(dir, entry, "a", checks); err != nil {
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
	// The bootloader renames this entry when it spends a boot attempt.
	if err := os.Rename(filepath.Join(dir, "kuberoot-b+3.conf"), filepath.Join(dir, "kuberoot-b+2-1.conf")); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read("b")[0].HealthChecks, checks) {
		t.Fatal("policy lost after boot-counter rename")
	}
	if _, err := markGoodIn(dir, "b"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read("b")[0].HealthChecks, checks) {
		t.Fatal("policy lost on MarkGood")
	}
	if err := preferIn(dir, "b", "a"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read("b")[0].HealthChecks, checks) {
		t.Fatal("policy lost on manual selection")
	}
	if err := retireSlot(dir, "b"); err != nil {
		t.Fatal(err)
	}
	if err := addProbationEntry(dir, entry, "a"); err != nil {
		t.Fatal(err)
	}
	if len(read("b")[0].HealthChecks) != 0 {
		t.Fatal("stale policy inherited by a later release")
	}
}

func TestHealthCheckDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	started := time.Now()
	err := CheckHealth(context.Background(), []HealthCheck{{URL: server.URL}})
	if err == nil || time.Since(started) > 5*time.Second {
		t.Fatalf("request budget not enforced: elapsed=%s error=%v", time.Since(started), err)
	}
}
