package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/tym83/kuberoot/pkg/distro"
	"github.com/tym83/kuberoot/pkg/etcdstore"
	"github.com/tym83/kuberoot/pkg/membership"
)

// clusterDir keeps what a worker received from its cluster.
const clusterDir = "/var/lib/kuberoot/cluster"

// configure writes the configuration for the node's current role and returns
// the services that role runs, as the distribution profile describes them. A
// node without a membership runs its own cluster.
func configure(node nodeInfo, cfg bootConfig) ([]service, bool, error) {
	m, err := membership.Load()
	if err != nil {
		// A damaged membership must not keep the node down: set it aside and run
		// standalone, reachable through its node API for repair.
		log.Printf("membership unreadable, running standalone: %v", err)
		quarantineMembership()
		m = nil
	}
	r := roleContext{node: node, controlPlane: true}
	roleName := roleControlPlane
	switch {
	case m == nil:
		if r.net, err = parseClusterNet(cfg.podCIDR, cfg.serviceCIDR); err != nil {
			return nil, false, err
		}
		if r.store, err = storeKind(cfg); err != nil {
			return nil, false, err
		}
	case m.Role == membership.RoleControlPlane:
		// Another control plane of the cluster: the same role as the node
		// that created it, with the cluster's ranges and keys.
		log.Printf("joining %s as a control plane", m.Server)
		if r.net, err = parseClusterNet(m.PodCIDR, m.ServiceCIDR); err != nil {
			return nil, false, err
		}
		if m.PodNetwork != "" {
			cfg.podNetwork = m.PodNetwork
		}
		podNetworkMode = cfg.podNetwork
		if err := joinControlPlane(m); err != nil {
			return nil, false, fmt.Errorf("join as a control plane: %w", err)
		}
		r.store = etcdstore.Etcd
	default:
		log.Printf("joining %s as %s", m.Server, strings.ToLower(m.Role))
		// A member takes the cluster's address ranges and pod network, whatever
		// its own boot arguments say: a node with a pod network of its own
		// would take the port of the cluster's tunnel and its routes.
		if r.net, err = parseClusterNet(m.PodCIDR, m.ServiceCIDR); err != nil {
			return nil, false, err
		}
		if m.PodNetwork != "" && m.PodNetwork != cfg.podNetwork {
			log.Printf("pod network %s, as the cluster's (boot argument: %s)", m.PodNetwork, cfg.podNetwork)
			cfg.podNetwork = m.PodNetwork
		}
		podNetworkMode = cfg.podNetwork
		r.controlPlane, r.member, roleName = false, m, roleWorker
	}
	spec, ok := activeProfile.Spec.Roles[roleName]
	if !ok {
		return nil, false, fmt.Errorf("distribution %s has no %s role", activeProfile.Metadata.Name, roleName)
	}
	activeRole = spec
	services, err := renderRole(spec, r, cfg)
	return services, spec.Addons, err
}

// activeRole is the profile's description of the role this node runs.
var activeRole distro.Role

// quarantineMembership moves the membership aside, kept for inspection.
func quarantineMembership() {
	_ = os.Rename(membership.Path, membership.Path+".broken")
}

// reconfigure switches roles in place: the node joined or left a cluster.
func reconfigure(node nodeInfo, cfg bootConfig) {
	lifecycle.Lock()
	defer lifecycle.Unlock()
	if shuttingDown {
		return
	}
	log.Printf("reconfiguring")
	stopServices()
	// The pod bridge keeps the old role's gateway address; the CNI plugin
	// recreates it for the new pod subnet.
	if link, err := netlink.LinkByName("cni0"); err == nil {
		_ = netlink.LinkDel(link)
	}
	services, controlPlane, err := configure(node, cfg)
	if err != nil {
		// Never leave the node without its API: fall back to standalone.
		log.Printf("reconfigure: %v; running standalone", err)
		quarantineMembership()
		services, controlPlane, err = configure(node, cfg)
		if err != nil {
			log.Printf("standalone configuration: %v", err)
			return
		}
	}
	startServices(services, cfg)
	if controlPlane {
		go applyAddons(generation(), cfg, node)
	}
}

// kubeletCertIssued: kube-proxy on a worker uses the kubelet's client
// certificate, which exists only once the kubelet has bootstrapped.
func kubeletCertIssued() bool {
	_, err := os.Stat("/var/lib/kubelet/pki/kubelet-client-current.pem")
	return err == nil
}

func workerKubeletConfig(clientCA string, cn clusterNet) string {
	cfg := fmt.Sprintf(kubeletConfig, clientCA, cn.dnsIP(), "", "")
	var kept []string
	for _, line := range strings.Split(cfg, "\n") {
		// No serving certificate files: the kubelet requests one from the cluster.
		if strings.HasPrefix(line, "tlsCertFile:") || strings.HasPrefix(line, "tlsPrivateKeyFile:") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n") + "rotateCertificates: true\nserverTLSBootstrap: true\n"
}

const tokenKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: cluster
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: bootstrap
  user:
    token: %s
contexts:
- name: bootstrap
  context:
    cluster: cluster
    user: bootstrap
current-context: bootstrap
`

const fileKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: cluster
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: node
  user:
    client-certificate: %s
    client-key: %s
contexts:
- name: node
  context:
    cluster: cluster
    user: node
current-context: node
`
