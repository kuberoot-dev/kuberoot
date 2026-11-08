package resourcestore

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	bolt "go.etcd.io/bbolt"
)

// The journal is the tape read in revision order: every version written
// after a revision, with the time of its write. A copy of the tape kept
// elsewhere (object storage, for one) is a base, the keys as they were at
// a revision the tape holds, and the journal after it; RestoreTape makes a
// member of a new store from such a copy, with the tape as it was up to
// any revision of it.

// TapeFileFormat names the format of a tape file: a header line, then the
// base's keys, then the journal after the base, one TapeKV a line.
const TapeFileFormat = "kuberoot-tape/v1"

// TapeFileHeader is the first line of a tape file.
type TapeFileHeader struct {
	Format string `json:"format"`
	// Base is the revision the base's keys are read at; Time is when it
	// was written, in Unix milliseconds, when known.
	Base int64 `json:"base"`
	Time int64 `json:"time,omitempty"`
}

// JournalPage is a part of the journal: the versions written in
// (After, Upto], in revision order.
type JournalPage struct {
	After int64
	Upto  int64
	// More is true when the journal goes on after Upto.
	More bool
	KVs  []KV
}

// Journal reads the versions written after the revision after, in
// revision order, up to limit of them but never part of a revision: the
// last revision read is read whole. limit 0 is no limit. after must be a
// revision the tape holds every version after.
//
// The versions are found by their keys, which hold the revision: a read
// walks the keys of every version, but decodes only what it returns.
func (s *Store) Journal(after, limit int64) (JournalPage, error) {
	page := JournalPage{After: after}
	err := s.view(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		current := getInt(m, currentKey)
		from, _ := tapeFrom(m)
		if after < from {
			return ErrBeforeTape
		}
		if after > current {
			return ErrFutureRev
		}
		type ref struct {
			bucket []byte
			key    []byte
			rev    int64
		}
		var refs []ref
		for _, name := range [][]byte{versionsBucket, archiveBucket} {
			c := tx.Bucket(name).Cursor()
			for k, _ := c.First(); k != nil; k, _ = c.Next() {
				if len(k) < 9 {
					continue
				}
				if _, r := splitVersionKey(k); r > after {
					refs = append(refs, ref{bucket: name, key: append([]byte(nil), k...), rev: r})
				}
			}
		}
		sort.SliceStable(refs, func(i, j int) bool { return refs[i].rev < refs[j].rev })
		page.Upto = current
		if limit > 0 && int64(len(refs)) > limit {
			n := int(limit)
			for n < len(refs) && refs[n].rev == refs[n-1].rev {
				n++
			}
			if n < len(refs) {
				page.More, page.Upto = true, refs[n-1].rev
				refs = refs[:n]
			}
		}
		for _, r := range refs {
			var ver Version
			if err := json.Unmarshal(tx.Bucket(r.bucket).Get(r.key), &ver); err != nil {
				return fmt.Errorf("%q: %w", r.key, err)
			}
			key, _ := splitVersionKey(r.key)
			page.KVs = append(page.KVs, KV{Key: key, Version: ver})
		}
		return nil
	})
	return page, err
}

// TapeRestoreOptions are what RestoreTape makes of a tape file.
type TapeRestoreOptions struct {
	// Revision is the newest revision restored; 0 is all of the file.
	Revision int64
	// LeaseTTL is the time to live, in seconds, of the leases the keys
	// restored are attached to: the file has their IDs, not their TTLs.
	// 0 is an hour.
	LeaseTTL int64
	// Name is this member's, "default" when empty.
	Name string
	// InitialCluster lists the new store's members, name -> peer URLs;
	// nil is this member alone, on http://127.0.0.1:2380.
	InitialCluster map[string][]string
	Token          string
}

// RestoreTape makes dataDir the data of a member of a new store from a
// tape file (TapeFileFormat): its keys at opts.Revision, with every version
// since the file's base, the time of each revision and the deletions, so
// that the tape reads, the history and the graveyard answer as they did,
// from the base on. The keys' revisions are kept: an API server sees the
// resource versions it saw. The etcd API reads from the base on.
func RestoreTape(r io.Reader, dataDir string, opts TapeRestoreOptions) (TapeInfo, error) {
	var info TapeInfo
	if opts.LeaseTTL == 0 {
		opts.LeaseTTL = 3600
	}
	if opts.Name == "" {
		opts.Name = "default"
	}
	if opts.InitialCluster == nil {
		opts.InitialCluster = map[string][]string{opts.Name: {"http://127.0.0.1:2380"}}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return info, err
	}
	if _, err := os.Stat(filepath.Join(dataDir, "raft.db")); err == nil {
		return info, fmt.Errorf("%s holds a member already", dataDir)
	}
	tmp := filepath.Join(dataDir, "state.db.tape")
	os.Remove(tmp)
	db, err := openDB(tmp)
	if err != nil {
		return info, err
	}
	info, err = replayTape(db, r, opts)
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = Restore(tmp, dataDir, opts.Name, opts.InitialCluster, opts.Token)
	}
	os.Remove(tmp)
	return info, err
}

// replayTape writes a tape file into a new state, in batches.
func replayTape(db *bolt.DB, r io.Reader, opts TapeRestoreOptions) (TapeInfo, error) {
	var info TapeInfo
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return info, err
		}
		return info, errors.New("the tape file is empty")
	}
	var h TapeFileHeader
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil || h.Format != TapeFileFormat {
		return info, fmt.Errorf("not a tape file (%s): %q", TapeFileFormat, truncate(sc.Bytes(), 80))
	}
	if h.Base < 1 {
		return info, fmt.Errorf("the tape file's base is revision %d", h.Base)
	}
	if opts.Revision != 0 && opts.Revision < h.Base {
		return info, fmt.Errorf("revision %d is before the tape file's base %d", opts.Revision, h.Base)
	}
	rev := h.Base
	err := db.Update(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		for _, k := range [][]byte{currentKey, compactKey, tapeFromKey} {
			if err := putInt(m, k, h.Base); err != nil {
				return err
			}
		}
		return recordTime(tx, h.Base, h.Time)
	})
	if err != nil {
		return info, err
	}
	var batch []TapeKV
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := db.Update(func(tx *bolt.Tx) error {
			for _, kv := range batch {
				if err := replayOne(tx, h.Base, kv); err != nil {
					return err
				}
			}
			return nil
		})
		batch = batch[:0]
		return err
	}
	line := 1
	for sc.Scan() {
		line++
		var kv TapeKV
		if err := json.Unmarshal(sc.Bytes(), &kv); err != nil {
			return info, fmt.Errorf("line %d: %w", line, err)
		}
		switch {
		case kv.Key == "":
			return info, fmt.Errorf("line %d: no key", line)
		case kv.ModRevision <= h.Base:
			if rev > h.Base {
				return info, fmt.Errorf("line %d: a key of the base (revision %d) after the journal began", line, kv.ModRevision)
			}
			if kv.Deleted {
				return info, fmt.Errorf("line %d: a deletion in the base", line)
			}
		case kv.ModRevision < rev:
			return info, fmt.Errorf("line %d: revision %d after %d: the journal is out of order", line, kv.ModRevision, rev)
		case kv.ModRevision > rev+1:
			return info, fmt.Errorf("line %d: the journal has no revision between %d and %d", line, rev, kv.ModRevision)
		}
		if opts.Revision != 0 && kv.ModRevision > opts.Revision {
			break
		}
		rev = max(rev, kv.ModRevision)
		batch = append(batch, kv)
		if len(batch) >= 1000 && batch[len(batch)-1].ModRevision > h.Base {
			if err := flush(); err != nil {
				return info, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return info, err
	}
	if err := flush(); err != nil {
		return info, err
	}
	if opts.Revision > rev {
		return info, fmt.Errorf("the tape file ends at revision %d, before %d", rev, opts.Revision)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		// The leases the keys left are attached to, afresh.
		seen := map[int64]bool{}
		c := tx.Bucket(leaseKeysBucket).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			if len(k) < 8 {
				continue
			}
			id := int64(binary.BigEndian.Uint64(k[:8]))
			if seen[id] {
				continue
			}
			seen[id] = true
			if err := leaseGrant(tx, Lease{ID: id, TTL: opts.LeaseTTL}); err != nil {
				return err
			}
		}
		m := tx.Bucket(metaBucket)
		info.Current, info.Compacted = getInt(m, currentKey), getInt(m, compactKey)
		info.From, info.Started = tapeFrom(m)
		info.FromTime, info.CurrentTime = timeOf(tx, info.From), timeOf(tx, info.Current)
		return nil
	})
	return info, err
}

// replayOne writes one version of a tape file: a key of the base as it
// was, or a write of the journal at its revision, with what the write
// changed in the latest versions, the graveyard, the leases' keys and the
// times.
func replayOne(tx *bolt.Tx, base int64, kv TapeKV) error {
	key := []byte(kv.Key)
	ver := Version{Value: kv.Value, CreateRevision: kv.CreateRevision, ModRevision: kv.ModRevision,
		Version: kv.Version, Lease: kv.Lease, Deleted: kv.Deleted}
	if ver.Deleted {
		ver = Version{ModRevision: kv.ModRevision, Deleted: true}
	}
	raw, err := json.Marshal(ver)
	if err != nil {
		return err
	}
	versions, leaseKeys := tx.Bucket(versionsBucket), tx.Bucket(leaseKeysBucket)
	prev := versionAt(versions, key, kv.ModRevision-1)
	if err := versions.Put(versionKey(key, kv.ModRevision), raw); err != nil {
		return err
	}
	if prev != nil && prev.Lease != 0 && (ver.Deleted || prev.Lease != ver.Lease) {
		if err := leaseKeys.Delete(leaseKey(prev.Lease, key)); err != nil {
			return err
		}
	}
	if !ver.Deleted && ver.Lease != 0 {
		if err := leaseKeys.Put(leaseKey(ver.Lease, key), nil); err != nil {
			return err
		}
	}
	if err := putInt(tx.Bucket(latestBucket), key, kv.ModRevision); err != nil {
		return err
	}
	if kv.ModRevision <= base {
		return nil
	}
	if err := markGrave(tx, key, ver.Deleted, kv.ModRevision); err != nil {
		return err
	}
	if err := recordTime(tx, kv.ModRevision, kv.Time); err != nil {
		return err
	}
	return putInt(tx.Bucket(metaBucket), currentKey, kv.ModRevision)
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

// TapeFileRevisionAt is the newest revision of a tape file written at or
// before at (Unix milliseconds): the base when the journal has none.
func TapeFileRevisionAt(r io.Reader, at int64) (int64, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	var h TapeFileHeader
	if !sc.Scan() || json.Unmarshal(sc.Bytes(), &h) != nil || h.Format != TapeFileFormat {
		if err := sc.Err(); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("not a tape file (%s)", TapeFileFormat)
	}
	if h.Time != 0 && at < h.Time {
		return 0, fmt.Errorf("the tape file begins after that moment")
	}
	rev := h.Base
	for sc.Scan() {
		var kv TapeKV
		if err := json.Unmarshal(sc.Bytes(), &kv); err != nil {
			return 0, err
		}
		if kv.ModRevision <= h.Base || kv.Time == 0 {
			continue
		}
		if kv.Time > at {
			break
		}
		rev = kv.ModRevision
	}
	return rev, sc.Err()
}
