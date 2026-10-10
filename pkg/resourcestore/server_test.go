package resourcestore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
)

// serve starts the store on a local port and returns an etcd client of it.
func serve(t *testing.T) *clientv3.Client {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	(&Server{Store: st, ClusterID: 1, MemberID: 1}).Register(g)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(func() { g.Stop(); st.Close() })
	c, err := clientv3.New(clientv3.Config{Endpoints: []string{l.Addr().String()}, DialTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestCreateUpdateDeleteAsTheAPIServerDoes(t *testing.T) {
	c := serve(t)
	ctx := context.Background()
	key := "/registry/configmaps/default/a"
	// Create if absent.
	r, err := c.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, "v1")).Commit()
	if err != nil || !r.Succeeded {
		t.Fatalf("create: %v %v", r, err)
	}
	created := r.Header.Revision
	// A second create fails and returns the current object.
	r, err = c.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, "again")).Else(clientv3.OpGet(key)).Commit()
	if err != nil || r.Succeeded || string(r.Responses[0].GetResponseRange().Kvs[0].Value) != "v1" {
		t.Fatalf("second create: %+v %v", r, err)
	}
	// Update if unchanged.
	r, err = c.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", created)).
		Then(clientv3.OpPut(key, "v2")).Commit()
	if err != nil || !r.Succeeded {
		t.Fatalf("update: %v", err)
	}
	// Stale update fails.
	r, _ = c.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", created)).Then(clientv3.OpPut(key, "v3")).Commit()
	if r.Succeeded {
		t.Fatal("a stale update succeeded")
	}
	g, _ := c.Get(ctx, key)
	if string(g.Kvs[0].Value) != "v2" || g.Kvs[0].CreateRevision != created || g.Kvs[0].Version != 2 {
		t.Fatalf("after update: %+v", g.Kvs[0])
	}
	// The old version is still there to read.
	old, _ := c.Get(ctx, key, clientv3.WithRev(created))
	if string(old.Kvs[0].Value) != "v1" {
		t.Fatalf("at the created revision: %q", old.Kvs[0].Value)
	}
	d, err := c.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", g.Kvs[0].ModRevision)).
		Then(clientv3.OpDelete(key)).Commit()
	if err != nil || !d.Succeeded {
		t.Fatalf("delete: %v", err)
	}
	if g, _ := c.Get(ctx, key); len(g.Kvs) != 0 {
		t.Fatal("deleted key still read")
	}
}

func TestListInPagesAndCompaction(t *testing.T) {
	c := serve(t)
	ctx := context.Background()
	for i := range 10 {
		if _, err := c.Put(ctx, fmt.Sprintf("/registry/pods/ns/p%02d", i), "x"); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = c.Put(ctx, "/registry/secrets/ns/s", "y")
	page, err := c.Get(ctx, "/registry/pods/", clientv3.WithPrefix(), clientv3.WithLimit(4))
	if err != nil || len(page.Kvs) != 4 || !page.More || page.Count != 10 {
		t.Fatalf("first page: %d kvs more=%v count=%d %v", len(page.Kvs), page.More, page.Count, err)
	}
	rev := page.Header.Revision
	// Continue from the last key at the same revision, as the API server does.
	next, _ := c.Get(ctx, string(page.Kvs[3].Key)+"\x00", clientv3.WithRange(clientv3.GetPrefixRangeEnd("/registry/pods/")),
		clientv3.WithRev(rev), clientv3.WithLimit(4))
	if len(next.Kvs) != 4 || string(next.Kvs[0].Key) != "/registry/pods/ns/p04" {
		t.Fatalf("second page starts at %s", next.Kvs[0].Key)
	}
	_, _ = c.Put(ctx, "/registry/pods/ns/p00", "changed")
	if _, err := c.Compact(ctx, rev+1); err != nil {
		t.Fatal(err)
	}
	// A continuation at a compacted revision expires, and the API server
	// then starts an inconsistent list from the current one.
	if _, err := c.Get(ctx, "/registry/pods/ns/p05", clientv3.WithRev(rev)); !errors.Is(err, rpctypes.ErrCompacted) {
		t.Fatalf("read at a compacted revision: %v", err)
	}
	if g, _ := c.Get(ctx, "/registry/pods/ns/p00"); string(g.Kvs[0].Value) != "changed" {
		t.Fatal("compaction lost the current version")
	}
}

func TestWatchFromThePast(t *testing.T) {
	c := serve(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, _ := c.Put(ctx, "/registry/ns/a", "1")
	_, _ = c.Put(ctx, "/registry/ns/b", "1")
	w := c.Watch(ctx, "/registry/ns/", clientv3.WithPrefix(), clientv3.WithRev(p.Header.Revision), clientv3.WithPrevKV())
	time.Sleep(200 * time.Millisecond)
	_, _ = c.Put(ctx, "/registry/ns/a", "2")
	_, _ = c.Delete(ctx, "/registry/ns/b")
	_, _ = c.Put(ctx, "/registry/other", "x")
	var got []string
	for resp := range w {
		for _, e := range resp.Events {
			prev := ""
			if e.PrevKv != nil {
				prev = string(e.PrevKv.Value)
			}
			got = append(got, fmt.Sprintf("%s %s=%s prev=%s", e.Type, e.Kv.Key, e.Kv.Value, prev))
		}
		if len(got) >= 4 {
			break
		}
	}
	want := []string{"PUT /registry/ns/a=1 prev=", "PUT /registry/ns/b=1 prev=", "PUT /registry/ns/a=2 prev=1", "DELETE /registry/ns/b= prev=1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events\n got %v\nwant %v", got, want)
	}
}

func TestLeaseExpiresItsKeys(t *testing.T) {
	c := serve(t)
	ctx := context.Background()
	l, err := c.Grant(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put(ctx, "/registry/events/ns/e", "x", clientv3.WithLease(l.ID)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if g, _ := c.Get(ctx, "/registry/events/ns/e"); len(g.Kvs) != 0 {
		t.Fatal("a key outlived its lease")
	}
}
