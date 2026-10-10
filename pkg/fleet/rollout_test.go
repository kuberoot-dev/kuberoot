package fleet

import (
	"reflect"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/fleet/v1alpha1"
)

func TestWaveSizes(t *testing.T) {
	for _, c := range []struct {
		waves []intstr.IntOrString
		n     int
		want  []int
	}{
		{nil, 5, []int{1, 1, 5}},
		{nil, 40, []int{1, 4, 40}},
		{nil, 0, []int{0, 0, 0}},
		{[]intstr.IntOrString{intstr.FromString("50%")}, 5, []int{5}},
		// As the CRD's default writes them: "1" is one node, not 1%.
		{[]intstr.IntOrString{intstr.FromString("1"), intstr.FromString("10%"), intstr.FromString("100%")}, 40, []int{1, 4, 40}},
		{[]intstr.IntOrString{intstr.FromInt32(2), intstr.FromString("50%"), intstr.FromInt32(1)}, 6, []int{2, 3, 6}},
	} {
		if got := waveSizes(c.waves, c.n); !reflect.DeepEqual(got, c.want) {
			t.Errorf("waveSizes(%v, %d) = %v, want %v", c.waves, c.n, got, c.want)
		}
	}
}

// fleetOf is a fleet of connected nodes, none with a release yet.
func fleetOf(names ...string) map[string]nodeView {
	out := map[string]nodeView{}
	for _, n := range names {
		out[n] = nodeView{Labels: map[string]string{"site": "shop"}, Connected: true}
	}
	return out
}

// apply gives the nodes the release, and the update phase to each.
func apply(nodes map[string]nodeView, names []string, r v1.Release) {
	for _, n := range names {
		v := nodes[n]
		rel := r
		v.Release = &rel
		nodes[n] = v
	}
}

func setPhase(nodes map[string]nodeView, name, phase string) {
	v := nodes[name]
	v.Update = &v1.UpdateStatus{Version: v.Release.Version, Phase: phase}
	nodes[name] = v
}

func rollout(spec v1.FleetRolloutSpec) *v1.FleetRollout {
	r := &v1.FleetRollout{Spec: spec}
	r.Name = "r"
	return r
}

func advance(t *testing.T, r *v1.FleetRollout, nodes map[string]nodeView, now time.Time) []string {
	t.Helper()
	st, assign, err := plan(r, nodes, now)
	if err != nil {
		t.Fatal(err)
	}
	r.Status = st
	apply(nodes, assign, r.Spec.Release)
	slices.Sort(assign)
	return assign
}

func TestRolloutWaves(t *testing.T) {
	nodes := fleetOf("f1", "f2", "f3", "f4", "f5")
	r := rollout(v1.FleetRolloutSpec{Release: v2})
	now := time.Now()
	if got := advance(t, r, nodes, now); !reflect.DeepEqual(got, []string{"f1"}) {
		t.Fatalf("wave 1 = %v", got)
	}
	if got := advance(t, r, nodes, now); got != nil || r.Status.Wave != "1/3" {
		t.Fatalf("wave 1 not through yet, but %v, wave %s", got, r.Status.Wave)
	}
	setPhase(nodes, "f1", v1.UpdateUpdated)
	// 10% of five is one node: wave 2 adds none, wave 3 takes the rest.
	if got := advance(t, r, nodes, now); !reflect.DeepEqual(got, []string{"f2", "f3", "f4", "f5"}) || r.Status.Wave != "3/3" {
		t.Fatalf("wave 3 = %v, %s", got, r.Status.Wave)
	}
	for _, n := range []string{"f2", "f3", "f4", "f5"} {
		setPhase(nodes, n, v1.UpdateUpdated)
	}
	advance(t, r, nodes, now)
	if r.Status.Phase != v1.RolloutComplete || r.Status.Updated != "5/5" {
		t.Errorf("status %+v", r.Status)
	}
}

func TestRolloutStopsOnRollback(t *testing.T) {
	nodes := fleetOf("f1", "f2", "f3")
	r := rollout(v1.FleetRolloutSpec{Release: v2})
	advance(t, r, nodes, time.Now())
	setPhase(nodes, "f1", v1.UpdateRolledBack)
	if got := advance(t, r, nodes, time.Now()); got != nil || r.Status.Phase != v1.RolloutStopped || r.Status.Failed != 1 {
		t.Fatalf("after a rollback: %v, %+v", got, r.Status)
	}
	if nodes["f2"].Release != nil {
		t.Error("f2 got the release of a stopped rollout")
	}
	// A failure allowed for goes on.
	r.Spec.MaxFailures = 1
	if got := advance(t, r, nodes, time.Now()); len(got) != 2 || r.Status.Phase != v1.RolloutProgressing {
		t.Errorf("with maxFailures 1: %v, %+v", got, r.Status)
	}
}

func TestRolloutOffline(t *testing.T) {
	for _, policy := range []string{v1.OfflineWait, v1.OfflineSkip} {
		nodes := fleetOf("f1", "f2", "f3")
		r := rollout(v1.FleetRolloutSpec{Release: v2, Offline: policy,
			Waves: []intstr.IntOrString{intstr.FromInt32(2), intstr.FromString("100%")}})
		advance(t, r, nodes, time.Now())
		setPhase(nodes, "f1", v1.UpdateUpdated)
		v := nodes["f2"]
		v.Connected = false
		nodes["f2"] = v
		got := advance(t, r, nodes, time.Now())
		switch policy {
		case v1.OfflineWait:
			if got != nil || r.Status.Nodes[1].State != stateOffline {
				t.Errorf("wait: %v, %+v", got, r.Status.Nodes)
			}
		case v1.OfflineSkip:
			if !reflect.DeepEqual(got, []string{"f3"}) || r.Status.Nodes[1].State != stateSkipped {
				t.Errorf("skip: %v, %+v", got, r.Status.Nodes)
			}
		}
		// Back, it catches up: its release was set all along.
		v.Connected = true
		nodes["f2"] = v
		setPhase(nodes, "f2", v1.UpdateUpdated)
		advance(t, r, nodes, time.Now())
		if policy == v1.OfflineWait && r.Status.Wave != "2/2" {
			t.Errorf("wait: after f2 caught up, wave %s", r.Status.Wave)
		}
	}
}

func TestRolloutPauseSoakSelector(t *testing.T) {
	nodes := fleetOf("f1", "f2", "lab")
	v := nodes["lab"]
	v.Labels = map[string]string{"site": "lab"}
	nodes["lab"] = v
	r := rollout(v1.FleetRolloutSpec{Release: v2, Paused: true,
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"site": "shop"}},
		Soak:     &metav1.Duration{Duration: time.Hour}})
	now := time.Now()
	if got := advance(t, r, nodes, now); got != nil || r.Status.Phase != v1.RolloutPaused || len(r.Status.Nodes) != 2 {
		t.Fatalf("paused: %v, %+v", got, r.Status)
	}
	r.Spec.Paused = false
	advance(t, r, nodes, now)
	setPhase(nodes, "f1", v1.UpdateUpdated)
	if got := advance(t, r, nodes, now); got != nil {
		t.Fatalf("soaking, yet %v", got)
	}
	if got := advance(t, r, nodes, now.Add(2*time.Hour)); !reflect.DeepEqual(got, []string{"f2"}) {
		t.Fatalf("after the soak: %v", got)
	}
	if nodes["lab"].Release != nil {
		t.Error("the selector let lab in")
	}
}
