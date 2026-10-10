package resourcestore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	bolt "go.etcd.io/bbolt"
)

// Restore makes dataDir the data of a new store from a snapshot (the
// Maintenance API's, a copy of a member's state): its keys, revisions and
// leases, without its members, log or applied requests. The member started
// on it starts a store of its own, as with etcdutl snapshot restore; other
// members join it.
func Restore(snapshot, dataDir string) error {
	if _, err := os.Stat(filepath.Join(dataDir, "raft.db")); err == nil {
		return fmt.Errorf("%s holds a member already", dataDir)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
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
		// The new log starts at 1: nothing of it is applied yet.
		if err := putInt(m, appliedKey, 0); err != nil {
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
		return nil
	})
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dataDir, "state.db"))
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
