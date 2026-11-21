package nodeapi

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"k8s.io/client-go/rest"
)

// The node's background controllers must tolerate the same slow local API
// as the shared control plane. Never mutate client-go's cached transports:
// other users of the same credentials may have different request budgets.
func scaleInternalClient(cfg *rest.Config) {
	n, err := strconv.Atoi(os.Getenv("KUBEROOT_TIMESCALE"))
	if err != nil || n <= 1 || n > 1000 {
		return
	}
	factor := time.Duration(n)
	if cfg.Dial == nil {
		cfg.Dial = (&net.Dialer{Timeout: 30 * time.Second * factor, KeepAlive: 30 * time.Second}).DialContext
	}
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		if tr, ok := rt.(*http.Transport); ok {
			clone := tr.Clone()
			clone.TLSHandshakeTimeout *= factor
			return clone
		}
		return rt
	})
}
