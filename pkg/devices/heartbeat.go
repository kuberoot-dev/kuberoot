package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	v1 "github.com/tym83/kuberoot/pkg/apis/devices/v1alpha1"
)

// heartbeat calls a URL outside the node on its period, while its query,
// if it has one, finds what it looks for: a dead man's switch, which an
// outside service turns into an alert when the calls stop.
type heartbeat struct {
	spec   v1.HeartbeatSpec
	cancel context.CancelFunc
	client *http.Client

	mu       sync.Mutex
	healthy  bool
	sent     int64
	lastSent time.Time
	err      string
}

func newHeartbeat(spec v1.HeartbeatSpec) *heartbeat {
	return &heartbeat{spec: spec, client: &http.Client{Timeout: 10 * time.Second}}
}

func (h *heartbeat) run(ctx context.Context) {
	every := h.spec.Every.Duration
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		err := h.beat(ctx)
		h.mu.Lock()
		h.healthy = err == nil
		if err != nil {
			h.err = err.Error()
		} else {
			h.err, h.sent, h.lastSent = "", h.sent+1, time.Now()
		}
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// beat checks the query, then calls the URL.
func (h *heartbeat) beat(ctx context.Context) error {
	if q := h.spec.Query; q != nil {
		n, err := series(ctx, h.client, q.URL, q.Expr)
		if err != nil {
			return fmt.Errorf("query: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("query found nothing: %s", q.Expr)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.spec.URL, nil)
	if err != nil {
		return err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s answered %s", h.spec.URL, resp.Status)
	}
	return nil
}

// series asks a Prometheus-compatible API how many series a query returns.
func series(ctx context.Context, c *http.Client, api, expr string) (int, error) {
	u := strings.TrimRight(api, "/") + "/api/v1/query?query=" + url.QueryEscape(expr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("%s: %w", resp.Status, err)
	}
	if out.Status != "success" {
		return 0, fmt.Errorf("%s: %s", resp.Status, out.Error)
	}
	if out.Data.ResultType == "scalar" {
		return 1, nil
	}
	return len(out.Data.Result), nil
}
