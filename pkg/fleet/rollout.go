package fleet

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/fleet/v1alpha1"
)

// Node states in a rollout, besides the update phases.
const (
	statePending = "Pending" // its wave has not come
	stateWaiting = "Waiting" // given the release, not started yet
	stateOffline = "Offline" // given the release, not connected
	stateSkipped = "Skipped" // offline when its wave came, and the rollout skips such nodes
	stateRemoved = "Removed" // its FleetNode is gone
	// stateSuperseded: a newer rollout gave the node its release.
	stateSuperseded = "Superseded"
)

// defaultWaves: one node first, then a tenth of them, then the rest.
var defaultWaves = []intstr.IntOrString{intstr.FromInt32(1), intstr.FromString("10%"), intstr.FromString("100%")}

// waveSizes turns waves into how many nodes have the release once each is
// through: growing, at least one more node than none, the last all n.
func waveSizes(waves []intstr.IntOrString, n int) []int {
	if len(waves) == 0 {
		waves = defaultWaves
	}
	sizes := make([]int, len(waves))
	prev := 0
	for i, w := range waves {
		size := int(w.IntVal)
		if w.Type == intstr.String {
			if pct, ok := strings.CutSuffix(w.StrVal, "%"); ok {
				if p, err := strconv.ParseFloat(pct, 64); err == nil {
					size = int(math.Ceil(p * float64(n) / 100))
				}
			} else {
				size, _ = strconv.Atoi(w.StrVal) // a count written as a string, "1"
			}
		}
		size = max(min(size, n), prev)
		if i == len(waves)-1 {
			size = n
		}
		sizes[i], prev = size, size
	}
	return sizes
}

// nodeView is what a rollout needs of a FleetNode.
type nodeView struct {
	Labels    map[string]string
	Connected bool
	Release   *v1.Release
	Update    *v1.UpdateStatus
	// Rollout last gave the node its release, and Newer says whether that
	// rollout is newer than the one planning: the newest rollout of a node
	// has it, the older ones leave it alone.
	Rollout string
	Newer   bool
	// Enrolled: the node has joined the fleet, so it can take a release.
	Enrolled bool
}

func sameRelease(a *v1.Release, b v1.Release) bool {
	return a != nil && a.Version == b.Version && a.URL == b.URL && a.Sha256 == b.Sha256
}

// plan advances a rollout one pass: it returns its new status and the nodes
// to give the release now. It never takes a release back from a node: a
// stopped rollout leaves what it started to finish.
func plan(r *v1.FleetRollout, nodes map[string]nodeView, now time.Time) (v1.FleetRolloutStatus, []string, error) {
	st := *r.Status.DeepCopy()
	version := r.Spec.Release.Version
	if st.Release == version {
		// Complete is final: nodes it updated may move on to newer releases.
		// Stopped is final until the spec changes, maxFailures say: a newer
		// rollout taking a failed node over does not restart this one.
		if st.Phase == v1.RolloutComplete || (st.Phase == v1.RolloutStopped && st.ObservedGeneration == r.Generation) {
			return st, nil, nil
		}
	}
	st.ObservedGeneration = r.Generation
	sel := labels.Everything()
	if r.Spec.Selector != nil {
		s, err := metav1.LabelSelectorAsSelector(r.Spec.Selector)
		if err != nil {
			return st, nil, err
		}
		sel = s
	}
	if st.Phase == "" || st.Release != version {
		// A new rollout, or a new release: the nodes it covers are picked now.
		var names []string
		for name, n := range nodes {
			if sel.Matches(labels.Set(n.Labels)) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		st = v1.FleetRolloutStatus{ObservedGeneration: r.Generation, Phase: v1.RolloutProgressing}
		for _, name := range names {
			st.Nodes = append(st.Nodes, v1.RolloutNode{Name: name})
		}
	} else if r.Spec.LateNodes != v1.LateNodesIgnore {
		// Enrolled nodes the selector picks now and did not then join the
		// last wave; a node a newer rollout has is that rollout's.
		in := map[string]bool{}
		for _, rn := range st.Nodes {
			in[rn.Name] = true
		}
		var late []string
		for name, n := range nodes {
			if !in[name] && n.Enrolled && !n.Newer && sel.Matches(labels.Set(n.Labels)) {
				late = append(late, name)
			}
		}
		sort.Strings(late)
		for _, name := range late {
			st.Nodes = append(st.Nodes, v1.RolloutNode{Name: name, Late: true})
		}
	}
	// Waves are sized by the nodes picked at the start, so a late node
	// moves no other node into an earlier wave.
	picked := 0
	for _, rn := range st.Nodes {
		if !rn.Late {
			picked++
		}
	}
	sizes := waveSizes(r.Spec.Waves, picked)
	wave, i := 0, 0
	for j := range st.Nodes {
		if st.Nodes[j].Late {
			st.Nodes[j].Wave = int32(len(sizes))
			continue
		}
		for wave < len(sizes)-1 && i >= sizes[wave] {
			wave++
		}
		st.Nodes[j].Wave = int32(wave + 1)
		i++
	}

	// Where each node is, and the highest wave given the release so far.
	current, updated := 0, 0
	var failed []string
	done := map[int]bool{}
	for i := range st.Nodes {
		rn := &st.Nodes[i]
		n, ok := nodes[rn.Name]
		switch {
		case !ok:
			rn.State = stateRemoved
		case n.Rollout != "" && n.Rollout != r.Name && n.Newer:
			rn.State = stateSuperseded
		case !sameRelease(n.Release, r.Spec.Release):
			rn.State = statePending
		default:
			current = max(current, int(rn.Wave))
			rn.State = stateWaiting
			if n.Update != nil && n.Update.Version == version && n.Update.Phase != "" {
				rn.State = n.Update.Phase
			}
			if !n.Connected && !terminal(rn.State) {
				rn.State = stateOffline
				if r.Spec.Offline == v1.OfflineSkip {
					rn.State = stateSkipped
				}
			}
			if failure(rn.State) {
				msg := rn.Name + " failed"
				if rn.State == v1.UpdateRolledBack {
					msg = rn.Name + " rolled back"
				}
				if n.Update != nil && n.Update.Message != "" {
					msg += ": " + n.Update.Message
				}
				failed = append(failed, msg)
			}
		}
		if rn.State == v1.UpdateUpdated {
			updated++
		}
	}
	waves := len(sizes)
	for w := 1; w <= waves; w++ {
		done[w] = true
	}
	for _, rn := range st.Nodes {
		switch rn.State {
		case v1.UpdateUpdated, v1.UpdateRolledBack, v1.UpdateFailed, stateSkipped, stateRemoved, stateSuperseded:
		default:
			done[int(rn.Wave)] = false
		}
	}
	st.Failed = int32(len(failed))
	st.Updated = fmt.Sprintf("%d/%d", updated, len(st.Nodes))
	st.Wave = fmt.Sprintf("%d/%d", current, waves)
	st.Release = version

	if len(failed) > int(r.Spec.MaxFailures) {
		st.Phase = v1.RolloutStopped
		st.Message = "stopped: " + strings.Join(failed, "; ")
		return st, nil, nil
	}
	var assign []string
	// Nodes of the waves under way that do not have the release yet.
	for _, rn := range st.Nodes {
		if int(rn.Wave) <= current && rn.State == statePending {
			assign = append(assign, rn.Name)
		}
	}
	allDone := true
	for w := 1; w <= current; w++ {
		allDone = allDone && done[w]
	}
	if !allDone || len(assign) > 0 {
		st.Phase, st.WaveDone = v1.RolloutProgressing, nil
		st.Message = fmt.Sprintf("wave %d of %d", current, waves)
		return st, assign, nil
	}
	// The next wave with nodes to give the release to.
	next := 0
	for _, rn := range st.Nodes {
		if int(rn.Wave) > current && rn.State == statePending && (next == 0 || int(rn.Wave) < next) {
			next = int(rn.Wave)
		}
	}
	if next == 0 {
		st.Phase, st.Wave = v1.RolloutComplete, fmt.Sprintf("%d/%d", waves, waves)
		st.Message = fmt.Sprintf("%d of %d nodes run %s", updated, len(st.Nodes), version)
		return st, nil, nil
	}
	if st.WaveDone == nil && current > 0 {
		t := metav1.NewTime(now)
		st.WaveDone = &t
	}
	if r.Spec.Paused {
		st.Phase = v1.RolloutPaused
		st.Message = fmt.Sprintf("paused before wave %d", next)
		return st, nil, nil
	}
	if r.Spec.Soak != nil && st.WaveDone != nil && now.Sub(st.WaveDone.Time) < r.Spec.Soak.Duration {
		st.Phase = v1.RolloutProgressing
		st.Message = fmt.Sprintf("wave %d soaks until %s", current, st.WaveDone.Add(r.Spec.Soak.Duration).UTC().Format(time.RFC3339))
		return st, nil, nil
	}
	for _, rn := range st.Nodes {
		if int(rn.Wave) == next && rn.State == statePending {
			assign = append(assign, rn.Name)
		}
	}
	st.Phase, st.WaveDone = v1.RolloutProgressing, nil
	st.Wave = fmt.Sprintf("%d/%d", next, waves)
	st.Message = fmt.Sprintf("wave %d of %d", next, waves)
	return st, assign, nil
}
