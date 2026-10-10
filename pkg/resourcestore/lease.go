package resourcestore

import (
	"context"
	"io"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"google.golang.org/grpc"
)

// leases are keys with a time to live: when a lease expires, its keys are
// deleted, as events are. Kept in memory: a restart forgets them and the
// keys stay, which a lab can live with.
type leases struct {
	store *Store
	mu    sync.Mutex
	next  int64
	items map[int64]*lease
}

type lease struct {
	ttl     int64
	expires time.Time
	keys    map[string]bool
}

func newLeases(s *Store) *leases { return &leases{store: s, items: map[int64]*lease{}} }

func (l *leases) attach(id int64, key []byte) {
	if l == nil || id == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if le, ok := l.items[id]; ok {
		le.keys[string(key)] = true
	}
}

// expire deletes the keys of leases past their time, once a second.
func (l *leases) expire() {
	for range time.Tick(time.Second) {
		var due [][]byte
		l.mu.Lock()
		for id, le := range l.items {
			if time.Now().After(le.expires) {
				for k := range le.keys {
					due = append(due, []byte(k))
				}
				delete(l.items, id)
			}
		}
		l.mu.Unlock()
		if len(due) == 0 {
			continue
		}
		var ops []Op
		for _, k := range due {
			ops = append(ops, Op{Key: k, Delete: true})
		}
		_, _ = l.store.Txn(nil, ops, nil)
	}
}

func (s *Server) LeaseGrant(_ context.Context, r *etcdserverpb.LeaseGrantRequest) (*etcdserverpb.LeaseGrantResponse, error) {
	l := s.leases
	l.mu.Lock()
	defer l.mu.Unlock()
	id := r.ID
	if id == 0 {
		l.next++
		id = time.Now().UnixNano()&0x7fffffffffff<<16 | l.next&0xffff
	}
	l.items[id] = &lease{ttl: r.TTL, expires: time.Now().Add(time.Duration(r.TTL) * time.Second), keys: map[string]bool{}}
	cur, _ := s.Store.Revisions()
	return &etcdserverpb.LeaseGrantResponse{Header: s.header(cur), ID: id, TTL: r.TTL}, nil
}

func (s *Server) LeaseRevoke(_ context.Context, r *etcdserverpb.LeaseRevokeRequest) (*etcdserverpb.LeaseRevokeResponse, error) {
	l := s.leases
	l.mu.Lock()
	le, ok := l.items[r.ID]
	delete(l.items, r.ID)
	l.mu.Unlock()
	if !ok {
		return nil, rpctypes.ErrGRPCLeaseNotFound
	}
	var ops []Op
	for k := range le.keys {
		ops = append(ops, Op{Key: []byte(k), Delete: true})
	}
	res, err := s.Store.Txn(nil, ops, nil)
	if err != nil {
		return nil, grpcErr(err)
	}
	return &etcdserverpb.LeaseRevokeResponse{Header: s.header(res.Revision)}, nil
}

func (s *Server) LeaseKeepAlive(stream grpc.BidiStreamingServer[etcdserverpb.LeaseKeepAliveRequest, etcdserverpb.LeaseKeepAliveResponse]) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		l := s.leases
		l.mu.Lock()
		le, ok := l.items[req.ID]
		var ttl int64
		if ok {
			le.expires = time.Now().Add(time.Duration(le.ttl) * time.Second)
			ttl = le.ttl
		}
		l.mu.Unlock()
		cur, _ := s.Store.Revisions()
		if err := stream.Send(&etcdserverpb.LeaseKeepAliveResponse{Header: s.header(cur), ID: req.ID, TTL: ttl}); err != nil {
			return err
		}
	}
}

func (s *Server) LeaseTimeToLive(_ context.Context, r *etcdserverpb.LeaseTimeToLiveRequest) (*etcdserverpb.LeaseTimeToLiveResponse, error) {
	l := s.leases
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, _ := s.Store.Revisions()
	le, ok := l.items[r.ID]
	if !ok {
		return &etcdserverpb.LeaseTimeToLiveResponse{Header: s.header(cur), ID: r.ID, TTL: -1}, nil
	}
	resp := &etcdserverpb.LeaseTimeToLiveResponse{Header: s.header(cur), ID: r.ID, GrantedTTL: le.ttl,
		TTL: int64(time.Until(le.expires).Seconds())}
	if r.Keys {
		for k := range le.keys {
			resp.Keys = append(resp.Keys, []byte(k))
		}
	}
	return resp, nil
}

func (s *Server) LeaseLeases(context.Context, *etcdserverpb.LeaseLeasesRequest) (*etcdserverpb.LeaseLeasesResponse, error) {
	l := s.leases
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, _ := s.Store.Revisions()
	resp := &etcdserverpb.LeaseLeasesResponse{Header: s.header(cur)}
	for id := range l.items {
		resp.Leases = append(resp.Leases, &etcdserverpb.LeaseStatus{ID: id})
	}
	return resp, nil
}
