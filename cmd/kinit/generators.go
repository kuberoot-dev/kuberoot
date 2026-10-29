package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
	"github.com/vishvananda/netlink"

	nodev1 "github.com/kuberoot-dev/kuberoot/pkg/apis/node/v1alpha1"
)

// roleContext is what generators and service templates know about the node
// and the role it is configured for.
type roleContext struct {
	node         nodeInfo
	net          clusterNet
	controlPlane bool
	member       *nodev1.MembershipSpec // set for a member of another node's cluster
	// store is the cluster store a control plane runs: kine or etcd.
	store string
}

// generators write what a role needs on disk; a distribution profile picks
// the ones its roles use.
var generators = map[string]func(roleContext) error{
	// node: identity and the node API's own PKI; every role needs it.
	"node": func(r roleContext) error {
		if err := createNodePKI(r.node); err != nil {
			return err
		}
		machineID, err := persistentMachineID()
		if err != nil {
			return err
		}
		nodeAdmin, err := kubeconfig(nodeAPIServer, nodeServingCA, r.node.name, "node-admin")
		if err != nil {
			return err
		}
		return writeFiles(map[string]string{"/etc/machine-id": machineID + "\n", nodeAdminKubeconfig: nodeAdmin},
			"/var/log/kuberoot")
	},
	// cluster: the PKI and component credentials of a cluster this node runs.
	"cluster": func(r roleContext) error {
		if err := createClusterPKI(r.node, r.net); err != nil {
			return err
		}
		files := map[string]string{}
		for _, user := range []string{"admin", "controller-manager", "scheduler", "kube-proxy", "kubelet-client", "node-api-delegation", "intents", "agent"} {
			kc, err := kubeconfig(apiServer, "ca.crt", "kuberoot", user)
			if err != nil {
				return err
			}
			files[filepath.Join(kubeDir, user+".kubeconfig")] = kc
		}
		key, err := encryptionKey()
		if err != nil {
			return err
		}
		files[filepath.Join(kubeDir, "admission.yaml")] = admissionConfig
		files[filepath.Join(kubeDir, "audit-policy.yaml")] = auditPolicy
		files[pkiPath("encryption.yaml")] = fmt.Sprintf(encryptionConfig, key)
		// Controllers that elect a leader among control planes keep their
		// lease in the namespace they run in; the package manager, a process
		// of the node, finds it where a pod would.
		files[inClusterNamespace] = "kube-system"
		activeNet = r.net
		if err := writeFiles(files, "/var/lib/kine"); err != nil {
			return err
		}
		// kine listens on a socket only root reaches, not on a TCP port any
		// host-network pod could talk to.
		return os.MkdirAll("/run/kine", 0o700)
	},
	// etcd: the store's credentials and data, on a control plane whose
	// cluster stores its state in etcd.
	"etcd": etcdGenerator,
	// member: what a member receives from its cluster, and its bootstrap credentials.
	"member": func(r roleContext) error {
		if r.member == nil {
			return fmt.Errorf("the member generator needs a membership")
		}
		m := r.member
		if err := os.MkdirAll(clusterDir, 0o700); err != nil {
			return err
		}
		for name, content := range map[string]string{
			"ca.crt": m.ClusterCA, "front-proxy-ca.crt": m.FrontProxyCA,
			"node-api.crt": m.NodeAPICert, "node-api.key": m.NodeAPIKey,
		} {
			if err := os.WriteFile(filepath.Join(clusterDir, name), []byte(content), 0o600); err != nil {
				return err
			}
		}
		ca := base64.StdEncoding.EncodeToString([]byte(m.ClusterCA))
		kubeletPKI := "/var/lib/kubelet/pki/kubelet-client-current.pem"
		server := m.Server
		if len(m.Servers) > 0 {
			// Several control planes: the node API balances between them on
			// the node, so the kubelet and kube-proxy reach whichever answers.
			server = apiBalancer
		}
		return writeFiles(map[string]string{
			filepath.Join(kubeDir, "bootstrap.kubeconfig"): fmt.Sprintf(tokenKubeconfig, server, ca, m.BootstrapToken),
			// kube-proxy acts with the node's own identity, which the cluster binds to the proxier role.
			filepath.Join(kubeDir, "kube-proxy.kubeconfig"): fmt.Sprintf(fileKubeconfig, server, ca, kubeletPKI, kubeletPKI),
		})
	},
	// containers: containerd and the bridge CNI template, filled with the pod
	// subnet the control plane assigns to this node. With pod-network=none a
	// CNI package (Cilium) writes its own configuration instead.
	"containers": func(r roleContext) error {
		// A configuration left from a previous role or cluster has the wrong subnet.
		_ = os.RemoveAll("/etc/cni/net.d")
		files := map[string]string{}
		dirs := []string{"/etc/cni/net.d"}
		switch profileRuntime() {
		case RuntimeCRIO:
			// CRI-O fills in no template: kuberoot-node writes the
			// configuration once the node has its subnet.
			files["/etc/crio/crio.conf.d/10-kuberoot.conf"] = crioConfig
			files["/etc/containers/policy.json"] = containersPolicy
			files["/etc/containers/registries.conf"] = containersRegistries
			dirs = append(dirs, "/var/lib/containers", "/var/lib/crio")
		default:
			files["/etc/containerd/config.toml"] = containerdConfig
			if podNetworkMode == "none" {
				files["/etc/containerd/config.toml"] = strings.Replace(containerdConfig, cniTemplateLine, "", 1)
			}
			dirs = append(dirs, "/var/lib/containerd")
		}
		if podNetworkMode != "none" {
			files["/etc/cni/kuberoot.conflist.tmpl"] = strings.Replace(cniConfig, "__MTU__", strconv.Itoa(podMTU(r.node.ip)), 1)
		}
		return writeFiles(files, dirs...)
	},
	// kubelet: its configuration, with serving certificates from the cluster CA
	// on the control plane and requested through the CSR API on members.
	"kubelet": func(r roleContext) error {
		cfg := fmt.Sprintf(withRuntime(kubeletConfig), pkiPath("ca.crt"), r.net.dnsIP(), pkiPath("kubelet-server.crt"), pkiPath("kubelet-server.key"))
		if r.member != nil {
			cfg = workerKubeletConfig(filepath.Join(clusterDir, "ca.crt"), r.net)
		}
		var extra map[string]any
		if activeProfile != nil {
			extra = activeProfile.Spec.Kubelet
		}
		cfg, err := withDistroKubelet(cfg, extra)
		if err != nil {
			return err
		}
		return writeFiles(map[string]string{filepath.Join(kubeDir, "kubelet.yaml"): cfg}, "/var/lib/kubelet")
	},
}

// podMTU is the MTU of the interface holding the node's address, the one pod
// traffic between nodes leaves through, less the VXLAN header when the node
// carries pod traffic in VXLAN.
func podMTU(nodeIP net.IP) int {
	mtu := 1500
	if links, err := netlink.LinkList(); err == nil {
	find:
		for _, l := range links {
			addrs, _ := netlink.AddrList(l, netlink.FAMILY_V4)
			for _, a := range addrs {
				if a.IP.Equal(nodeIP) && l.Attrs().MTU > 0 {
					mtu = l.Attrs().MTU
					break find
				}
			}
		}
	}
	if podNetworkMode == "vxlan" {
		mtu -= vxlanOverhead
	}
	return mtu
}

// vxlanOverhead matches nodeapi.VXLANOverhead; kinit does not import the
// node API server for one number.
const vxlanOverhead = 50

// encryptionKey is the key Secrets are encrypted with in the store, made once
// and kept with the cluster's PKI.
func encryptionKey() (string, error) {
	path := pkiPath("encryption.key")
	if raw, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	return encoded, atomicfile.WriteFile(path, []byte(encoded+"\n"), 0o600)
}

// admissionConfig makes the baseline Pod Security Standard the default
// everywhere but kube-system; a namespace that needs more asks for it with
// the usual pod-security.kubernetes.io labels.
const admissionConfig = `apiVersion: apiserver.config.k8s.io/v1
kind: AdmissionConfiguration
plugins:
- name: PodSecurity
  configuration:
    apiVersion: pod-security.admission.config.k8s.io/v1
    kind: PodSecurityConfiguration
    defaults:
      enforce: baseline
      enforce-version: latest
      warn: restricted
      warn-version: latest
    exemptions:
      namespaces: [kube-system]
`

// auditPolicy records who changed what; reads stay out of the log.
const auditPolicy = `apiVersion: audit.k8s.io/v1
kind: Policy
omitStages: [RequestReceived]
rules:
- level: None
  verbs: [get, list, watch]
- level: None
  resources:
  - group: coordination.k8s.io
    resources: [leases]
  - group: ""
    resources: [events]
- level: Metadata
`

const encryptionConfig = `apiVersion: apiserver.config.k8s.io/v1
kind: EncryptionConfiguration
resources:
- resources: [secrets]
  providers:
  - secretbox:
      keys:
      - name: key1
        secret: %s
  - identity: {}
`

func writeFiles(files map[string]string, dirs ...string) error {
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// kubeletAssignments are the CPU and memory managers' records of what they
// gave each container.
var kubeletAssignments = []string{"/var/lib/kubelet/cpu_manager_state", "/var/lib/kubelet/memory_manager_state"}

// forgetResourceAssignments drops those records at boot. No container runs
// yet, so they describe nothing, and the static managers refuse to start
// when the machine they recorded differs from the one booting: a new kernel
// leaves a few pages more or less of memory.
func forgetResourceAssignments() {
	for _, f := range kubeletAssignments {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			log.Printf("kubelet assignments: %v", err)
		}
	}
}
