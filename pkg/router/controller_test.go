package router

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func object(generation int64) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGeneration(generation)
	return u
}

func TestSpecChangedOnlyOnNewGeneration(t *testing.T) {
	status := object(3)
	status.Object["status"] = map[string]any{"observedGeneration": int64(3)}
	if specChanged(object(3), status) {
		t.Error("a status write started a reconcile")
	}
	annotated := object(3)
	annotated.SetAnnotations(map[string]string{"description": "unrelated"})
	if specChanged(object(3), annotated) {
		t.Error("an annotation started a reconcile")
	}
	if !specChanged(object(3), object(4)) {
		t.Error("a spec change did not start a reconcile")
	}
	deleting := object(3)
	now := metav1.Now()
	deleting.SetDeletionTimestamp(&now)
	if !specChanged(object(3), deleting) {
		t.Error("a deletion did not start a reconcile")
	}
	if !specChanged("tombstone", object(3)) {
		t.Error("an unknown object must start a reconcile")
	}
}

func TestReportAllYieldsToASpecChange(t *testing.T) {
	c := &Controller{poke: make(chan struct{}, 1)}
	all := make([]listed, 10)
	reported := 0
	c.reportAll(all, func(listed) {
		reported++
		if reported == 3 {
			c.signal() // an edit arrives while statuses are written
		}
	})
	if reported != 3 {
		t.Fatalf("reported %d statuses after the edit arrived, want 3", reported)
	}
	if !c.changePending() {
		t.Fatal("the pending change was consumed; Run would not reconcile it")
	}
	if !c.changePending() {
		t.Fatal("changePending must leave the signal in place")
	}
}

func TestReportAllWritesEverythingWhenQuiet(t *testing.T) {
	c := &Controller{poke: make(chan struct{}, 1)}
	reported := 0
	c.reportAll(make([]listed, 7), func(listed) { reported++ })
	if reported != 7 {
		t.Fatalf("reported %d of 7", reported)
	}
	var zero Controller // reconcile without Run: no channel, never pending
	if zero.changePending() {
		t.Fatal("nil channel reported a pending change")
	}
	zero.signal()
}
