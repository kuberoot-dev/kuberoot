package nodeapi

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

func TestPollWatchSendsInitialEventsAndTheirBookmark(t *testing.T) {
	list := func(context.Context) (map[string]runtime.Object, error) {
		return map[string]runtime.Object{
			"kine": &node.NodeService{ObjectMeta: metav1.ObjectMeta{Name: "kine", ResourceVersion: "7"}},
			"bird": &node.NodeService{ObjectMeta: metav1.ObjectMeta{Name: "bird", ResourceVersion: "8"}},
		}, nil
	}
	yes := true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := pollWatch(ctx, &metainternalversion.ListOptions{SendInitialEvents: &yes, AllowWatchBookmarks: true},
		func() runtime.Object { return &node.NodeService{} }, list)
	defer w.Stop()
	var got []watch.EventType
	var names []string
	for len(got) < 3 {
		select {
		case e := <-w.ResultChan():
			got = append(got, e.Type)
			m, _ := meta.Accessor(e.Object)
			names = append(names, m.GetName())
			if e.Type == watch.Bookmark && m.GetAnnotations()[initialEventsEnd] != "true" {
				t.Errorf("bookmark without %s: %v", initialEventsEnd, m.GetAnnotations())
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("events so far: %v", got)
		}
	}
	if got[0] != watch.Added || got[1] != watch.Added || got[2] != watch.Bookmark || names[0] != "bird" || names[1] != "kine" {
		t.Errorf("events %v for %v", got, names)
	}
}

func TestPollWatchWithoutInitialEventsSendsOnlyChanges(t *testing.T) {
	list := func(context.Context) (map[string]runtime.Object, error) {
		return map[string]runtime.Object{"kine": &node.NodeService{ObjectMeta: metav1.ObjectMeta{Name: "kine", ResourceVersion: "7"}}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := pollWatch(ctx, &metainternalversion.ListOptions{}, func() runtime.Object { return &node.NodeService{} }, list)
	defer w.Stop()
	select {
	case e := <-w.ResultChan():
		t.Fatalf("unexpected %v", e.Type)
	case <-time.After(2500 * time.Millisecond):
	}
}
