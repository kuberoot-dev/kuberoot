package resourcestore

import (
	"context"
	"io"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
)

// leaseClock keeps the leases' expiry. Leases and their keys are
// replicated state; their clock is not: as in etcd, the leader keeps it,
// keep-alives go to the leader, and a new leader gives every lease its
// full time to live again, so that no lease expires because of a change
// of leader. The leader revokes an expired lease through the log.
type leaseClock struct {
	n      *Node
	mu     sync.Mutex
	leader bool
	ttl    map[int64]int64
	expiry map[int64]time.Time
}

func newLeaseClock(n *Node) *leaseClock {
	return &leaseClock{n: n, ttl: map[int64]int64{}, expiry: map[int64]time.Time{}}
}

// applied follows grants and revocations as every member applies them.
func (c *leaseClock) applied(granted []Lease, revoked []int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range granted {
		c.ttl[l.ID] = l.TTL
		c.expiry[l.ID] = time.Now().Add(time.Duration(l.TTL) * time.Second)
	}
	for _, id := range revoked {
		delete(c.ttl, id)
		delete(c.expiry, id)
	}
}

// reload reads the leases from a state that a snapshot replaced.
func (c *leaseClock) reload() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl, c.expiry = map[int64]int64{}, map[int64]time.Time{}
	for _, l := range c.n.store.Leases() {
		c.ttl[l.ID] = l.TTL
		c.expiry[l.ID] = time.Now().Add(time.Duration(l.TTL) * time.Second)
	}
}

// leadership: a member that becomes leader restarts every lease's clock.
func (c *leaseClock) leadership(leader bool) {
	c.mu.Lock()
	c.leader = leader
	c.mu.Unlock()
	if leader {
		c.reload()
	}
}

// renew restarts a lease's clock; -1 when there is no such lease.
func (c *leaseClock) renew(id int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl, ok := c.ttl[id]
	if !ok {
		return -1
	}
	c.expiry[id] = time.Now().Add(time.Duration(ttl) * time.Second)
	return ttl
}

func (c *leaseClock) remaining(id int64) (ttl, remaining int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl, ok := c.ttl[id]
	if !ok {
		return -1, -1
	}
	return ttl, int64(time.Until(c.expiry[id]).Seconds() + 0.5)
}

// run revokes, on the leader, the leases that expired.
func (c *leaseClock) run() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-c.n.stopc:
			return
		case <-t.C:
		}
		var due []int64
		c.mu.Lock()
		if c.leader {
			now := time.Now()
			for id, at := range c.expiry {
				if now.After(at) {
					due = append(due, id)
					// Tried again later if the revocation is lost.
					c.expiry[id] = now.Add(3 * time.Second)
				}
			}
		}
		c.mu.Unlock()
		for _, id := range due {
			go func(id int64) {
				_, _ = c.n.propose(context.Background(), &request{LeaseRevoke: id})
			}(id)
		}
	}
}

// leaseOnLeader renews a lease or reads its time left, on the leader.
func (n *Node) leaseOnLeader(ctx context.Context, id int64, renew bool) (ttl, remaining int64, err error) {
	if err := n.waitLeader(ctx); err != nil {
		return 0, 0, err
	}
	if lead := n.lead.Load(); lead != n.id {
		path := pathLease + "?id=" + strconv.FormatInt(id, 16)
		if renew {
			path += "&renew=1"
		}
		var a leaseAnswer
		if err := n.transport.call(ctx, lead, path, &a); err != nil {
			return 0, 0, err
		}
		return a.TTL, a.Remaining, nil
	}
	if renew {
		ttl = n.leases.renew(id)
		return ttl, ttl, nil
	}
	ttl, remaining = n.leases.remaining(id)
	return ttl, remaining, nil
}

func (s *Server) LeaseGrant(ctx context.Context, r *etcdserverpb.LeaseGrantRequest) (*etcdserverpb.LeaseGrantResponse, error) {
	id := r.ID
	if id == 0 {
		id = rand.Int64N(1<<62) + 1
	}
	res, err := s.Node.propose(ctx, &request{LeaseGrant: &Lease{ID: id, TTL: r.TTL}})
	if err != nil {
		return nil, grpcErr(err)
	}
	return &etcdserverpb.LeaseGrantResponse{Header: s.header(res.Revision), ID: id, TTL: r.TTL}, nil
}

func (s *Server) LeaseRevoke(ctx context.Context, r *etcdserverpb.LeaseRevokeRequest) (*etcdserverpb.LeaseRevokeResponse, error) {
	res, err := s.Node.propose(ctx, &request{LeaseRevoke: r.ID})
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
		ctx, cancel := context.WithTimeout(stream.Context(), 5*time.Second)
		ttl, _, err := s.Node.leaseOnLeader(ctx, req.ID, true)
		cancel()
		if err != nil {
			return grpcErr(err)
		}
		if ttl < 0 {
			ttl = 0 // the client reads 0 as expired
		}
		cur, _ := s.Node.store.Revisions()
		if err := stream.Send(&etcdserverpb.LeaseKeepAliveResponse{Header: s.header(cur), ID: req.ID, TTL: ttl}); err != nil {
			return err
		}
	}
}

func (s *Server) LeaseTimeToLive(ctx context.Context, r *etcdserverpb.LeaseTimeToLiveRequest) (*etcdserverpb.LeaseTimeToLiveResponse, error) {
	cur, _ := s.Node.store.Revisions()
	l, keys, ok := s.Node.store.LeaseOf(r.ID)
	if !ok {
		return &etcdserverpb.LeaseTimeToLiveResponse{Header: s.header(cur), ID: r.ID, TTL: -1}, nil
	}
	_, remaining, err := s.Node.leaseOnLeader(ctx, r.ID, false)
	if err != nil {
		return nil, grpcErr(err)
	}
	resp := &etcdserverpb.LeaseTimeToLiveResponse{Header: s.header(cur), ID: r.ID, GrantedTTL: l.TTL, TTL: remaining}
	if r.Keys {
		resp.Keys = keys
	}
	return resp, nil
}

func (s *Server) LeaseLeases(context.Context, *etcdserverpb.LeaseLeasesRequest) (*etcdserverpb.LeaseLeasesResponse, error) {
	cur, _ := s.Node.store.Revisions()
	resp := &etcdserverpb.LeaseLeasesResponse{Header: s.header(cur)}
	for _, l := range s.Node.store.Leases() {
		resp.Leases = append(resp.Leases, &etcdserverpb.LeaseStatus{ID: l.ID})
	}
	return resp, nil
}
