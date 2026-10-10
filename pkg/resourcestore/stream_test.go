package resourcestore

import (
	"context"
	"fmt"
	"testing"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
)

func TestRangeStreamMergesToRange(t *testing.T) {
	c := serve(t)
	ctx := context.Background()
	for i := range 1200 {
		if _, err := c.Put(ctx, fmt.Sprintf("/registry/leases/ns/l%04d", i), "x"); err != nil {
			t.Fatal(err)
		}
	}
	stream, err := etcdserverpb.NewKVClient(c.ActiveConnection()).RangeStream(ctx,
		&etcdserverpb.RangeRequest{Key: []byte("/registry/leases/"), RangeEnd: []byte("/registry/leases0")})
	if err != nil {
		t.Fatal(err)
	}
	var kvs int
	var last *etcdserverpb.RangeResponse
	for {
		r, err := stream.Recv()
		if err != nil {
			break
		}
		kvs += len(r.RangeResponse.Kvs)
		last = r.RangeResponse
	}
	if kvs != 1200 || last.Header == nil || last.Header.Revision == 0 || last.Count != 1200 {
		t.Fatalf("streamed %d kvs, last chunk %+v", kvs, last)
	}
}
