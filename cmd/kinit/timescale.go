package main

import (
	"fmt"
	"sigs.k8s.io/yaml"
	"strings"
	"time"
)

// Set once at boot, before services and readiness polling start.
var bootTimescale = 1

func (c bootConfig) timeScale() int {
	if c.timescale > 0 {
		return c.timescale
	}
	if c.emulator {
		return 60
	}
	return 1
}
func scaleDuration(d time.Duration) time.Duration { return d * time.Duration(bootTimescale) }

func scaleServices(services []service, n int) []service {
	if n == 1 {
		return services
	}
	duration := func(seconds int) string { return (time.Duration(seconds*n) * time.Second).String() }
	for i := range services {
		s := &services[i]
		s.env = append(s.env, "GOGC=100", fmt.Sprintf("KUBEROOT_TIMESCALE=%d", n))
		switch s.name {
		case "kube-apiserver":
			s.args = append(s.args, "--request-timeout="+duration(60), fmt.Sprintf("--min-request-timeout=%d", 1800*n),
				"--storage-initialization-timeout="+duration(60), "--etcd-healthcheck-timeout="+duration(2), "--etcd-readycheck-timeout="+duration(2),
				"--kubelet-timeout="+duration(5))
		case "kube-controller-manager":
			s.args = append(s.args, "--node-monitor-grace-period="+duration(50), "--node-startup-grace-period="+duration(60))
		case "kine":
			for j, arg := range s.args {
				s.args[j] = strings.ReplaceAll(arg, "_busy_timeout=30000", fmt.Sprintf("_busy_timeout=%d", 30000*n))
			}
		}
		if s.name == "kube-controller-manager" || s.name == "kube-scheduler" {
			s.args = append(s.args, "--leader-elect-lease-duration="+duration(15), "--leader-elect-renew-deadline="+duration(10), "--leader-elect-retry-period="+duration(2))
		}
	}
	return services
}

func scaledKubeletConfig(cfg string, n int) (string, error) {
	if n == 1 {
		return cfg, nil
	}
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(cfg), &fields); err != nil {
		return "", err
	}
	for key, fallback := range map[string]string{
		"nodeStatusUpdateFrequency": "10s", "nodeStatusReportFrequency": "5m",
		"runtimeRequestTimeout": "2m", "syncFrequency": "1m", "volumeStatsAggPeriod": "1m",
	} {
		value, ok := fields[key]
		if !ok {
			value = fallback
		}
		d, err := time.ParseDuration(fmt.Sprint(value))
		if err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
		fields[key] = (d * time.Duration(n)).String()
	}
	lease := 40
	if v, ok := fields["nodeLeaseDurationSeconds"].(float64); ok {
		lease = int(v)
	}
	fields["nodeLeaseDurationSeconds"] = lease * n
	out, err := yaml.Marshal(fields)
	return string(out), err
}

func scaledContainerdConfig(cfg string, n int) string {
	if n == 1 {
		return cfg
	}
	return cfg + fmt.Sprintf(`
[timeouts]
  "io.containerd.timeout.shim.load" = "%ds"
  "io.containerd.timeout.shim.cleanup" = "%ds"
  "io.containerd.timeout.shim.shutdown" = "%ds"
  "io.containerd.timeout.task.state" = "%ds"
[plugins.'io.containerd.cri.v1.images']
  image_pull_progress_timeout = "%ds"
`, 5*n, 5*n, 3*n, 2*n, 300*n)
}

// Only bundled add-ons are changed. User workload probes keep Kubernetes
// semantics, including an explicitly requested one-second timeout.
func scaledAddon(raw string, n int) (string, error) {
	if n == 1 {
		return raw, nil
	}
	docs := strings.Split(raw, "\n---")
	for i, doc := range docs {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			return "", err
		}
		var visit func(any)
		visit = func(value any) {
			switch v := value.(type) {
			case map[string]any:
				for key, child := range v {
					if key == "livenessProbe" || key == "readinessProbe" || key == "startupProbe" {
						if probe, ok := child.(map[string]any); ok {
							for field, fallback := range map[string]float64{"timeoutSeconds": 1, "periodSeconds": 10, "initialDelaySeconds": 0} {
								value, ok := probe[field].(float64)
								if !ok {
									value = fallback
								}
								probe[field] = value * float64(n)
							}
						}
					} else {
						visit(child)
					}
				}
			case []any:
				for _, child := range v {
					visit(child)
				}
			}
		}
		visit(obj)
		// The bundled CoreDNS Kubernetes plugin normally waits only 5s
		// for its first informer sync before starting with an empty cache.
		if obj["kind"] == "ConfigMap" {
			if data, ok := obj["data"].(map[string]any); ok {
				if corefile, ok := data["Corefile"].(string); ok {
					data["Corefile"] = strings.Replace(corefile, "kubernetes cluster.local in-addr.arpa ip6.arpa {", fmt.Sprintf("kubernetes cluster.local in-addr.arpa ip6.arpa {\n            startup_timeout %ds", 5*n), 1)
				}
			}
		}
		out, err := yaml.Marshal(obj)
		if err != nil {
			return "", err
		}
		docs[i] = string(out)
	}
	return strings.Join(docs, "---\n"), nil
}
