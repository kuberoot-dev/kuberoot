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
)

// HealthCheck is an optional application check required throughout probation.
// Requests originate on the node, without proxy environment variables, client
// credentials or redirects. HTTPS uses the system trust roots.
type HealthCheck struct {
	URL string `json:"url"`
	// StatusCode defaults to 200.
	StatusCode int32 `json:"statusCode,omitempty"`
	// BodySHA256, when set, checks the complete response body (at most 1 MiB).
	BodySHA256 string `json:"bodySHA256,omitempty"`
}

const healthPrefix = "# kuberoot-health-checks "

func ValidateHealthChecks(checks []HealthCheck) error {
	if len(checks) > 8 {
		return fmt.Errorf("at most 8 health checks are allowed")
	}
	for i, c := range checks {
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

// CheckHealth shares one three-second budget across all checks. A failing or
// malformed policy must never become a successful assessment.
func CheckHealth(ctx context.Context, checks []HealthCheck) error {
	if err := ValidateHealthChecks(checks); err != nil {
		return err
	}
	if len(checks) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i, c := range checks {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
		if err != nil {
			return fmt.Errorf("health check %d: invalid request", i)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("health check %d: request failed: %w", i, err)
		}
		code := c.StatusCode
		if code == 0 {
			code = http.StatusOK
		}
		if resp.StatusCode != int(code) {
			resp.Body.Close()
			return fmt.Errorf("health check %d: HTTP %d, expected %d", i, resp.StatusCode, code)
		}
		if c.BodySHA256 != "" {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
			resp.Body.Close()
			if readErr != nil {
				return fmt.Errorf("health check %d: reading response: %w", i, readErr)
			}
			if len(body) > 1<<20 {
				return fmt.Errorf("health check %d: body exceeds 1 MiB", i)
			}
			sum := sha256.Sum256(body)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), c.BodySHA256) {
				return fmt.Errorf("health check %d: response digest mismatch", i)
			}
		} else {
			resp.Body.Close()
		}
	}
	return nil
}

// The policy is part of the atomic boot entry, not shared application state.
// Retiring or replacing a slot also retires its policy. Legacy entries without
// the comment retain the existing service/API-only assessment.
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
