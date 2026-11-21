package nodeapi

import (
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/rest"
	"net/http"
	"testing"
	"time"
)

func TestInternalClientTiming(t *testing.T) {
	for _, factor := range []string{"", "1", "0", "bad", "1001"} {
		t.Setenv("KUBEROOT_TIMESCALE", factor)
		cfg := &rest.Config{}
		scaleInternalClient(cfg)
		if cfg.WrapTransport != nil || cfg.Dial != nil || cfg.Proxy != nil {
			t.Fatalf("%q changed defaults", factor)
		}
	}
	// Exercise the real client-go factory, including its private GC wrapper.
	base := &rest.Config{Host: "https://verified.example", Timeout: 7 * time.Second,
		TLSClientConfig: rest.TLSClientConfig{ServerName: "verified.example"}}
	original, err := rest.TransportFor(base)
	if err != nil {
		t.Fatal(err)
	}
	unwrap := func(rt http.RoundTripper) *http.Transport {
		for {
			if tr, ok := rt.(*http.Transport); ok {
				return tr
			}
			wrapper, ok := rt.(utilnet.RoundTripperWrapper)
			if !ok {
				t.Fatalf("unexpected transport %T", rt)
			}
			rt = wrapper.WrappedRoundTripper()
		}
	}
	t.Setenv("KUBEROOT_TIMESCALE", "60")
	cfg := rest.CopyConfig(base)
	scaleInternalClient(cfg)
	scaled, err := rest.TransportFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, after := unwrap(original), unwrap(scaled)
	if before == after || before.TLSHandshakeTimeout != 10*time.Second || after.TLSHandshakeTimeout != 10*time.Minute {
		t.Fatal("transport scaling lost isolation")
	}
	if after.TLSClientConfig.ServerName != "verified.example" || after.TLSClientConfig.InsecureSkipVerify || cfg.Timeout != 7*time.Second {
		t.Fatal("TLS verification or explicit request budget changed")
	}
}
