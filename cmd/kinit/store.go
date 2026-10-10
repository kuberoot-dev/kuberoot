package main

import (
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	nodev1 "github.com/tym83/kuberoot/pkg/apis/node/v1alpha1"
	"github.com/tym83/kuberoot/pkg/etcdstore"
	"github.com/tym83/kuberoot/pkg/membership"
)

// kineStoreFile is a single control plane's store.
const kineStoreFile = "/var/lib/kine/state.db"

// inClusterNamespace is where controller-runtime reads the namespace of its
// leader election lease when it is not told one.
const inClusterNamespace = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// apiBalancer is where a worker of several control planes reaches the
// cluster API: the node API forwards to whichever control plane answers.
// The API servers' certificates name 127.0.0.1.
const apiBalancer = "https://127.0.0.1:6444"

// storeKind is the store of the cluster this control plane runs, decided
// when the cluster is created and kept: a cluster that has data in kine
// stays on kine, a new one takes what the boot asked for, kine by default.
func storeKind(cfg bootConfig) (string, error) {
	if k := etcdstore.Kind(); k != "" {
		return k, nil
	}
	kind := etcdstore.Kine
	if _, err := os.Stat(kineStoreFile); err != nil && cfg.datastore == etcdstore.Etcd {
		kind = etcdstore.Etcd
	}
	return kind, etcdstore.SetKind(kind)
}

// joinControlPlane takes what a joining control plane shares with the
// others: the cluster's authorities and keys, and the store it joins. They
// are kept with the node's own credentials and dropped from the membership.
func joinControlPlane(m *nodev1.MembershipSpec) error {
	share := m.ControlPlane
	if share == nil {
		return nil // taken on an earlier boot
	}
	if err := os.MkdirAll(pkiDir, 0o700); err != nil {
		return err
	}
	for name, content := range map[string]string{
		"ca.crt": m.ClusterCA, "ca.key": share.ClusterCAKey,
		"front-proxy-ca.crt": m.FrontProxyCA, "front-proxy-ca.key": share.FrontProxyCAKey,
		"sa.key": share.ServiceAccountKey, "sa.pub": share.ServiceAccountPub,
		"encryption.key":      share.EncryptionKey,
		etcdstore.CA + ".crt": share.EtcdCA, etcdstore.CA + ".key": share.EtcdCAKey,
	} {
		if content == "" {
			return fmt.Errorf("the membership lacks %s", name)
		}
		if err := os.WriteFile(pkiPath(name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	// A member left from an earlier time in another store would not start.
	if err := os.RemoveAll(etcdstore.DataDir); err != nil {
		return err
	}
	if err := etcdstore.WriteInit(etcdstore.Joining(share.EtcdInitialCluster)); err != nil {
		return err
	}
	if err := etcdstore.SetKind(etcdstore.Etcd); err != nil {
		return err
	}
	kept := *m
	kept.ControlPlane = nil
	return membership.Save(kept)
}

// storeFacts are what the profile's services know of the cluster store.
type storeFacts struct {
	// Kind is kine or etcd.
	Kind string
	// Servers is the API server's --etcd-servers.
	Servers string
	// LeaderElect: with other control planes, controllers elect a leader.
	LeaderElect string
}

type etcdFacts struct {
	Name, PeerURL, ClientURLs, AdvertiseClientURL string
	InitialCluster, State                         string
}

func newStoreFacts(kind string, node nodeInfo) (storeFacts, etcdFacts) {
	if kind != etcdstore.Etcd {
		return storeFacts{Kind: etcdstore.Kine, Servers: "unix:///run/kine/kine.sock", LeaderElect: "false"}, etcdFacts{}
	}
	ip := node.ip.String()
	init := etcdstore.ReadInit(node.name, ip)
	return storeFacts{Kind: etcdstore.Etcd, Servers: etcdstore.Endpoint, LeaderElect: "true"},
		etcdFacts{
			Name: node.name, PeerURL: etcdstore.PeerURL(ip),
			ClientURLs:         etcdstore.ClientURL(ip) + "," + etcdstore.Endpoint,
			AdvertiseClientURL: etcdstore.ClientURL(ip),
			InitialCluster:     init.InitialCluster, State: init.State,
		}
}

// etcdGenerator issues the store's credentials from its own authority and
// prepares its data: a member of a highly available control plane only.
func etcdGenerator(r roleContext) error {
	if etcdstore.Kind() != etcdstore.Etcd {
		return nil
	}
	ca, err := loadOrCreateAuthority(etcdstore.CA, "kuberoot-etcd-ca")
	if err != nil {
		return err
	}
	ip := r.node.ip
	local := []net.IP{ip, net.ParseIP("127.0.0.1")}
	server := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	client := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	both := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	for _, s := range []certSpec{
		{file: etcdstore.Server, commonName: r.node.name, usages: server, dnsNames: []string{r.node.name, "localhost"}, ips: local},
		// Members authenticate each other with the same certificate both ways.
		{file: etcdstore.Peer, commonName: r.node.name, usages: both, dnsNames: []string{r.node.name}, ips: []net.IP{ip}},
		{file: etcdstore.APIServer, commonName: "kube-apiserver-etcd-client", usages: client},
		{file: etcdstore.Admin, commonName: "kuberoot:etcd-admin", usages: client},
	} {
		if err := ca.issue(s); err != nil {
			return fmt.Errorf("issue %s: %w", s.file, err)
		}
	}
	if err := os.MkdirAll(etcdstore.DataDir, 0o700); err != nil {
		return err
	}
	return restoreStore(r.node)
}

// restoreStore starts the store from a snapshot an installation left: a
// restored backup, or the store of a single member carried to its disk. The
// node comes back as the only member; other control planes join it again.
func restoreStore(node nodeInfo) error {
	if _, err := os.Stat(etcdstore.RestoreFile); err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(etcdstore.DataDir, "member")); err == nil {
		log.Printf("cluster store: a snapshot to restore, and a store already: keeping the store")
		return os.Remove(etcdstore.RestoreFile)
	}
	peer := etcdstore.PeerURL(node.ip.String())
	_ = os.RemoveAll(etcdstore.DataDir)
	out, err := exec.Command("/usr/bin/etcdutl", "snapshot", "restore", etcdstore.RestoreFile,
		"--data-dir", etcdstore.DataDir, "--name", node.name,
		"--initial-cluster", node.name+"="+peer, "--initial-advertise-peer-urls", peer).CombinedOutput()
	if err != nil {
		return fmt.Errorf("restore the cluster store: %v: %s", err, strings.TrimSpace(string(out)))
	}
	log.Printf("cluster store restored from a snapshot, as its only member")
	_ = os.Remove(etcdstore.InitFile)
	return os.Remove(etcdstore.RestoreFile)
}
