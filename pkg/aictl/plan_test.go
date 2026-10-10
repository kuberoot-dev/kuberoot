package aictl

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/ai/v1alpha1"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func model(replicas int32, sha string, current string) v1.Model {
	m := v1.Model{Spec: v1.ModelSpec{Replicas: replicas, Source: v1.Source{URL: "http://x/" + sha, SHA256: sha}}}
	if current != "" {
		m.Status.Current = &v1.Source{URL: "http://x/" + current, SHA256: current}
	}
	return m
}

func ready(sha string, nodes ...string) map[string]Server {
	out := map[string]Server{}
	for _, n := range nodes {
		out[n] = Server{Node: n, Version: sha, Phase: "Ready"}
	}
	return out
}

func TestPlaceKeepsAndSpreads(t *testing.T) {
	now := time.Now()
	nodes := []Node{{Name: "a", Ready: true}, {Name: "b", Ready: true}, {Name: "c", Ready: false, DownSince: now.Add(-time.Minute)}, {Name: "d", Ready: true}}
	m := model(2, shaA, "")
	m.Status.Replicas = []v1.Replica{{Node: "c"}, {Node: "b"}}
	got := Place(m, nodes, map[string]int{"a": 2, "d": 0}, nil, now)
	if len(got) != 2 || got[0] != "b" || got[1] != "d" {
		t.Fatalf("got %v: want b kept, c replaced by the least loaded d", got)
	}
	// A node down only for a moment keeps its replica.
	nodes[2].DownSince = now.Add(-5 * time.Second)
	if got := Place(m, nodes, nil, nil, now); got[0] != "c" || got[1] != "b" {
		t.Fatalf("got %v: a short outage moved the replica", got)
	}
}

func TestFirstDeploy(t *testing.T) {
	m := model(2, shaA, "")
	want, st := Rollout(m, []string{"a", "b"}, nil, 0, time.Now())
	if want["a"] != shaA || want["b"] != shaA || st.Phase != "Deploying" {
		t.Fatalf("want %v status %+v", want, st)
	}
	_, st = Rollout(m, []string{"a", "b"}, ready(shaA, "a", "b"), 0, time.Now())
	if st.Phase != "Ready" || st.Current == nil || st.Current.SHA256 != shaA {
		t.Fatalf("status %+v", st)
	}
}

func TestCanaryThenOneAtATime(t *testing.T) {
	now := time.Now()
	nodes := []string{"a", "b", "c"}
	m := model(3, shaB, shaA)
	want, st := Rollout(m, nodes, ready(shaA, nodes...), 0, now)
	if st.Canary != "a" || want["a"] != shaB || want["b"] != shaA || want["c"] != shaA || st.Phase != "RollingOut" {
		t.Fatalf("canary step: want %v status %+v", want, st)
	}
	m.Status = st
	servers := ready(shaA, "b", "c")
	servers["a"] = Server{Node: "a", Version: shaB, Phase: "Ready"}
	// Ready, not tried yet: the others wait.
	if want, _ := Rollout(m, nodes, servers, 0, now); want["b"] != shaA {
		t.Fatalf("moved on before the trial request: %v", want)
	}
	// Answered: one more node at a time.
	want, st = Rollout(m, nodes, servers, 1, now)
	if want["b"] != shaB || want["c"] != shaA {
		t.Fatalf("after the trial: %v", want)
	}
	servers["b"] = Server{Node: "b", Version: shaB, Phase: "Loading"}
	if want, _ := Rollout(m, nodes, servers, 1, now); want["c"] != shaA {
		t.Fatalf("c moved while b still loads: %v", want)
	}
	servers = ready(shaB, nodes...)
	_, st = Rollout(m, nodes, servers, 1, now)
	if st.Phase != "Ready" || st.Current.SHA256 != shaB || st.Previous.SHA256 != shaA || st.Canary != "" {
		t.Fatalf("finished: %+v", st)
	}
}

func TestRollback(t *testing.T) {
	now := time.Now()
	nodes := []string{"a", "b"}
	m := model(2, shaB, shaA)
	_, m.Status = Rollout(m, nodes, ready(shaA, nodes...), 0, now)
	servers := ready(shaA, "b")
	servers["a"] = Server{Node: "a", Version: shaB, Phase: "Ready"}
	want, st := Rollout(m, nodes, servers, -ProbeFailures, now)
	if want["a"] != shaA || st.FailedVersion != shaB || st.Phase != "RolledBack" {
		t.Fatalf("unanswered version kept: want %v status %+v", want, st)
	}
	// Not tried again while the spec names it.
	m.Status = st
	if want, st := Rollout(m, nodes, ready(shaA, nodes...), 0, now); want["a"] != shaA || st.Phase != "RolledBack" {
		t.Fatalf("tried again: %v %+v", want, st)
	}
	// Failed on the node, or too slow: rolled back too.
	m = model(2, shaB, shaA)
	_, m.Status = Rollout(m, nodes, ready(shaA, nodes...), 0, now)
	servers["a"] = Server{Node: "a", Version: shaB, Phase: "Failed"}
	if _, st := Rollout(m, nodes, servers, 0, now); st.FailedVersion != shaB {
		t.Fatalf("a failed server kept: %+v", st)
	}
	servers["a"] = Server{Node: "a", Version: shaB, Phase: "Downloading"}
	started := metav1.NewTime(now.Add(-RolloutTimeout - time.Minute))
	m.Status.RolloutStartedAt = &started
	if _, st := Rollout(m, nodes, servers, 0, now); st.FailedVersion != shaB {
		t.Fatalf("a slow version kept: %+v", st)
	}
}

func TestPlaceSparesTheControlPlane(t *testing.T) {
	nodes := []Node{{Name: "a", Ready: true, ControlPlane: true}, {Name: "b", Ready: true}, {Name: "c", Ready: true}}
	got := Place(model(2, shaA, ""), nodes, map[string]int{"b": 3, "c": 3}, nil, time.Now())
	if got[0] != "b" || got[1] != "c" {
		t.Fatalf("got %v: busy workers go before the control plane", got)
	}
	if got := Place(model(3, shaA, ""), nodes, nil, nil, time.Now()); len(got) != 3 {
		t.Fatalf("got %v: the control plane takes what the workers cannot", got)
	}
}

func TestPlaceCountsGPUs(t *testing.T) {
	nodes := []Node{{Name: "cpu", Ready: true}, {Name: "g1", Ready: true, GPUs: 1}, {Name: "g4", Ready: true, GPUs: 4}}
	m := model(2, shaA, "")
	m.Spec.GPUs = 2
	if got := Place(m, nodes, nil, nil, time.Now()); len(got) != 1 || got[0] != "g4" {
		t.Fatalf("got %v: only g4 has two GPUs", got)
	}
	if got := Place(m, nodes, nil, map[string]int{"g4": 3}, time.Now()); len(got) != 0 {
		t.Fatalf("got %v: g4 has one GPU free", got)
	}
}

func TestEngineChangeIsANewVersion(t *testing.T) {
	now := time.Now()
	nodes := []string{"a", "b"}
	m := model(2, shaA, shaA)
	m.Spec.Source.Engine = &v1.Engine{URL: "http://x/engine", SHA256: shaB}
	want, st := Rollout(m, nodes, ready(shaA, nodes...), 0, now)
	v := Version(m.Spec.Source)
	if st.Phase != "RollingOut" || want[st.Canary] != v || v != shaA+"+"+shaB {
		t.Fatalf("a new engine went everywhere at once, or not at all: %v %+v", want, st)
	}
}
