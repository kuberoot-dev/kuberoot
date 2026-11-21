package nodeapi

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/rest"
)

// The node's background controllers must tolerate the same slow local API
// as the shared control plane. A non-nil Proxy makes client-go construct an
// uncached transport. Keep its GC and certificate/CA rotation wrappers intact;
// CA rotation clones this private transport, preserving its timing budget.
func scaleInternalClient(cfg *rest.Config) {
	n, err := strconv.Atoi(os.Getenv("KUBEROOT_TIMESCALE"))
	if err != nil || n <= 1 || n > 1000 {
		return
	}
	factor := time.Duration(n)
	if cfg.Dial == nil {
		cfg.Dial = (&net.Dialer{Timeout: 30 * time.Second * factor, KeepAlive: 30 * time.Second}).DialContext
	}
	if cfg.Proxy == nil {
		cfg.Proxy = http.ProxyFromEnvironment
	}
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		for inner := rt; inner != nil; {
			if tr, ok := inner.(*http.Transport); ok {
				tr.TLSHandshakeTimeout *= factor
				break
			}
			wrapper, ok := inner.(utilnet.RoundTripperWrapper)
			if !ok {
				break
			}
			inner = wrapper.WrappedRoundTripper()
		}
		return rt
	})
}
