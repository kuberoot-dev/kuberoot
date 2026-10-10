package nodeapi

import (
	"errors"
	"sync"

	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/vm"
)

// objects are things a node keeps on disk and makes match their specs every
// few seconds. Each has a lock of its own: a long step on one (weights or an
// image downloading, a machine shutting down) holds no other back.
type objects struct {
	locks  sync.Map // "<kind>/<name>" -> *sync.Mutex
	errsMu sync.Mutex
	errs   map[string]string
	// poke asks for a pass right away, after a change.
	poke chan struct{}
}

func newObjects() *objects {
	return &objects{errs: map[string]string{}, poke: make(chan struct{}, 1)}
}

func (o *objects) lock(key string) *sync.Mutex {
	l, _ := o.locks.LoadOrStore(key, &sync.Mutex{})
	return l.(*sync.Mutex)
}

func (o *objects) changed() {
	select {
	case o.poke <- struct{}{}:
	default:
	}
}

func (o *objects) errOf(key string) string {
	o.errsMu.Lock()
	defer o.errsMu.Unlock()
	return o.errs[key]
}

func (o *objects) setErr(key string, err error) {
	o.errsMu.Lock()
	defer o.errsMu.Unlock()
	if err != nil {
		if o.errs[key] != err.Error() {
			klog.Errorf("%s: %v", key, err)
		}
		o.errs[key] = err.Error()
	} else {
		delete(o.errs, key)
	}
}

// async runs step for an object unless a step for it is still running.
func (o *objects) async(key string, step func() error) {
	l := o.lock(key)
	if !l.TryLock() {
		return
	}
	go func() {
		defer l.Unlock()
		err := step()
		if errors.Is(err, vm.ErrWaiting) {
			err = nil // not a failure: the next pass tries again
		}
		o.setErr(key, err)
	}()
}
