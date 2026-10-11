package resourcestore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	bolt "go.etcd.io/bbolt"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

// Restore makes dataDir the data of member name of a new store, whose
// members initialCluster lists (name -> peer URLs), from a snapshot (the
// Maintenance API's, a copy of a member's state): its keys, revisions and
// leases, without the old members, log or applied requests; as etcdutl
// snapshot restore does.
//
// The new log starts with a Raft snapshot of that state, not empty: a
// member that joins later must get the state, not replay a log that holds
// none of what was restored.
func Restore(snapshot, dataDir, name string, initialCluster map[string][]string, token string) error {
	if token == "" {
		token = "etcd-cluster"
	}
	if _, ok := initialCluster[name]; !ok {
		return fmt.Errorf("the initial cluster has no member %q", name)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "raft.db")); err == nil {
		return fmt.Errorf("%s holds a member already", dataDir)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	var members []Member
	var ids []uint64
	var self uint64
	for n, urls := range initialCluster {
		id := memberID(urls, token, nil)
		members = append(members, Member{ID: id, Name: n, PeerURLs: urls})
		ids = append(ids, id)
		if n == name {
			self = id
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	voters := append([]uint64(nil), ids...)
	cluster := clusterIDOf(ids)

	// The log's snapshot is at index 1, of term 1; the state has applied it.
	const index, term = 1, 1
	tmp := filepath.Join(dataDir, "state.db.restore")
	if err := copyFile(snapshot, tmp); err != nil {
		return err
	}
	db, err := bolt.Open(tmp, 0o600, nil)
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%s: %w", snapshot, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		m := tx.Bucket(metaBucket)
		if m == nil || getInt(m, currentKey) == 0 {
			return fmt.Errorf("%s is not a snapshot of the store", snapshot)
		}
		if err := putInt(m, appliedKey, index); err != nil {
			return err
		}
		for _, b := range [][]byte{membersBucket, removedBucket, requestsBucket} {
			if tx.Bucket(b) != nil {
				if err := tx.DeleteBucket(b); err != nil {
					return err
				}
			}
			if _, err := tx.CreateBucket(b); err != nil {
				return err
			}
		}
		for _, mb := range members {
			if err := putMember(tx, mb); err != nil {
				return err
			}
		}
		return nil
	})
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = writeRaftStart(filepath.Join(dataDir, "raft.db"), self, cluster, index, term, voters)
	}
	if err != nil {
		os.Remove(tmp)
		os.Remove(filepath.Join(dataDir, "raft.db"))
		return err
	}
	return os.Rename(tmp, filepath.Join(dataDir, "state.db"))
}

// writeRaftStart writes a Raft log that starts with a snapshot at index.
func writeRaftStart(path string, self, cluster, index, term uint64, voters []uint64) error {
	if self == 0 {
		return errors.New("no member ID")
	}
	l, err := openRaftLog(path)
	if err != nil {
		return err
	}
	defer l.Close()
	if err := l.setIdentity(self, cluster); err != nil {
		return err
	}
	snap := &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{
		Index: proto.Uint64(index), Term: proto.Uint64(term), ConfState: &raftpb.ConfState{Voters: voters}}}
	hs := &raftpb.HardState{Term: proto.Uint64(term), Vote: proto.Uint64(0), Commit: proto.Uint64(index)}
	return l.save(hs, nil, snap)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
