package resourcestore

import (
	"context"
	"fmt"
	"go.etcd.io/etcd/client/v3/snapshot"
	"go.uber.org/zap"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
)

// member is a store member in a test: its own data directory, peer and
// client ports, which it keeps across a restart.
type member struct {
	t          *testing.T
	name, dir  string
	peerURL    string
	clientAddr string
	tune       func(*Config)
	node       *Node
	grpc       *grpc.Server
	http       *http.Server
	client     *clientv3.Client
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func newMember(t *testing.T, name string, tune func(*Config)) *member {
	return &member{t: t, name: name, dir: filepath.Join(t.TempDir(), name), tune: tune,
		peerURL: "http://" + freeAddr(t), clientAddr: freeAddr(t)}
}

// newCluster starts a new store of n members.
func newCluster(t *testing.T, n int, tune func(*Config)) []*member {
	t.Helper()
	var ms []*member
	initial := map[string][]string{}
	for i := range n {
		m := newMember(t, fmt.Sprintf("m%d", i), tune)
		ms = append(ms, m)
		initial[m.name] = []string{m.peerURL}
	}
	for _, m := range ms {
		m.start(initial, "new")
	}
	waitLeader(t, ms)
	return ms
}

func (m *member) start(initial map[string][]string, state string) {
	m.t.Helper()
	peerL, err := net.Listen("tcp", strings.TrimPrefix(m.peerURL, "http://"))
	if err != nil {
		m.t.Fatal(err)
	}
	cfg := Config{Name: m.name, DataDir: m.dir, InitialCluster: initial, InitialClusterState: state,
		PeerURLs: []string{m.peerURL}, ClientURLs: []string{"http://" + m.clientAddr},
		TickMs: 20, ElectionTicks: 10, RequestTimeout: 5 * time.Second}
	if m.tune != nil {
		m.tune(&cfg)
	}
	node, err := StartNode(cfg)
	if err != nil {
		peerL.Close()
		m.t.Fatal(err)
	}
	m.node = node
	m.http = &http.Server{Handler: node.PeerHandler()}
	go func() { _ = m.http.Serve(peerL) }()
	cl, err := net.Listen("tcp", m.clientAddr)
	if err != nil {
		m.t.Fatal(err)
	}
	m.grpc = grpc.NewServer()
	(&Server{Node: node}).Register(m.grpc)
	go func() { _ = m.grpc.Serve(cl) }()
	m.client, err = clientv3.New(clientv3.Config{Endpoints: []string{m.clientAddr}, DialTimeout: 3 * time.Second})
	if err != nil {
		m.t.Fatal(err)
	}
	m.t.Cleanup(m.stop)
}

func (m *member) stop() {
	if m.node == nil {
		return
	}
	m.client.Close()
	m.grpc.Stop()
	_ = m.http.Close()
	m.node.Stop()
	m.node = nil
}

func (m *member) restart() {
	m.t.Helper()
	m.start(nil, "")
}

// waitLeader waits until the running members agree on a leader, and
// returns it.
func waitLeader(t *testing.T, ms []*member) *member {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var lead uint64
		agree := true
		for _, m := range ms {
			if m.node == nil {
				continue
			}
			l := m.node.Leader()
			if l == 0 || (lead != 0 && l != lead) {
				agree = false
				break
			}
			lead = l
		}
		if agree && lead != 0 {
			for _, m := range ms {
				if m.node != nil && m.node.ID() == lead {
					return m
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no leader")
	return nil
}

func followerOf(ms []*member, lead *member) *member {
	for _, m := range ms {
		if m != lead && m.node != nil {
			return m
		}
	}
	return nil
}

// eventually retries fn until it holds, for what a member does in its own
// time.
func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

func localValue(m *member, key string) string {
	r, err := m.client.Get(context.Background(), key, clientv3.WithSerializable())
	if err != nil || len(r.Kvs) == 0 {
		return ""
	}
	return string(r.Kvs[0].Value)
}

func TestWritesReplicateAndReadsAreLinearizableOnFollowers(t *testing.T) {
	ms := newCluster(t, 3, nil)
	lead := waitLeader(t, ms)
	f := followerOf(ms, lead)
	ctx := context.Background()
	for i := range 20 {
		key := fmt.Sprintf("/registry/configmaps/default/c%d", i)
		// Write on one follower, read on the leader and the other at once:
		// a linearizable read waits for the write.
		p, err := f.client.Put(ctx, key, "v")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			g, err := m.client.Get(ctx, key)
			if err != nil || len(g.Kvs) != 1 || g.Kvs[0].ModRevision != p.Header.Revision {
				t.Fatalf("read on %s after a write on %s: %v %v", m.name, f.name, g, err)
			}
		}
	}
	// Every member holds the same revisions.
	eventually(t, "the same revision on every member", func() bool {
		r0, _ := ms[0].node.store.Revisions()
		for _, m := range ms[1:] {
			if r, _ := m.node.store.Revisions(); r != r0 {
				return false
			}
		}
		return true
	})
}

func TestLeaderLossAndRestart(t *testing.T) {
	ms := newCluster(t, 3, nil)
	ctx := context.Background()
	lead := waitLeader(t, ms)
	if _, err := lead.client.Put(ctx, "/a", "1"); err != nil {
		t.Fatal(err)
	}
	lead.stop()
	rest := []*member{}
	for _, m := range ms {
		if m != lead {
			rest = append(rest, m)
		}
	}
	next := waitLeader(t, rest)
	if next == lead {
		t.Fatal("the stopped member leads")
	}
	f := followerOf(rest, next)
	if _, err := f.client.Put(ctx, "/a", "2"); err != nil {
		t.Fatalf("write without the old leader: %v", err)
	}
	// The old leader comes back from its log and catches up.
	lead.restart()
	eventually(t, "the restarted member catches up", func() bool { return localValue(lead, "/a") == "2" })
	g, err := lead.client.Get(ctx, "/a")
	if err != nil || g.Kvs[0].Version != 2 {
		t.Fatalf("after the restart: %v %v", g, err)
	}
}

func TestLearnerJoinsFromASnapshotAndIsPromoted(t *testing.T) {
	small := func(c *Config) { c.SnapshotCount, c.CatchUpEntries = 50, 10 }
	ms := newCluster(t, 3, small)
	ctx := context.Background()
	for i := range 300 {
		if _, err := ms[i%3].client.Put(ctx, fmt.Sprintf("/registry/pods/ns/p%03d", i), "x"); err != nil {
			t.Fatal(err)
		}
	}
	// The log before the snapshot is gone: the new member needs the state.
	if first, _ := ms[0].node.storage.FirstIndex(); first < 100 {
		t.Fatalf("log not compacted: first index %d", first)
	}
	nm := newMember(t, "m3", small)
	add, err := ms[1].client.MemberAddAsLearner(ctx, []string{nm.peerURL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ms[1].client.MemberPromote(ctx, add.Member.ID); err == nil {
		t.Fatal("a learner that has not started was promoted")
	}
	initial := map[string][]string{nm.name: {nm.peerURL}}
	for _, m := range ms {
		initial[m.name] = []string{m.peerURL}
	}
	nm.start(initial, "existing")
	eventually(t, "the learner holds the state", func() bool { return localValue(nm, "/registry/pods/ns/p299") == "x" })
	if !nm.node.IsLearner() {
		t.Fatal("the new member is not a learner")
	}
	// Promoted through a follower, which asks the leader.
	lead := waitLeader(t, ms)
	f := followerOf(ms, lead)
	eventually(t, "promotion", func() bool {
		_, err := f.client.MemberPromote(ctx, add.Member.ID)
		return err == nil
	})
	eventually(t, "the member votes", func() bool { return !nm.node.IsLearner() })
	l, err := ms[0].client.MemberList(ctx)
	if err != nil || len(l.Members) != 4 {
		t.Fatalf("members: %v %v", l, err)
	}
	for _, m := range l.Members {
		if m.IsLearner || m.Name == "" {
			t.Fatalf("member %+v", m)
		}
	}
	// Four voters, a majority of three: one can go, and the store writes on.
	ms[0].stop()
	all := append(ms[1:], nm)
	waitLeader(t, all)
	if _, err := nm.client.Put(ctx, "/after", "y"); err != nil {
		t.Fatal(err)
	}
}

func TestRemovedMemberStops(t *testing.T) {
	ms := newCluster(t, 3, nil)
	ctx := context.Background()
	lead := waitLeader(t, ms)
	gone := followerOf(ms, lead)
	goneID := gone.node.ID()
	if _, err := lead.client.MemberRemove(ctx, goneID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gone.node.Removed:
	case <-time.After(10 * time.Second):
		t.Fatal("the removed member did not learn it")
	}
	gone.stop()
	var rest []*member
	for _, m := range ms {
		if m != gone {
			rest = append(rest, m)
		}
	}
	l, err := lead.client.MemberList(ctx)
	if err != nil || len(l.Members) != 2 {
		t.Fatalf("members: %v %v", l, err)
	}
	// Two of two: both must answer, and they do.
	if _, err := followerOf(rest, lead).client.Put(ctx, "/x", "1"); err != nil {
		t.Fatal(err)
	}
	// The ID does not come back.
	if _, err := lead.client.MemberRemove(ctx, goneID); err == nil {
		t.Fatal("removed twice")
	}
}

func TestRestartKeepsStateAndRevisions(t *testing.T) {
	ms := newCluster(t, 1, func(c *Config) { c.SnapshotCount, c.CatchUpEntries = 20, 5 })
	m := ms[0]
	ctx := context.Background()
	var last int64
	for i := range 50 {
		p, err := m.client.Put(ctx, fmt.Sprintf("/k%02d", i), "v")
		if err != nil {
			t.Fatal(err)
		}
		last = p.Header.Revision
	}
	m.stop()
	m.restart()
	waitLeader(t, ms)
	g, err := m.client.Get(ctx, "/k", clientv3.WithPrefix())
	if err != nil || len(g.Kvs) != 50 || g.Header.Revision != last {
		t.Fatalf("after a restart: %d keys at %d (want 50 at %d), %v", len(g.Kvs), g.Header.Revision, last, err)
	}
	p, err := m.client.Put(ctx, "/k00", "w")
	if err != nil || p.Header.Revision != last+1 {
		t.Fatalf("next write at %d, want %d: %v", p.Header.Revision, last+1, err)
	}
}

// A snapshot from the Maintenance API restores into a store of its own,
// as an installation carries a control plane's store to its disk.
func TestRestoreFromASnapshot(t *testing.T) {
	ms := newCluster(t, 3, nil)
	ctx := context.Background()
	var last int64
	for i := range 20 {
		p, err := ms[i%3].client.Put(ctx, fmt.Sprintf("/k%02d", i), "v")
		if err != nil {
			t.Fatal(err)
		}
		last = p.Header.Revision
	}
	rc, err := ms[1].client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "snapshot.db")
	if err := writeFile(file, rc); err != nil {
		t.Fatal(err)
	}
	rc.Close()
	m := newMember(t, "restored", nil)
	if err := Restore(file, m.dir, m.name, map[string][]string{m.name: {m.peerURL}}, ""); err != nil {
		t.Fatal(err)
	}
	m.start(nil, "")
	waitLeader(t, []*member{m})
	g, err := m.client.Get(ctx, "/k", clientv3.WithPrefix())
	if err != nil || len(g.Kvs) != 20 || g.Header.Revision != last {
		t.Fatalf("restored: %d keys at %d, want 20 at %d: %v", len(g.Kvs), g.Header.Revision, last, err)
	}
	p, err := m.client.Put(ctx, "/k00", "w")
	if err != nil || p.Header.Revision != last+1 {
		t.Fatalf("a write after the restore: %v %v", p, err)
	}
	l, err := m.client.MemberList(ctx)
	if err != nil || len(l.Members) != 1 || l.Members[0].ID != m.node.ID() {
		t.Fatalf("members after the restore: %v %v", l, err)
	}
	// A member that joins the restored store gets what was restored, not
	// only what was written since: the log of a restored store starts with
	// a snapshot of its state.
	j := newMember(t, "joined", nil)
	add, err := m.client.MemberAddAsLearner(ctx, []string{j.peerURL})
	if err != nil {
		t.Fatal(err)
	}
	j.start(map[string][]string{m.name: {m.peerURL}, j.name: {j.peerURL}}, "existing")
	eventually(t, "the joined member holds the restored keys", func() bool { return localValue(j, "/k19") == "v" })
	eventually(t, "promotion", func() bool {
		_, err := m.client.MemberPromote(ctx, add.Member.ID)
		return err == nil
	})
	g, err = j.client.Get(ctx, "/k", clientv3.WithPrefix())
	if err != nil || len(g.Kvs) != 20 || g.Header.Revision != last+1 {
		t.Fatalf("on the joined member: %d keys at %d, want 20 at %d: %v", len(g.Kvs), g.Header.Revision, last+1, err)
	}
}

func TestLeasesAreReplicated(t *testing.T) {
	ms := newCluster(t, 3, nil)
	ctx := context.Background()
	lead := waitLeader(t, ms)
	f := followerOf(ms, lead)
	l, err := f.client.Grant(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.Put(ctx, "/registry/events/ns/e", "x", clientv3.WithLease(l.ID)); err != nil {
		t.Fatal(err)
	}
	// Every member holds the lease and its key.
	for _, m := range ms {
		eventually(t, "the lease on "+m.name, func() bool {
			_, keys, ok := m.node.store.LeaseOf(int64(l.ID))
			return ok && len(keys) == 1
		})
	}
	// A keep-alive on a follower reaches the leader's clock.
	ka, err := f.client.KeepAliveOnce(ctx, l.ID)
	if err != nil || ka.TTL != 3 {
		t.Fatalf("keep-alive: %v %v", ka, err)
	}
	// The leader goes; the lease does not, and the new leader expires it.
	lead.stop()
	var rest []*member
	for _, m := range ms {
		if m != lead {
			rest = append(rest, m)
		}
	}
	waitLeader(t, rest)
	if localValue(f, "/registry/events/ns/e") != "x" {
		t.Fatal("the leased key went with the leader")
	}
	eventually(t, "the lease expires", func() bool {
		g, err := f.client.Get(ctx, "/registry/events/ns/e")
		return err == nil && len(g.Kvs) == 0
	})
	ttl, err := f.client.TimeToLive(ctx, l.ID)
	if err != nil || ttl.TTL != -1 {
		t.Fatalf("time to live of an expired lease: %v %v", ttl, err)
	}
}

// etcdctl snapshot save takes the store's snapshot, which ends with the
// state's checksum as etcd's does; the restore checks it and refuses a
// damaged copy.
func TestEtcdctlSnapshotRestores(t *testing.T) {
	ms := newCluster(t, 1, nil)
	ctx := context.Background()
	if _, err := ms[0].client.Put(ctx, "/k", "v"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := snapshot.SaveWithVersion(ctx, zap.NewNop(), clientv3.Config{Endpoints: []string{ms[0].clientAddr}}, file); err != nil {
		t.Fatalf("etcdctl's snapshot save: %v", err)
	}
	m := newMember(t, "restored", nil)
	if err := Restore(file, m.dir, m.name, map[string][]string{m.name: {m.peerURL}}, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	raw[100] ^= 0xff
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	d := newMember(t, "damaged", nil)
	if err := Restore(file, d.dir, d.name, map[string][]string{d.name: {d.peerURL}}, ""); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a damaged snapshot: %v", err)
	}
}
