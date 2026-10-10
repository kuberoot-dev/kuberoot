package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuberoot-dev/kuberoot/pkg/etcdstore"
)

func servicesOf(t *testing.T, distro, role string, f facts) map[string]string {
	t.Helper()
	p, err := loadProfileFile("../../distros/" + distro + "/profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	services, err := renderServices(p.Spec.Roles[role], f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, s := range services {
		out[s.name] = strings.Join(s.args, " ")
	}
	return out
}

func TestControlPlaneRunsTheStoreItWasCreatedWith(t *testing.T) {
	etcdstore.InitFile = filepath.Join(t.TempDir(), "etcd-init")
	for _, distro := range []string{"edge", "ai", "rt", "observability"} {
		kine := servicesOf(t, distro, roleControlPlane, edgeFacts(false))
		if _, ok := kine["etcd"]; ok {
			t.Errorf("%s: a kine control plane runs etcd", distro)
		}
		if !strings.Contains(kine["kube-apiserver"], "--etcd-servers=unix:///run/kine/kine.sock") || strings.Contains(kine["kube-apiserver"], "--etcd-cafile") {
			t.Errorf("%s: kine API server: %s", distro, kine["kube-apiserver"])
		}
		if !strings.Contains(kine["kube-controller-manager"], "--leader-elect=false") {
			t.Errorf("%s: one control plane elects no leader", distro)
		}

		f := edgeFacts(false)
		f.Store, f.Etcd = newStoreFacts(etcdstore.Etcd, nodeInfo{name: "node-1", ip: net.ParseIP("192.168.100.11")})
		ha := servicesOf(t, distro, roleControlPlane, f)
		if _, ok := ha["kine"]; ok {
			t.Errorf("%s: an etcd control plane runs kine", distro)
		}
		e := ha["etcd"]
		for _, want := range []string{"--name=node-1", "--initial-cluster=node-1=https://192.168.100.11:2380", "--initial-cluster-state=new",
			"--listen-client-urls=https://192.168.100.11:2379,https://127.0.0.1:2379", "--client-cert-auth", "--peer-client-cert-auth"} {
			if !strings.Contains(e, want) {
				t.Errorf("%s: etcd lacks %s: %s", distro, want, e)
			}
		}
		a := ha["kube-apiserver"]
		if !strings.Contains(a, "--etcd-servers=https://127.0.0.1:2379") || !strings.Contains(a, "--etcd-certfile=") {
			t.Errorf("%s: etcd API server: %s", distro, a)
		}
		for _, svc := range []string{"kube-controller-manager", "kube-scheduler"} {
			if !strings.Contains(ha[svc], "--leader-elect=true") {
				t.Errorf("%s: %s elects no leader among control planes", distro, svc)
			}
		}
		if !strings.Contains(ha["kuberoot-node"], "--etcd") {
			t.Errorf("%s: the node API does not serve the store's members", distro)
		}
	}
}

func TestWorkerBalancesBetweenControlPlanes(t *testing.T) {
	f := edgeFacts(false)
	if a := servicesOf(t, "edge", roleWorker, f)["kuberoot-node"]; strings.Contains(a, "--api-servers") {
		t.Errorf("a worker of one control plane balances: %s", a)
	}
	f.Member.Servers = "https://10.0.0.1:6443,https://10.0.0.2:6443"
	a := servicesOf(t, "edge", roleWorker, f)["kuberoot-node"]
	if !strings.Contains(a, "--api-servers=https://10.0.0.1:6443,https://10.0.0.2:6443") || !strings.Contains(a, "--api-kubeconfig=/var/lib/kubelet/kubeconfig") {
		t.Errorf("worker of several control planes: %s", a)
	}
}

func TestStoreKind(t *testing.T) {
	dir := t.TempDir()
	etcdstore.KindFile = filepath.Join(dir, "datastore")
	// A new cluster asked to be highly available.
	if k, err := storeKind(bootConfig{datastore: etcdstore.Etcd}); err != nil || k != etcdstore.Etcd {
		t.Fatalf("new etcd cluster: %s %v", k, err)
	}
	// Once decided, the boot arguments no longer matter.
	if k, _ := storeKind(bootConfig{}); k != etcdstore.Etcd {
		t.Fatalf("kept: %s", k)
	}
	if err := os.Remove(etcdstore.KindFile); err != nil {
		t.Fatal(err)
	}
	if k, _ := storeKind(bootConfig{}); k != etcdstore.Kine {
		t.Fatalf("default: %s", k)
	}
}
