package nodeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
	"github.com/kuberoot-dev/kuberoot/pkg/rt"
)

// latencyTests are the node's latency tests, kept on disk with their
// results, and run one at a time in the order they were made: two at once
// would measure each other.
type latencyTests struct {
	dir    string
	mu     sync.Mutex
	cancel map[string]context.CancelFunc // the running test
	poke   chan struct{}
}

type latencyRecord struct {
	// Created to the nanosecond: tests made in the same second keep their order.
	Created time.Time              `json:"created"`
	Spec    node.LatencyTestSpec   `json:"spec"`
	Status  node.LatencyTestStatus `json:"status"`
}

func newLatencyTests(dir string) *latencyTests {
	return &latencyTests{dir: dir, cancel: map[string]context.CancelFunc{}, poke: make(chan struct{}, 1)}
}

func (l *latencyTests) file(name string) string { return filepath.Join(l.dir, name+".json") }

func (l *latencyTests) load(name string) (latencyRecord, error) {
	var r latencyRecord
	raw, err := os.ReadFile(l.file(name))
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(raw, &r)
}

func (l *latencyTests) save(name string, r latencyRecord) error {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(l.file(name), raw, 0o600)
}

func (l *latencyTests) names() []string {
	files, _ := filepath.Glob(filepath.Join(l.dir, "*.json"))
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimSuffix(filepath.Base(f), ".json"))
	}
	return out
}

// next is the oldest test still to run.
func (l *latencyTests) next() (string, latencyRecord, bool) {
	var pending []string
	recs := map[string]latencyRecord{}
	for _, n := range l.names() {
		if r, err := l.load(n); err == nil && r.Status.Phase == "Pending" {
			pending, recs[n] = append(pending, n), r
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		a, b := recs[pending[i]].Created, recs[pending[j]].Created
		if !a.Equal(b) {
			return a.Before(b)
		}
		return pending[i] < pending[j]
	})
	if len(pending) == 0 {
		return "", latencyRecord{}, false
	}
	return pending[0], recs[pending[0]], true
}

// run runs the tests as they come, until ctx ends. A test the node was
// running when it went down failed.
func (l *latencyTests) run(ctx context.Context) {
	for _, n := range l.names() {
		if r, err := l.load(n); err == nil && r.Status.Phase == "Running" {
			r.Status.Phase, r.Status.Message = "Failed", "the node restarted during the test"
			_ = l.save(n, r)
		}
	}
	for ctx.Err() == nil {
		if name, r, ok := l.next(); ok {
			l.runOne(ctx, name, r)
			continue
		}
		select {
		case <-ctx.Done():
		case <-l.poke:
		case <-time.After(30 * time.Second):
		}
	}
}

func (l *latencyTests) runOne(ctx context.Context, name string, r latencyRecord) {
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	l.mu.Lock()
	l.cancel[name] = cancel
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.cancel, name)
		l.mu.Unlock()
	}()
	now := metav1.Now()
	r.Status = node.LatencyTestStatus{Phase: "Running", StartedAt: &now, Kernel: kernelDescription()}
	if err := l.save(name, r); err != nil {
		klog.Errorf("latency test %s: %v", name, err)
		return
	}
	work, err := os.MkdirTemp("", "latency-")
	if err != nil {
		klog.Errorf("latency test %s: %v", name, err)
		return
	}
	defer os.RemoveAll(work)
	res, err := rt.Run(tctx, r.Spec, work)
	if _, gone := l.load(name); gone != nil {
		return // deleted while it ran
	}
	done := metav1.Now()
	res.StartedAt, res.FinishedAt, res.Kernel = r.Status.StartedAt, &done, r.Status.Kernel
	res.Phase = "Succeeded"
	if err != nil {
		res.Phase, res.Message = "Failed", err.Error()
	}
	r.Status = res
	if err := l.save(name, r); err != nil {
		klog.Errorf("latency test %s: %v", name, err)
	}
}

// kernelDescription is the running kernel's release, and how preemptible
// it is.
func kernelDescription() string {
	release, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	version, _ := os.ReadFile("/proc/sys/kernel/version")
	desc := strings.TrimSpace(string(release))
	if strings.Contains(string(version), "PREEMPT_RT") {
		desc += " PREEMPT_RT"
	}
	return desc
}

// latencyStorage serves the node's latency tests.
type latencyStorage struct{ l *latencyTests }

var (
	_ rest.Creater         = latencyStorage{}
	_ rest.GracefulDeleter = latencyStorage{}
)

func (latencyStorage) New() runtime.Object     { return &node.LatencyTest{} }
func (latencyStorage) NewList() runtime.Object { return &node.LatencyTestList{} }
func (latencyStorage) Destroy()                {}
func (latencyStorage) NamespaceScoped() bool   { return false }
func (latencyStorage) GetSingularName() string { return "latencytest" }

func (s latencyStorage) object(name string) (*node.LatencyTest, error) {
	r, err := s.l.load(name)
	if err != nil {
		return nil, apierrors.NewNotFound(node.Resource("latencytests"), name)
	}
	t := &node.LatencyTest{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(r.Created),
		UID: types.UID(string(nodeUID()) + "-latencytest-" + name)}, Spec: r.Spec, Status: r.Status}
	t.ResourceVersion = resourceVersion(t.Spec, t.Status)
	return t, nil
}

func (s latencyStorage) Get(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	return s.object(name)
}

func (s latencyStorage) List(context.Context, *metainternalversion.ListOptions) (runtime.Object, error) {
	list := &node.LatencyTestList{}
	names := s.l.names()
	sort.Strings(names)
	for _, n := range names {
		if t, err := s.object(n); err == nil {
			list.Items = append(list.Items, *t)
		}
	}
	return list, nil
}

func (s latencyStorage) snapshot(context.Context) (map[string]runtime.Object, error) {
	out := map[string]runtime.Object{}
	for _, n := range s.l.names() {
		if t, err := s.object(n); err == nil {
			out[n] = t
		}
	}
	return out, nil
}

func (s latencyStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

func (s latencyStorage) Create(_ context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	t := obj.(*node.LatencyTest)
	if t.Name == "" {
		return nil, apierrors.NewBadRequest("a latency test needs a name")
	}
	if err := rt.Validate(t.Spec); err != nil {
		return nil, apierrors.NewBadRequest(err.Error())
	}
	if _, err := s.l.load(t.Name); err == nil {
		return nil, apierrors.NewAlreadyExists(node.Resource("latencytests"), t.Name)
	}
	r := latencyRecord{Created: time.Now(), Spec: rt.Defaults(t.Spec), Status: node.LatencyTestStatus{Phase: "Pending"}}
	if err := s.l.save(t.Name, r); err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	select {
	case s.l.poke <- struct{}{}:
	default:
	}
	return s.object(t.Name)
}

// Delete forgets a test, stopping it if it runs.
func (s latencyStorage) Delete(_ context.Context, name string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	obj, err := s.object(name)
	if err != nil {
		return nil, false, err
	}
	if err := os.Remove(s.l.file(name)); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	s.l.mu.Lock()
	if cancel := s.l.cancel[name]; cancel != nil {
		cancel()
	}
	s.l.mu.Unlock()
	return obj, true, nil
}

func (latencyStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.LatencyTest:
		items = []runtime.Object{o}
	case *node.LatencyTestList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	us := func(v int64) string { return fmt.Sprintf("%dus", v) }
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Phase", ""), col("CPUs", ""), col("Duration", ""),
		col("Max", ""), col("P99", ""), col("P99.99", ""), col("Kernel", ""), col("Message", "")},
		items, func(o runtime.Object) []any {
			t := o.(*node.LatencyTest)
			var cpus []string
			for _, c := range t.Status.CPUs {
				cpus = append(cpus, fmt.Sprint(c.CPU))
			}
			return []any{t.Name, t.Status.Phase, strings.Join(cpus, ","), fmt.Sprintf("%ds", t.Spec.DurationSeconds),
				us(t.Status.MaxMicroseconds), us(t.Status.P99Microseconds), us(t.Status.P9999Microseconds), t.Status.Kernel, t.Status.Message}
		}), nil
}
