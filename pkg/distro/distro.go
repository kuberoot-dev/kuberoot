// Package distro is the distribution profile built into a kuberoot image:
// what its node roles generate and run, and which node API resources it serves.
package distro

import (
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

// Path is where the image carries its profile.
const Path = "/usr/share/kuberoot/profile.yaml"

// Role names.
const (
	RoleControlPlane = "controlPlane"
	RoleWorker       = "worker"
)

type Profile struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec Spec `json:"spec"`
}

// Module is a kernel module and its parameters.
type Module struct {
	Name   string `json:"name"`
	Params string `json:"params,omitempty"`
}

type Spec struct {
	Sysctls map[string]string `json:"sysctls,omitempty"`
	// Modules are loaded at boot, in order, from the out-of-tree modules
	// built with the kernel; module loading is then disabled.
	Modules []Module `json:"modules,omitempty"`
	// Kubelet holds fields of the KubeletConfiguration the distribution sets
	// on top of the image's own, such as how the kubelet hands out CPUs.
	Kubelet map[string]any `json:"kubelet,omitempty"`
	// Runtime is the container runtime the kubelet talks to: containerd,
	// the default, or cri-o, which can restore containers from checkpoints.
	// The image must carry it.
	Runtime string          `json:"runtime,omitempty"`
	Roles   map[string]Role `json:"roles"`
	NodeAPI NodeAPI         `json:"nodeAPI,omitempty"`
}

type Role struct {
	// Generators, by name, write what the role needs on disk.
	Generators []string `json:"generators"`
	// Addons: apply the bundled add-ons, the image's own and its distribution's.
	Addons bool `json:"addons,omitempty"`
	// Packages: run on kubepkg; subscribe to the package repository and
	// install the distribution's meta package, kuberoot-<distribution>.
	Packages bool      `json:"packages,omitempty"`
	Services []Service `json:"services"`
}

type Service struct {
	Name string `json:"name"`
	// After names what must be ready before the first start: apiserver or kubelet-cert.
	After string `json:"after,omitempty"`
	// When is a template over the role's facts: the service runs only when
	// it renders true. It picks, for one, the cluster store a node runs.
	When string   `json:"when,omitempty"`
	Env  []string `json:"env,omitempty"`
	// Args are templates over the role's facts; arguments that render empty are dropped.
	Args []string `json:"args"`
	// Alternatives, when set, are interchangeable builds of the service's
	// program, of which a node runs one.
	Alternatives *Alternatives `json:"alternatives,omitempty"`
}

// Alternatives are builds of one service the node chooses between. On its
// first boot a node picks one at random and keeps it: every later boot runs
// the same one, so the nodes of a cluster end up running a mix of them.
type Alternatives struct {
	// Choices: each replaces the service's program, its first argument, and
	// adds arguments of its own after the service's.
	Choices []Alternative `json:"choices"`
	// Switch, when set, moves a node whose choice keeps crashing to the next
	// one; without it a node never changes its choice.
	Switch *AlternativeSwitch `json:"switch,omitempty"`
}

type Alternative struct {
	// Name identifies the choice on the node and in its labels: a label value.
	Name    string   `json:"name"`
	Program string   `json:"program"`
	Args    []string `json:"args,omitempty"`
}

// AlternativeSwitch: after Crashes crashes within Within, the node switches
// the service to its next choice, at most once per boot and once per
// Cooldown, and never while the slot it booted is on probation, where a
// failing release is the upgrade's to roll back. A crash is an exit kinit did
// not ask for, with an error or a signal, within a minute of starting.
type AlternativeSwitch struct {
	Crashes  int    `json:"crashes"`
	Within   string `json:"within"`
	Cooldown string `json:"cooldown"`
}

// Durations returns Within and Cooldown.
func (s *AlternativeSwitch) Durations() (within, cooldown time.Duration, err error) {
	if within, err = time.ParseDuration(s.Within); err != nil || within <= 0 {
		return 0, 0, fmt.Errorf("within %q: want a positive duration", s.Within)
	}
	if cooldown, err = time.ParseDuration(s.Cooldown); err != nil || cooldown < 0 {
		return 0, 0, fmt.Errorf("cooldown %q: want a duration", s.Cooldown)
	}
	return within, cooldown, nil
}

func (a *Alternatives) validate() error {
	if len(a.Choices) < 2 {
		return fmt.Errorf("alternatives need at least two choices")
	}
	seen := map[string]bool{}
	for _, c := range a.Choices {
		if errs := validation.IsValidLabelValue(c.Name); c.Name == "" || len(errs) > 0 {
			return fmt.Errorf("alternative %q: not a label value", c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("alternative %q appears twice", c.Name)
		}
		seen[c.Name] = true
		if c.Program == "" {
			return fmt.Errorf("alternative %s has no program", c.Name)
		}
	}
	if a.Switch != nil {
		if a.Switch.Crashes < 1 {
			return fmt.Errorf("switch: crashes must be at least 1")
		}
		if _, _, err := a.Switch.Durations(); err != nil {
			return fmt.Errorf("switch: %w", err)
		}
	}
	return nil
}

// NodeAPI picks the resources the node API serves; empty serves them all.
type NodeAPI struct {
	Resources []string `json:"resources,omitempty"`
}

// Load reads and checks a profile file.
func Load(path string) (*Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw, path)
}

func Parse(raw []byte, source string) (*Profile, error) {
	p := &Profile{}
	if err := yaml.UnmarshalStrict(raw, p); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if p.Kind != "Distribution" {
		return nil, fmt.Errorf("%s: kind %q, want Distribution", source, p.Kind)
	}
	for role, r := range p.Spec.Roles {
		for _, s := range r.Services {
			if s.Alternatives == nil {
				continue
			}
			if err := s.Alternatives.validate(); err != nil {
				return nil, fmt.Errorf("%s: %s/%s: %w", source, role, s.Name, err)
			}
		}
	}
	return p, nil
}
