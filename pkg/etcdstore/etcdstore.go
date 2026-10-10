// Package etcdstore is the cluster store of a highly available control
// plane: etcd, one member on each control plane node, with an authority of
// its own. A single control plane keeps kine, which needs no peers.
package etcdstore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Kinds of cluster store.
const (
	Kine = "kine"
	Etcd = "etcd"
)

var (
	// PKIDir holds the store's credentials with the node's others.
	PKIDir = "/var/lib/kuberoot/pki"
	// KindFile records the store a cluster was created with: it never
	// changes under a running cluster.
	KindFile = "/var/lib/kuberoot/datastore"
	// InitFile holds how this member joins the store the first time:
	// "new" or "existing", and the initial cluster.
	InitFile = "/var/lib/kuberoot/etcd-init"
	// RestoreFile is a snapshot to start the store from, written by an
	// installation that restores a backup or carries a single member.
	RestoreFile = "/var/lib/kuberoot/etcd-restore.db"
	DataDir     = "/var/lib/etcd"
	// Endpoint is this node's member, for its API server and node API.
	Endpoint = "https://127.0.0.1:2379"
)

// Credential files, in PKIDir.
const (
	CA            = "etcd-ca"
	Server        = "etcd-server"
	Peer          = "etcd-peer"
	APIServer     = "apiserver-etcd-client"
	Admin         = "etcd-admin" // the node API's: members and snapshots
	peerPort      = "2380"
	clientPort    = "2379"
	stateNew      = "new"
	stateExisting = "existing"
)

func path(name string) string { return filepath.Join(PKIDir, name) }

// PeerURL is where the other members reach a member at addr.
func PeerURL(addr string) string { return "https://" + net.JoinHostPort(addr, peerPort) }

// ClientURL is where API servers reach a member at addr.
func ClientURL(addr string) string { return "https://" + net.JoinHostPort(addr, clientPort) }

// Kind is the store this node's cluster was created with, "" before one is.
func Kind() string {
	raw, err := os.ReadFile(KindFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// SetKind records the store of the cluster this node creates or joins.
func SetKind(kind string) error {
	if err := os.MkdirAll(filepath.Dir(KindFile), 0o700); err != nil {
		return err
	}
	return os.WriteFile(KindFile, []byte(kind+"\n"), 0o600)
}

// Init is how this member joins the store the first time it starts.
type Init struct {
	State          string // new or existing
	InitialCluster string
}

// WriteInit records how this member first joins the store: alone, as the
// first member, or into an existing store with the members it lists.
func WriteInit(i Init) error {
	if err := os.MkdirAll(filepath.Dir(InitFile), 0o700); err != nil {
		return err
	}
	return os.WriteFile(InitFile, []byte(i.State+"\n"+i.InitialCluster+"\n"), 0o600)
}

// ReadInit reads it back; a node that created the store alone has none
// written and gets its own name and address.
func ReadInit(name, addr string) Init {
	raw, err := os.ReadFile(InitFile)
	if err == nil {
		lines := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)
		if len(lines) == 2 && (lines[0] == stateNew || lines[0] == stateExisting) {
			return Init{State: lines[0], InitialCluster: lines[1]}
		}
	}
	return Init{State: stateNew, InitialCluster: name + "=" + PeerURL(addr)}
}

// Joining is the Init of a member that joins an existing store.
func Joining(initialCluster string) Init {
	return Init{State: stateExisting, InitialCluster: initialCluster}
}

// InitialCluster lists the store's members as --initial-cluster does, with
// the new member, which has no name yet, named name.
func InitialCluster(members []*etcdserverpb.Member, newID uint64, name string) string {
	var parts []string
	for _, m := range members {
		n := m.Name
		if m.ID == newID || n == "" {
			n = name
		}
		for _, u := range m.PeerURLs {
			parts = append(parts, n+"="+u)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Client reaches this node's member with the node API's credentials.
func Client(endpoints ...string) (*clientv3.Client, error) {
	if len(endpoints) == 0 {
		endpoints = []string{Endpoint}
	}
	cfg, err := tlsConfig(Admin)
	if err != nil {
		return nil, err
	}
	return clientv3.New(clientv3.Config{Endpoints: endpoints, TLS: cfg, DialTimeout: 5 * time.Second})
}

func tlsConfig(identity string) (*tls.Config, error) {
	pair, err := tls.LoadX509KeyPair(path(identity+".crt"), path(identity+".key"))
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(path(CA + ".crt"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%s holds no certificate", path(CA+".crt"))
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

// Running reports whether this node runs a member of the store.
func Running() bool {
	_, err := os.Stat(path(Admin + ".crt"))
	return err == nil && Kind() == Etcd
}

// Snapshot writes a consistent snapshot of the store to dst.
func Snapshot(ctx context.Context, dst string) error {
	c, err := Client()
	if err != nil {
		return err
	}
	defer c.Close()
	rc, err := c.Snapshot(ctx)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(dst+".part", dst)
}

// Members lists the store's members and the leader's ID.
func Members(ctx context.Context, c *clientv3.Client) ([]*etcdserverpb.Member, uint64, error) {
	resp, err := c.MemberList(ctx)
	if err != nil {
		return nil, 0, err
	}
	st, err := c.Status(ctx, c.Endpoints()[0])
	if err != nil {
		return resp.Members, 0, nil
	}
	return resp.Members, st.Leader, nil
}

// HostOf is the address in a member URL.
func HostOf(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// ErrNotEtcd: the cluster stores its state in kine, which has no members.
var ErrNotEtcd = errors.New("this cluster stores its state in kine, which has no other members: a highly available control plane needs a cluster created with kuberoot.datastore=etcd")
