// Package resourcestore is a prototype of a store in the model of
// Kubernetes resources: an ordered, multi-version key-value store that
// speaks the part of the etcd v3 API kube-apiserver uses. See
// docs/resource-store.md. Single member, no Raft yet: a lab, not a store to
// run a cluster on.
package resourcestore

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	bolt "go.etcd.io/bbolt"
)

// Buckets of the bbolt file.
var (
	// versions: key 0x00 big-endian revision -> version, ordered by key then revision.
	versionsBucket = []byte("versions")
	// latest: key -> revision of its newest version.
	latestBucket = []byte("latest")
	// meta: the current and compacted revisions.
	metaBucket = []byte("meta")
	currentKey = []byte("current")
	compactKey = []byte("compacted")
)

// Errors as etcd reports them.
var (
	ErrCompacted = errors.New("etcdserver: mvcc: required revision has been compacted")
	ErrFutureRev = errors.New("etcdserver: mvcc: required revision is a future revision")
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

// Store is the multi-version store over one bbolt file.
type Store struct {
	db *bolt.DB
	// mu orders writes: one revision at a time, published to watchers in order.
	mu       sync.Mutex
	watchers *watchers
}

// Open opens or creates the store at path.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{versionsBucket, latestBucket, metaBucket} {
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
	return &Store{db: db, watchers: newWatchers()}, nil
}

func (s *Store) Close() error { return s.db.Close() }

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
	_ = s.db.View(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		current, compacted = getInt(m, currentKey), getInt(m, compactKey)
		return nil
	})
	return current, compacted
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

// Range reads [key, end) at rev (the current revision when 0): end empty
// reads key alone, end "\x00" reads everything from key. limit 0 is no
// limit. It returns the revision read at and whether more keys remain.
func (s *Store) Range(key, end []byte, rev, limit int64, countOnly bool) (kvs []KV, count int64, readRev int64, more bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		current, compacted := getInt(m, currentKey), getInt(m, compactKey)
		if rev == 0 {
			rev = current
		}
		if rev > current {
			return ErrFutureRev
		}
		if rev < compacted {
			return ErrCompacted
		}
		readRev = rev
		versions := tx.Bucket(versionsBucket)
		latest := tx.Bucket(latestBucket)
		visit := func(k []byte) {
			ver := versionAt(versions, k, rev)
			if ver == nil {
				return
			}
			count++
			if countOnly || (limit > 0 && int64(len(kvs)) >= limit) {
				if limit > 0 && int64(len(kvs)) >= limit {
					more = true
				}
				return
			}
			kvs = append(kvs, KV{Key: append([]byte(nil), k...), Version: *ver})
		}
		if len(end) == 0 {
			if latest.Get(key) != nil {
				visit(key)
			}
			return nil
		}
		c := latest.Cursor()
		for k, _ := c.Seek(key); k != nil; k, _ = c.Next() {
			if !(len(end) == 1 && end[0] == 0) && bytes.Compare(k, end) >= 0 {
				break
			}
			visit(k)
		}
		return nil
	})
	return kvs, count, readRev, more, err
}

// Op is one write of a transaction.
type Op struct {
	Key, End []byte // End: delete [Key, End); empty: Key alone
	Value    []byte
	Lease    int64
	Delete   bool
}

// Compare is one condition of a transaction, as etcd's Compare with
// target MOD, CREATE, VERSION or VALUE and result EQUAL, GREATER, LESS or
// NOT_EQUAL.
type Compare struct {
	Key    []byte
	Target string // mod, create, version, value
	Result string // ==, >, <, !=
	Int    int64
	Value  []byte
}

func (c Compare) holds(ver *Version) bool {
	var cmp int
	switch c.Target {
	case "value":
		var v []byte
		if ver != nil {
			v = ver.Value
		}
		cmp = bytes.Compare(v, c.Value)
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

// TxnResult is what a transaction did: whether its comparisons held, the
// revision it committed at (the current one when it wrote nothing), the
// previous versions of what it wrote and, for each comparison's key, its
// current version.
type TxnResult struct {
	Succeeded bool
	Revision  int64
	Prev      []*KV
	Deleted   []int64
	Current   map[string]*KV
}

// Txn checks every comparison against the current state and applies the
// success or the failure ops, all at one new revision.
func (s *Store) Txn(cmps []Compare, success, failure []Op) (TxnResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res TxnResult
	var events []Event
	err := s.db.Update(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		versions, latest := tx.Bucket(versionsBucket), tx.Bucket(latestBucket)
		current := getInt(m, currentKey)
		res.Current = map[string]*KV{}
		res.Succeeded = true
		for _, c := range cmps {
			ver := versionAt(versions, c.Key, current)
			if ver != nil {
				res.Current[string(c.Key)] = &KV{Key: c.Key, Version: *ver}
			}
			if !c.holds(ver) {
				res.Succeeded = false
			}
		}
		ops := success
		if !res.Succeeded {
			ops = failure
		}
		rev := current + 1
		wrote := false
		write := func(key []byte, ver Version) error {
			raw, err := json.Marshal(ver)
			if err != nil {
				return err
			}
			if err := versions.Put(versionKey(key, rev), raw); err != nil {
				return err
			}
			wrote = true
			return putInt(latest, key, rev)
		}
		for _, op := range ops {
			if !op.Delete {
				prev := versionAt(versions, op.Key, current)
				ver := Version{Value: op.Value, CreateRevision: rev, ModRevision: rev, Version: 1, Lease: op.Lease}
				var prevKV *KV
				if prev != nil {
					ver.CreateRevision, ver.Version = prev.CreateRevision, prev.Version+1
					prevKV = &KV{Key: op.Key, Version: *prev}
				}
				if err := write(op.Key, ver); err != nil {
					return err
				}
				res.Prev = append(res.Prev, prevKV)
				events = append(events, Event{KV: KV{Key: op.Key, Version: ver}, Prev: prevKV})
				continue
			}
			var keys [][]byte
			if len(op.End) == 0 {
				keys = [][]byte{op.Key}
			} else {
				c := latest.Cursor()
				for k, _ := c.Seek(op.Key); k != nil; k, _ = c.Next() {
					if !(len(op.End) == 1 && op.End[0] == 0) && bytes.Compare(k, op.End) >= 0 {
						break
					}
					keys = append(keys, append([]byte(nil), k...))
				}
			}
			var n int64
			for _, k := range keys {
				prev := versionAt(versions, k, current)
				if prev == nil {
					continue
				}
				if err := write(k, Version{ModRevision: rev, Deleted: true}); err != nil {
					return err
				}
				n++
				prevKV := &KV{Key: k, Version: *prev}
				res.Prev = append(res.Prev, prevKV)
				events = append(events, Event{KV: KV{Key: k, Version: Version{ModRevision: rev, Deleted: true}}, Prev: prevKV})
			}
			res.Deleted = append(res.Deleted, n)
		}
		if !wrote {
			res.Revision = current
			return nil
		}
		res.Revision = rev
		return putInt(m, currentKey, rev)
	})
	if err == nil && len(events) > 0 {
		s.watchers.publish(res.Revision, events)
	}
	return res, err
}

// Compact drops every version older than the newest one each key had at
// rev, and deletions at or before rev.
func (s *Store) Compact(rev int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		current, compacted := getInt(m, currentKey), getInt(m, compactKey)
		if rev > current {
			return ErrFutureRev
		}
		if rev <= compacted {
			return ErrCompacted
		}
		versions, latest := tx.Bucket(versionsBucket), tx.Bucket(latestBucket)
		var drop [][]byte
		var gone [][]byte
		c := versions.Cursor()
		var prevKey []byte
		// For each key, every version before its newest at or before rev goes;
		// that newest one goes too when it is a deletion with nothing after it.
		var keep []byte
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
	})
}

// Changes are the committed changes of [key, end) after rev, from the
// stored versions; for a watch that starts in the past.
func (s *Store) Changes(key, end []byte, after int64) ([]Event, int64, error) {
	var out []Event
	var current int64
	err := s.db.View(func(tx *bolt.Tx) error {
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
	sortEvents(out)
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

func sortEvents(ev []Event) {
	// By revision, then key: the order the changes were committed in.
	for i := 1; i < len(ev); i++ {
		for j := i; j > 0 && (ev[j].KV.ModRevision < ev[j-1].KV.ModRevision ||
			(ev[j].KV.ModRevision == ev[j-1].KV.ModRevision && bytes.Compare(ev[j].KV.Key, ev[j-1].KV.Key) < 0)); j-- {
			ev[j], ev[j-1] = ev[j-1], ev[j]
		}
	}
}

// DBSize is the store file's size.
func (s *Store) DBSize() int64 {
	var n int64
	_ = s.db.View(func(tx *bolt.Tx) error { n = tx.Size(); return nil })
	return n
}
