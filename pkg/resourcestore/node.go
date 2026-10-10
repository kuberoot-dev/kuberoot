package resourcestore

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
	"k8s.io/klog/v2"
)

// Errors as etcd reports them.
var (
	errNoLeader        = errors.New("etcdserver: no leader")
	errTimeout         = errors.New("etcdserver: request timed out")
	errStopped         = errors.New("etcdserver: server stopped")
	errMemberNotFound  = errors.New("etcdserver: member not found")
	errPeerURLExists   = errors.New("etcdserver: Peer URLs already exists")
	errNotLearner      = errors.New("etcdserver: can only promote a learner member")
	errLearnerNotReady = errors.New("etcdserver: can only promote a learner member which is in sync with leader")
	errTooManyLearners = errors.New("etcdserver: too many learner members in cluster")
)

// Config configures a member, with etcd's flags' meanings.
type Config struct {
	// Name is this member's name in InitialCluster.
	Name    string
	DataDir string
	// InitialCluster maps member names to peer URLs; InitialClusterState is
	// "new" for a store started by these members, "existing" for a member
	// added to a running store (MemberAdd) that starts for the first time.
	InitialCluster      map[string][]string
	InitialClusterState string
	ClusterToken        string
	// PeerURLs are where the other members reach this one; ClientURLs are
	// published for clients.
	PeerURLs, ClientURLs []string
	// PeerClientTLS: how this member reaches its peers, nil for plain HTTP.
	PeerClientTLS *tls.Config
	// SnapshotCount: entries applied between snapshots of the state, after
	// which the log before them can go.
	SnapshotCount uint64
	// TickMs and ElectionTicks: heartbeat interval and election timeout in
	// heartbeats.
	TickMs        int
	ElectionTicks int
	// CatchUpEntries are kept in the log after a snapshot, for members a
	// little behind, so that they do not need the whole state.
	CatchUpEntries uint64
	// RequestTimeout bounds a write's wait for its entry to be applied.
	RequestTimeout time.Duration
}

func (c *Config) defaults() {
	if c.ClusterToken == "" {
		c.ClusterToken = "etcd-cluster"
	}
	if c.SnapshotCount == 0 {
		c.SnapshotCount = 10000
	}
	if c.TickMs == 0 {
		c.TickMs = 100
	}
	if c.ElectionTicks == 0 {
		c.ElectionTicks = 10
	}
	if c.CatchUpEntries == 0 {
		c.CatchUpEntries = 5000
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 7 * time.Second
	}
}

// Node is one member: Raft, its log, its peers and the state it applies.
type Node struct {
	cfg       Config
	id        uint64
	clusterID uint64
	store     *Store
	log       *raftLog
	storage   *raft.MemoryStorage
	raft      raft.Node
	transport *transport

	reqSeq atomic.Uint64
	waitMu sync.Mutex
	waits  map[uint64]chan applyResult

	lead, term, committed atomic.Uint64
	learner               atomic.Bool

	appliedMu  sync.Mutex
	applied    uint64
	appliedSig chan struct{}
	leaderSig  chan struct{}

	confState *raftpb.ConfState
	snapIndex uint64

	readMu     sync.RWMutex
	readNext   *readNotifier
	readWake   chan struct{}
	readStates chan raft.ReadState

	leases *leaseClock

	stopOnce sync.Once
	stopc    chan struct{}
	done     chan struct{}
	removed  atomic.Bool
	// Removed is closed when this member learns it was removed from the store.
	Removed chan struct{}
}

type readNotifier struct {
	c   chan struct{}
	err error
}

func newReadNotifier() *readNotifier { return &readNotifier{c: make(chan struct{})} }

func (r *readNotifier) notify(err error) { r.err = err; close(r.c) }

// memberID is etcd's: from the peer URLs and the cluster token, so that
// every member of a new store computes the same IDs.
func memberID(peerURLs []string, token string, now *time.Time) uint64 {
	urls := append([]string(nil), peerURLs...)
	sort.Strings(urls)
	b := []byte(strings.Join(urls, ""))
	b = append(b, []byte(token)...)
	if now != nil {
		b = append(b, []byte(fmt.Sprintf("%d", now.Unix()))...)
	}
	h := sha1.Sum(b)
	return binary.BigEndian.Uint64(h[:8])
}

func clusterIDOf(ids []uint64) uint64 {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	b := make([]byte, 0, 8*len(ids))
	for _, id := range ids {
		b = binary.BigEndian.AppendUint64(b, id)
	}
	h := sha1.Sum(b)
	return binary.BigEndian.Uint64(h[:8])
}

// StartNode opens a member's data and starts it: a member that has run
// before restarts from its log; a new one starts the store with the
// initial cluster or, "existing", joins the store it was added to.
func StartNode(cfg Config) (*Node, error) {
	cfg.defaults()
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	st, err := Open(filepath.Join(cfg.DataDir, "state.db"))
	if err != nil {
		return nil, err
	}
	rl, err := openRaftLog(filepath.Join(cfg.DataDir, "raft.db"))
	if err != nil {
		st.Close()
		return nil, err
	}
	n := &Node{cfg: cfg, store: st, log: rl, waits: map[uint64]chan applyResult{},
		appliedSig: make(chan struct{}), leaderSig: make(chan struct{}),
		readNext: newReadNotifier(), readWake: make(chan struct{}, 1), readStates: make(chan raft.ReadState, 64),
		stopc: make(chan struct{}), done: make(chan struct{}), Removed: make(chan struct{})}
	n.reqSeq.Store(uint64(time.Now().UnixNano()))
	n.transport = newTransport(n, cfg.PeerClientTLS)
	n.leases = newLeaseClock(n)
	st.onLease = n.leases.applied
	if err := n.start(); err != nil {
		rl.Close()
		st.Close()
		return nil, err
	}
	go n.run()
	go n.readLoop()
	go n.leases.run()
	go n.publishAttributes()
	return n, nil
}

func (n *Node) raftConfig(storage *raft.MemoryStorage) *raft.Config {
	return &raft.Config{
		ID: n.id, ElectionTick: n.cfg.ElectionTicks, HeartbeatTick: 1, Storage: storage,
		MaxSizePerMsg: 1 << 20, MaxInflightMsgs: 512, MaxUncommittedEntriesSize: 1 << 30,
		CheckQuorum: true, PreVote: true, ReadOnlyOption: raft.ReadOnlySafe,
		Logger: raftLogger{},
	}
}

func (n *Node) start() error {
	storage, empty, err := n.log.load()
	if err != nil {
		return err
	}
	n.storage = storage
	if !empty {
		n.id, n.clusterID = n.log.identity()
		snap, err := storage.Snapshot()
		if err != nil {
			return err
		}
		n.confState = confStateOf(snap)
		n.snapIndex = snap.GetMetadata().GetIndex()
		n.applied = n.store.Applied()
		n.syncMembers()
		klog.Infof("member %x restarts: snapshot at %d, applied %d", n.id, n.snapIndex, n.applied)
		n.raft = raft.RestartNode(n.raftConfig(storage))
		return nil
	}
	self, ok := n.cfg.InitialCluster[n.cfg.Name]
	if !ok {
		return fmt.Errorf("--initial-cluster has no member %q", n.cfg.Name)
	}
	switch n.cfg.InitialClusterState {
	case "existing":
		var others []string
		for name, urls := range n.cfg.InitialCluster {
			if name != n.cfg.Name {
				others = append(others, urls...)
			}
		}
		pm, err := fetchMembers(n.transport.client, others)
		if err != nil {
			return fmt.Errorf("join: %w", err)
		}
		for _, m := range pm.Members {
			if sameURLs(m.PeerURLs, self) {
				n.id = m.ID
			} else {
				n.transport.setPeer(m.ID, m.PeerURLs)
			}
		}
		if n.id == 0 {
			return fmt.Errorf("join: no member of the store has the peer URLs %v; add it first", self)
		}
		n.clusterID = pm.ClusterID
		if err := n.log.setIdentity(n.id, n.clusterID); err != nil {
			return err
		}
		klog.Infof("member %x joins store %x", n.id, n.clusterID)
		n.raft = raft.RestartNode(n.raftConfig(storage))
	default:
		var peers []raft.Peer
		var ids []uint64
		names := make([]string, 0, len(n.cfg.InitialCluster))
		for name := range n.cfg.InitialCluster {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			urls := n.cfg.InitialCluster[name]
			id := memberID(urls, n.cfg.ClusterToken, nil)
			ctx, _ := json.Marshal(Member{ID: id, PeerURLs: urls})
			peers = append(peers, raft.Peer{ID: id, Context: ctx})
			ids = append(ids, id)
			if name == n.cfg.Name {
				n.id = id
			} else {
				n.transport.setPeer(id, urls)
			}
		}
		n.clusterID = clusterIDOf(ids)
		if err := n.log.setIdentity(n.id, n.clusterID); err != nil {
			return err
		}
		klog.Infof("member %x starts store %x with %d members", n.id, n.clusterID, len(peers))
		n.raft = raft.StartNode(n.raftConfig(storage), peers)
	}
	return nil
}

func sameURLs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Stop stops the member; its data stays.
func (n *Node) Stop() {
	n.stopOnce.Do(func() {
		close(n.stopc)
		<-n.done
		n.transport.stop()
		n.raft.Stop()
		n.log.Close()
		n.store.Close()
	})
}

// ID is the member's ID; ClusterID the store's.
func (n *Node) ID() uint64        { return n.id }
func (n *Node) ClusterID() uint64 { return n.clusterID }
func (n *Node) Leader() uint64    { return n.lead.Load() }
func (n *Node) Term() uint64      { return n.term.Load() }
func (n *Node) Store() *Store     { return n.store }
func (n *Node) IsLearner() bool   { return n.learner.Load() }

// PeerHandler serves the other members.
func (n *Node) PeerHandler() http.Handler { return n.transport.handler() }

// stamp marks a request to a peer with who sends it, to whom, and in which
// store.
func (n *Node) stamp(req *http.Request, to uint64) {
	req.Header.Set(headerFrom, strconv.FormatUint(n.id, 16))
	req.Header.Set(headerTo, strconv.FormatUint(to, 16))
	req.Header.Set(headerCID, strconv.FormatUint(n.clusterID, 16))
}

// admit checks a peer's request: from this store, from a member that was
// not removed.
func (n *Node) admit(w http.ResponseWriter, r *http.Request) bool {
	cid, _ := strconv.ParseUint(r.Header.Get(headerCID), 16, 64)
	if cid != n.clusterID {
		http.Error(w, fmt.Sprintf("cluster %x, not %x", n.clusterID, cid), http.StatusPreconditionFailed)
		return false
	}
	from, _ := strconv.ParseUint(r.Header.Get(headerFrom), 16, 64)
	if n.store.Removed(from) {
		http.Error(w, errRemoved.Error(), http.StatusGone)
		return false
	}
	return true
}

func (n *Node) snapshotFile(index uint64) string {
	return filepath.Join(n.cfg.DataDir, fmt.Sprintf("snapshot-%016x.db", index))
}

// syncMembers takes the peers and this member's learner flag from the
// state, as a restart or a snapshot finds them.
func (n *Node) syncMembers() {
	for _, m := range n.store.Members() {
		if m.ID == n.id {
			n.learner.Store(m.IsLearner)
			continue
		}
		n.transport.setPeer(m.ID, m.PeerURLs)
	}
}

// removedSelf stops a member that was removed from the store.
func (n *Node) removedSelf() {
	if n.removed.CompareAndSwap(false, true) {
		klog.Warningf("member %x: %v", n.id, errRemoved)
		close(n.Removed)
	}
}

// run is the member's loop: it ticks Raft, persists and sends what Raft
// is ready with, and applies committed entries.
func (n *Node) run() {
	defer close(n.done)
	ticker := time.NewTicker(time.Duration(n.cfg.TickMs) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-n.stopc:
			return
		case <-ticker.C:
			n.raft.Tick()
		case rd := <-n.raft.Ready():
			if err := n.ready(rd); err != nil {
				klog.Fatalf("member %x: %v", n.id, err)
			}
			n.raft.Advance()
		}
	}
}

func (n *Node) ready(rd raft.Ready) error {
	if rd.SoftState != nil {
		n.setLeader(rd.SoftState.Lead, rd.SoftState.RaftState == raft.StateLeader)
	}
	if !raft.IsEmptyHardState(rd.HardState) {
		n.term.Store(rd.HardState.GetTerm())
		n.committed.Store(rd.HardState.GetCommit())
	}
	for _, rs := range rd.ReadStates {
		select {
		case n.readStates <- rs:
		default:
		}
	}
	if !raft.IsEmptySnap(rd.Snapshot) {
		if err := n.receiveSnapshot(rd.Snapshot); err != nil {
			return err
		}
	}
	if err := n.log.save(rd.HardState, rd.Entries, rd.Snapshot); err != nil {
		return err
	}
	if !raft.IsEmptySnap(rd.Snapshot) {
		if err := n.storage.ApplySnapshot(rd.Snapshot); err != nil {
			return err
		}
	}
	if err := n.storage.Append(rd.Entries); err != nil {
		return err
	}
	n.transport.send(rd.Messages)
	if err := n.apply(rd.CommittedEntries); err != nil {
		return err
	}
	return n.maybeSnapshot()
}

func (n *Node) setLeader(lead uint64, leader bool) {
	old := n.lead.Swap(lead)
	if old == lead {
		return
	}
	klog.Infof("member %x: leader is %x", n.id, lead)
	n.leases.leadership(leader)
	if lead != 0 {
		n.appliedMu.Lock()
		close(n.leaderSig)
		n.leaderSig = make(chan struct{})
		n.appliedMu.Unlock()
	}
}

// receiveSnapshot takes the state file that came with a snapshot.
func (n *Node) receiveSnapshot(snap *raftpb.Snapshot) error {
	index := snap.GetMetadata().GetIndex()
	file := n.snapshotFile(index)
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("snapshot at %d: no state file: %w", index, err)
	}
	if err := n.store.Replace(file); err != nil {
		return fmt.Errorf("snapshot at %d: %w", index, err)
	}
	n.confState = confStateOf(snap)
	n.snapIndex = index
	n.syncMembers()
	n.setApplied(n.store.Applied())
	n.leases.reload()
	klog.Infof("member %x: state from a snapshot at %d, applied %d", n.id, index, n.Applied())
	// Older snapshot files are of no use any more.
	old, _ := filepath.Glob(filepath.Join(n.cfg.DataDir, "snapshot-*.db"))
	for _, f := range old {
		_ = os.Remove(f)
	}
	return nil
}

// apply applies committed entries: requests in batches, one write of the
// state each, and membership changes one by one, as Raft needs them.
func (n *Node) apply(ents []*raftpb.Entry) error {
	if len(ents) == 0 {
		return nil
	}
	var batch []logEntry
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		results, err := n.store.Apply(batch)
		if err != nil {
			return err
		}
		for i, e := range batch {
			if e.Req != nil && e.Req.From == n.id {
				n.answer(e.Req.ID, results[i])
			}
		}
		n.setApplied(batch[len(batch)-1].Index)
		batch = batch[:0]
		return nil
	}
	for _, ent := range ents {
		e, err := decodeEntry(ent)
		if err != nil {
			return err
		}
		if e.Conf == nil {
			batch = append(batch, e)
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		if err := n.applyConf(e); err != nil {
			return err
		}
	}
	return flush()
}

func (n *Node) applyConf(e logEntry) error {
	cc := proto.Clone(e.Conf).(*raftpb.ConfChange)
	results, err := n.store.Apply([]logEntry{e})
	if err != nil {
		return err
	}
	res := results[0]
	if res.Err != nil {
		// An ID that was removed does not come back; Raft is told nothing.
		cc.NodeId = proto.Uint64(raft.None)
	}
	n.confState = n.raft.ApplyConfChange(cc)
	id, typ := cc.GetNodeId(), cc.GetType()
	switch typ {
	case raftpb.ConfChangeAddNode, raftpb.ConfChangeAddLearnerNode, raftpb.ConfChangeUpdateNode:
		if id == n.id && typ != raftpb.ConfChangeUpdateNode {
			n.learner.Store(typ == raftpb.ConfChangeAddLearnerNode)
		}
		if id != raft.None && len(e.Member.PeerURLs) > 0 {
			n.transport.setPeer(id, e.Member.PeerURLs)
		}
	case raftpb.ConfChangeRemoveNode:
		if id == n.id {
			n.removedSelf()
		} else {
			n.transport.removePeer(id)
		}
	}
	klog.Infof("member %x: %s %x applied at %d", n.id, typ, id, e.Index)
	n.answer(cc.GetId(), res)
	n.setApplied(e.Index)
	return n.snapshot(e.Index, false)
}

func (n *Node) setApplied(i uint64) {
	n.appliedMu.Lock()
	defer n.appliedMu.Unlock()
	if i <= n.applied {
		return
	}
	n.applied = i
	close(n.appliedSig)
	n.appliedSig = make(chan struct{})
}

func (n *Node) waitApplied(ctx context.Context, i uint64) error {
	for {
		n.appliedMu.Lock()
		done, sig := n.applied >= i, n.appliedSig
		n.appliedMu.Unlock()
		if done {
			return nil
		}
		select {
		case <-sig:
		case <-ctx.Done():
			return errTimeout
		case <-n.stopc:
			return errStopped
		}
	}
}

// waitLeader waits until the member knows a leader.
func (n *Node) waitLeader(ctx context.Context) error {
	for {
		if n.lead.Load() != 0 {
			return nil
		}
		n.appliedMu.Lock()
		sig := n.leaderSig
		n.appliedMu.Unlock()
		if n.lead.Load() != 0 {
			return nil
		}
		select {
		case <-sig:
		case <-ctx.Done():
			return errNoLeader
		case <-n.stopc:
			return errStopped
		}
	}
}

// maybeSnapshot takes a snapshot of the state every SnapshotCount applied
// entries and lets the log before it go, keeping some for slow members.
func (n *Node) maybeSnapshot() error {
	applied := n.Applied()
	if applied <= n.snapIndex || applied-n.snapIndex < n.cfg.SnapshotCount {
		return nil
	}
	return n.snapshot(applied, true)
}

// snapshot records a snapshot at index; compact lets the log before it go.
// A snapshot is also taken at every membership change, without compacting:
// a member added later is sent the newest snapshot, and Raft refuses one
// whose members do not include it.
func (n *Node) snapshot(index uint64, compact bool) error {
	snap, err := n.storage.CreateSnapshot(index, n.confState, nil)
	if err == raft.ErrSnapOutOfDate {
		return nil
	}
	if err != nil {
		return err
	}
	compactTo := uint64(1)
	if compact && index > n.cfg.CatchUpEntries {
		compactTo = index - n.cfg.CatchUpEntries
	}
	if err := n.log.saveSnapshot(snap, compactTo); err != nil {
		return err
	}
	if compactTo > 1 {
		if err := n.storage.Compact(compactTo); err != nil && err != raft.ErrCompacted {
			return err
		}
	}
	n.snapIndex = index
	klog.V(2).Infof("member %x: snapshot at %d, log from %d", n.id, index, compactTo)
	return nil
}

func (n *Node) nextID() uint64 { return n.reqSeq.Add(1) }

func (n *Node) register(id uint64) chan applyResult {
	ch := make(chan applyResult, 1)
	n.waitMu.Lock()
	n.waits[id] = ch
	n.waitMu.Unlock()
	return ch
}

func (n *Node) forget(id uint64) {
	n.waitMu.Lock()
	delete(n.waits, id)
	n.waitMu.Unlock()
}

func (n *Node) answer(id uint64, r applyResult) {
	n.waitMu.Lock()
	ch, ok := n.waits[id]
	delete(n.waits, id)
	n.waitMu.Unlock()
	if ok {
		ch <- r
	}
}

// propose replicates a request and waits until this member applied it.
func (n *Node) propose(ctx context.Context, r *request) (applyResult, error) {
	if n.removed.Load() {
		return applyResult{}, errRemoved
	}
	ctx, cancel := context.WithTimeout(ctx, n.cfg.RequestTimeout)
	defer cancel()
	if err := n.waitLeader(ctx); err != nil {
		return applyResult{}, err
	}
	r.From, r.ID = n.id, n.nextID()
	data, err := json.Marshal(r)
	if err != nil {
		return applyResult{}, err
	}
	ch := n.register(r.ID)
	defer n.forget(r.ID)
	for {
		err = n.raft.Propose(ctx, data)
		if !errors.Is(err, raft.ErrProposalDropped) {
			break
		}
		// Dropped, not appended: no leader for the moment, or a leader
		// handing over. Proposing again cannot apply it twice.
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return applyResult{}, errTimeout
		}
		if err := n.waitLeader(ctx); err != nil {
			return applyResult{}, err
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return applyResult{}, errTimeout
		}
		return applyResult{}, err
	}
	select {
	case res := <-ch:
		return res, res.Err
	case <-ctx.Done():
		return applyResult{}, errTimeout
	case <-n.stopc:
		return applyResult{}, errStopped
	}
}

// proposeConf proposes a membership change and waits until it is applied.
func (n *Node) proposeConf(ctx context.Context, typ raftpb.ConfChangeType, id uint64, m *Member) ([]Member, error) {
	ctx, cancel := context.WithTimeout(ctx, n.cfg.RequestTimeout)
	defer cancel()
	if err := n.waitLeader(ctx); err != nil {
		return nil, err
	}
	cc := &raftpb.ConfChange{Type: typ.Enum(), NodeId: proto.Uint64(id), Id: proto.Uint64(n.nextID())}
	if m != nil {
		cc.Context, _ = json.Marshal(m)
	}
	ch := n.register(cc.GetId())
	defer n.forget(cc.GetId())
	if err := n.raft.ProposeConfChange(ctx, cc); err != nil {
		return nil, err
	}
	select {
	case res := <-ch:
		return res.Members, res.Err
	case <-ctx.Done():
		return nil, errTimeout
	case <-n.stopc:
		return nil, errStopped
	}
}

// readLoop serves linearizable reads: waiting reads share one read index
// from the leader, and go once this member has applied up to it.
func (n *Node) readLoop() {
	for {
		select {
		case <-n.stopc:
			return
		case <-n.readWake:
		}
		n.readMu.Lock()
		nr := n.readNext
		n.readNext = newReadNotifier()
		n.readMu.Unlock()
		nr.notify(n.readIndex())
	}
}

func (n *Node) readIndex() error {
	ctx, cancel := context.WithTimeout(context.Background(), n.cfg.RequestTimeout)
	defer cancel()
	id := n.nextID()
	rctx := binary.BigEndian.AppendUint64(nil, id)
	for {
		if err := n.waitLeader(ctx); err != nil {
			return err
		}
		if err := n.raft.ReadIndex(ctx, rctx); err != nil {
			return errTimeout
		}
		retry := time.NewTimer(500 * time.Millisecond)
	wait:
		for {
			select {
			case rs := <-n.readStates:
				if string(rs.RequestCtx) != string(rctx) {
					continue // an earlier read's, given up on
				}
				retry.Stop()
				return n.waitApplied(ctx, rs.Index)
			case <-retry.C:
				// The leader may have dropped it: during an election, or before
				// it committed an entry of its term. Ask again.
				break wait
			case <-ctx.Done():
				retry.Stop()
				return errTimeout
			case <-n.stopc:
				retry.Stop()
				return errStopped
			}
		}
	}
}

// linearizableRead waits until this member's state holds every write
// committed before the call: a read then sees them, on any member.
func (n *Node) linearizableRead(ctx context.Context) error {
	n.readMu.RLock()
	nr := n.readNext
	n.readMu.RUnlock()
	select {
	case n.readWake <- struct{}{}:
	default:
	}
	select {
	case <-nr.c:
		return nr.err
	case <-ctx.Done():
		return ctx.Err()
	case <-n.stopc:
		return errStopped
	}
}

// publishAttributes records this member's name and client URLs in the
// store once it runs, as etcd members do.
func (n *Node) publishAttributes() {
	for {
		for _, m := range n.store.Members() {
			if m.ID == n.id && m.Name == n.cfg.Name && sameURLs(m.ClientURLs, n.cfg.ClientURLs) {
				return
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), n.cfg.RequestTimeout)
		_, err := n.propose(ctx, &request{Attributes: &Member{ID: n.id, Name: n.cfg.Name, ClientURLs: n.cfg.ClientURLs}})
		cancel()
		if err == nil {
			continue
		}
		select {
		case <-n.stopc:
			return
		case <-time.After(time.Second):
		}
	}
}

// AddMember adds a member, a learner or a voter, that is not running yet.
func (n *Node) AddMember(ctx context.Context, peerURLs []string, learner bool) (Member, []Member, error) {
	for _, m := range n.store.Members() {
		for _, u := range m.PeerURLs {
			for _, p := range peerURLs {
				if u == p {
					return Member{}, nil, errPeerURLExists
				}
			}
		}
		if learner && m.IsLearner {
			return Member{}, nil, errTooManyLearners
		}
	}
	now := time.Now()
	m := Member{ID: memberID(peerURLs, n.cfg.ClusterToken, &now), PeerURLs: peerURLs, IsLearner: learner}
	t := raftpb.ConfChangeAddNode
	if learner {
		t = raftpb.ConfChangeAddLearnerNode
	}
	members, err := n.proposeConf(ctx, t, m.ID, &m)
	return m, members, err
}

// RemoveMember removes a member.
func (n *Node) RemoveMember(ctx context.Context, id uint64) ([]Member, error) {
	if !n.isMember(id) {
		return nil, errMemberNotFound
	}
	return n.proposeConf(ctx, raftpb.ConfChangeRemoveNode, id, nil)
}

// UpdateMember changes a member's peer URLs.
func (n *Node) UpdateMember(ctx context.Context, id uint64, peerURLs []string) ([]Member, error) {
	if !n.isMember(id) {
		return nil, errMemberNotFound
	}
	return n.proposeConf(ctx, raftpb.ConfChangeUpdateNode, id, &Member{ID: id, PeerURLs: peerURLs})
}

func (n *Node) isMember(id uint64) bool {
	for _, m := range n.store.Members() {
		if m.ID == id {
			return true
		}
	}
	return false
}

// PromoteMember gives a learner its vote. Only the leader knows whether
// the learner has caught up, so another member asks it.
func (n *Node) PromoteMember(ctx context.Context, id uint64) ([]Member, error) {
	ctx, cancel := context.WithTimeout(ctx, n.cfg.RequestTimeout)
	defer cancel()
	if err := n.waitLeader(ctx); err != nil {
		return nil, err
	}
	if lead := n.lead.Load(); lead != n.id {
		var members []Member
		if err := n.transport.call(ctx, lead, pathPromote+"?id="+strconv.FormatUint(id, 16), &members); err != nil {
			return nil, errors.New(err.Error())
		}
		return members, nil
	}
	return n.promote(ctx, id)
}

func (n *Node) promote(ctx context.Context, id uint64) ([]Member, error) {
	var m *Member
	for _, x := range n.store.Members() {
		if x.ID == id {
			x := x
			m = &x
		}
	}
	if m == nil {
		return nil, errMemberNotFound
	}
	if !m.IsLearner {
		return nil, errNotLearner
	}
	st := n.raft.Status()
	if st.RaftState != raft.StateLeader {
		return nil, errNoLeader
	}
	// As etcd: the learner's log is within a tenth of the leader's.
	pr, ok := st.Progress[id]
	if !ok || float64(pr.Match) < float64(st.Progress[n.id].Match)*0.9 {
		return nil, errLearnerNotReady
	}
	return n.proposeConf(ctx, raftpb.ConfChangeAddNode, id, &Member{ID: id, PeerURLs: m.PeerURLs})
}

// TransferLeadership hands the leadership to another voter.
func (n *Node) TransferLeadership(ctx context.Context, to uint64) error {
	lead := n.lead.Load()
	if lead != n.id {
		return errors.New("etcdserver: not leader")
	}
	n.raft.TransferLeadership(ctx, lead, to)
	for n.lead.Load() != to {
		select {
		case <-ctx.Done():
			return errTimeout
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil
}

// Status is Raft's status.
func (n *Node) Status() raft.Status { return n.raft.Status() }

// Applied is the index this member has applied up to.
func (n *Node) Applied() uint64 {
	n.appliedMu.Lock()
	defer n.appliedMu.Unlock()
	return n.applied
}

func confStateOf(snap *raftpb.Snapshot) *raftpb.ConfState {
	if cs := snap.GetMetadata().GetConfState(); cs != nil {
		return cs
	}
	return &raftpb.ConfState{}
}

// raftLogger sends Raft's log to klog.
type raftLogger struct{}

func (raftLogger) Debug(v ...any)                 { klog.V(4).Info(v...) }
func (raftLogger) Debugf(format string, v ...any) { klog.V(4).Infof(format, v...) }
func (raftLogger) Error(v ...any)                 { klog.Error(v...) }
func (raftLogger) Errorf(format string, v ...any) { klog.Errorf(format, v...) }
func (raftLogger) Info(v ...any)                  { klog.V(1).Info(v...) }
func (raftLogger) Infof(format string, v ...any)  { klog.V(1).Infof(format, v...) }
func (raftLogger) Warning(v ...any)               { klog.Warning(v...) }
func (raftLogger) Warningf(format string, v ...any) {
	klog.Warningf(format, v...)
}
func (raftLogger) Fatal(v ...any)                 { klog.Fatal(v...) }
func (raftLogger) Fatalf(format string, v ...any) { klog.Fatalf(format, v...) }
func (raftLogger) Panic(v ...any)                 { panic(fmt.Sprint(v...)) }
func (raftLogger) Panicf(format string, v ...any) { panic(fmt.Sprintf(format, v...)) }
