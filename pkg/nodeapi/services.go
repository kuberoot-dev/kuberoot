package nodeapi

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
)

// serviceStorage exposes the services kinit supervises. Setting
// spec.restartedAt restarts one, the same way kubectl rollout restart does.
type serviceStorage struct {
	kinit *supervisor.Client
	mu    sync.Mutex
	specs map[string]node.NodeServiceSpec
}

var (
	_ rest.Storage              = &serviceStorage{}
	_ rest.Scoper               = &serviceStorage{}
	_ rest.SingularNameProvider = &serviceStorage{}
	_ rest.Getter               = &serviceStorage{}
	_ rest.Lister               = &serviceStorage{}
	_ rest.Updater              = &serviceStorage{}
	_ rest.Watcher              = &serviceStorage{}
)

func newServiceStorage(kinit *supervisor.Client) *serviceStorage {
	return &serviceStorage{kinit: kinit, specs: map[string]node.NodeServiceSpec{}}
}

func (s *serviceStorage) New() runtime.Object     { return &node.NodeService{} }
func (s *serviceStorage) NewList() runtime.Object { return &node.NodeServiceList{} }
func (s *serviceStorage) Destroy()                {}
func (s *serviceStorage) NamespaceScoped() bool   { return false }
func (s *serviceStorage) GetSingularName() string { return "nodeservice" }

func (s *serviceStorage) snapshot(ctx context.Context) (map[string]runtime.Object, error) {
	statuses, err := s.kinit.Services(ctx)
	if err != nil {
		return nil, apierrors.NewServiceUnavailable("kinit: " + err.Error())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]runtime.Object, len(statuses))
	for _, st := range statuses {
		out[st.Name] = toNodeService(st, s.specs[st.Name])
	}
	return out, nil
}

func (s *serviceStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	all, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	obj, ok := all[name]
	if !ok {
		return nil, apierrors.NewNotFound(node.Resource("nodeservices"), name)
	}
	return obj, nil
}

func (s *serviceStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	all, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	list := &node.NodeServiceList{}
	for _, obj := range all {
		list.Items = append(list.Items, *obj.(*node.NodeService))
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list, nil
}

func (s *serviceStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

func (s *serviceStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo,
	_ rest.ValidateObjectFunc, _ rest.ValidateObjectUpdateFunc, _ bool, _ *metav1.UpdateOptions) (runtime.Object, bool, error) {
	current, err := s.Get(ctx, name, nil)
	if err != nil {
		return nil, false, err
	}
	updated, err := objInfo.UpdatedObject(ctx, current)
	if err != nil {
		return nil, false, err
	}
	spec := updated.(*node.NodeService).Spec
	previous := current.(*node.NodeService).Spec.RestartedAt
	if spec.RestartedAt != nil && (previous == nil || !spec.RestartedAt.Equal(previous)) {
		if err := s.kinit.Restart(ctx, name); err != nil {
			return nil, false, apierrors.NewInternalError(err)
		}
	}
	s.mu.Lock()
	s.specs[name] = spec
	s.mu.Unlock()
	obj, err := s.Get(ctx, name, nil)
	return obj, false, err
}

func toNodeService(st supervisor.ServiceStatus, spec node.NodeServiceSpec) *node.NodeService {
	status := node.NodeServiceStatus{
		State:           st.State,
		PID:             int32(st.PID),
		Restarts:        int32(st.Restarts),
		StartedAt:       metav1.NewTime(st.StartedAt.Truncate(1e9)),
		LastExit:        st.LastExit,
		MemoryBytes:     readInt(filepath.Join(st.Cgroup, "memory.current")),
		CPUUsageSeconds: cgroupCPUSeconds(st.Cgroup),
	}
	return &node.NodeService{
		ObjectMeta: metav1.ObjectMeta{
			Name:              st.Name,
			UID:               types.UID(string(nodeUID()) + "-" + st.Name),
			CreationTimestamp: status.StartedAt,
			// Usage counters are left out: they would change the version on every poll.
			ResourceVersion: resourceVersion(spec, status.State, status.PID, status.Restarts),
		},
		Spec:   spec,
		Status: status,
	}
}

func readInt(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return n
}

func cgroupCPUSeconds(cgroup string) int64 {
	f, err := os.Open(filepath.Join(cgroup, "cpu.stat"))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "usage_usec "); ok {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n / 1e6
		}
	}
	return 0
}

func (s *serviceStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.NodeService:
		items = []runtime.Object{o}
	case *node.NodeServiceList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{
		col("Name", "name"), col("State", ""), col("PID", ""), col("Restarts", ""), col("Memory", ""), col("CPU", ""), col("Age", ""),
	}, items, func(o runtime.Object) []any {
		svc := o.(*node.NodeService)
		return []any{svc.Name, svc.Status.State, svc.Status.PID, svc.Status.Restarts,
			bytesHuman(svc.Status.MemoryBytes), strconv.FormatInt(svc.Status.CPUUsageSeconds, 10) + "s", age(svc.Status.StartedAt.Time)}
	}), nil
}

// logStorage serves nodeservices/<name>/log as a plain text stream of the service log.
type logStorage struct{ kinit *supervisor.Client }

func (l *logStorage) New() runtime.Object { return &node.NodeService{} }
func (l *logStorage) Destroy()            {}

func (l *logStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	statuses, err := l.kinit.Services(ctx)
	if err != nil {
		return nil, apierrors.NewServiceUnavailable(err.Error())
	}
	for _, st := range statuses {
		if st.Name == name {
			return &logStream{path: st.LogFile}, nil
		}
	}
	return nil, apierrors.NewNotFound(node.Resource("nodeservices"), name)
}

// logStream satisfies rest.ResourceStreamer, which the API server writes out verbatim.
type logStream struct{ path string }

func (s *logStream) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }
func (s *logStream) DeepCopyObject() runtime.Object   { return &logStream{path: s.path} }

func (s *logStream) InputStream(context.Context, string, string) (io.ReadCloser, bool, string, error) {
	f, err := os.Open(s.path)
	return f, false, "text/plain", err
}

func nodeUID() types.UID {
	raw, _ := os.ReadFile("/etc/machine-id")
	return types.UID(strings.TrimSpace(string(raw)))
}
