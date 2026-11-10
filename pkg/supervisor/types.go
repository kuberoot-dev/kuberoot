// Package supervisor describes the local control interface kinit serves on a
// unix socket: the status of node services and the actions on them.
package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Socket is where kinit listens; only root on the node can reach it.
const Socket = "/run/kuberoot/kinit.sock"

// ServiceStatus is the observed state of one supervised service.
type ServiceStatus struct {
	Name      string    `json:"name"`
	State     string    `json:"state"` // Running or Restarting
	PID       int       `json:"pid,omitempty"`
	Restarts  int       `json:"restarts"`
	StartedAt time.Time `json:"startedAt,omitempty"`
	LastExit  string    `json:"lastExit,omitempty"`
	Cgroup    string    `json:"cgroup"`
	LogFile   string    `json:"logFile"`
	// Alternative is the build the node runs, for a service with
	// alternatives in the profile.
	Alternative string `json:"alternative,omitempty"`
}

const (
	StateRunning    = "Running"
	StateRestarting = "Restarting"
	// StateWaiting: not started yet, waiting for what it depends on.
	StateWaiting = "Waiting"
)

// Client talks to kinit over its socket.
type Client struct{ http *http.Client }

func NewClient() *Client {
	return &Client{http: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", Socket)
		}},
	}}
}

func (c *Client) Services(ctx context.Context) ([]ServiceStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://kinit/v1/services", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []ServiceStatus
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *Client) Restart(ctx context.Context, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://kinit/v1/services/"+name+"/restart", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("restart %s: %s", name, resp.Status)
	}
	return nil
}

// Reboot asks kinit to stop the services and restart the machine.
func (c *Client) Reboot(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://kinit/v1/reboot", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// Reconfigure asks kinit to stop every service, regenerate the node
// configuration (role, certificates) and start again, without a reboot.
func (c *Client) Reconfigure(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://kinit/v1/reconfigure", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
