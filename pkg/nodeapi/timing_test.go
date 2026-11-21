package nodeapi

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

func TestInternalClientTiming(t *testing.T) {
	for _, factor := range []string{"", "1", "0", "bad", "1001"} {
		t.Setenv("KUBEROOT_TIMESCALE", factor)
		cfg := &rest.Config{}
		scaleInternalClient(cfg)
		if cfg.WrapTransport != nil || cfg.Dial != nil {
			t.Fatalf("%q changed defaults", factor)
		}
	}
	t.Setenv("KUBEROOT_TIMESCALE", "60")
	cfg := &rest.Config{}
	scaleInternalClient(cfg)
	original := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, TLSClientConfig: &tls.Config{ServerName: "verified.example"}}
	scaled := cfg.WrapTransport(original).(*http.Transport)
	if scaled == original || original.TLSHandshakeTimeout != 10*time.Second || scaled.TLSHandshakeTimeout != 10*time.Minute || scaled.TLSClientConfig.ServerName != "verified.example" || cfg.Dial == nil {
		t.Fatal("transport scaling lost isolation or TLS verification")
	}
}
