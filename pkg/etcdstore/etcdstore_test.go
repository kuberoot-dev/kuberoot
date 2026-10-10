package etcdstore

import (
	"path/filepath"
	"testing"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
)

func TestInitialCluster(t *testing.T) {
	members := []*etcdserverpb.Member{
		{ID: 1, Name: "cp-b", PeerURLs: []string{PeerURL("10.0.0.2")}},
		{ID: 2, Name: "cp-a", PeerURLs: []string{PeerURL("10.0.0.1")}},
		// The learner just added: no name until it starts.
		{ID: 3, PeerURLs: []string{PeerURL("10.0.0.3")}, IsLearner: true},
	}
	got := InitialCluster(members, 3, "cp-c")
	want := "cp-a=https://10.0.0.1:2380,cp-b=https://10.0.0.2:2380,cp-c=https://10.0.0.3:2380"
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestInit(t *testing.T) {
	dir := t.TempDir()
	InitFile = filepath.Join(dir, "etcd-init")
	// Never written: the node created the store alone.
	if got := ReadInit("cp-a", "10.0.0.1"); got.State != "new" || got.InitialCluster != "cp-a=https://10.0.0.1:2380" {
		t.Fatalf("alone: %+v", got)
	}
	if err := WriteInit(Joining("cp-a=https://10.0.0.1:2380,cp-b=https://10.0.0.2:2380")); err != nil {
		t.Fatal(err)
	}
	if got := ReadInit("cp-b", "10.0.0.2"); got.State != "existing" || got.InitialCluster != "cp-a=https://10.0.0.1:2380,cp-b=https://10.0.0.2:2380" {
		t.Fatalf("joining: %+v", got)
	}
	if ClientURL("fd00::1") != "https://[fd00::1]:2379" || HostOf(PeerURL("10.0.0.9")) != "10.0.0.9" {
		t.Fatal("URLs")
	}
}

func TestKind(t *testing.T) {
	KindFile = filepath.Join(t.TempDir(), "datastore")
	if Kind() != "" {
		t.Fatal("kind before a cluster")
	}
	if err := SetKind(Etcd); err != nil || Kind() != Etcd {
		t.Fatalf("kind %q %v", Kind(), err)
	}
}
