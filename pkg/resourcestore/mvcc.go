// Package resourcestore is a lab store in the model of Kubernetes
// resources: an ordered, multi-version key-value store that speaks the part
// of the etcd v3 API kube-apiserver uses, replicated by Raft
// (go.etcd.io/raft). See docs/resource-store.md. Not a store to run a
// cluster on.
package resourcestore

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"

	bolt "go.etcd.io/bbolt"
)

// Buckets of the state file.
var (
	// versions: key 0x00 big-endian revision -> version, ordered by key then revision.
	versionsBucket = []byte("versions")
	// latest: key -> revision of its newest version.
	latestBucket = []byte("latest")
	// meta: the current and compacted revisions, the last applied log index.
	metaBucket = []byte("meta")
	// leases: lease ID -> lease; leaseKeys: lease ID ++ key -> nothing.
	leasesBucket    = []byte("leases")
	leaseKeysBucket = []byte("leasekeys")
	// members: member ID -> member; removed: IDs that may not come back.
	membersBucket = []byte("members")
	removedBucket = []byte("removed")
	// requests: proposer ++ request ID -> the index it was applied at, so
	// that a request proposed again after a change of leader applies once.
	requestsBucket = []byte("requests")

	currentKey = []byte("current")
	compactKey = []byte("compacted")
	appliedKey = []byte("applied")
)

// Errors as etcd reports them.
var (
	ErrCompacted     = errors.New("etcdserver: mvcc: required revision has been compacted")
	ErrFutureRev     = errors.New("etcdserver: mvcc: required revision is a future revision")
	ErrLeaseNotFound = errors.New("etcdserver: requested lease not found")
	ErrLeaseExists   = errors.New("etcdserver: lease already exists")
	errKeyNotFound   = errors.New("etcdserver: key not found")
)

// Version is one version of a key.
type Version struct {
	Value          []byte `json:"v,omitempty"`
	CreateRevision int64  `json:"c"`
	ModRevision    int64  `json:"m"`
	Version        int64  `json:"n"` // writes since creation, as etcd counts them
	Lease          int64  `json:"l,omitempty"`
	Deleted        bool   `json:"d,omitempty"`
}

// KV is a key and its version, as reads return them.
type KV struct {
	Key []byte
	Version
}

// Event is a committed change, for watches.
type Event struct {
	KV   KV
	Prev *KV
}

// Lease is a time to live that keys are attached to. Its expiry is not in
// the replicated state: the leader keeps the clock (see lease.go).
type Lease struct {
	ID  int64 `json:"id"`
	TTL int64 `json:"ttl"`
}

// Member is a member of the store, as the etcd Cluster API lists it.
type Member struct {
	ID         uint64   `json:"id"`
	Name       string   `json:"name,omitempty"`
	PeerURLs   []string `json:"peerURLs"`
	ClientURLs []string `json:"clientURLs,omitempty"`
	IsLearner  bool     `json:"isLearner,omitempty"`
}

// Store is the replicated state: the multi-version keys, the leases and
// the members, in one bbolt file. It changes only by Apply, in log order.
type Store struct {
	path string
	// mu guards db against its replacement by a snapshot from the leader.
	mu       sync.RWMutex
	db       *bolt.DB
	watchers *watchers
	// onLease follows lease grants and revocations, for the leader's clock.
	onLease func(granted []Lease, revoked []int64)
}

// Open opens or creates the state file at path.
func Open(path string) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, db: db, watchers: newWatchers()}, nil
}

func openDB(path string) (*bolt.DB, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, err
	}
	// The state is not synced on every apply: the Raft log is, and it holds
	// every entry after the last snapshot, which syncs the state first
	// (Sync). A member that crashes applies the rest again, as etcd does
	// with its backend. One sync per round of Raft instead of two.
	db.NoSync = true
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{versionsBucket, latestBucket, metaBucket, leasesBucket, leaseKeysBucket, membersBucket, removedBucket, requestsBucket} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		// A new store is at revision 1, as etcd's is: the API server takes 0
		// for "no revision" and refuses it from the store.
		if m := tx.Bucket(metaBucket); getInt(m, currentKey) == 0 {
			return putInt(m, currentKey, 1)
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Sync(); err != nil {
		s.db.Close()
		return err
	}
	return s.db.Close()
}

// Sync writes the state to disk: the log before it may then go.
func (s *Store) Sync() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db.Sync()
}

func (s *Store) view(fn func(tx *bolt.Tx) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db.View(fn)
}

func versionKey(key []byte, rev int64) []byte {
	out := make([]byte, 0, len(key)+9)
	out = append(out, key...)
	out = append(out, 0)
	return binary.BigEndian.AppendUint64(out, uint64(rev))
}

func splitVersionKey(vk []byte) ([]byte, int64) {
	n := len(vk)
	return vk[:n-9], int64(binary.BigEndian.Uint64(vk[n-8:]))
}

func idKey(id uint64) []byte { return binary.BigEndian.AppendUint64(nil, id) }

func getInt(b *bolt.Bucket, k []byte) int64 {
	v := b.Get(k)
	if len(v) != 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(v))
}

func putInt(b *bolt.Bucket, k []byte, n int64) error {
	return b.Put(k, binary.BigEndian.AppendUint64(nil, uint64(n)))
}

// Revisions are the current and compacted revisions.
func (s *Store) Revisions() (current, compacted int64) {
	_ = s.view(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		current, compacted = getInt(m, currentKey), getInt(m, compactKey)
		return nil
	})
	return current, compacted
}

// Applied is the index of the last log entry the state holds.
func (s *Store) Applied() uint64 {
	var n int64
	_ = s.view(func(tx *bolt.Tx) error { n = getInt(tx.Bucket(metaBucket), appliedKey); return nil })
	return uint64(n)
}

// versionAt is key's newest version not after rev, from the versions
// bucket; nil when it had none or the newest was a deletion.
func versionAt(versions *bolt.Bucket, key []byte, rev int64) *Version {
	c := versions.Cursor()
	k, v := c.Seek(versionKey(key, rev+1))
	// Step back to the version at or before rev, of this key.
	if k == nil {
		k, v = c.Last()
	} else {
		k, v = c.Prev()
	}
	if k == nil {
		return nil
	}
	if kk, _ := splitVersionKey(k); !bytes.Equal(kk, key) {
		return nil
	}
	var ver Version
	if json.Unmarshal(v, &ver) != nil || ver.Deleted {
		return nil
	}
	return &ver
}

// RangeResult is what a range read returns.
type RangeResult struct {
	KVs   []KV
	Count int64
	// Rev is the revision read at; More: keys past the limit remain.
	Rev  int64
	More bool
}

// Range reads [key, end) at rev (the current revision when 0): end empty
// reads key alone, end "\x00" reads everything from key. limit 0 is no
// limit.
func (s *Store) Range(key, end []byte, rev, limit int64, countOnly bool) (RangeResult, error) {
	var res RangeResult
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		res, err = rangeTx(tx, key, end, rev, limit, countOnly)
		return err
	})
	return res, err
}

func rangeTx(tx *bolt.Tx, key, end []byte, rev, limit int64, countOnly bool) (RangeResult, error) {
	var res RangeResult
	m := tx.Bucket(metaBucket)
	current, compacted := getInt(m, currentKey), getInt(m, compactKey)
	if rev == 0 {
		rev = current
	}
	if rev > current {
		return res, ErrFutureRev
	}
	if rev < compacted {
		return res, ErrCompacted
	}
	res.Rev = rev
	versions := tx.Bucket(versionsBucket)
	latest := tx.Bucket(latestBucket)
	visit := func(k []byte) {
		ver := versionAt(versions, k, rev)
		if ver == nil {
			return
		}
		res.Count++
		if countOnly {
			return
		}
		if limit > 0 && int64(len(res.KVs)) >= limit {
			res.More = true
			return
		}
		res.KVs = append(res.KVs, KV{Key: append([]byte(nil), k...), Version: *ver})
	}
	if len(end) == 0 {
		if latest.Get(key) != nil {
			visit(key)
		}
		return res, nil
	}
	c := latest.Cursor()
	for k, _ := c.Seek(key); k != nil; k, _ = c.Next() {
		if !(len(end) == 1 && end[0] == 0) && bytes.Compare(k, end) >= 0 {
			break
		}
		visit(k)
	}
	return res, nil
}

// Op is one operation of a transaction: a put, a delete of [Key, End), or
// a range read.
type Op struct {
	Key   []byte `json:"k"`
	End   []byte `json:"e,omitempty"`
	Value []byte `json:"v,omitempty"`
	Lease int64  `json:"l,omitempty"`
	// IgnoreValue and IgnoreLease keep the key's current value or lease.
	IgnoreValue bool `json:"iv,omitempty"`
	IgnoreLease bool `json:"il,omitempty"`
	Delete      bool `json:"d,omitempty"`
	// Range reads; Limit, Rev and CountOnly as Range takes them.
	Range     bool  `json:"r,omitempty"`
	Limit     int64 `json:"lim,omitempty"`
	Rev       int64 `json:"rev,omitempty"`
	CountOnly bool  `json:"co,omitempty"`
}

// Compare is one condition of a transaction, as etcd's Compare with
// target MOD, CREATE, VERSION, VALUE or LEASE and result EQUAL, GREATER,
// LESS or NOT_EQUAL.
type Compare struct {
	Key    []byte `json:"k"`
	End    []byte `json:"e,omitempty"`
	Target string `json:"t"` // mod, create, version, value, lease
	Result string `json:"r"` // ==, >, <, !=
	Int    int64  `json:"i,omitempty"`
	Value  []byte `json:"v,omitempty"`
}

func (c Compare) holds(ver *Version) bool {
	var cmp int
	switch c.Target {
	case "value":
		if ver == nil {
			// etcd: a value comparison with a missing key fails.
			return false
		}
		cmp = bytes.Compare(ver.Value, c.Value)
	default:
		var n int64
		if ver != nil {
			switch c.Target {
			case "mod":
				n = ver.ModRevision
			case "create":
				n = ver.CreateRevision
			case "version":
				n = ver.Version
			case "lease":
				n = ver.Lease
			}
		}
		switch {
		case n < c.Int:
			cmp = -1
		case n > c.Int:
			cmp = 1
		}
	}
	switch c.Result {
	case "==":
		return cmp == 0
	case "!=":
		return cmp != 0
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	}
	return false
}

// TxnRequest is an etcd transaction: comparisons, then the success or the
// failure operations.
type TxnRequest struct {
	Compares []Compare `json:"c,omitempty"`
	Success  []Op      `json:"s,omitempty"`
	Failure  []Op      `json:"f,omitempty"`
}

// OpResult is what one operation of a transaction did.
type OpResult struct {
	// Prev: the versions a put or delete replaced.
	Prev    []*KV
	Deleted int64
	Range   *RangeResult
}

// TxnResult is what a transaction did: whether its comparisons held, the
// revision it committed at (the current one when it wrote nothing) and
// each operation's result.
type TxnResult struct {
	Succeeded bool
	Revision  int64
	Ops       []OpResult
}

// writer collects the writes of one log entry at one new revision.
type writer struct {
	tx                *bolt.Tx
	meta, versions    *bolt.Bucket
	latest, leaseKeys *bolt.Bucket
	current, rev      int64
	wrote             bool
	events            []Event
}

func newWriter(tx *bolt.Tx) *writer {
	w := &writer{tx: tx, meta: tx.Bucket(metaBucket), versions: tx.Bucket(versionsBucket),
		latest: tx.Bucket(latestBucket), leaseKeys: tx.Bucket(leaseKeysBucket)}
	w.current = getInt(w.meta, currentKey)
	w.rev = w.current + 1
	return w
}

func leaseKey(id int64, key []byte) []byte { return append(idKey(uint64(id)), key...) }

func (w *writer) put(key []byte, ver Version, prev *Version) error {
	raw, err := json.Marshal(ver)
	if err != nil {
		return err
	}
	if err := w.versions.Put(versionKey(key, w.rev), raw); err != nil {
		return err
	}
	if prev != nil && prev.Lease != 0 && prev.Lease != ver.Lease {
		if err := w.leaseKeys.Delete(leaseKey(prev.Lease, key)); err != nil {
			return err
		}
	}
	if !ver.Deleted && ver.Lease != 0 {
		if err := w.leaseKeys.Put(leaseKey(ver.Lease, key), nil); err != nil {
			return err
		}
	}
	w.wrote = true
	return putInt(w.latest, key, w.rev)
}

// del deletes [key, end) and returns the versions it deleted.
func (w *writer) del(key, end []byte) ([]*KV, error) {
	var keys [][]byte
	if len(end) == 0 {
		keys = [][]byte{key}
	} else {
		c := w.latest.Cursor()
		for k, _ := c.Seek(key); k != nil; k, _ = c.Next() {
			if !(len(end) == 1 && end[0] == 0) && bytes.Compare(k, end) >= 0 {
				break
			}
			keys = append(keys, append([]byte(nil), k...))
		}
	}
	var out []*KV
	for _, k := range keys {
		prev := versionAt(w.versions, k, w.current)
		if prev == nil {
			continue
		}
		gone := Version{ModRevision: w.rev, Deleted: true}
		if err := w.put(k, gone, prev); err != nil {
			return nil, err
		}
		prevKV := &KV{Key: k, Version: *prev}
		out = append(out, prevKV)
		w.events = append(w.events, Event{KV: KV{Key: k, Version: gone}, Prev: prevKV})
	}
	return out, nil
}

func (w *writer) finish() (int64, error) {
	if !w.wrote {
		return w.current, nil
	}
	return w.rev, putInt(w.meta, currentKey, w.rev)
}

// txn checks every comparison against the current state and applies the
// success or the failure ops, all at one new revision.
func txn(w *writer, r *TxnRequest) (TxnResult, error) {
	res := TxnResult{Succeeded: true}
	for _, c := range r.Compares {
		keys := [][]byte{c.Key}
		if len(c.End) > 0 {
			rr, err := rangeTx(w.tx, c.Key, c.End, 0, 0, false)
			if err != nil {
				return res, err
			}
			keys = keys[:0]
			for _, kv := range rr.KVs {
				keys = append(keys, kv.Key)
			}
		}
		for _, k := range keys {
			if !c.holds(versionAt(w.versions, k, w.current)) {
				res.Succeeded = false
			}
		}
	}
	ops := r.Success
	if !res.Succeeded {
		ops = r.Failure
	}
	leases := w.tx.Bucket(leasesBucket)
	// A put to a lease that does not exist, or that keeps the value or
	// lease of a key that does not exist, fails the whole transaction
	// before it writes anything.
	for _, op := range ops {
		if op.Range || op.Delete {
			continue
		}
		if op.Lease != 0 && !op.IgnoreLease && leases.Get(idKey(uint64(op.Lease))) == nil {
			return res, ErrLeaseNotFound
		}
		if (op.IgnoreValue || op.IgnoreLease) && versionAt(w.versions, op.Key, w.current) == nil {
			return res, errKeyNotFound
		}
	}
	for _, op := range ops {
		var or OpResult
		switch {
		case op.Range:
			rr, err := rangeTx(w.tx, op.Key, op.End, op.Rev, op.Limit, op.CountOnly)
			if err != nil {
				return res, err
			}
			or.Range = &rr
		case op.Delete:
			prev, err := w.del(op.Key, op.End)
			if err != nil {
				return res, err
			}
			or.Prev, or.Deleted = prev, int64(len(prev))
		default:
			prev := versionAt(w.versions, op.Key, w.current)
			ver := Version{Value: op.Value, CreateRevision: w.rev, ModRevision: w.rev, Version: 1, Lease: op.Lease}
			var prevKV *KV
			if prev != nil {
				ver.CreateRevision, ver.Version = prev.CreateRevision, prev.Version+1
				prevKV = &KV{Key: op.Key, Version: *prev}
				if op.IgnoreValue {
					ver.Value = prev.Value
				}
				if op.IgnoreLease {
					ver.Lease = prev.Lease
				}
			}
			if err := w.put(op.Key, ver, prev); err != nil {
				return res, err
			}
			or.Prev = []*KV{prevKV}
			w.events = append(w.events, Event{KV: KV{Key: op.Key, Version: ver}, Prev: prevKV})
		}
		res.Ops = append(res.Ops, or)
	}
	rev, err := w.finish()
	res.Revision = rev
	return res, err
}

// compact drops every version older than the newest one each key had at
// rev, and deletions at or before rev.
func compact(tx *bolt.Tx, rev int64) error {
	m := tx.Bucket(metaBucket)
	current, compacted := getInt(m, currentKey), getInt(m, compactKey)
	if rev > current {
		return ErrFutureRev
	}
	if rev <= compacted {
		return ErrCompacted
	}
	versions, latest := tx.Bucket(versionsBucket), tx.Bucket(latestBucket)
	var drop, gone [][]byte
	c := versions.Cursor()
	var prevKey, keep []byte
	// For each key, every version before its newest at or before rev goes;
	// that newest one goes too when it is a deletion with nothing after it.
	for k, v := c.First(); k != nil; k, v = c.Next() {
		key, r := splitVersionKey(k)
		if !bytes.Equal(key, prevKey) {
			prevKey, keep = append([]byte(nil), key...), nil
		}
		if r > rev {
			continue
		}
		if keep != nil {
			drop = append(drop, keep)
		}
		keep = append([]byte(nil), k...)
		var ver Version
		if json.Unmarshal(v, &ver) == nil && ver.Deleted && getInt(latest, key) == r {
			drop = append(drop, keep)
			gone = append(gone, prevKey)
			keep = nil
		}
	}
	for _, k := range drop {
		if err := versions.Delete(k); err != nil {
			return err
		}
	}
	for _, k := range gone {
		if err := latest.Delete(k); err != nil {
			return err
		}
	}
	return putInt(m, compactKey, rev)
}

// leaseGrant creates a lease.
func leaseGrant(tx *bolt.Tx, l Lease) error {
	b := tx.Bucket(leasesBucket)
	if b.Get(idKey(uint64(l.ID))) != nil {
		return ErrLeaseExists
	}
	raw, _ := json.Marshal(l)
	return b.Put(idKey(uint64(l.ID)), raw)
}

// leaseRevoke deletes a lease and its keys, at one new revision.
func leaseRevoke(w *writer, id int64) (int64, error) {
	b := w.tx.Bucket(leasesBucket)
	if b.Get(idKey(uint64(id))) == nil {
		return w.current, ErrLeaseNotFound
	}
	prefix := idKey(uint64(id))
	var keys [][]byte
	c := w.leaseKeys.Cursor()
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		keys = append(keys, append([]byte(nil), k[8:]...))
	}
	for _, k := range keys {
		if _, err := w.del(k, nil); err != nil {
			return 0, err
		}
		if err := w.leaseKeys.Delete(leaseKey(id, k)); err != nil {
			return 0, err
		}
	}
	if err := b.Delete(prefix); err != nil {
		return 0, err
	}
	return w.finish()
}

// Leases are the store's leases.
func (s *Store) Leases() []Lease {
	var out []Lease
	_ = s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(leasesBucket).ForEach(func(_, v []byte) error {
			var l Lease
			if json.Unmarshal(v, &l) == nil {
				out = append(out, l)
			}
			return nil
		})
	})
	return out
}

// LeaseOf is one lease and its keys; ok is false when there is none.
func (s *Store) LeaseOf(id int64) (l Lease, keys [][]byte, ok bool) {
	_ = s.view(func(tx *bolt.Tx) error {
		v := tx.Bucket(leasesBucket).Get(idKey(uint64(id)))
		if v == nil {
			return nil
		}
		ok = json.Unmarshal(v, &l) == nil
		prefix := idKey(uint64(id))
		c := tx.Bucket(leaseKeysBucket).Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			keys = append(keys, append([]byte(nil), k[8:]...))
		}
		return nil
	})
	return l, keys, ok
}

// Members are the store's members, ordered by ID.
func (s *Store) Members() []Member {
	var out []Member
	_ = s.view(func(tx *bolt.Tx) error {
		out = membersTx(tx)
		return nil
	})
	return out
}

func membersTx(tx *bolt.Tx) []Member {
	var out []Member
	_ = tx.Bucket(membersBucket).ForEach(func(_, v []byte) error {
		var m Member
		if json.Unmarshal(v, &m) == nil {
			out = append(out, m)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Removed reports whether a member was removed from the store.
func (s *Store) Removed(id uint64) bool {
	var gone bool
	_ = s.view(func(tx *bolt.Tx) error { gone = tx.Bucket(removedBucket).Get(idKey(id)) != nil; return nil })
	return gone
}

func putMember(tx *bolt.Tx, m Member) error {
	raw, _ := json.Marshal(m)
	return tx.Bucket(membersBucket).Put(idKey(m.ID), raw)
}

func memberTx(tx *bolt.Tx, id uint64) (Member, bool) {
	var m Member
	v := tx.Bucket(membersBucket).Get(idKey(id))
	return m, v != nil && json.Unmarshal(v, &m) == nil
}

// Changes are the committed changes of [key, end) after rev, from the
// stored versions; for a watch that starts in the past.
func (s *Store) Changes(key, end []byte, after int64) ([]Event, int64, error) {
	var out []Event
	var current int64
	err := s.view(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		current = getInt(m, currentKey)
		if after < getInt(m, compactKey) {
			return ErrCompacted
		}
		versions := tx.Bucket(versionsBucket)
		c := versions.Cursor()
		var prev *KV
		var prevKey []byte
		for k, v := c.Seek(key); k != nil; k, v = c.Next() {
			kk, r := splitVersionKey(k)
			if !inRange(kk, key, end) {
				if len(end) == 0 && !bytes.Equal(kk, key) {
					break
				}
				if len(end) > 0 && !(len(end) == 1 && end[0] == 0) && bytes.Compare(kk, end) >= 0 {
					break
				}
				continue
			}
			if !bytes.Equal(kk, prevKey) {
				prev, prevKey = nil, append([]byte(nil), kk...)
			}
			var ver Version
			if json.Unmarshal(v, &ver) != nil {
				return fmt.Errorf("version of %q at %d", kk, r)
			}
			cur := &KV{Key: append([]byte(nil), kk...), Version: ver}
			if r > after {
				out = append(out, Event{KV: *cur, Prev: prev})
			}
			if ver.Deleted {
				prev = nil
			} else {
				prev = cur
			}
		}
		return nil
	})
	// By revision, then key: the order the changes were committed in.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].KV.ModRevision != out[j].KV.ModRevision {
			return out[i].KV.ModRevision < out[j].KV.ModRevision
		}
		return bytes.Compare(out[i].KV.Key, out[j].KV.Key) < 0
	})
	return out, current, err
}

func inRange(k, key, end []byte) bool {
	switch {
	case len(end) == 0:
		return bytes.Equal(k, key)
	case len(end) == 1 && end[0] == 0:
		return bytes.Compare(k, key) >= 0
	}
	return bytes.Compare(k, key) >= 0 && bytes.Compare(k, end) < 0
}

// DBSize is the state file's size.
func (s *Store) DBSize() int64 {
	var n int64
	_ = s.view(func(tx *bolt.Tx) error { n = tx.Size(); return nil })
	return n
}

// WriteTo writes a consistent copy of the state file: what a snapshot
// sends to a member that is too far behind for the log.
func (s *Store) WriteTo(w io.Writer) (int64, error) {
	var n int64
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		n, err = tx.WriteTo(w)
		return err
	})
	return n, err
}

// Replace takes the state from a file a snapshot brought: the watches of
// the old state end, and their clients start again from the new one.
func (s *Store) Replace(file string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Close(); err != nil {
		return err
	}
	if err := os.Rename(file, s.path); err != nil {
		return err
	}
	db, err := openDB(s.path)
	if err != nil {
		return err
	}
	s.db = db
	s.watchers.cancelAll()
	return nil
}

// ReadTxn answers a transaction that only reads, from the current state.
func (s *Store) ReadTxn(r *TxnRequest) (TxnResult, error) {
	var res TxnResult
	err := s.view(func(tx *bolt.Tx) error {
		if err := checkRanges(tx, r); err != nil {
			return err
		}
		var err error
		res, err = txn(newWriter(tx), r)
		return err
	})
	return res, err
}
