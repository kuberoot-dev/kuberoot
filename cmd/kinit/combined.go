package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// combineServices retains each component's rendered standard flags. One
// supervisor entry owns the shared failure domain: any component exiting
// restarts the group, so no half-running control plane remains unnoticed.
func combineServices(services []service) ([]service, error) {
	return combineServicesFile(services, filepath.Join(kubeDir, "components.json"))
}

func combineServicesFile(services []service, path string) ([]service, error) {
	if activeProfile == nil || activeProfile.Metadata.Name != "slim" {
		return services, nil
	}
	// Workers retain independent agents and their kubelet certificate barrier.
	controlPlane := false
	for _, s := range services {
		if s.name == "kube-apiserver" {
			controlPlane = true
		}
	}
	if !controlPlane {
		return services, nil
	}
	components := map[string][]string{}
	var kept []service
	for _, s := range services {
		switch s.name {
		case "kine", "kube-apiserver", "kube-controller-manager", "kube-scheduler", "kubelet", "kube-proxy":
			if s.alt != nil {
				return nil, fmt.Errorf("slim component %s cannot use alternatives", s.name)
			}
			components[s.name] = s.args[1:]
		default:
			kept = append(kept, s)
		}
	}
	if len(components) == 0 {
		return kept, nil
	}
	raw, err := json.Marshal(components)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, err
	}
	var info unix.Sysinfo_t
	_ = unix.Sysinfo(&info)
	env := controlMemoryEnv(uint64(info.Totalram) * uint64(info.Unit))
	return append(kept, service{name: "kuberoot-control", args: []string{"/usr/bin/kuberoot-control", path}, env: env}), nil
}

// Leave most RAM to the kernel, executable pages, containerd, node API and
// workloads. This is a shared Go soft limit; SQLite's C memory is separate.
func controlMemoryEnv(total uint64) []string {
	limit := uint64(300 << 20)
	if total > 0 {
		limit = min(limit, total*2/5)
	}
	return []string{"GOGC=25", fmt.Sprintf("GOMEMLIMIT=%dMiB", max(1, limit>>20))}
}
