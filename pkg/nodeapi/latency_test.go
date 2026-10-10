package nodeapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/rt"
)

// fakeCyclictest writes a one-thread report wherever --json= points.
const fakeCyclictest = `#!/bin/sh
for a in "$@"; do case $a in --json=*) out=${a#--json=};; esac; done
printf '{"thread":{"0":{"histogram":{"4":99,"9":1},"cycles":100,"min":3,"max":9,"avg":4.1,"cpu":2}}}' > "$out"
`

func TestLatencyTestsRunInOrder(t *testing.T) {
	dir := t.TempDir()
	rt.Cyclictest = filepath.Join(dir, "cyclictest")
	if err := os.WriteFile(rt.Cyclictest, []byte(fakeCyclictest), 0o755); err != nil {
		t.Fatal(err)
	}
	l := newLatencyTests(filepath.Join(dir, "tests"))
	st := latencyStorage{l}
	for _, n := range []string{"second", "first"} {
		if _, err := st.Create(context.Background(), &node.LatencyTest{ObjectMeta: metav1.ObjectMeta{Name: n},
			Spec: node.LatencyTestSpec{CPUs: "2", DurationSeconds: 1}}, nil, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := st.Create(context.Background(), &node.LatencyTest{ObjectMeta: metav1.ObjectMeta{Name: "bad"},
		Spec: node.LatencyTestSpec{Priority: 200}}, nil, nil); err == nil {
		t.Fatal("a priority of 200 was taken")
	}
	if name, _, _ := l.next(); name != "second" {
		t.Fatalf("next is %q, not the oldest", name)
	}
	// A test that was running when the node went down failed.
	r, _ := l.load("first")
	r.Status.Phase = "Running"
	_ = l.save("first", r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		obj, _ := st.Get(context.Background(), "second", nil)
		if obj.(*node.LatencyTest).Status.Phase == "Succeeded" || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	obj, _ := st.Get(context.Background(), "second", nil)
	got := obj.(*node.LatencyTest).Status
	if got.Phase != "Succeeded" || got.MaxMicroseconds != 9 || got.P99Microseconds != 4 || got.FinishedAt == nil {
		t.Fatalf("second: %+v", got)
	}
	if obj, _ := st.Get(context.Background(), "first", nil); obj.(*node.LatencyTest).Status.Phase != "Failed" {
		t.Fatalf("an interrupted test: %+v", obj.(*node.LatencyTest).Status)
	}
	if _, _, err := st.Delete(context.Background(), "second", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(context.Background(), "second", nil); err == nil {
		t.Fatal("a deleted test is still there")
	}
}
