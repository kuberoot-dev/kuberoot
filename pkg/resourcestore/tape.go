package resourcestore

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"

	bolt "go.etcd.io/bbolt"
)

// The tape is the store's history past compaction. The API server compacts
// every five minutes, and the etcd API keeps answering as etcd does: a read
// or a watch before the compacted revision fails. A member started with
// KeepHistory moves what a compaction drops to the archive bucket instead
// of deleting it, and the tape reads both: the cluster at any revision
// since the tape started, every version of a key, the keys deleted and what
// they held. Each revision also keeps the time its write was proposed, so
// the tape can be read at a moment rather than a revision.
//
// All of it is replicated state: the archive changes only by compactions,
// the times come with the entries, so every member holds the same tape.

// ErrBeforeTape is a read of a revision older than the tape holds.
var ErrBeforeTape = errors.New("resourcestore: revision is older than the tape")

// markGrave keeps the graveyard: a key deleted at rev is in it until it is
// written again.
func markGrave(tx *bolt.Tx, key []byte, deleted bool, rev int64) error {
	b := tx.Bucket(graveyardBucket)
	if deleted {
		return putInt(b, key, rev)
	}
	if b.Get(key) == nil {
		return nil
	}
	return b.Delete(key)
}

// recordTime keeps when rev was written. Entries proposed before the tape
// carry no time.
func recordTime(tx *bolt.Tx, rev, at int64) error {
	if at == 0 {
		return nil
	}
	if err := putInt(tx.Bucket(revTimesBucket), idKey(uint64(rev)), at); err != nil {
		return err
	}
	k := binary.BigEndian.AppendUint64(idKey(uint64(at)), uint64(rev))
	return tx.Bucket(timeRevsBucket).Put(k, nil)
}

// startTape records where the tape starts, at its first archiving
// compaction: everything after the revision compacted before it.
func startTape(m *bolt.Bucket, compacted int64) error {
	if m.Get(tapeFromKey) != nil {
		return nil
	}
	return putInt(m, tapeFromKey, compacted)
}

// TapeInfo is what the tape holds.
type TapeInfo struct {
	Current   int64 `json:"current"`
	Compacted int64 `json:"compacted"`
	// From is the oldest revision the tape reads; Started is false while
	// no compaction has archived, and From is then the compacted revision.
	From    int64 `json:"from"`
	Started bool  `json:"started"`
	// FromTime and CurrentTime, in Unix milliseconds, when known.
	FromTime    int64 `json:"fromTime,omitempty"`
	CurrentTime int64 `json:"currentTime,omitempty"`
}

func tapeFrom(m *bolt.Bucket) (int64, bool) {
	if m.Get(tapeFromKey) == nil {
		return max(getInt(m, compactKey), 1), false
	}
	return max(getInt(m, tapeFromKey), 1), true
}

// Tape tells what the tape holds.
func (s *Store) Tape() TapeInfo {
	var t TapeInfo
	_ = s.view(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		t.Current, t.Compacted = getInt(m, currentKey), getInt(m, compactKey)
		t.From, t.Started = tapeFrom(m)
		t.FromTime, t.CurrentTime = timeOf(tx, t.From), timeOf(tx, t.Current)
		return nil
	})
	return t
}

// timeOf is when rev was written: the time of the newest write at or
// before it; 0 when unknown.
func timeOf(tx *bolt.Tx, rev int64) int64 {
	c := tx.Bucket(revTimesBucket).Cursor()
	k, v := c.Seek(idKey(uint64(rev + 1)))
	if k == nil {
		k, v = c.Last()
	} else {
		k, v = c.Prev()
	}
	if k == nil || len(v) != 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(v))
}

// RevisionAt is the newest revision written at or before at (Unix
// milliseconds); 0 when there is none the tape knows the time of.
func (s *Store) RevisionAt(at int64) int64 {
	var rev int64
	_ = s.view(func(tx *bolt.Tx) error {
		c := tx.Bucket(timeRevsBucket).Cursor()
		k, _ := c.Seek(idKey(uint64(at + 1)))
		if k == nil {
			k, _ = c.Last()
		} else {
			k, _ = c.Prev()
		}
		if len(k) == 16 {
			rev = int64(binary.BigEndian.Uint64(k[8:]))
		}
		return nil
	})
	return rev
}

// TimeOf is when rev was written, in Unix milliseconds; 0 when unknown.
func (s *Store) TimeOf(rev int64) int64 {
	var at int64
	_ = s.view(func(tx *bolt.Tx) error { at = timeOf(tx, rev); return nil })
	return at
}

// newestIn is key's newest version not after rev in one bucket of
// versions, and its revision.
func newestIn(b *bolt.Bucket, key []byte, rev int64) (*Version, int64) {
	c := b.Cursor()
	k, v := c.Seek(versionKey(key, rev+1))
	if k == nil {
		k, v = c.Last()
	} else {
		k, v = c.Prev()
	}
	if k == nil {
		return nil, 0
	}
	kk, r := splitVersionKey(k)
	if !bytes.Equal(kk, key) {
		return nil, 0
	}
	var ver Version
	if json.Unmarshal(v, &ver) != nil {
		return nil, 0
	}
	return &ver, r
}

// tapeVersionAt is key's version at rev from the live versions and the
// archive; nil when it had none or was deleted.
func tapeVersionAt(tx *bolt.Tx, key []byte, rev int64) *Version {
	live, lr := newestIn(tx.Bucket(versionsBucket), key, rev)
	old, or := newestIn(tx.Bucket(archiveBucket), key, rev)
	ver := live
	if or > lr {
		ver = old
	}
	if ver == nil || ver.Deleted {
		return nil
	}
	return ver
}

// keysIn walks the distinct keys of a bucket of versions in [key, end).
func keysIn(b *bolt.Bucket, key, end []byte, fn func(k []byte)) {
	c := b.Cursor()
	for k, _ := c.Seek(key); k != nil; {
		kk, _ := splitVersionKey(k)
		// Past the end: every key from here sorts after it.
		if !inRange(kk, key, end) {
			return
		}
		fn(append([]byte(nil), kk...))
		// The next key sorts after every version of this one.
		k, _ = c.Seek(append(append([]byte(nil), kk...), 1))
	}
}

// TapeRange reads [key, end) at rev as the store was then, from the tape:
// like Range, but any revision since the tape started. limit 0 is no limit.
func (s *Store) TapeRange(key, end []byte, rev, limit int64) (RangeResult, error) {
	var res RangeResult
	err := s.view(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		current := getInt(m, currentKey)
		from, _ := tapeFrom(m)
		if rev == 0 {
			rev = current
		}
		if rev > current {
			return ErrFutureRev
		}
		if rev < from {
			return ErrBeforeTape
		}
		res.Rev = rev
		visit := func(k []byte) {
			ver := tapeVersionAt(tx, k, rev)
			if ver == nil {
				return
			}
			res.Count++
			if limit > 0 && int64(len(res.KVs)) >= limit {
				res.More = true
				return
			}
			res.KVs = append(res.KVs, KV{Key: k, Version: *ver})
		}
		if len(end) == 0 {
			visit(append([]byte(nil), key...))
			return nil
		}
		// Keys live now, and keys whose every version is archived.
		seen := map[string]bool{}
		var keys [][]byte
		add := func(k []byte) {
			if !seen[string(k)] {
				seen[string(k)] = true
				keys = append(keys, k)
			}
		}
		keysIn(tx.Bucket(versionsBucket), key, end, add)
		keysIn(tx.Bucket(archiveBucket), key, end, add)
		sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
		for _, k := range keys {
			visit(k)
		}
		return nil
	})
	return res, err
}

// History is every version of key the store holds, archived or live,
// deletions included, oldest first.
func (s *Store) History(key []byte) []KV {
	var out []KV
	_ = s.view(func(tx *bolt.Tx) error {
		for _, b := range []*bolt.Bucket{tx.Bucket(archiveBucket), tx.Bucket(versionsBucket)} {
			c := b.Cursor()
			prefix := append(append([]byte(nil), key...), 0)
			for k, v := c.Seek(prefix); k != nil && len(k) == len(key)+9 && bytes.HasPrefix(k, prefix); k, v = c.Next() {
				var ver Version
				if json.Unmarshal(v, &ver) == nil {
					out = append(out, KV{Key: append([]byte(nil), key...), Version: ver})
				}
			}
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].ModRevision < out[j].ModRevision })
	return out
}

// Grave is a deleted key: when it was deleted and what it last held.
type Grave struct {
	Key       []byte
	DeletedAt int64
	// Last is its version just before the deletion; nil when the tape no
	// longer holds it.
	Last *Version
}

// Graveyard is the keys in [key, end) deleted and not written since, by
// key. Deletions before the tape are not in it.
func (s *Store) Graveyard(key, end []byte) []Grave {
	var out []Grave
	_ = s.view(func(tx *bolt.Tx) error {
		c := tx.Bucket(graveyardBucket).Cursor()
		for k, v := c.Seek(key); k != nil; k, v = c.Next() {
			if !inRange(k, key, end) {
				break
			}
			if len(v) != 8 {
				continue
			}
			g := Grave{Key: append([]byte(nil), k...), DeletedAt: int64(binary.BigEndian.Uint64(v))}
			g.Last = tapeVersionAt(tx, k, g.DeletedAt-1)
			out = append(out, g)
		}
		return nil
	})
	return out
}

// pruneTape forgets the tape before the newest revision written at or
// before the moment before (Unix milliseconds), as a compaction of the
// archive at that revision: each key keeps its version at the cut, unless
// a live version already holds it or it was deleted; deletions before the
// cut leave the graveyard. The tape then starts at the cut.
func pruneTape(tx *bolt.Tx, before int64) error {
	m := tx.Bucket(metaBucket)
	from, started := tapeFrom(m)
	if !started {
		return nil
	}
	cut := int64(0)
	c := tx.Bucket(timeRevsBucket).Cursor()
	k, _ := c.Seek(idKey(uint64(before + 1)))
	if k == nil {
		k, _ = c.Last()
	} else {
		k, _ = c.Prev()
	}
	if len(k) == 16 {
		cut = int64(binary.BigEndian.Uint64(k[8:]))
	}
	// The archive holds nothing after the compacted revision.
	cut = min(cut, getInt(m, compactKey))
	if cut <= from {
		return nil
	}
	arch, versions := tx.Bucket(archiveBucket), tx.Bucket(versionsBucket)
	var drop [][]byte
	var prevKey, keep []byte
	keepDeleted := false
	settle := func() {
		// The version a key had at the cut stays, unless the live
		// versions hold one at or before the cut, or it is a deletion.
		if keep == nil {
			return
		}
		if _, r := newestIn(versions, prevKey, cut); r != 0 || keepDeleted {
			drop = append(drop, keep)
		}
	}
	ac := arch.Cursor()
	for k, v := ac.First(); k != nil; k, v = ac.Next() {
		key, r := splitVersionKey(k)
		if !bytes.Equal(key, prevKey) {
			settle()
			prevKey, keep = append([]byte(nil), key...), nil
		}
		if r > cut {
			continue
		}
		if keep != nil {
			drop = append(drop, keep)
		}
		keep = append([]byte(nil), k...)
		var ver Version
		keepDeleted = json.Unmarshal(v, &ver) == nil && ver.Deleted
	}
	settle()
	for _, k := range drop {
		if err := arch.Delete(k); err != nil {
			return err
		}
	}
	if err := deleteWhere(tx.Bucket(graveyardBucket), func(k, v []byte) bool {
		return len(v) == 8 && int64(binary.BigEndian.Uint64(v)) <= cut
	}); err != nil {
		return err
	}
	cutTime := timeOf(tx, cut)
	if err := deleteWhere(tx.Bucket(revTimesBucket), func(k, _ []byte) bool {
		return int64(binary.BigEndian.Uint64(k)) < cut
	}); err != nil {
		return err
	}
	if err := deleteWhere(tx.Bucket(timeRevsBucket), func(k, _ []byte) bool {
		return len(k) == 16 && int64(binary.BigEndian.Uint64(k[:8])) < cutTime
	}); err != nil {
		return err
	}
	return putInt(m, tapeFromKey, cut)
}

// deleteWhere deletes the entries of b that gone says are gone.
func deleteWhere(b *bolt.Bucket, gone func(k, v []byte) bool) error {
	var keys [][]byte
	_ = b.ForEach(func(k, v []byte) error {
		if gone(k, v) {
			keys = append(keys, append([]byte(nil), k...))
		}
		return nil
	})
	for _, k := range keys {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}
