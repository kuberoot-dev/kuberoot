package resourcestore

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server is the store behind the etcd v3 API: KV, Watch, Lease and the
// Maintenance status the API server asks for.
type Server struct {
	etcdserverpb.UnimplementedKVServer
	etcdserverpb.UnimplementedWatchServer
	etcdserverpb.UnimplementedLeaseServer
	etcdserverpb.UnimplementedMaintenanceServer
	etcdserverpb.UnimplementedClusterServer

	Store *Store
	// ClusterID and MemberID identify this single member.
	ClusterID, MemberID uint64

	leases *leases
}

// Register serves the store on a gRPC server.
func (s *Server) Register(g *grpc.Server) {
	if s.leases == nil {
		s.leases = newLeases(s.Store)
		go s.leases.expire()
	}
	etcdserverpb.RegisterKVServer(g, s)
	etcdserverpb.RegisterWatchServer(g, s)
	etcdserverpb.RegisterLeaseServer(g, s)
	etcdserverpb.RegisterMaintenanceServer(g, s)
	etcdserverpb.RegisterClusterServer(g, s)
}

func (s *Server) header(rev int64) *etcdserverpb.ResponseHeader {
	return &etcdserverpb.ResponseHeader{ClusterId: s.ClusterID, MemberId: s.MemberID, Revision: rev, RaftTerm: 1}
}

func toPB(kv KV, keysOnly bool) *mvccpb.KeyValue {
	out := &mvccpb.KeyValue{Key: kv.Key, CreateRevision: kv.CreateRevision, ModRevision: kv.ModRevision,
		Version: kv.Version.Version, Lease: kv.Lease}
	if !keysOnly {
		out.Value = kv.Value
	}
	return out
}

func grpcErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrCompacted):
		return rpctypes.ErrGRPCCompacted
	case errors.Is(err, ErrFutureRev):
		return rpctypes.ErrGRPCFutureRev
	}
	return status.Error(codes.Internal, err.Error())
}

func (s *Server) Range(_ context.Context, r *etcdserverpb.RangeRequest) (*etcdserverpb.RangeResponse, error) {
	kvs, count, rev, more, err := s.Store.Range(r.Key, r.RangeEnd, r.Revision, r.Limit, r.CountOnly)
	if err != nil {
		return nil, grpcErr(err)
	}
	resp := &etcdserverpb.RangeResponse{Header: s.header(rev), Count: count, More: more}
	if r.Revision == 0 {
		cur, _ := s.Store.Revisions()
		resp.Header.Revision = cur
	}
	for _, kv := range kvs {
		resp.Kvs = append(resp.Kvs, toPB(kv, r.KeysOnly))
	}
	return resp, nil
}

// RangeStream is Range in chunks, as the API server lists large prefixes
// from etcd 3.7: the keys in pieces, then the header, more and count last.
func (s *Server) RangeStream(r *etcdserverpb.RangeRequest, stream grpc.ServerStreamingServer[etcdserverpb.RangeStreamResponse]) error {
	resp, err := s.Range(stream.Context(), r)
	if err != nil {
		return err
	}
	const chunk = 500
	kvs := resp.Kvs
	for len(kvs) > chunk {
		if err := stream.Send(&etcdserverpb.RangeStreamResponse{RangeResponse: &etcdserverpb.RangeResponse{Kvs: kvs[:chunk]}}); err != nil {
			return err
		}
		kvs = kvs[chunk:]
	}
	resp.Kvs = kvs
	return stream.Send(&etcdserverpb.RangeStreamResponse{RangeResponse: resp})
}

func (s *Server) Put(_ context.Context, r *etcdserverpb.PutRequest) (*etcdserverpb.PutResponse, error) {
	res, err := s.Store.Txn(nil, []Op{{Key: r.Key, Value: r.Value, Lease: r.Lease}}, nil)
	if err != nil {
		return nil, grpcErr(err)
	}
	s.leases.attach(r.Lease, r.Key)
	resp := &etcdserverpb.PutResponse{Header: s.header(res.Revision)}
	if r.PrevKv && len(res.Prev) > 0 && res.Prev[0] != nil {
		resp.PrevKv = toPB(*res.Prev[0], false)
	}
	return resp, nil
}

func (s *Server) DeleteRange(_ context.Context, r *etcdserverpb.DeleteRangeRequest) (*etcdserverpb.DeleteRangeResponse, error) {
	res, err := s.Store.Txn(nil, []Op{{Key: r.Key, End: r.RangeEnd, Delete: true}}, nil)
	if err != nil {
		return nil, grpcErr(err)
	}
	resp := &etcdserverpb.DeleteRangeResponse{Header: s.header(res.Revision)}
	if len(res.Deleted) > 0 {
		resp.Deleted = res.Deleted[0]
	}
	if r.PrevKv {
		for _, p := range res.Prev {
			if p != nil {
				resp.PrevKvs = append(resp.PrevKvs, toPB(*p, false))
			}
		}
	}
	return resp, nil
}

func compareOf(c *etcdserverpb.Compare) Compare {
	out := Compare{Key: c.Key}
	switch c.Result {
	case etcdserverpb.Compare_EQUAL:
		out.Result = "=="
	case etcdserverpb.Compare_NOT_EQUAL:
		out.Result = "!="
	case etcdserverpb.Compare_GREATER:
		out.Result = ">"
	case etcdserverpb.Compare_LESS:
		out.Result = "<"
	}
	switch c.Target {
	case etcdserverpb.Compare_MOD:
		out.Target, out.Int = "mod", c.GetModRevision()
	case etcdserverpb.Compare_CREATE:
		out.Target, out.Int = "create", c.GetCreateRevision()
	case etcdserverpb.Compare_VERSION:
		out.Target, out.Int = "version", c.GetVersion()
	case etcdserverpb.Compare_VALUE:
		out.Target, out.Value = "value", c.GetValue()
	}
	return out
}

// opsOf turns a transaction branch into writes; ranges in it are read at
// the revision the transaction commits from.
func opsOf(reqs []*etcdserverpb.RequestOp) ([]Op, []*etcdserverpb.RangeRequest) {
	var ops []Op
	var ranges []*etcdserverpb.RangeRequest
	for _, r := range reqs {
		switch {
		case r.GetRequestPut() != nil:
			p := r.GetRequestPut()
			ops = append(ops, Op{Key: p.Key, Value: p.Value, Lease: p.Lease})
		case r.GetRequestDeleteRange() != nil:
			d := r.GetRequestDeleteRange()
			ops = append(ops, Op{Key: d.Key, End: d.RangeEnd, Delete: true})
		case r.GetRequestRange() != nil:
			ranges = append(ranges, r.GetRequestRange())
		}
	}
	return ops, ranges
}

// Txn serves what the API server sends: compare one key, then write it or
// read it. Reads and writes in one branch are not mixed by the API server;
// a branch with only reads reads after the comparison.
func (s *Server) Txn(ctx context.Context, r *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error) {
	var cmps []Compare
	for _, c := range r.Compare {
		cmps = append(cmps, compareOf(c))
	}
	sOps, sRanges := opsOf(r.Success)
	fOps, fRanges := opsOf(r.Failure)
	res, err := s.Store.Txn(cmps, sOps, fOps)
	if err != nil {
		return nil, grpcErr(err)
	}
	branch, ranges, ops := r.Success, sRanges, sOps
	if !res.Succeeded {
		branch, ranges, ops = r.Failure, fRanges, fOps
	}
	_ = ops
	resp := &etcdserverpb.TxnResponse{Header: s.header(res.Revision), Succeeded: res.Succeeded}
	ri, pi := 0, 0
	for _, op := range branch {
		switch {
		case op.GetRequestRange() != nil:
			rr, err := s.Range(ctx, ranges[ri])
			ri++
			if err != nil {
				return nil, err
			}
			resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponseRange{ResponseRange: rr}})
		case op.GetRequestPut() != nil:
			p := op.GetRequestPut()
			s.leases.attach(p.Lease, p.Key)
			pr := &etcdserverpb.PutResponse{Header: s.header(res.Revision)}
			if p.PrevKv && pi < len(res.Prev) && res.Prev[pi] != nil {
				pr.PrevKv = toPB(*res.Prev[pi], false)
			}
			pi++
			resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponsePut{ResponsePut: pr}})
		case op.GetRequestDeleteRange() != nil:
			dr := &etcdserverpb.DeleteRangeResponse{Header: s.header(res.Revision)}
			if len(res.Deleted) > 0 {
				dr.Deleted = res.Deleted[0]
			}
			if op.GetRequestDeleteRange().PrevKv {
				for ; pi < len(res.Prev); pi++ {
					if res.Prev[pi] != nil {
						dr.PrevKvs = append(dr.PrevKvs, toPB(*res.Prev[pi], false))
					}
				}
			}
			resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponseDeleteRange{ResponseDeleteRange: dr}})
		}
	}
	return resp, nil
}

func (s *Server) Compact(_ context.Context, r *etcdserverpb.CompactionRequest) (*etcdserverpb.CompactionResponse, error) {
	if err := s.Store.Compact(r.Revision); err != nil {
		return nil, grpcErr(err)
	}
	cur, _ := s.Store.Revisions()
	return &etcdserverpb.CompactionResponse{Header: s.header(cur)}, nil
}

// Watch serves one stream of watches: each create request replays the
// changes after its start revision, then follows new ones.
func (s *Server) Watch(stream grpc.BidiStreamingServer[etcdserverpb.WatchRequest, etcdserverpb.WatchResponse]) error {
	var (
		sendMu sync.Mutex
		nextID int64
		mu     sync.Mutex
		subs   = map[int64]int64{} // watch id -> watchers id
	)
	send := func(r *etcdserverpb.WatchResponse) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(r)
	}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, sid := range subs {
			s.Store.watchers.cancel(sid)
		}
	}()
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case req.GetCreateRequest() != nil:
			c := req.GetCreateRequest()
			mu.Lock()
			id := c.WatchId
			if id == 0 {
				nextID++
				id = nextID
			}
			mu.Unlock()
			s.startWatch(stream.Context(), id, c, send, &mu, subs)
		case req.GetCancelRequest() != nil:
			id := req.GetCancelRequest().WatchId
			mu.Lock()
			if sid, ok := subs[id]; ok {
				s.Store.watchers.cancel(sid)
				delete(subs, id)
			}
			mu.Unlock()
			cur, _ := s.Store.Revisions()
			_ = send(&etcdserverpb.WatchResponse{Header: s.header(cur), WatchId: id, Canceled: true})
		case req.GetProgressRequest() != nil:
			cur, _ := s.Store.Revisions()
			_ = send(&etcdserverpb.WatchResponse{Header: s.header(cur), WatchId: -1})
		}
	}
}

func eventsPB(events []Event, prevKV bool) []*mvccpb.Event {
	var out []*mvccpb.Event
	for _, e := range events {
		ev := &mvccpb.Event{Kv: toPB(e.KV, false)}
		if e.KV.Deleted {
			ev.Type = mvccpb.DELETE
			ev.Kv = &mvccpb.KeyValue{Key: e.KV.Key, ModRevision: e.KV.ModRevision}
		}
		if prevKV && e.Prev != nil {
			ev.PrevKv = toPB(*e.Prev, false)
		}
		out = append(out, ev)
	}
	return out
}

func (s *Server) startWatch(ctx context.Context, id int64, c *etcdserverpb.WatchCreateRequest,
	send func(*etcdserverpb.WatchResponse) error, mu *sync.Mutex, subs map[int64]int64) {
	// Subscribe first, then replay: a change committed between the two is
	// in both, and dropped from the live feed by its revision.
	sid, ch := s.Store.watchers.subscribe(c.Key, c.RangeEnd)
	mu.Lock()
	subs[id] = sid
	mu.Unlock()
	cur, compacted := s.Store.Revisions()
	_ = send(&etcdserverpb.WatchResponse{Header: s.header(cur), WatchId: id, Created: true})
	go func() {
		last := cur
		if c.StartRevision > 0 {
			if c.StartRevision <= compacted {
				_ = send(&etcdserverpb.WatchResponse{Header: s.header(cur), WatchId: id, Canceled: true,
					CompactRevision: compacted, CancelReason: rpctypes.ErrCompacted.Error()})
				return
			}
			past, at, err := s.Store.Changes(c.Key, c.RangeEnd, c.StartRevision-1)
			if err == nil && len(past) > 0 {
				_ = send(&etcdserverpb.WatchResponse{Header: s.header(at), WatchId: id, Events: eventsPB(past, c.PrevKv)})
			}
			last = at
		}
		var ticker <-chan time.Time
		if c.ProgressNotify {
			t := time.NewTicker(10 * time.Second)
			defer t.Stop()
			ticker = t.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker:
				now, _ := s.Store.Revisions()
				_ = send(&etcdserverpb.WatchResponse{Header: s.header(now), WatchId: id})
			case b, ok := <-ch:
				if !ok {
					return
				}
				if b.rev <= last {
					continue
				}
				last = b.rev
				if err := send(&etcdserverpb.WatchResponse{Header: s.header(b.rev), WatchId: id, Events: eventsPB(b.events, c.PrevKv)}); err != nil {
					return
				}
			}
		}
	}()
}

func (s *Server) Status(context.Context, *etcdserverpb.StatusRequest) (*etcdserverpb.StatusResponse, error) {
	cur, _ := s.Store.Revisions()
	return &etcdserverpb.StatusResponse{Header: s.header(cur), Version: "3.7.0", DbSize: s.Store.DBSize(),
		DbSizeInUse: s.Store.DBSize(), Leader: s.MemberID, RaftIndex: uint64(cur), RaftTerm: 1, RaftAppliedIndex: uint64(cur)}, nil
}

func (s *Server) MemberList(context.Context, *etcdserverpb.MemberListRequest) (*etcdserverpb.MemberListResponse, error) {
	cur, _ := s.Store.Revisions()
	return &etcdserverpb.MemberListResponse{Header: s.header(cur),
		Members: []*etcdserverpb.Member{{ID: s.MemberID, Name: "kuberoot-store"}}}, nil
}
