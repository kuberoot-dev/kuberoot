// Package nodeapi serves the node.kuberoot.dev API: the node's own operating
// system, exposed as Kubernetes resources and reachable with plain kubectl.
package nodeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

// resourceVersion fingerprints an object's content, so it changes exactly when the object does.
func resourceVersion(parts ...any) string {
	h := fnv.New64a()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		_, _ = h.Write(b)
	}
	return fmt.Sprintf("%d", h.Sum64()>>1)
}

// initialEventsEnd marks the bookmark that ends a watch's initial events.
const initialEventsEnd = "k8s.io/initial-events-end"

// pollWatch turns a list function into a watch: it emits MODIFIED, ADDED and
// DELETED events as polled snapshots differ. Node state has no change feed.
//
// A watch that asks for the initial events (sendInitialEvents, how informers
// list since Kubernetes 1.32) gets every object as ADDED and then the
// bookmark that says they are all there. Without it the cluster's
// controllers waited for that bookmark in vain and started over, logging a
// warning every few seconds for every resource of the node API.
func pollWatch(ctx context.Context, opts *metainternalversion.ListOptions, newObj func() runtime.Object, list func(context.Context) (map[string]runtime.Object, error)) watch.Interface {
	events := make(chan watch.Event)
	w := watch.NewProxyWatcher(events)
	go func() {
		defer close(events)
		prev, _ := list(ctx)
		if opts != nil && opts.SendInitialEvents != nil && *opts.SendInitialEvents {
			names := make([]string, 0, len(prev))
			for name := range prev {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if !send(ctx, w, events, watch.Event{Type: watch.Added, Object: prev[name]}) {
					return
				}
			}
			if bookmark := initialEventsBookmark(newObj()); bookmark != nil && opts.AllowWatchBookmarks {
				if !send(ctx, w, events, watch.Event{Type: watch.Bookmark, Object: bookmark}) {
					return
				}
			}
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.StopChan():
				return
			case <-ticker.C:
			}
			cur, err := list(ctx)
			if err != nil {
				continue
			}
			for name, obj := range cur {
				old, ok := prev[name]
				switch {
				case !ok:
					events <- watch.Event{Type: watch.Added, Object: obj}
				case versionOf(old) != versionOf(obj):
					events <- watch.Event{Type: watch.Modified, Object: obj}
				}
			}
			for name, obj := range prev {
				if _, ok := cur[name]; !ok {
					events <- watch.Event{Type: watch.Deleted, Object: obj}
				}
			}
			prev = cur
		}
	}()
	return w
}

// send delivers an event unless the watch ends first.
func send(ctx context.Context, w *watch.ProxyWatcher, events chan<- watch.Event, e watch.Event) bool {
	select {
	case events <- e:
		return true
	case <-ctx.Done():
	case <-w.StopChan():
	}
	return false
}

// initialEventsBookmark makes obj, an empty object of the watched kind,
// the bookmark that ends the initial events.
func initialEventsBookmark(obj runtime.Object) runtime.Object {
	m, err := meta.Accessor(obj)
	if err != nil {
		return nil
	}
	m.SetAnnotations(map[string]string{initialEventsEnd: "true"})
	m.SetResourceVersion("1")
	return obj
}

func versionOf(obj runtime.Object) string {
	if m, ok := obj.(metav1.Object); ok {
		return m.GetResourceVersion()
	}
	return ""
}

func age(t time.Time) string {
	if t.IsZero() {
		return "<unknown>"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func bytesHuman(b int64) string {
	const mi = 1 << 20
	if b < mi {
		return fmt.Sprintf("%dKi", b>>10)
	}
	return fmt.Sprintf("%dMi", b/mi)
}

// table builds the server-side Table kubectl prints for get.
func table(columns []metav1.TableColumnDefinition, objs []runtime.Object, cells func(runtime.Object) []any) *metav1.Table {
	t := &metav1.Table{ColumnDefinitions: columns}
	for _, o := range objs {
		t.Rows = append(t.Rows, metav1.TableRow{Cells: cells(o), Object: runtime.RawExtension{Object: o}})
	}
	return t
}

func col(name, format string) metav1.TableColumnDefinition {
	return metav1.TableColumnDefinition{Name: name, Type: "string", Format: format}
}
