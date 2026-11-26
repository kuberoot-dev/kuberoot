package nodeapi

import (
	"context"
	"encoding/json"
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	"net/http"
	"net/url"
	"time"
)

func routerForwardingReady(ctx context.Context) bool {
	client := clientFrom("/etc/kubernetes/admin.kubeconfig")
	if client == nil {
		return false
	}
	raw, err := client.CoreV1().RESTClient().Get().AbsPath("/apis/router.kuberoot.dev/v1alpha1/safeguards/default").DoRaw(ctx)
	if apierrors.IsNotFound(err) {
		return true
	}
	if err != nil {
		return false
	}
	var guard struct {
		Spec struct {
			ForwardingChecks []string `json:"forwardingChecks"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &guard) != nil {
		return false
	}
	if err := checkForwarding(ctx, guard.Spec.ForwardingChecks); err != nil {
		klog.Warningf("boot assessment: forwarding health check failed: %v", err)
		return false
	}
	return true
}

func checkForwarding(ctx context.Context, checks []string) error {
	if len(checks) > 8 {
		return fmt.Errorf("at most eight forwarding checks allowed")
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i, target := range checks {
		u, err := url.Parse(target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return fmt.Errorf("check %d has invalid HTTP(S) URL", i)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return fmt.Errorf("check %d cannot create request", i)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("check %d unavailable", i)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("check %d returned HTTP %d", i, resp.StatusCode)
		}
	}
	return nil
}
