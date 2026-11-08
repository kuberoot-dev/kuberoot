package resourcestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
)

// journalStore writes a history with compactions in the middle, the
// first without the tape and the second with it, a deletion range, a key attached to a lease and a key written
// again after its deletion: revisions 2 to 11, revision r at r seconds.
func journalStore(t *testing.T) *Store {
	t.Helper()
	s := openStore(t)
	var i uint64
	entry := func(at int64, txn *TxnRequest) logEntry {
		i++
		return logEntry{Index: i, Req: &request{From: 1, ID: i, At: at, Txn: txn}}
	}
	put := func(at int64, key, value string, lease int64) logEntry {
		return entry(at, &TxnRequest{Success: []Op{{Key: []byte(key), Value: []byte(value), Lease: lease}}})
	}
	del := func(at int64, key, end string) logEntry {
		op := Op{Key: []byte(key), Delete: true}
		if end != "" {
			op.End = []byte(end)
		}
		return entry(at, &TxnRequest{Success: []Op{op}})
	}
	i++
	grant := logEntry{Index: i, Req: &request{From: 1, ID: i, LeaseGrant: &Lease{ID: 77, TTL: 30}}}
	entries := []logEntry{
		grant,
		put(2000, "/a", "1", 0),   // 2
		put(3000, "/b", "x", 0),   // 3
		put(4000, "/a", "2", 0),   // 4
		put(5000, "/c/1", "y", 0), // 5
		put(6000, "/c/2", "z", 0), // 6
	}
	// Compacted without the tape first: the tape starts at 4.
	i++
	entries = append(entries, compactEntry(i, 4, false))
	entries = append(entries,
		del(7000, "/c/", "/c0"),       // 7: both /c keys
		put(8000, "/b", "x2", 0),      // 8
		put(9000, "/l", "leased", 77), // 9
	)
	i++
	entries = append(entries, compactEntry(i, 8, true))
	entries = append(entries,
		del(10000, "/b", ""),           // 10
		put(11000, "/c/1", "again", 0), // 11
	)
	if _, err := s.Apply(entries); err != nil {
		t.Fatal(err)
	}
	return s
}

// tapeFile writes the store's tape as a tape file: the base at where the
// tape starts, then the journal, in pages of limit.
func tapeFile(t *testing.T, s *Store, limit int64) []byte {
	t.Helper()
	info := s.Tape()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	_ = enc.Encode(TapeFileHeader{Format: TapeFileFormat, Base: info.From, Time: s.TimeOf(info.From)})
	base, err := s.TapeRange([]byte{0}, []byte{0}, info.From, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range base.KVs {
		_ = enc.Encode(s.tapeKV(kv))
	}
	after := info.From
	for {
		page, err := s.Journal(after, limit)
		if err != nil {
			t.Fatal(err)
		}
		for _, kv := range page.KVs {
			_ = enc.Encode(s.tapeKV(kv))
		}
		after = page.Upto
		if !page.More {
			return buf.Bytes()
		}
	}
}

func TestJournalReadsWholeRevisionsInOrder(t *testing.T) {
	s := journalStore(t)
	if from := s.Tape().From; from != 4 {
		t.Fatalf("the tape starts at %d", from)
	}
	if _, err := s.Journal(3, 0); !errors.Is(err, ErrBeforeTape) {
		t.Fatalf("before the tape: %v", err)
	}
	all, err := s.Journal(4, 0)
	if err != nil {
		t.Fatal(err)
	}
	var revs []int64
	for _, kv := range all.KVs {
		revs = append(revs, kv.ModRevision)
	}
	if want := []int64{5, 6, 7, 7, 8, 9, 10, 11}; !reflect.DeepEqual(revs, want) || all.More || all.Upto != 11 {
		t.Fatalf("journal after 4: %v more=%v upto=%d", revs, all.More, all.Upto)
	}
	// A page of three ends with the whole of revision 7.
	page, err := s.Journal(4, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.KVs) != 4 || !page.More || page.Upto != 7 {
		t.Fatalf("page: %d versions, more=%v, upto=%d", len(page.KVs), page.More, page.Upto)
	}
}

// A tape file restores a store whose tape reads as the original's did, up
// to the revision asked: every revision, every key's history, the
// graveyard, the times, the leases.
func TestRestoreTapeAtARevision(t *testing.T) {
	s := journalStore(t)
	file := tapeFile(t, s, 2)
	for _, upto := range []int64{0, 9, 7, 4} {
		dir := t.TempDir()
		info, err := RestoreTape(bytes.NewReader(file), dir, TapeRestoreOptions{Revision: upto})
		if err != nil {
			t.Fatalf("restore at %d: %v", upto, err)
		}
		want := upto
		if want == 0 {
			want = 11
		}
		if info.From != 4 || info.Current != want {
			t.Fatalf("restored at %d: %+v", upto, info)
		}
		r, err := Open(filepath.Join(dir, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		for rev := int64(4); rev <= want; rev++ {
			a, err := s.TapeRange([]byte{0}, []byte{0}, rev, 0)
			if err != nil {
				t.Fatal(err)
			}
			b, err := r.TapeRange([]byte{0}, []byte{0}, rev, 0)
			if err != nil {
				t.Fatalf("restored at %d, read at %d: %v", upto, rev, err)
			}
			if !reflect.DeepEqual(a.KVs, b.KVs) {
				t.Fatalf("restored at %d, revision %d:\n%+v\n%+v", upto, rev, a.KVs, b.KVs)
			}
			if s.TimeOf(rev) != r.TimeOf(rev) {
				t.Fatalf("revision %d: time %d, restored %d", rev, s.TimeOf(rev), r.TimeOf(rev))
			}
		}
		if _, err := r.TapeRange([]byte{0}, []byte{0}, want+1, 0); !errors.Is(err, ErrFutureRev) {
			t.Fatalf("restored at %d reads past it: %v", upto, err)
		}
		if upto == 0 {
			graves := func(st *Store) []string {
				var out []string
				for _, g := range st.Graveyard([]byte{0}, []byte{0}) {
					out = append(out, string(g.Key)+"@"+string(g.Last.Value))
				}
				return out
			}
			if a, b := graves(s), graves(r); !reflect.DeepEqual(a, b) || len(a) != 2 {
				t.Fatalf("graveyard %v, restored %v", a, b)
			}
			if got := r.History([]byte("/c/1")); len(got) != 3 {
				t.Fatalf("history of /c/1 restored: %+v", got)
			}
			if r.RevisionAt(8500) != 8 {
				t.Fatalf("the moment 8.5s: revision %d", r.RevisionAt(8500))
			}
			if ls := r.Leases(); len(ls) != 1 || ls[0].ID != 77 || ls[0].TTL != 3600 {
				t.Fatalf("leases restored: %+v", ls)
			}
			if rr, err := r.Range([]byte("/a"), nil, 0, 0, false); err != nil || len(rr.KVs) != 1 || rr.KVs[0].ModRevision != 4 {
				t.Fatalf("the etcd API keeps the revisions: %+v %v", rr, err)
			}
		}
		r.Close()
		if _, err := RestoreTape(bytes.NewReader(file), dir, TapeRestoreOptions{}); err == nil {
			t.Fatal("restored over a member")
		}
	}
	if rev, err := TapeFileRevisionAt(bytes.NewReader(file), 9500); err != nil || rev != 9 {
		t.Fatalf("revision at 9.5s: %d %v", rev, err)
	}
}

// A journal with a revision missing is refused, not restored with a hole.
func TestRestoreTapeRefusesAGap(t *testing.T) {
	s := journalStore(t)
	lines := bytes.Split(bytes.TrimSpace(tapeFile(t, s, 0)), []byte("\n"))
	var kept [][]byte
	for _, l := range lines {
		if !bytes.Contains(l, []byte(`"modRevision":8,`)) {
			kept = append(kept, l)
		}
	}
	_, err := RestoreTape(bytes.NewReader(bytes.Join(kept, []byte("\n"))), t.TempDir(), TapeRestoreOptions{})
	if err == nil {
		t.Fatal("a journal with a gap restored")
	}
}

func TestTapeHandlerServesTheJournal(t *testing.T) {
	s := journalStore(t)
	srv := httptest.NewServer(s.TapeHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/tape/v1/journal?after=8&limit=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var page TapeJournalResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.KVs) != 1 || page.KVs[0].Key != "/l" || page.KVs[0].Lease != 77 || page.KVs[0].Time != 9000 || !page.More || page.Upto != 9 {
		t.Fatalf("journal page: %+v", page)
	}
	resp2, err := http.Get(srv.URL + "/tape/v1/journal?after=1")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("before the tape: %s", resp2.Status)
	}
}
