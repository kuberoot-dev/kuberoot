// Package distro is the distribution profile built into a kuberoot image:
// what its node roles generate and run, and which node API resources it serves.
package distro

import (
	"fmt"
	"os"

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
	Kubelet map[string]any  `json:"kubelet,omitempty"`
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
	return p, nil
}
