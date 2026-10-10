package resourcestore

import (
	"path/filepath"
	"testing"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func putEntry(index, from, id uint64, key, value string) logEntry {
	return logEntry{Index: index, Req: &request{From: from, ID: id,
		Txn: &TxnRequest{Success: []Op{{Key: []byte(key), Value: []byte(value)}}}}}
}

// A request proposed again after a change of leader can commit twice; it
// applies once.
func TestARequestAppliesOnce(t *testing.T) {
	s := openStore(t)
	res, err := s.Apply([]logEntry{putEntry(1, 7, 100, "/a", "1"), putEntry(2, 7, 100, "/a", "1"), putEntry(3, 8, 100, "/b", "1")})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Duplicate || !res[1].Duplicate || res[2].Duplicate {
		t.Fatalf("duplicates: %v %v %v", res[0].Duplicate, res[1].Duplicate, res[2].Duplicate)
	}
	rr, _ := s.Range([]byte("/a"), nil, 0, 0, false)
	if len(rr.KVs) != 1 || rr.KVs[0].Version.Version != 1 {
		t.Fatalf("/a: %+v", rr.KVs)
	}
	if cur, _ := s.Revisions(); cur != 3 {
		t.Fatalf("revision %d, want 3: one write each for /a and /b after 1", cur)
	}
}

// Entries a state holds already, from an earlier run or a snapshot, are
// not applied again.
func TestApplySkipsWhatTheStateHolds(t *testing.T) {
	s := openStore(t)
	if _, err := s.Apply([]logEntry{putEntry(1, 1, 1, "/a", "1"), putEntry(2, 1, 2, "/a", "2")}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Apply([]logEntry{putEntry(2, 1, 2, "/a", "2"), putEntry(3, 1, 3, "/a", "3")})
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Duplicate || res[1].Duplicate {
		t.Fatalf("replay: %+v", res)
	}
	rr, _ := s.Range([]byte("/a"), nil, 0, 0, false)
	if string(rr.KVs[0].Value) != "3" || rr.KVs[0].Version.Version != 3 || s.Applied() != 3 {
		t.Fatalf("/a %+v, applied %d", rr.KVs[0], s.Applied())
	}
}

// A failed request leaves the state as it was, and the log goes on.
func TestAFailedRequestChangesNothing(t *testing.T) {
	s := openStore(t)
	e := logEntry{Index: 1, Req: &request{From: 1, ID: 1, Txn: &TxnRequest{Success: []Op{
		{Key: []byte("/a"), Value: []byte("1")},
		{Key: []byte("/b"), Value: []byte("1"), Lease: 42},
	}}}}
	res, err := s.Apply([]logEntry{e})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err != ErrLeaseNotFound {
		t.Fatalf("a put to a missing lease: %v", res[0].Err)
	}
	if rr, _ := s.Range([]byte("/a"), nil, 0, 0, false); len(rr.KVs) != 0 {
		t.Fatal("a failed transaction wrote")
	}
	if s.Applied() != 1 {
		t.Fatalf("applied %d", s.Applied())
	}
}

// Revoking a lease deletes its keys at one revision; a key that moved to
// another lease stays.
func TestLeaseRevocationDeletesItsKeys(t *testing.T) {
	s := openStore(t)
	grant := func(i uint64, id int64) logEntry {
		return logEntry{Index: i, Req: &request{From: 1, ID: i, LeaseGrant: &Lease{ID: id, TTL: 60}}}
	}
	put := func(i uint64, key string, lease int64) logEntry {
		return logEntry{Index: i, Req: &request{From: 1, ID: i, Txn: &TxnRequest{Success: []Op{{Key: []byte(key), Value: []byte("v"), Lease: lease}}}}}
	}
	_, err := s.Apply([]logEntry{grant(1, 5), grant(2, 6), put(3, "/x", 5), put(4, "/y", 5), put(5, "/y", 6),
		{Index: 6, Req: &request{From: 1, ID: 6, LeaseRevoke: 5}}})
	if err != nil {
		t.Fatal(err)
	}
	if rr, _ := s.Range([]byte("/"), []byte{0}, 0, 0, false); len(rr.KVs) != 1 || string(rr.KVs[0].Key) != "/y" {
		t.Fatalf("after the revocation: %+v", rr.KVs)
	}
	if _, _, ok := s.LeaseOf(5); ok {
		t.Fatal("the revoked lease is still there")
	}
	if _, keys, _ := s.LeaseOf(6); len(keys) != 1 {
		t.Fatalf("lease 6 keys: %q", keys)
	}
}
