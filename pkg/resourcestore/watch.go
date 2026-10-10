package resourcestore

import (
	"sync"
)

// watchers hands committed changes to the watches that follow them.
type watchers struct {
	mu   sync.Mutex
	next int64
	subs map[int64]*sub
}

type sub struct {
	key, end []byte
	ch       chan batch
}

// batch is the changes of one revision that a watch asked for.
type batch struct {
	rev    int64
	events []Event
}

func newWatchers() *watchers { return &watchers{subs: map[int64]*sub{}} }

// subscribe follows [key, end); the channel is buffered, and a watch that
// falls behind it is closed, as etcd cancels a slow watcher.
func (w *watchers) subscribe(key, end []byte) (int64, <-chan batch) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.next++
	s := &sub{key: key, end: end, ch: make(chan batch, 1024)}
	w.subs[w.next] = s
	return w.next, s.ch
}

func (w *watchers) cancel(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s, ok := w.subs[id]; ok {
		close(s.ch)
		delete(w.subs, id)
	}
}

func (w *watchers) publish(rev int64, events []Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, s := range w.subs {
		var mine []Event
		for _, e := range events {
			if inRange(e.KV.Key, s.key, s.end) {
				mine = append(mine, e)
			}
		}
		if len(mine) == 0 {
			continue
		}
		select {
		case s.ch <- batch{rev: rev, events: mine}:
		default:
			close(s.ch)
			delete(w.subs, id)
		}
	}
}
