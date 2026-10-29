package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
	"os"
	"path/filepath"
	"strings"
)

const (
	kubeDir   = "/etc/kubernetes"
	apiServer = "https://127.0.0.1:6443"

	nodeAPIServer       = "https://127.0.0.1:50000"
	nodeAPIServiceDNS   = "kuberoot-node.kube-system.svc"
	nodeAdminKubeconfig = "/etc/kuberoot/node-admin.kubeconfig"
)

// persistentMachineID keeps the machine identity on the state partition when
// there is one, so the node looks like the same machine after a reboot.
func persistentMachineID() (string, error) {
	const path = "/var/lib/kuberoot/machine-id"
	if raw, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	machineID := hex.EncodeToString(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return machineID, atomicfile.WriteFile(path, []byte(machineID+"\n"), 0o644)
}

// kubeconfig embeds the credentials so the file is usable off the node too.
func kubeconfig(server, caFile, name, user string) (string, error) {
	b64 := func(name string) (string, error) {
		raw, err := os.ReadFile(pkiPath(name))
		return base64.StdEncoding.EncodeToString(raw), err
	}
	ca, err := b64(caFile)
	if err != nil {
		return "", err
	}
	crt, err := b64(user + ".crt")
	if err != nil {
		return "", err
	}
	key, err := b64(user + ".key")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: %[1]s
  cluster:
    server: %[2]s
    certificate-authority-data: %[3]s
users:
- name: %[1]s-%[4]s
  user:
    client-certificate-data: %[5]s
    client-key-data: %[6]s
contexts:
- name: %[1]s
  context:
    cluster: %[1]s
    user: %[1]s-%[4]s
current-context: %[1]s
`, name, server, ca, user, crt, key), nil
}

const containerdConfig = `version = 3
root = "/var/lib/containerd"
state = "/run/containerd"

[grpc]
  address = "/run/containerd/containerd.sock"

[plugins.'io.containerd.cri.v1.runtime'.cni]
  # The built-in plugins, and /opt/cni/bin, writable, where CNI packages
  # such as Cilium install theirs.
  bin_dirs = ["/usr/libexec/cni", "/opt/cni/bin"]
  conf_dir = "/etc/cni/net.d"
  # The kubelet passes the pod subnet the control plane assigned to this node;
  # containerd fills it into this template and writes the CNI configuration.
  conf_template = "/etc/cni/kuberoot.conflist.tmpl"


[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc]
  runtime_type = "io.containerd.runc.v2"

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc.options]
  BinaryName = "/usr/bin/runc"
  SystemdCgroup = false
`

// cniTemplateLine is left out of the containerd configuration when a CNI
// package provides the pod network.
const cniTemplateLine = `  conf_template = "/etc/cni/kuberoot.conflist.tmpl"
`

// podNetworkMode is the boot's pod-network setting, for the generators.
var podNetworkMode = "vxlan"

const cniConfig = `{
  "cniVersion": "1.0.0",
  "name": "kuberoot",
  "plugins": [
    {
      "type": "bridge",
      "bridge": "cni0",
      "isGateway": true,
      "ipMasq": true,
      "ipMasqBackend": "nftables",
      "hairpinMode": true,
      "mtu": __MTU__,
      "ipam": {
        "type": "host-local",
        "ranges": [[{"subnet": "{{.PodCIDR}}"}]],
        "routes": [{"dst": "0.0.0.0/0"}]
      }
    },
    {"type": "portmap", "backend": "nftables", "capabilities": {"portMappings": true}}
  ]
}
`

const kubeletConfig = `apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
authentication:
  anonymous:
    enabled: false
  webhook:
    enabled: true
  x509:
    clientCAFile: %s
authorization:
  mode: Webhook
cgroupDriver: cgroupfs
containerRuntimeEndpoint: __RUNTIME_ENDPOINT__
clusterDomain: cluster.local
clusterDNS:
- %s
tlsCertFile: %s
tlsPrivateKeyFile: %s
failSwapOn: false
resolvConf: /etc/resolv.conf
`

// Container runtimes a profile may choose.
const (
	RuntimeContainerd = "containerd"
	RuntimeCRIO       = "cri-o"
)

// runtimeEndpoint is where the kubelet reaches the runtime.
func runtimeEndpoint(runtime string) string {
	if runtime == RuntimeCRIO {
		return "unix:///run/crio/crio.sock"
	}
	return "unix:///run/containerd/containerd.sock"
}

// crioConfig runs containers with runc, the pod network from the CNI
// configuration kuberoot-node writes for the node's subnet, and checkpoint
// and restore through CRIU.
const crioConfig = `[crio]
root = "/var/lib/containers/storage"
runroot = "/run/containers/storage"
storage_driver = "overlay"

[crio.api]
listen = "/run/crio/crio.sock"

[crio.runtime]
cgroup_manager = "cgroupfs"
conmon_cgroup = "pod"
conmon = "/usr/bin/conmon"
pinns_path = "/usr/bin/pinns"
default_runtime = "runc"
enable_criu_support = true

[crio.runtime.runtimes.runc]
runtime_path = "/usr/bin/runc"
runtime_root = "/run/runc"

[crio.network]
network_dir = "/etc/cni/net.d"
plugin_dirs = ["/usr/libexec/cni", "/opt/cni/bin"]
`

// containersPolicy accepts any image: what may run is the cluster's
// admission's to say, as with containerd.
const containersPolicy = `{"default":[{"type":"insecureAcceptAnything"}]}
`

// containersRegistries: short names are Docker Hub's, as with containerd;
// the node's own registry on the loopback is plain HTTP.
const containersRegistries = `unqualified-search-registries = ["docker.io"]

[[registry]]
location = "localhost:5001"
insecure = true
`

// profileRuntime is the active profile's container runtime.
func profileRuntime() string {
	if activeProfile != nil && activeProfile.Spec.Runtime == RuntimeCRIO {
		return RuntimeCRIO
	}
	return RuntimeContainerd
}

// withRuntime points a kubelet configuration at the profile's runtime.
func withRuntime(cfg string) string {
	return strings.Replace(cfg, "__RUNTIME_ENDPOINT__", runtimeEndpoint(profileRuntime()), 1)
}
