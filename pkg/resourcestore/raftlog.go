package resourcestore

import (
	"encoding/binary"
	"errors"

	bolt "go.etcd.io/bbolt"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

// raftLog keeps what Raft must not forget across a restart: the log, the
// hard state (term, vote, commit) and the last snapshot's metadata, in a
// bbolt file of its own next to the state.
type raftLog struct {
	db *bolt.DB
}

var (
	entriesBucket  = []byte("entries")
	raftMetaBucket = []byte("raft")
	hardStateKey   = []byte("hardstate")
	snapshotKey    = []byte("snapshot")
	memberIDKey    = []byte("member")
	clusterIDKey   = []byte("cluster")
)

func openRaftLog(path string) (*raftLog, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{entriesBucket, raftMetaBucket} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &raftLog{db: db}, nil
}

func (l *raftLog) Close() error { return l.db.Close() }

func indexKey(i uint64) []byte { return binary.BigEndian.AppendUint64(nil, i) }

// identity is this member's ID and its cluster's, 0 before it has one.
func (l *raftLog) identity() (member, cluster uint64) {
	_ = l.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(raftMetaBucket)
		member, cluster = uint64(getInt(b, memberIDKey)), uint64(getInt(b, clusterIDKey))
		return nil
	})
	return member, cluster
}

func (l *raftLog) setIdentity(member, cluster uint64) error {
	return l.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(raftMetaBucket)
		if err := putInt(b, memberIDKey, int64(member)); err != nil {
			return err
		}
		return putInt(b, clusterIDKey, int64(cluster))
	})
}

// save persists one Ready's hard state, entries and snapshot, in one
// synced write: Raft may send nothing that depends on them before.
func (l *raftLog) save(hs *raftpb.HardState, ents []*raftpb.Entry, snap *raftpb.Snapshot) error {
	if raft.IsEmptyHardState(hs) && len(ents) == 0 && raft.IsEmptySnap(snap) {
		return nil
	}
	return l.db.Update(func(tx *bolt.Tx) error {
		b, eb := tx.Bucket(raftMetaBucket), tx.Bucket(entriesBucket)
		if !raft.IsEmptySnap(snap) {
			raw, err := proto.Marshal(&raftpb.Snapshot{Metadata: snap.GetMetadata()})
			if err != nil {
				return err
			}
			if err := b.Put(snapshotKey, raw); err != nil {
				return err
			}
			if err := deleteBelow(eb, snap.GetMetadata().GetIndex()+1); err != nil {
				return err
			}
		}
		if len(ents) > 0 {
			// New entries replace any the log has from the first one on.
			if err := deleteFrom(eb, ents[0].GetIndex()); err != nil {
				return err
			}
			for _, e := range ents {
				raw, err := proto.Marshal(e)
				if err != nil {
					return err
				}
				if err := eb.Put(indexKey(e.GetIndex()), raw); err != nil {
					return err
				}
			}
		}
		if !raft.IsEmptyHardState(hs) {
			raw, err := proto.Marshal(hs)
			if err != nil {
				return err
			}
			if err := b.Put(hardStateKey, raw); err != nil {
				return err
			}
		}
		return nil
	})
}

// saveSnapshot records a snapshot taken of the state, and drops the log
// before compactTo, which the snapshot holds.
func (l *raftLog) saveSnapshot(snap *raftpb.Snapshot, compactTo uint64) error {
	return l.db.Update(func(tx *bolt.Tx) error {
		raw, err := proto.Marshal(&raftpb.Snapshot{Metadata: snap.GetMetadata()})
		if err != nil {
			return err
		}
		if err := tx.Bucket(raftMetaBucket).Put(snapshotKey, raw); err != nil {
			return err
		}
		return deleteBelow(tx.Bucket(entriesBucket), compactTo)
	})
}

func deleteBelow(b *bolt.Bucket, index uint64) error {
	c := b.Cursor()
	for k, _ := c.First(); k != nil && binary.BigEndian.Uint64(k) < index; k, _ = c.First() {
		if err := c.Delete(); err != nil {
			return err
		}
	}
	return nil
}

func deleteFrom(b *bolt.Bucket, index uint64) error {
	c := b.Cursor()
	for k, _ := c.Seek(indexKey(index)); k != nil; k, _ = c.Seek(indexKey(index)) {
		if err := c.Delete(); err != nil {
			return err
		}
	}
	return nil
}

// load reads the log back into a memory storage for Raft; empty tells a
// member that has never run.
func (l *raftLog) load() (st *raft.MemoryStorage, empty bool, err error) {
	st = raft.NewMemoryStorage()
	err = l.db.View(func(tx *bolt.Tx) error {
		b, eb := tx.Bucket(raftMetaBucket), tx.Bucket(entriesBucket)
		snap := &raftpb.Snapshot{}
		if raw := b.Get(snapshotKey); raw != nil {
			if err := proto.Unmarshal(raw, snap); err != nil {
				return err
			}
			if err := st.ApplySnapshot(snap); err != nil {
				return err
			}
		}
		hs := &raftpb.HardState{}
		raw := b.Get(hardStateKey)
		if raw != nil {
			if err := proto.Unmarshal(raw, hs); err != nil {
				return err
			}
		}
		var ents []*raftpb.Entry
		snapIndex := snap.GetMetadata().GetIndex()
		err := eb.ForEach(func(k, v []byte) error {
			if binary.BigEndian.Uint64(k) <= snapIndex {
				return nil
			}
			e := &raftpb.Entry{}
			if err := proto.Unmarshal(v, e); err != nil {
				return err
			}
			ents = append(ents, e)
			return nil
		})
		if err != nil {
			return err
		}
		empty = raw == nil && len(ents) == 0 && raft.IsEmptySnap(snap)
		if err := st.SetHardState(hs); err != nil {
			return err
		}
		if len(ents) > 0 {
			if ents[0].GetIndex() != snapIndex+1 {
				return errors.New("raft log: entries do not follow the snapshot")
			}
			return st.Append(ents)
		}
		return nil
	})
	return st, empty, err
}
