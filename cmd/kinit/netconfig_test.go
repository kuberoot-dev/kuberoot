package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClusterNetDefaultsAndDerivedAddresses(t *testing.T) {
	cn, err := parseClusterNet("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cn.pod.String() != defaultPodCIDR || cn.service.String() != defaultServiceCIDR {
		t.Errorf("ranges = %s", cn)
	}
	if got := cn.apiServiceIP().String(); got != "10.201.0.1" {
		t.Errorf("API service IP = %s", got)
	}
	if got := cn.dnsIP().String(); got != "10.201.0.10" {
		t.Errorf("DNS IP = %s", got)
	}
}

func TestClusterNetRejects(t *testing.T) {
	for _, c := range [][2]string{{"10.0.0.0/24", ""}, {"nonsense", ""}, {"", "10.0.0.0/33"}} {
		if _, err := parseClusterNet(c[0], c[1]); err == nil {
			t.Errorf("parseClusterNet(%q, %q) accepted", c[0], c[1])
		}
	}
}

func TestParseCmdline(t *testing.T) {
	cfg := parseCmdline("console=ttyS0 kuberoot.dev kuberoot.mode=install kuberoot.nameserver=1.1.1.1,8.8.8.8 " +
		"kuberoot.ip=eth1:10.0.0.5/24 kuberoot.distro=router kuberoot.repo-plain-http kuberoot.pod-cidr=10.50.0.0/16")
	if !cfg.dev || !cfg.install || !cfg.repoPlainHTTP {
		t.Errorf("switches = %+v", cfg)
	}
	if len(cfg.nameservers) != 2 || cfg.static != "eth1:10.0.0.5/24" || cfg.distro != "router" || cfg.podCIDR != "10.50.0.0/16" {
		t.Errorf("values = %+v", cfg)
	}
	def := parseCmdline("")
	if def.distro != "" || def.repo == "" {
		t.Errorf("defaults = %+v", def)
	}
	// Not asked for at boot: the distribution the image was built as.
	saved := activeProfile
	defer func() { activeProfile = saved }()
	activeProfile = nil
	if got := distroName(def); got != "edge" {
		t.Errorf("no profile: %s", got)
	}
	activeProfile, _ = loadProfileFile("../../distros/ai/profile.yaml")
	if got := distroName(def); got != "ai" {
		t.Errorf("an ai image installs %s", got)
	}
	if got := distroName(cfg); got != "router" {
		t.Errorf("asked for router at boot: %s", got)
	}
}

func TestPodNetworkFromCmdline(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "vxlan",
		"kuberoot.pod-network=none":      "none",
		"kuberoot.pod-network=host-gw":   "host-gw",
		"kuberoot.pod-network=something": "vxlan",
	} {
		if got := parseCmdline(in).podNetwork; got != want {
			t.Errorf("%q: pod network %q, want %q", in, got, want)
		}
	}
}

func TestNoPodNetworkLeavesTheCNITemplateOut(t *testing.T) {
	if !strings.Contains(containerdConfig, cniTemplateLine) {
		t.Fatal("the containerd configuration does not contain the CNI template line it is meant to drop")
	}
	if strings.Contains(strings.Replace(containerdConfig, cniTemplateLine, "", 1), "conf_template") {
		t.Error("conf_template is still set with pod-network=none")
	}
}

func TestContainerdHandsDeviceNodesToTheUserAContainerRunsAs(t *testing.T) {
	if !strings.Contains(containerdConfig, "device_ownership_from_security_context = true") {
		t.Error("containerd does not take device ownership from the security context, so a container asking for a device through it cannot open the device node")
	}
}

func TestNodeKeepsTheNameItFirstBootedWith(t *testing.T) {
	saved := nodeNameFile
	nodeNameFile = filepath.Join(t.TempDir(), "kuberoot", "node-name")
	defer func() { nodeNameFile = saved }()
	if got := keptNodeName("kuberoot-aaaaaa"); got != "kuberoot-aaaaaa" {
		t.Errorf("first boot: %s", got)
	}
	// New hardware, or the state restored onto it.
	if got := keptNodeName("kuberoot-bbbbbb"); got != "kuberoot-aaaaaa" {
		t.Errorf("after the network card changed: %s, want the first name", got)
	}
}
