package resourcestore

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// TapeKV is a version as the tape API returns it: the key as text, the
// value as the API server stored it (base64 in JSON).
type TapeKV struct {
	Key            string `json:"key"`
	Value          []byte `json:"value,omitempty"`
	CreateRevision int64  `json:"createRevision,omitempty"`
	ModRevision    int64  `json:"modRevision"`
	Version        int64  `json:"version,omitempty"`
	Lease          int64  `json:"lease,omitempty"`
	Deleted        bool   `json:"deleted,omitempty"`
	// Time is when the version was written, in Unix milliseconds.
	Time int64 `json:"time,omitempty"`
}

// TapeRangeResponse is a range read from the tape.
type TapeRangeResponse struct {
	Revision int64    `json:"revision"`
	Time     int64    `json:"time,omitempty"`
	Count    int64    `json:"count"`
	More     bool     `json:"more,omitempty"`
	KVs      []TapeKV `json:"kvs"`
}

// TapeJournalResponse is a page of the journal: the versions written in
// (after, upto], in revision order.
type TapeJournalResponse struct {
	After int64    `json:"after"`
	Upto  int64    `json:"upto"`
	More  bool     `json:"more,omitempty"`
	KVs   []TapeKV `json:"kvs"`
}

// TapeGrave is a deleted key in the tape API.
type TapeGrave struct {
	Key       string  `json:"key"`
	DeletedAt int64   `json:"deletedAt"`
	Time      int64   `json:"time,omitempty"`
	Last      *TapeKV `json:"last,omitempty"`
}

// TapeHandler serves the tape, read-only, as JSON:
//
//	GET /tape/v1/info
//	GET /tape/v1/range?key=K[&end=E][&rev=R|&at=RFC3339][&limit=N]
//	GET /tape/v1/history?key=K
//	GET /tape/v1/graveyard?key=K[&end=E]
//	GET /tape/v1/journal?after=R[&limit=N]
//
// end "\x00" (end=%00) reads every key from key. It reads values as the
// API server stored them, Secrets included: serve it only to whoever may
// read the store itself.
func (s *Store) TapeHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tape/v1/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.Tape())
	})
	mux.HandleFunc("GET /tape/v1/range", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rev, err := s.tapeRevision(q.Get("rev"), q.Get("at"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		limit, _ := strconv.ParseInt(q.Get("limit"), 10, 64)
		res, err := s.TapeRange([]byte(q.Get("key")), []byte(q.Get("end")), rev, limit)
		switch {
		case errors.Is(err, ErrBeforeTape), errors.Is(err, ErrFutureRev):
			http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := TapeRangeResponse{Revision: res.Rev, Time: s.TimeOf(res.Rev), Count: res.Count, More: res.More, KVs: []TapeKV{}}
		for _, kv := range res.KVs {
			out.KVs = append(out.KVs, s.tapeKV(kv))
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /tape/v1/history", func(w http.ResponseWriter, r *http.Request) {
		out := []TapeKV{}
		for _, kv := range s.History([]byte(r.URL.Query().Get("key"))) {
			out = append(out, s.tapeKV(kv))
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /tape/v1/graveyard", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out := []TapeGrave{}
		for _, g := range s.Graveyard([]byte(q.Get("key")), []byte(q.Get("end"))) {
			tg := TapeGrave{Key: string(g.Key), DeletedAt: g.DeletedAt, Time: s.TimeOf(g.DeletedAt)}
			if g.Last != nil {
				kv := s.tapeKV(KV{Key: g.Key, Version: *g.Last})
				tg.Last = &kv
			}
			out = append(out, tg)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /tape/v1/journal", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		after, err := strconv.ParseInt(q.Get("after"), 10, 64)
		if err != nil {
			http.Error(w, "after: "+err.Error(), http.StatusBadRequest)
			return
		}
		limit, _ := strconv.ParseInt(q.Get("limit"), 10, 64)
		page, err := s.Journal(after, limit)
		switch {
		case errors.Is(err, ErrBeforeTape), errors.Is(err, ErrFutureRev):
			http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := TapeJournalResponse{After: page.After, Upto: page.Upto, More: page.More, KVs: []TapeKV{}}
		for _, kv := range page.KVs {
			out.KVs = append(out.KVs, s.tapeKV(kv))
		}
		writeJSON(w, out)
	})
	return mux
}

// tapeRevision is the revision a request reads: rev, the revision at a
// moment, or the current one.
func (s *Store) tapeRevision(rev, at string) (int64, error) {
	switch {
	case rev != "":
		return strconv.ParseInt(rev, 10, 64)
	case at != "":
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return 0, err
		}
		r := s.RevisionAt(t.UnixMilli())
		if r == 0 {
			return 0, errors.New("the tape knows no revision at or before " + at)
		}
		return r, nil
	}
	return 0, nil
}

func (s *Store) tapeKV(kv KV) TapeKV {
	return TapeKV{
		Key: string(kv.Key), Value: kv.Value, CreateRevision: kv.CreateRevision,
		ModRevision: kv.ModRevision, Version: kv.Version.Version, Lease: kv.Lease, Deleted: kv.Deleted,
		Time: s.TimeOf(kv.ModRevision),
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
