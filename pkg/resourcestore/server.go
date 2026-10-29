package resourcestore

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/raft/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server serves a member on the etcd v3 API: KV, Watch, Lease, Cluster and
// the Maintenance calls clients ask for.
type Server struct {
	etcdserverpb.UnimplementedKVServer
	etcdserverpb.UnimplementedWatchServer
	etcdserverpb.UnimplementedLeaseServer
	etcdserverpb.UnimplementedMaintenanceServer
	etcdserverpb.UnimplementedClusterServer

	Node *Node
}

// Register serves the member on a gRPC server.
func (s *Server) Register(g *grpc.Server) {
	etcdserverpb.RegisterKVServer(g, s)
	etcdserverpb.RegisterWatchServer(g, s)
	etcdserverpb.RegisterLeaseServer(g, s)
	etcdserverpb.RegisterMaintenanceServer(g, s)
	etcdserverpb.RegisterClusterServer(g, s)
}

func (s *Server) header(rev int64) *etcdserverpb.ResponseHeader {
	return &etcdserverpb.ResponseHeader{ClusterId: s.Node.clusterID, MemberId: s.Node.id, Revision: rev, RaftTerm: s.Node.Term()}
}

func toPB(kv KV, keysOnly bool) *mvccpb.KeyValue {
	out := &mvccpb.KeyValue{Key: kv.Key, CreateRevision: kv.CreateRevision, ModRevision: kv.ModRevision,
		Version: kv.Version.Version, Lease: kv.Lease}
	if !keysOnly {
		out.Value = kv.Value
	}
	return out
}

// grpcErr turns the store's errors into the ones etcd's clients know, so
// that they retry or give up as they do with etcd.
func grpcErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrCompacted):
		return rpctypes.ErrGRPCCompacted
	case errors.Is(err, ErrFutureRev):
		return rpctypes.ErrGRPCFutureRev
	case errors.Is(err, ErrLeaseNotFound):
		return rpctypes.ErrGRPCLeaseNotFound
	case errors.Is(err, ErrLeaseExists):
		return rpctypes.ErrGRPCLeaseExist
	case errors.Is(err, errNoLeader):
		return rpctypes.ErrGRPCNoLeader
	case errors.Is(err, errTimeout), errors.Is(err, context.DeadlineExceeded):
		return rpctypes.ErrGRPCTimeout
	case errors.Is(err, errStopped), errors.Is(err, raft.ErrStopped):
		return rpctypes.ErrGRPCStopped
	case errors.Is(err, errRemoved):
		return status.Error(codes.FailedPrecondition, errRemoved.Error())
	case errors.Is(err, errMemberNotFound):
		return rpctypes.ErrGRPCMemberNotFound
	case errors.Is(err, errPeerURLExists):
		return rpctypes.ErrGRPCPeerURLExist
	case errors.Is(err, errKeyNotFound):
		return rpctypes.ErrGRPCKeyNotFound
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	}
	switch err.Error() {
	case errNotLearner.Error():
		return rpctypes.ErrGRPCMemberNotLearner
	case errLearnerNotReady.Error():
		return rpctypes.ErrGRPCLearnerNotReady
	case errTooManyLearners.Error():
		return rpctypes.ErrGRPCTooManyLearners
	}
	return status.Error(codes.Internal, err.Error())
}

func rangeResponse(h *etcdserverpb.ResponseHeader, rr RangeResult, keysOnly bool) *etcdserverpb.RangeResponse {
	resp := &etcdserverpb.RangeResponse{Header: h, Count: rr.Count, More: rr.More}
	for _, kv := range rr.KVs {
		resp.Kvs = append(resp.Kvs, toPB(kv, keysOnly))
	}
	return resp
}

func (s *Server) Range(ctx context.Context, r *etcdserverpb.RangeRequest) (*etcdserverpb.RangeResponse, error) {
	if !r.Serializable {
		if err := s.Node.linearizableRead(ctx); err != nil {
			return nil, grpcErr(err)
		}
	}
	rr, err := s.Node.store.Range(r.Key, r.RangeEnd, r.Revision, r.Limit, r.CountOnly)
	if err != nil {
		return nil, grpcErr(err)
	}
	cur, _ := s.Node.store.Revisions()
	if r.Revision == 0 {
		cur = rr.Rev
	}
	return rangeResponse(s.header(cur), rr, r.KeysOnly), nil
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

func (s *Server) txn(ctx context.Context, t *TxnRequest) (TxnResult, error) {
	res, err := s.Node.propose(ctx, &request{Txn: t})
	return res.Txn, err
}

func (s *Server) Put(ctx context.Context, r *etcdserverpb.PutRequest) (*etcdserverpb.PutResponse, error) {
	res, err := s.txn(ctx, &TxnRequest{Success: []Op{putOp(r)}})
	if err != nil {
		return nil, grpcErr(err)
	}
	resp := &etcdserverpb.PutResponse{Header: s.header(res.Revision)}
	if r.PrevKv && len(res.Ops) > 0 && len(res.Ops[0].Prev) > 0 && res.Ops[0].Prev[0] != nil {
		resp.PrevKv = toPB(*res.Ops[0].Prev[0], false)
	}
	return resp, nil
}

func (s *Server) DeleteRange(ctx context.Context, r *etcdserverpb.DeleteRangeRequest) (*etcdserverpb.DeleteRangeResponse, error) {
	res, err := s.txn(ctx, &TxnRequest{Success: []Op{{Key: r.Key, End: r.RangeEnd, Delete: true}}})
	if err != nil {
		return nil, grpcErr(err)
	}
	return deleteResponse(s.header(res.Revision), res.Ops[0], r.PrevKv), nil
}

func deleteResponse(h *etcdserverpb.ResponseHeader, or OpResult, prevKV bool) *etcdserverpb.DeleteRangeResponse {
	resp := &etcdserverpb.DeleteRangeResponse{Header: h, Deleted: or.Deleted}
	if prevKV {
		for _, p := range or.Prev {
			if p != nil {
				resp.PrevKvs = append(resp.PrevKvs, toPB(*p, false))
			}
		}
	}
	return resp
}

func putOp(p *etcdserverpb.PutRequest) Op {
	return Op{Key: p.Key, Value: p.Value, Lease: p.Lease, IgnoreValue: p.IgnoreValue, IgnoreLease: p.IgnoreLease}
}

func compareOf(c *etcdserverpb.Compare) Compare {
	out := Compare{Key: c.Key, End: c.RangeEnd}
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
	case etcdserverpb.Compare_LEASE:
		out.Target, out.Int = "lease", c.GetLease()
	}
	return out
}

// opsOf turns a transaction branch into operations; nested transactions
// are not supported. writes reports whether any operation writes.
func opsOf(reqs []*etcdserverpb.RequestOp) (ops []Op, writes bool, err error) {
	for _, r := range reqs {
		switch {
		case r.GetRequestPut() != nil:
			ops = append(ops, putOp(r.GetRequestPut()))
			writes = true
		case r.GetRequestDeleteRange() != nil:
			d := r.GetRequestDeleteRange()
			ops = append(ops, Op{Key: d.Key, End: d.RangeEnd, Delete: true})
			writes = true
		case r.GetRequestRange() != nil:
			g := r.GetRequestRange()
			ops = append(ops, Op{Key: g.Key, End: g.RangeEnd, Range: true, Limit: g.Limit, Rev: g.Revision, CountOnly: g.CountOnly})
		default:
			return nil, false, status.Error(codes.Unimplemented, "nested transactions are not supported")
		}
	}
	return ops, writes, nil
}

func (s *Server) Txn(ctx context.Context, r *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error) {
	t := &TxnRequest{}
	for _, c := range r.Compare {
		t.Compares = append(t.Compares, compareOf(c))
	}
	var sw, fw bool
	var err error
	if t.Success, sw, err = opsOf(r.Success); err != nil {
		return nil, err
	}
	if t.Failure, fw, err = opsOf(r.Failure); err != nil {
		return nil, err
	}
	var res TxnResult
	if sw || fw {
		res, err = s.txn(ctx, t)
	} else {
		// Only reads: answered here, after the reads before it.
		if err = s.Node.linearizableRead(ctx); err == nil {
			res, err = s.Node.store.ReadTxn(t)
		}
	}
	if err != nil {
		return nil, grpcErr(err)
	}
	branch := r.Success
	if !res.Succeeded {
		branch = r.Failure
	}
	resp := &etcdserverpb.TxnResponse{Header: s.header(res.Revision), Succeeded: res.Succeeded}
	for i, op := range branch {
		or := res.Ops[i]
		switch {
		case op.GetRequestRange() != nil:
			rr := rangeResponse(s.header(res.Revision), *or.Range, op.GetRequestRange().KeysOnly)
			resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponseRange{ResponseRange: rr}})
		case op.GetRequestPut() != nil:
			pr := &etcdserverpb.PutResponse{Header: s.header(res.Revision)}
			if op.GetRequestPut().PrevKv && len(or.Prev) > 0 && or.Prev[0] != nil {
				pr.PrevKv = toPB(*or.Prev[0], false)
			}
			resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponsePut{ResponsePut: pr}})
		case op.GetRequestDeleteRange() != nil:
			dr := deleteResponse(s.header(res.Revision), or, op.GetRequestDeleteRange().PrevKv)
			resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponseDeleteRange{ResponseDeleteRange: dr}})
		}
	}
	return resp, nil
}

func (s *Server) Compact(ctx context.Context, r *etcdserverpb.CompactionRequest) (*etcdserverpb.CompactionResponse, error) {
	res, err := s.Node.propose(ctx, &request{Compact: r.Revision, Archive: s.Node.cfg.KeepHistory})
	if err != nil {
		return nil, grpcErr(err)
	}
	return &etcdserverpb.CompactionResponse{Header: s.header(res.Revision)}, nil
}

// watchStream is one client stream of watches; delivered tracks, per
// watch, the revision all its events up to have been sent, so that a
// progress notification for the stream never claims more.
type watchStream struct {
	sendMu    sync.Mutex
	stream    grpc.BidiStreamingServer[etcdserverpb.WatchRequest, etcdserverpb.WatchResponse]
	mu        sync.Mutex
	nextID    int64
	subs      map[int64]int64 // watch id -> watchers id
	delivered map[int64]*atomic.Int64
}

func (w *watchStream) send(r *etcdserverpb.WatchResponse) error {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	return w.stream.Send(r)
}

// Watch serves one stream of watches: each create request replays the
// changes after its start revision, then follows new ones.
func (s *Server) Watch(stream grpc.BidiStreamingServer[etcdserverpb.WatchRequest, etcdserverpb.WatchResponse]) error {
	ws := &watchStream{stream: stream, subs: map[int64]int64{}, delivered: map[int64]*atomic.Int64{}}
	watchers := s.Node.store.watchers
	defer func() {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		for _, sid := range ws.subs {
			watchers.cancel(sid)
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
			ws.mu.Lock()
			id := c.WatchId
			if id == 0 {
				ws.nextID++
				id = ws.nextID
			}
			ws.mu.Unlock()
			s.startWatch(stream.Context(), ws, id, c)
		case req.GetCancelRequest() != nil:
			id := req.GetCancelRequest().WatchId
			ws.mu.Lock()
			if sid, ok := ws.subs[id]; ok {
				watchers.cancel(sid)
				delete(ws.subs, id)
				delete(ws.delivered, id)
			}
			ws.mu.Unlock()
			cur, _ := s.Node.store.Revisions()
			_ = ws.send(&etcdserverpb.WatchResponse{Header: s.header(cur), WatchId: id, Canceled: true})
		case req.GetProgressRequest() != nil:
			rev, _ := s.Node.store.Revisions()
			ws.mu.Lock()
			for _, d := range ws.delivered {
				if v := d.Load(); v < rev {
					rev = v
				}
			}
			ws.mu.Unlock()
			_ = ws.send(&etcdserverpb.WatchResponse{Header: s.header(rev), WatchId: -1})
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

func (s *Server) startWatch(ctx context.Context, ws *watchStream, id int64, c *etcdserverpb.WatchCreateRequest) {
	store := s.Node.store
	// Subscribe first, then replay: a change applied between the two is
	// in both, and dropped from the live feed by its revision.
	sid, ch := store.watchers.subscribe(c.Key, c.RangeEnd)
	cur, compacted := store.Revisions()
	delivered := &atomic.Int64{}
	start := cur
	if c.StartRevision > 0 && c.StartRevision-1 < cur {
		start = c.StartRevision - 1
	}
	delivered.Store(start)
	ws.mu.Lock()
	ws.subs[id] = sid
	ws.delivered[id] = delivered
	ws.mu.Unlock()
	_ = ws.send(&etcdserverpb.WatchResponse{Header: s.header(cur), WatchId: id, Created: true})
	go func() {
		last := cur
		if c.StartRevision > 0 && c.StartRevision <= cur {
			if c.StartRevision <= compacted {
				_ = ws.send(&etcdserverpb.WatchResponse{Header: s.header(cur), WatchId: id, Canceled: true,
					CompactRevision: compacted, CancelReason: rpctypes.ErrCompacted.Error()})
				return
			}
			past, at, err := store.Changes(c.Key, c.RangeEnd, c.StartRevision-1)
			if err != nil {
				_ = ws.send(&etcdserverpb.WatchResponse{Header: s.header(cur), WatchId: id, Canceled: true,
					CompactRevision: compacted, CancelReason: err.Error()})
				return
			}
			if len(past) > 0 {
				_ = ws.send(&etcdserverpb.WatchResponse{Header: s.header(at), WatchId: id, Events: eventsPB(past, c.PrevKv)})
			}
			last = at
		} else if c.StartRevision > cur {
			// A future revision: events from it on.
			last = c.StartRevision - 1
		}
		delivered.Store(last)
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
				_ = ws.send(&etcdserverpb.WatchResponse{Header: s.header(last), WatchId: id})
			case b, ok := <-ch:
				if !ok {
					// Too slow, or the state was replaced: the client watches again.
					now, _ := store.Revisions()
					_ = ws.send(&etcdserverpb.WatchResponse{Header: s.header(now), WatchId: id, Canceled: true,
						CancelReason: "etcdserver: mvcc: watcher fell behind"})
					return
				}
				if b.rev <= last {
					continue
				}
				var evs []Event
				for _, e := range b.events {
					if e.KV.ModRevision > last {
						evs = append(evs, e)
					}
				}
				if len(evs) > 0 {
					if err := ws.send(&etcdserverpb.WatchResponse{Header: s.header(b.rev), WatchId: id, Events: eventsPB(evs, c.PrevKv)}); err != nil {
						return
					}
				}
				last = b.rev
				delivered.Store(last)
			}
		}
	}()
}

func (s *Server) Status(context.Context, *etcdserverpb.StatusRequest) (*etcdserverpb.StatusResponse, error) {
	n := s.Node
	cur, _ := n.store.Revisions()
	resp := &etcdserverpb.StatusResponse{Header: s.header(cur), Version: "3.7.0", DbSize: n.store.DBSize(),
		DbSizeInUse: n.store.DBSize(), Leader: n.Leader(), RaftIndex: n.committed.Load(), RaftTerm: n.Term(),
		RaftAppliedIndex: n.Applied(), IsLearner: n.IsLearner()}
	if n.Leader() == 0 {
		resp.Errors = append(resp.Errors, errNoLeader.Error())
	}
	return resp, nil
}

func membersPB(ms []Member) []*etcdserverpb.Member {
	var out []*etcdserverpb.Member
	for _, m := range ms {
		out = append(out, &etcdserverpb.Member{ID: m.ID, Name: m.Name, PeerURLs: m.PeerURLs, ClientURLs: m.ClientURLs, IsLearner: m.IsLearner})
	}
	return out
}

func (s *Server) MemberList(ctx context.Context, r *etcdserverpb.MemberListRequest) (*etcdserverpb.MemberListResponse, error) {
	if r.Linearizable {
		if err := s.Node.linearizableRead(ctx); err != nil {
			return nil, grpcErr(err)
		}
	}
	cur, _ := s.Node.store.Revisions()
	return &etcdserverpb.MemberListResponse{Header: s.header(cur), Members: membersPB(s.Node.store.Members())}, nil
}

func (s *Server) MemberAdd(ctx context.Context, r *etcdserverpb.MemberAddRequest) (*etcdserverpb.MemberAddResponse, error) {
	m, members, err := s.Node.AddMember(ctx, r.PeerURLs, r.IsLearner)
	if err != nil {
		return nil, grpcErr(err)
	}
	cur, _ := s.Node.store.Revisions()
	return &etcdserverpb.MemberAddResponse{Header: s.header(cur), Member: membersPB([]Member{m})[0], Members: membersPB(members)}, nil
}

func (s *Server) MemberRemove(ctx context.Context, r *etcdserverpb.MemberRemoveRequest) (*etcdserverpb.MemberRemoveResponse, error) {
	members, err := s.Node.RemoveMember(ctx, r.ID)
	if err != nil {
		return nil, grpcErr(err)
	}
	cur, _ := s.Node.store.Revisions()
	return &etcdserverpb.MemberRemoveResponse{Header: s.header(cur), Members: membersPB(members)}, nil
}

func (s *Server) MemberUpdate(ctx context.Context, r *etcdserverpb.MemberUpdateRequest) (*etcdserverpb.MemberUpdateResponse, error) {
	members, err := s.Node.UpdateMember(ctx, r.ID, r.PeerURLs)
	if err != nil {
		return nil, grpcErr(err)
	}
	cur, _ := s.Node.store.Revisions()
	return &etcdserverpb.MemberUpdateResponse{Header: s.header(cur), Members: membersPB(members)}, nil
}

func (s *Server) MemberPromote(ctx context.Context, r *etcdserverpb.MemberPromoteRequest) (*etcdserverpb.MemberPromoteResponse, error) {
	members, err := s.Node.PromoteMember(ctx, r.ID)
	if err != nil {
		return nil, grpcErr(err)
	}
	cur, _ := s.Node.store.Revisions()
	return &etcdserverpb.MemberPromoteResponse{Header: s.header(cur), Members: membersPB(members)}, nil
}

func (s *Server) MoveLeader(ctx context.Context, r *etcdserverpb.MoveLeaderRequest) (*etcdserverpb.MoveLeaderResponse, error) {
	if err := s.Node.TransferLeadership(ctx, r.TargetID); err != nil {
		return nil, grpcErr(err)
	}
	cur, _ := s.Node.store.Revisions()
	return &etcdserverpb.MoveLeaderResponse{Header: s.header(cur)}, nil
}

// Snapshot streams a consistent copy of this member's state file. It is
// the store's own format, not etcd's: etcdutl cannot restore it.
func (s *Server) Snapshot(_ *etcdserverpb.SnapshotRequest, stream grpc.ServerStreamingServer[etcdserverpb.SnapshotResponse]) error {
	pr, pw := io.Pipe()
	go func() {
		_, err := s.Node.store.WriteTo(pw)
		pw.CloseWithError(err)
	}()
	buf := make([]byte, 1<<20)
	for {
		n, err := io.ReadFull(pr, buf)
		if n > 0 {
			if serr := stream.Send(&etcdserverpb.SnapshotResponse{Header: s.header(0), Blob: append([]byte(nil), buf[:n]...)}); serr != nil {
				return serr
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}
}

func (s *Server) Alarm(context.Context, *etcdserverpb.AlarmRequest) (*etcdserverpb.AlarmResponse, error) {
	cur, _ := s.Node.store.Revisions()
	return &etcdserverpb.AlarmResponse{Header: s.header(cur)}, nil
}
