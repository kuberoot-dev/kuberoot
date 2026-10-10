package main

import (
	"os"
	"strings"
)

// bootConfig holds the kuberoot.* kernel parameters.
type bootConfig struct {
	// dev prints the admin kubeconfig to the console; development builds only.
	dev bool
	// verbose streams every service's output to the console.
	verbose bool
	// nameservers override the DNS servers handed out by DHCP.
	nameservers []string
	// install runs the console installer: the system booted from boot media.
	install bool
	// distro names the kuberoot distribution; its add-ons are the kubepkg
	// meta package kuberoot-<distro>. Unset, it is the distribution the
	// image was built as.
	distro string
	// repo is the kubepkg repository index, repoKey its public key (base64 PEM),
	// and repoPlainHTTP allows a test registry without TLS.
	repo, repoKey string
	repoPlainHTTP bool
	// podCIDR and serviceCIDR set the address ranges of a cluster this node runs.
	podCIDR, serviceCIDR string
	// podNetwork is how pod traffic crosses between nodes: vxlan (default),
	// host-gw on a plain layer-2 network, or none when a CNI package such as
	// Cilium takes the pod network over.
	podNetwork string
	// static is an interface with a fixed address, "eth1:192.168.100.11/24";
	// that address becomes the node address in the cluster.
	static string
	// datastore is the store of a cluster this node creates: kine, the
	// default, for one control plane, or etcd, which other control planes
	// can join. It is read once, when the cluster is created.
	// kuberoot-store runs the resource store lab in etcd's place, on an
	// image built with it (KUBEROOT_STORE_LAB=1); never a default.
	datastore string
}

func loadCmdline() bootConfig {
	raw, _ := os.ReadFile("/proc/cmdline")
	return parseCmdline(string(raw))
}

func parseCmdline(cmdline string) bootConfig {
	var cfg bootConfig
	for _, f := range strings.Fields(cmdline) {
		switch f {
		case "kuberoot.dev":
			cfg.dev = true
		case "kuberoot.verbose":
			cfg.verbose = true
		case "kuberoot.mode=install":
			cfg.install = true
		case "kuberoot.repo-plain-http":
			cfg.repoPlainHTTP = true
		default:
			if v, ok := strings.CutPrefix(f, "kuberoot.nameserver="); ok {
				cfg.nameservers = append(cfg.nameservers, strings.Split(v, ",")...)
			}
			if v, ok := strings.CutPrefix(f, "kuberoot.ip="); ok {
				cfg.static = v
			}
			if v, ok := strings.CutPrefix(f, "kuberoot.distro="); ok {
				cfg.distro = v
			}
			if v, ok := strings.CutPrefix(f, "kuberoot.repo="); ok {
				cfg.repo = v
			}
			if v, ok := strings.CutPrefix(f, "kuberoot.repo-key="); ok {
				cfg.repoKey = v
			}
			if v, ok := strings.CutPrefix(f, "kuberoot.pod-cidr="); ok {
				cfg.podCIDR = v
			}
			if v, ok := strings.CutPrefix(f, "kuberoot.service-cidr="); ok {
				cfg.serviceCIDR = v
			}
			if v, ok := strings.CutPrefix(f, "kuberoot.pod-network="); ok {
				cfg.podNetwork = v
			}
			if v, ok := strings.CutPrefix(f, "kuberoot.datastore="); ok {
				cfg.datastore = v
			}
		}
	}
	switch cfg.podNetwork {
	case "vxlan", "host-gw", "none":
	default:
		cfg.podNetwork = "vxlan"
	}
	if cfg.repo == "" {
		cfg.repo = "https://kuberoot-dev.github.io/kubepkg-recipes/index.yaml"
	}
	return cfg
}
