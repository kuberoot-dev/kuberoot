// Package membership stores which cluster a node belongs to. kinit reads it to
// pick the node's role; the node API writes it when the node joins or leaves.
package membership

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	nodev1 "github.com/kuberoot-dev/kuberoot/pkg/apis/node/v1alpha1"
	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
)

// Path is on the state partition, so membership survives reboots.
const Path = "/var/lib/kuberoot/membership.json"

// Roles a node joins a cluster in.
const (
	RoleWorker       = "Worker"
	RoleControlPlane = "ControlPlane"
)

// Load returns the membership, or nil for a standalone node.
func Load() (*nodev1.MembershipSpec, error) {
	raw, err := os.ReadFile(Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var spec nodev1.MembershipSpec
	return &spec, json.Unmarshal(raw, &spec)
}

func Save(spec nodev1.MembershipSpec) error {
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(Path), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFile(Path, raw, 0o600)
}

func Remove() error {
	err := os.Remove(Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
