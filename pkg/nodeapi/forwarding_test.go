package nodeapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForwardingChecksRequireEveryObserver(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, good.URL, 302) }))
	defer redirect.Close()
	for _, tc := range []struct {
		name string
		urls []string
		ok   bool
	}{
		{"unset", nil, true}, {"healthy", []string{good.URL}, true}, {"dataplane broken", []string{good.URL, bad.URL}, false},
		{"redirect cannot mask failure", []string{redirect.URL}, false}, {"bad scheme", []string{"file:///etc/passwd"}, false}, {"userinfo", []string{"http://user:pass@localhost"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkForwarding(context.Background(), tc.urls) == nil; got != tc.ok {
				t.Fatalf("success=%v", got)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if checkForwarding(ctx, []string{good.URL}) == nil {
		t.Fatal("ignored canceled probation context")
	}
}
