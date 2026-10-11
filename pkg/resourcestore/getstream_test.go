package resourcestore

import (
	"context"
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// The API server lists through the client's GetStream; an empty prefix must
// still carry the store's revision.
func TestGetStreamOfAnEmptyPrefixHasARevision(t *testing.T) {
	c := serve(t)
	ctx := context.Background()
	stream, err := c.KV.GetStream(ctx, "/registry/configmaps/", clientv3.WithRange(clientv3.GetPrefixRangeEnd("/registry/configmaps/")))
	if err != nil {
		t.Fatal(err)
	}
	var chunks int
	var rev int64
	for resp := range stream {
		if err := resp.Err(); err != nil {
			t.Fatal(err)
		}
		chunks++
		if resp.RangeResponse.Header != nil && rev == 0 {
			rev = resp.RangeResponse.Header.Revision
		}
	}
	if chunks == 0 || rev == 0 {
		t.Fatalf("%d chunks, revision %d", chunks, rev)
	}
}
