package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

func dirs(t *testing.T) {
	d := t.TempDir()
	StateDir, BlobDir, RunDir, LogDir = d+"/state", d+"/blobs", d+"/run", d+"/log"
}

func sum(b string) string {
	h := sha256.Sum256([]byte(b))
	return hex.EncodeToString(h[:])
}

func TestArgs(t *testing.T) {
	got := strings.Join(Args(node.ModelServerSpec{SHA256: "abc", Model: "qwen", Port: 8100, ContextSize: 4096, Parallel: 2}), " ")
	for _, want := range []string{"--model " + BlobDir + "/abc.gguf", "--alias qwen", "--port 8100", "--ctx-size 4096", "--parallel 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
	if strings.Contains(got, "--threads") {
		t.Errorf("threads passed when unset: %s", got)
	}
}

func TestFetchChecksTheHash(t *testing.T) {
	dirs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("weights")) }))
	defer srv.Close()
	s := &Servers{}
	if err := s.fetch(context.Background(), srv.URL, sum("other"), Blob(sum("other"))); err == nil || !strings.Contains(err.Error(), "not the") {
		t.Fatalf("a mismatching download was kept: %v", err)
	}
	if _, err := os.Stat(Blob(sum("other"))); err == nil {
		t.Fatal("the mismatching weights are on disk")
	}
	if err := s.fetch(context.Background(), srv.URL, sum("weights"), Blob(sum("weights"))); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(Blob(sum("weights"))); string(raw) != "weights" {
		t.Fatalf("blob holds %q", raw)
	}
}

func TestPruneKeepsCurrentAndPrevious(t *testing.T) {
	dirs(t)
	s := &Servers{}
	if err := os.MkdirAll(BlobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"v1", "v2", "v3"} {
		if err := os.WriteFile(Blob(sum(b)), []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec := node.ModelServerSpec{URL: "u", SHA256: sum("v1"), Model: "m", Port: 8100}
	if err := s.Save("m", spec); err != nil {
		t.Fatal(err)
	}
	spec.SHA256 = sum("v2")
	if err := s.Save("m", spec); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	for b, keep := range map[string]bool{"v1": true, "v2": true, "v3": false} {
		if _, err := os.Stat(Blob(sum(b))); (err == nil) != keep {
			t.Errorf("%s kept=%v, want %v", b, err == nil, keep)
		}
	}
	if err := s.Delete("m"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Blob(sum("v2"))); err == nil {
		t.Error("weights stayed after their server went")
	}
}

func TestStatusTellsAServerThatDied(t *testing.T) {
	dirs(t)
	s := &Servers{}
	spec := node.ModelServerSpec{URL: "u", SHA256: sum("v1"), Model: "m", Port: 8100}
	if st := s.Status("m", spec); st.Phase != "Starting" {
		t.Fatalf("never started: %+v", st)
	}
	if err := os.MkdirAll(RunDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.pidFile("m")+".config", []byte(configKey(spec, nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := s.Status("m", spec); st.Phase != "Failed" {
		t.Fatalf("started and gone: %+v", st)
	}
	spec.SHA256 = sum("v2")
	if st := s.Status("m", spec); st.Phase != "Starting" {
		t.Fatalf("other weights asked for: %+v", st)
	}
}

func TestAssignGPUs(t *testing.T) {
	dirs(t)
	orig := GPUsPresent
	GPUsPresent = func() []int32 { return []int32{0, 1, 2} }
	defer func() { GPUsPresent = orig }()
	s := &Servers{}
	save := func(name string, n int32) node.ModelServerSpec {
		spec := node.ModelServerSpec{URL: "u", SHA256: sum(name), Model: name, Port: 8100, GPUs: n}
		if err := s.Save(name, spec); err != nil {
			t.Fatal(err)
		}
		return spec
	}
	a := save("a", 2)
	got, err := s.assignGPUs("a", a)
	if err != nil || joinInts(got) != "0,1" {
		t.Fatalf("a: %v %v", got, err)
	}
	b := save("b", 2)
	if _, err := s.assignGPUs("b", b); err == nil || !strings.Contains(err.Error(), "1 free") {
		t.Fatalf("b got GPUs a holds: %v", err)
	}
	b = save("b", 1)
	if got, err := s.assignGPUs("b", b); err != nil || joinInts(got) != "2" {
		t.Fatalf("b: %v %v", got, err)
	}
	// Asked again, a keeps what it has.
	if got, _ := s.assignGPUs("a", a); joinInts(got) != "0,1" {
		t.Fatalf("a moved: %v", got)
	}
	a.EngineSHA256 = sum("cuda")
	if !strings.Contains(configKey(a, []int32{0, 1}), EngineBlob(sum("cuda"))) || !strings.Contains(strings.Join(Args(a), " "), "--n-gpu-layers 999") {
		t.Fatal("a GPU server does not run its engine on its GPUs")
	}
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if got := s.GPUsOf("a"); got != nil {
		t.Fatalf("GPUs kept after delete: %v", got)
	}
}

func TestGPUServerNeedsAnEngine(t *testing.T) {
	dirs(t)
	s := &Servers{}
	err := s.Apply(context.Background(), "m", node.ModelServerSpec{URL: "u", SHA256: sum("w"), Model: "m", Port: 8100, GPUs: 1})
	if err == nil || !strings.Contains(err.Error(), "engine") {
		t.Fatalf("a GPU server started with no engine: %v", err)
	}
}

func TestPruneKeepsEngines(t *testing.T) {
	dirs(t)
	s := &Servers{}
	_ = os.MkdirAll(BlobDir, 0o700)
	for _, f := range []string{Blob(sum("w")), EngineBlob(sum("e1")), EngineBlob(sum("e2")), EngineBlob(sum("old"))} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec := node.ModelServerSpec{URL: "u", SHA256: sum("w"), Model: "m", Port: 8100, EngineURL: "e", EngineSHA256: sum("e1")}
	_ = s.Save("m", spec)
	spec.EngineSHA256 = sum("e2")
	_ = s.Save("m", spec)
	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	for f, keep := range map[string]bool{Blob(sum("w")): true, EngineBlob(sum("e1")): true, EngineBlob(sum("e2")): true, EngineBlob(sum("old")): false} {
		if _, err := os.Stat(f); (err == nil) != keep {
			t.Errorf("%s kept=%v, want %v", filepath.Base(f), err == nil, keep)
		}
	}
}
