package resourcestore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// tapeEntries write /a three times and /b once, delete /b, one second
// apart: revision r at (r-1) seconds, revisions 2 to 6.
func tapeEntries() []logEntry {
	put := func(i uint64, key, value string) logEntry {
		e := putEntry(i, 1, i, key, value)
		e.Req.At = int64(i) * 1000
		return e
	}
	del := logEntry{Index: 4, Req: &request{From: 1, ID: 4, At: 4000,
		Txn: &TxnRequest{Success: []Op{{Key: []byte("/b"), Delete: true}}}}}
	return []logEntry{put(1, "/a", "1"), put(2, "/a", "2"), put(3, "/b", "x"), del, put(5, "/a", "3")}
}

func compactEntry(i uint64, rev int64, archive bool) logEntry {
	return logEntry{Index: i, Req: &request{From: 1, ID: i, Compact: rev, Archive: archive}}
}

// A compaction that archives keeps the etcd API's answers and the tape
// reads behind them.
func TestTapeReadsWhatCompactionArchived(t *testing.T) {
	s := openStore(t)
	if _, err := s.Apply(append(tapeEntries(), compactEntry(6, 5, true))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Range([]byte("/a"), nil, 3, 0, false); err != ErrCompacted {
		t.Fatalf("the etcd API at a compacted revision: %v", err)
	}
	at := func(rev int64) map[string]string {
		t.Helper()
		rr, err := s.TapeRange([]byte("/"), []byte{0}, rev, 0)
		if err != nil {
			t.Fatalf("tape at %d: %v", rev, err)
		}
		out := map[string]string{}
		for _, kv := range rr.KVs {
			out[string(kv.Key)] = string(kv.Value)
		}
		return out
	}
	if got := at(3); len(got) != 1 || got["/a"] != "2" {
		t.Fatalf("at 3: %v", got)
	}
	if got := at(4); len(got) != 2 || got["/b"] != "x" {
		t.Fatalf("at 4: %v", got)
	}
	if got := at(5); len(got) != 1 || got["/a"] != "2" {
		t.Fatalf("at 5, /b deleted: %v", got)
	}
	if got := at(0); got["/a"] != "3" {
		t.Fatalf("now: %v", got)
	}
	if h := s.History([]byte("/a")); len(h) != 3 || string(h[0].Value) != "1" || string(h[2].Value) != "3" {
		t.Fatalf("history of /a: %+v", h)
	}
	if h := s.History([]byte("/b")); len(h) != 2 || !h[1].Deleted {
		t.Fatalf("history of /b: %+v", h)
	}
	g := s.Graveyard([]byte("/"), []byte{0})
	if len(g) != 1 || string(g[0].Key) != "/b" || g[0].DeletedAt != 5 || g[0].Last == nil || string(g[0].Last.Value) != "x" {
		t.Fatalf("graveyard: %+v", g)
	}
	if r := s.RevisionAt(2500); r != 3 {
		t.Fatalf("revision at 2.5 s: %d", r)
	}
	if info := s.Tape(); !info.Started || info.From != 1 || info.Compacted != 5 || info.CurrentTime != 5000 {
		t.Fatalf("tape: %+v", info)
	}
}

// Without the tape a compaction deletes as before, and the graveyard
// forgets what it deleted.
func TestCompactionWithoutTapeDeletes(t *testing.T) {
	s := openStore(t)
	if _, err := s.Apply(append(tapeEntries(), compactEntry(6, 5, false))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TapeRange([]byte("/a"), nil, 3, 0); err != ErrBeforeTape {
		t.Fatalf("before the tape: %v", err)
	}
	// Its newest version at the compacted revision, and the one after.
	if h := s.History([]byte("/a")); len(h) != 2 {
		t.Fatalf("history after a deleting compaction: %+v", h)
	}
	if g := s.Graveyard([]byte("/"), []byte{0}); len(g) != 0 {
		t.Fatalf("graveyard: %+v", g)
	}
}

// The tape starts where the store was compacted when it first archived.
func TestTapeStartsAtItsFirstArchive(t *testing.T) {
	s := openStore(t)
	if _, err := s.Apply(append(tapeEntries(), compactEntry(6, 3, false), compactEntry(7, 5, true))); err != nil {
		t.Fatal(err)
	}
	if info := s.Tape(); info.From != 3 {
		t.Fatalf("tape from %d, want 3", info.From)
	}
	if rr, err := s.TapeRange([]byte("/b"), nil, 4, 0); err != nil || len(rr.KVs) != 1 {
		t.Fatalf("/b at 4: %+v %v", rr, err)
	}
}

// A key written again leaves the graveyard.
func TestRewrittenKeyLeavesTheGraveyard(t *testing.T) {
	s := openStore(t)
	if _, err := s.Apply(append(tapeEntries(), putEntry(6, 1, 6, "/b", "y"))); err != nil {
		t.Fatal(err)
	}
	if g := s.Graveyard([]byte("/"), []byte{0}); len(g) != 0 {
		t.Fatalf("graveyard: %+v", g)
	}
}

func TestTapeHTTP(t *testing.T) {
	s := openStore(t)
	if _, err := s.Apply(append(tapeEntries(), compactEntry(6, 5, true))); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.TapeHandler())
	defer srv.Close()
	get := func(path string, v any) int {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode
	}
	var rr TapeRangeResponse
	at := time.UnixMilli(2500).UTC().Format(time.RFC3339)
	// RFC 3339 here has whole seconds: 2.5 s reads as 2 s, revision 3.
	if code := get("/tape/v1/range?key=/&end=%00&at="+at, &rr); code != 200 || rr.Revision != 3 || len(rr.KVs) != 1 || string(rr.KVs[0].Value) != "2" {
		t.Fatalf("range at %s: %d %+v", at, code, rr)
	}
	var graves []TapeGrave
	if code := get("/tape/v1/graveyard?key=/&end=%00", &graves); code != 200 || len(graves) != 1 || graves[0].Time != 4000 {
		t.Fatalf("graveyard: %d %+v", code, graves)
	}
	if code := get("/tape/v1/range?key=/a&rev=99", &rr); code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("a future revision: %d", code)
	}
}

// Retention cuts the tape at the revision written at the moment it keeps
// from: each key keeps its version at the cut, deletions before the cut
// leave the graveyard.
func TestTapeRetentionCutsTheOldestHistory(t *testing.T) {
	s := openStore(t)
	retain := func(i uint64, rev, at, keep int64) logEntry {
		e := compactEntry(i, rev, true)
		e.Req.At, e.Req.Retain = at, keep
		return e
	}
	put := putEntry(7, 1, 7, "/c", "1")
	put.Req.At = 6000
	// The cut: the revision written at or before 8000-4500 = 3500 ms,
	// revision 4 (/b written at 3000 ms).
	if _, err := s.Apply(append(tapeEntries(), compactEntry(6, 5, true), put, retain(8, 7, 8000, 4500))); err != nil {
		t.Fatal(err)
	}
	if info := s.Tape(); info.From != 4 {
		t.Fatalf("tape from %d, want 4", info.From)
	}
	if _, err := s.TapeRange([]byte("/"), []byte{0}, 3, 0); err != ErrBeforeTape {
		t.Fatalf("before the cut: %v", err)
	}
	rr, err := s.TapeRange([]byte("/"), []byte{0}, 4, 0)
	if err != nil || len(rr.KVs) != 2 || string(rr.KVs[0].Value) != "2" || string(rr.KVs[1].Value) != "x" {
		t.Fatalf("at the cut: %+v %v", rr.KVs, err)
	}
	if h := s.History([]byte("/a")); len(h) != 2 || h[0].ModRevision != 3 {
		t.Fatalf("history of /a after the cut: %+v", h)
	}
	if g := s.Graveyard([]byte("/"), []byte{0}); len(g) != 1 {
		t.Fatalf("a deletion after the cut stays in the graveyard: %+v", g)
	}
	// Cut past the deletion of /b (revision 5, at 4000 ms): /b is gone
	// from the tape and the graveyard.
	put2 := putEntry(9, 1, 9, "/c", "2")
	put2.Req.At = 9000
	if _, err := s.Apply([]logEntry{put2, retain(10, 8, 10000, 6000)}); err != nil {
		t.Fatal(err)
	}
	if h := s.History([]byte("/b")); len(h) != 0 {
		t.Fatalf("history of /b: %+v", h)
	}
	if g := s.Graveyard([]byte("/"), []byte{0}); len(g) != 0 {
		t.Fatalf("graveyard: %+v", g)
	}
	if r := s.RevisionAt(1500); r != 0 {
		t.Fatalf("times before the cut stay: revision %d at 1.5 s", r)
	}
}
