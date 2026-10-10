package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStateArchiveOfAnEtcdMember(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "lib/kuberoot/pki/etcd-ca.key"), "etcd ca key")
	writeFile(t, filepath.Join(root, "lib/kuberoot/datastore"), "etcd\n")
	running, snapshot := etcdRunning, etcdSnapshot
	defer func() { etcdRunning, etcdSnapshot = running, snapshot }()
	etcdRunning = func() bool { return true }
	etcdSnapshot = func(_ context.Context, dst string) error { return os.WriteFile(dst, []byte("etcd snapshot"), 0o600) }

	var buf bytes.Buffer
	if err := writeStateArchive(&buf, root, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "state.tar.gz")
	if err := os.WriteFile(archive, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	mnt := t.TempDir()
	if err := extractState(archive, mnt); err != nil {
		t.Fatalf("an archive with an etcd snapshot is a backup of a control plane: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(mnt, etcdRestore)); string(got) != "etcd snapshot" {
		t.Fatalf("the snapshot kinit restores from: %q", got)
	}
	if _, err := os.Stat(filepath.Join(mnt, kineStore)); err == nil {
		t.Fatal("a kine store in an etcd member's archive")
	}
	if got, _ := os.ReadFile(filepath.Join(mnt, "lib/kuberoot/pki/etcd-ca.key")); string(got) != "etcd ca key" {
		t.Fatal("the store's authority is not carried")
	}
}
