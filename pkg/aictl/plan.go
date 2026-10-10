// Package aictl runs a cluster's language models through the node APIs of
// its nodes: it places each model's replicas, rolls a new version out one
// node at a time behind a first node that must answer, rolls back when it
// does not, and serves every model at one OpenAI-compatible endpoint.
package aictl

import (
	"slices"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/ai/v1alpha1"
)

// Node is a node of the cluster as the controller sees it.
type Node struct {
	Name, Address string
	Ready         bool
	DownSince     time.Time
	// ControlPlane: the node runs the cluster's API; models go there only
	// when no other node can take them.
	ControlPlane bool
	// GPUs the node has.
	GPUs int
}

// Server is a model server as a node reports it: the version it runs, or
// is asked to run while it is not running it yet.
type Server struct {
	Node, Version, Phase string
}

// Version names a version of a model: its weights' SHA-256, and its
// engine's after a + when it has one of its own.
func Version(s v1.Source) string {
	if s.Engine != nil {
		return s.SHA256 + "+" + s.Engine.SHA256
	}
	return s.SHA256
}

var (
	// FailoverAfter: a replica's node down this long is replaced.
	FailoverAfter = 30 * time.Second
	// RolloutTimeout ends a new version that is not ready on its first node
	// in time, its download included.
	RolloutTimeout = 15 * time.Minute
	// ProbeFailures: a first node that fails this many trial requests in a
	// row fails the version.
	ProbeFailures = 3
)

// Place picks the model's nodes: those it has, unless they are gone or
// down for long, and the least loaded ready nodes for the rest.
//
// A model on GPUs goes only to nodes with as many free: gpusUsed counts, by
// node, the GPUs of other models' replicas.
func Place(m v1.Model, nodes []Node, load map[string]int, gpusUsed map[string]int, now time.Time) []string {
	byName := map[string]Node{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	var out []string
	for _, r := range m.Status.Replicas {
		n, ok := byName[r.Node]
		if ok && (n.Ready || now.Sub(n.DownSince) < FailoverAfter) && !slices.Contains(out, r.Node) {
			out = append(out, r.Node)
		}
	}
	var free []Node
	for _, n := range nodes {
		fits := m.Spec.GPUs == 0 || n.GPUs-gpusUsed[n.Name] >= int(m.Spec.GPUs)
		if n.Ready && fits && !slices.Contains(out, n.Name) {
			free = append(free, n)
		}
	}
	sort.Slice(free, func(i, j int) bool {
		if free[i].ControlPlane != free[j].ControlPlane {
			return !free[i].ControlPlane
		}
		if load[free[i].Name] != load[free[j].Name] {
			return load[free[i].Name] < load[free[j].Name]
		}
		return free[i].Name < free[j].Name
	})
	for _, n := range free {
		if len(out) >= int(m.Spec.Replicas) {
			break
		}
		out = append(out, n.Name)
	}
	if len(out) > int(m.Spec.Replicas) {
		// Fewer replicas asked for: the ones on nodes that are down go first.
		sort.SliceStable(out, func(i, j int) bool { return byName[out[i]].Ready && !byName[out[j]].Ready })
		out = out[:m.Spec.Replicas]
	}
	return out
}

// Rollout decides which version each replica runs, and the model's status
// for it. probes counts, for the first node of a new version, its trial
// requests: positive for one answered, negative for failures in a row.
func Rollout(m v1.Model, replicas []string, servers map[string]Server, probe int, now time.Time) (map[string]string, v1.ModelStatus) {
	st := m.Status
	st.Replicas = nil
	target := m.Spec.Source
	tv := Version(target)
	want := map[string]string{}
	all := func(sha string) {
		for _, r := range replicas {
			want[r] = sha
		}
	}
	readyOn := func(node, sha string) bool {
		s, ok := servers[node]
		return ok && s.Version == sha && s.Phase == "Ready"
	}
	if st.FailedVersion != "" && st.FailedVersion != tv {
		st.FailedVersion = ""
	}
	switch {
	case st.Current == nil:
		all(tv)
		st.Phase, st.Message = "Deploying", ""
		if countReady(replicas, tv, readyOn) == len(replicas) && len(replicas) == int(m.Spec.Replicas) {
			cur := target
			st.Current, st.Phase = &cur, "Ready"
		}
	case tv == Version(*st.Current) || tv == st.FailedVersion:
		all(Version(*st.Current))
		st.Canary, st.RolloutStartedAt = "", nil
		st.Phase, st.Message = "Ready", ""
		if tv == st.FailedVersion {
			st.Phase = "RolledBack"
			st.Message = "the new version did not answer on its first node; the version before serves"
		} else if countReady(replicas, Version(*st.Current), readyOn) < int(m.Spec.Replicas) {
			st.Phase = "Degraded"
		}
	default:
		cur := Version(*st.Current)
		if !slices.Contains(replicas, st.Canary) {
			st.Canary = replicas[0]
		}
		if st.RolloutStartedAt == nil {
			t := metav1.NewTime(now)
			st.RolloutStartedAt = &t
		}
		fail := func(why string) {
			all(cur)
			st.FailedVersion, st.Canary, st.RolloutStartedAt = tv, "", nil
			st.Phase, st.Message = "RolledBack", why
		}
		canaryReady := readyOn(st.Canary, tv)
		switch {
		case servers[st.Canary].Version == tv && servers[st.Canary].Phase == "Failed":
			fail("the new version failed on " + st.Canary)
		case !canaryReady && now.Sub(st.RolloutStartedAt.Time) > RolloutTimeout:
			fail("the new version was not ready on " + st.Canary + " in " + RolloutTimeout.String())
		case canaryReady && probe <= -ProbeFailures:
			fail("the new version did not answer on " + st.Canary)
		default:
			all(cur)
			want[st.Canary] = tv
			st.Phase, st.Message = "RollingOut", "trying the new version on "+st.Canary
			if canaryReady && probe > 0 {
				// The others follow one at a time, each once the ones
				// before it serve the new version.
				st.Message = "moving to the new version"
				for _, r := range replicas {
					if r == st.Canary {
						continue
					}
					if s := servers[r]; s.Version == tv {
						want[r] = tv
						if !readyOn(r, tv) {
							break
						}
						continue
					}
					want[r] = tv
					break
				}
				if countReady(replicas, tv, readyOn) == len(replicas) && len(replicas) == int(m.Spec.Replicas) {
					prev, next := *st.Current, target
					st.Previous, st.Current = &prev, &next
					st.Canary, st.RolloutStartedAt = "", nil
					st.Phase, st.Message = "Ready", ""
				}
			}
		}
	}
	return want, st
}

func countReady(replicas []string, sha string, readyOn func(string, string) bool) int {
	n := 0
	for _, r := range replicas {
		if readyOn(r, sha) {
			n++
		}
	}
	return n
}
