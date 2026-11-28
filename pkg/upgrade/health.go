package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
)

// HealthCheck is one workload check required throughout probation, on top of
// the node's own readiness: an HTTP request from the node, or Ready pods.
type HealthCheck struct {
	URL string `json:"url,omitempty"`
	// StatusCode defaults to 200.
	StatusCode int32 `json:"statusCode,omitempty"`
	// BodySHA256, when set, checks the complete response body (at most 1 MiB).
	BodySHA256 string     `json:"bodySHA256,omitempty"`
	Pods       *PodsCheck `json:"pods,omitempty"`
}

// PodsCheck requires the selected pods on this node to be Ready: all of them,
// and at least MinReady (1 when unset).
type PodsCheck struct {
	Namespace string `json:"namespace"`
	Selector  string `json:"selector"`
	MinReady  int32  `json:"minReady,omitempty"`
}

// PodReadiness counts the pods on this node that match selector in namespace,
// and how many of them are Ready.
type PodReadiness func(ctx context.Context, namespace, selector string) (ready, total int, err error)

const (
	healthPrefix    = "# kuberoot-health-checks "
	maxHealthChecks = 8
	maxBody         = 1 << 20
	// healthBudget bounds one round of checks, so a stuck endpoint cannot
	// hold up crash detection between assessment ticks.
	healthBudget = 3 * time.Second
)

func ValidateHealthChecks(checks []HealthCheck) error {
	if len(checks) > maxHealthChecks {
		return fmt.Errorf("healthChecks: at most %d checks are allowed", maxHealthChecks)
	}
	for i, c := range checks {
		if (c.URL == "") == (c.Pods == nil) {
			return fmt.Errorf("healthChecks[%d]: set exactly one of url and pods", i)
		}
		if c.Pods != nil {
			if c.StatusCode != 0 || c.BodySHA256 != "" {
				return fmt.Errorf("healthChecks[%d]: statusCode and bodySHA256 apply to url only", i)
			}
			if errs := validation.IsDNS1123Label(c.Pods.Namespace); len(errs) > 0 {
				return fmt.Errorf("healthChecks[%d].pods.namespace: %s", i, strings.Join(errs, "; "))
			}
			sel, err := labels.Parse(c.Pods.Selector)
			if err != nil {
				return fmt.Errorf("healthChecks[%d].pods.selector: %v", i, err)
			}
			if sel.Empty() {
				return fmt.Errorf("healthChecks[%d].pods.selector must select some pods, not all", i)
			}
			if c.Pods.MinReady < 0 {
				return fmt.Errorf("healthChecks[%d].pods.minReady must not be negative", i)
			}
			continue
		}
		u, err := url.Parse(c.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || len(c.URL) > 2048 || strings.ContainsAny(c.URL, "\r\n") {
			return fmt.Errorf("healthChecks[%d].url must be an absolute HTTP(S) URL without credentials or fragment", i)
		}
		if c.StatusCode != 0 && (c.StatusCode < 200 || c.StatusCode > 599) {
			return fmt.Errorf("healthChecks[%d].statusCode must be 200..599", i)
		}
		if c.BodySHA256 != "" {
			b, err := hex.DecodeString(c.BodySHA256)
			if err != nil || len(b) != sha256.Size {
				return fmt.Errorf("healthChecks[%d].bodySHA256 must be a SHA256 hex digest", i)
			}
		}
	}
	return nil
}

// CheckHealth runs every check within one shared budget and returns the first
// failure. A malformed policy is a failure, never a pass.
func CheckHealth(ctx context.Context, checks []HealthCheck, pods PodReadiness) error {
	if err := ValidateHealthChecks(checks); err != nil {
		return err
	}
	if len(checks) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, healthBudget)
	defer cancel()
	// No proxy from the environment: the check is about this node's path.
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i, c := range checks {
		var err error
		if c.Pods != nil {
			err = checkPods(ctx, *c.Pods, pods)
		} else {
			err = checkHTTP(ctx, client, c)
		}
		if err != nil {
			return fmt.Errorf("health check %d: %w", i, err)
		}
	}
	return nil
}

func checkPods(ctx context.Context, c PodsCheck, pods PodReadiness) error {
	if pods == nil {
		return fmt.Errorf("pods %s in %s: no cluster client", c.Selector, c.Namespace)
	}
	ready, total, err := pods(ctx, c.Namespace, c.Selector)
	if err != nil {
		return fmt.Errorf("pods %s in %s: %w", c.Selector, c.Namespace, err)
	}
	want := max(int(c.MinReady), 1)
	if ready < total || ready < want {
		return fmt.Errorf("pods %s in %s: %d of %d on this node Ready, want all and at least %d", c.Selector, c.Namespace, ready, total, want)
	}
	return nil
}

func checkHTTP(ctx context.Context, client *http.Client, c HealthCheck) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return fmt.Errorf("%s: invalid request", c.URL)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.URL, err)
	}
	defer resp.Body.Close()
	code := c.StatusCode
	if code == 0 {
		code = http.StatusOK
	}
	if resp.StatusCode != int(code) {
		return fmt.Errorf("%s: HTTP %d, want %d", c.URL, resp.StatusCode, code)
	}
	if c.BodySHA256 == "" {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("%s: reading response: %w", c.URL, err)
	}
	if len(body) > maxBody {
		return fmt.Errorf("%s: body exceeds 1 MiB", c.URL)
	}
	sum := sha256.Sum256(body)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), c.BodySHA256) {
		return fmt.Errorf("%s: response body SHA256 %x, want %s", c.URL, sum, c.BodySHA256)
	}
	return nil
}

// healthPolicy reads the checks a boot entry carries. The policy belongs to
// the slot's entry: retiring or restaging the slot retires it too. An entry
// without one keeps the service and API assessment alone.
func healthPolicy(raw string) ([]HealthCheck, error) {
	var checks []HealthCheck
	found := false
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, strings.TrimSpace(healthPrefix)) {
			continue
		}
		if found {
			return nil, fmt.Errorf("duplicate health-check policy")
		}
		found = true
		value, ok := strings.CutPrefix(line, healthPrefix)
		if !ok {
			return nil, fmt.Errorf("malformed health-check policy")
		}
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&checks); err != nil {
			return nil, fmt.Errorf("invalid health-check policy: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("trailing health-check policy data")
		}
	}
	return checks, ValidateHealthChecks(checks)
}

func healthPolicyLine(checks []HealthCheck) (string, error) {
	if err := ValidateHealthChecks(checks); err != nil || len(checks) == 0 {
		return "", err
	}
	policy, err := json.Marshal(checks)
	if err != nil {
		return "", err
	}
	return healthPrefix + string(policy), nil
}
