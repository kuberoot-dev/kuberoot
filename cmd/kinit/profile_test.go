package main

import (
	"net"
	"strings"
	"testing"

	"github.com/kuberoot-dev/kuberoot/pkg/etcdstore"
)

func edgeFacts(plainHTTP bool) facts {
	var f facts
	f.Node.Name, f.Node.IP = "node-1", "192.168.100.11"
	f.Net.Pod, f.Net.Service, f.Net.DNS = "10.200.0.0/16", "10.201.0.0/16", "10.201.0.10"
	f.Net.PodNetwork = "vxlan"
	f.Boot.Distro, f.Boot.RepoPlainHTTP = "edge", plainHTTP
	f.Store, f.Etcd = newStoreFacts(etcdstore.Kine, nodeInfo{name: f.Node.Name, ip: net.ParseIP(f.Node.IP)})
	return f
}

func TestEdgeProfileRendersBothRoles(t *testing.T) {
	p, err := loadProfileFile("../../distros/edge/profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{roleControlPlane, roleWorker} {
		spec, ok := p.Spec.Roles[role]
		if !ok {
			t.Fatalf("no %s role", role)
		}
		for _, g := range spec.Generators {
			if _, ok := generators[g]; !ok {
				t.Errorf("%s: unknown generator %q", role, g)
			}
		}
		services, err := renderServices(spec, edgeFacts(false))
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		for _, s := range services {
			for _, a := range s.args {
				if strings.Contains(a, "{{") || strings.Contains(a, "<no value>") || strings.HasSuffix(a, "=") {
					t.Errorf("%s/%s: argument %q not rendered", role, s.name, a)
				}
			}
		}
	}
}

func TestEdgeProfileArguments(t *testing.T) {
	p, _ := loadProfileFile("../../distros/edge/profile.yaml")
	args := func(plain bool, name string) string {
		services, err := renderServices(p.Spec.Roles[roleControlPlane], edgeFacts(plain))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range services {
			if s.name == name {
				return strings.Join(s.args, " ")
			}
		}
		t.Fatalf("no service %s", name)
		return ""
	}
	if a := args(false, "kube-apiserver"); !strings.Contains(a, "--service-cluster-ip-range=10.201.0.0/16") || !strings.Contains(a, "--advertise-address=192.168.100.11") {
		t.Errorf("kube-apiserver: %s", a)
	}
	if a := args(false, "kube-controller-manager"); !strings.Contains(a, "--allocate-node-cidrs=true") || !strings.Contains(a, "--cluster-cidr=10.200.0.0/16") {
		t.Errorf("kube-controller-manager: %s", a)
	}
	if strings.Contains(args(false, "kubepkg-operator"), "--plain-http") || !strings.Contains(args(true, "kubepkg-operator"), "--plain-http") {
		t.Error("--plain-http does not follow kuberoot.repo-plain-http")
	}
}

func TestRescueProfileParses(t *testing.T) {
	activeProfile = nil
	p := loadProfileOrRescue() // no profile at the image path on a build host
	if p.Metadata.Name != "rescue" || len(p.Spec.Roles[roleControlPlane].Services) != 1 {
		t.Errorf("rescue profile = %+v", p.Spec.Roles)
	}
}
