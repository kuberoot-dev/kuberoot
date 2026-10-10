package nodeapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
	"github.com/kuberoot-dev/kuberoot/pkg/gpu"
	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
)

const osConfigState = "/var/lib/kuberoot/osconfig.json"

// osConfigStorage serves the single OSConfig of this node. Its spec is
// applied to the running system on every update.
type osConfigStorage struct {
	nodeName string
	kinit    *supervisor.Client
	mu       sync.Mutex
	spec     node.OSConfigSpec
}

var (
	_ rest.Storage              = &osConfigStorage{}
	_ rest.Scoper               = &osConfigStorage{}
	_ rest.SingularNameProvider = &osConfigStorage{}
	_ rest.Getter               = &osConfigStorage{}
	_ rest.Lister               = &osConfigStorage{}
	_ rest.Updater              = &osConfigStorage{}
	_ rest.Watcher              = &osConfigStorage{}
)

func newOSConfigStorage(nodeName string, kinit *supervisor.Client) *osConfigStorage {
	s := &osConfigStorage{nodeName: nodeName, kinit: kinit}
	if raw, err := os.ReadFile(osConfigState); err == nil {
		_ = json.Unmarshal(raw, &s.spec)
	}
	// The spec is desired state: the running system follows it from boot on,
	// not only from the next change.
	reapply := s.spec
	reapply.RebootRequestedAt = nil
	if err := applyOSSpec(reapply); err != nil {
		klog.Errorf("applying the stored OS configuration: %v", err)
	}
	return s
}

func (s *osConfigStorage) New() kruntime.Object     { return &node.OSConfig{} }
func (s *osConfigStorage) NewList() kruntime.Object { return &node.OSConfigList{} }
func (s *osConfigStorage) Destroy()                 {}
func (s *osConfigStorage) NamespaceScoped() bool    { return false }
func (s *osConfigStorage) GetSingularName() string  { return "osconfig" }

func (s *osConfigStorage) Get(_ context.Context, name string, _ *metav1.GetOptions) (kruntime.Object, error) {
	if name != s.nodeName {
		return nil, apierrors.NewNotFound(node.Resource("osconfigs"), name)
	}
	return s.current(), nil
}

func (s *osConfigStorage) List(_ context.Context, _ *metainternalversion.ListOptions) (kruntime.Object, error) {
	return &node.OSConfigList{Items: []node.OSConfig{*s.current()}}, nil
}

func (s *osConfigStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, func(context.Context) (map[string]kruntime.Object, error) {
		return map[string]kruntime.Object{s.nodeName: s.current()}, nil
	}), nil
}

func (s *osConfigStorage) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo,
	_ rest.ValidateObjectFunc, _ rest.ValidateObjectUpdateFunc, _ bool, _ *metav1.UpdateOptions) (kruntime.Object, bool, error) {
	if name != s.nodeName {
		return nil, false, apierrors.NewNotFound(node.Resource("osconfigs"), name)
	}
	updated, err := objInfo.UpdatedObject(ctx, s.current())
	if err != nil {
		return nil, false, err
	}
	spec := updated.(*node.OSConfig).Spec
	if err := applyOSSpec(spec); err != nil {
		return nil, false, apierrors.NewBadRequest(err.Error())
	}
	s.mu.Lock()
	s.spec = spec
	raw, _ := json.Marshal(spec)
	s.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(osConfigState), 0o755)
	_ = atomicfile.WriteFile(osConfigState, raw, 0o600)
	obj := s.current()
	if r := spec.RebootRequestedAt; r != nil && r.After(obj.Status.BootTime.Time) {
		go func() {
			time.Sleep(2 * time.Second) // answer the request first
			_ = s.kinit.Reboot(context.Background())
		}()
	}
	return obj, false, nil
}

// applyOSSpec makes the running system match the spec. Every sysctl is
// checked before any is written, so a typo does not leave a half-applied change.
func applyOSSpec(spec node.OSConfigSpec) error {
	for key := range spec.Sysctls {
		if _, err := os.Stat(sysctlPath(key)); err != nil {
			return fmt.Errorf("unknown sysctl %q", key)
		}
	}
	for key, value := range spec.Sysctls {
		if err := os.WriteFile(sysctlPath(key), []byte(value), 0o644); err != nil {
			return fmt.Errorf("sysctl %s=%s: %w", key, value, err)
		}
	}
	if len(spec.Nameservers) > 0 {
		var b strings.Builder
		for _, ns := range spec.Nameservers {
			if net.ParseIP(ns) == nil {
				return fmt.Errorf("nameserver %q is not an IP address", ns)
			}
			fmt.Fprintf(&b, "nameserver %s\n", ns)
		}
		if err := os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func sysctlPath(key string) string {
	return "/proc/sys/" + strings.ReplaceAll(key, ".", "/")
}

func (s *osConfigStorage) current() *node.OSConfig {
	s.mu.Lock()
	spec := *s.spec.DeepCopy()
	s.mu.Unlock()
	status := readOSStatus()
	return &node.OSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              s.nodeName,
			UID:               nodeUID(),
			CreationTimestamp: status.BootTime,
			ResourceVersion:   resourceVersion(spec, status.Addresses, status.Hostname),
		},
		Spec:   spec,
		Status: status,
	}
}

func readOSStatus() node.OSConfigStatus {
	var uts unix.Utsname
	_ = unix.Uname(&uts)
	var info unix.Sysinfo_t
	_ = unix.Sysinfo(&info)
	release := osRelease()
	hostname, _ := os.Hostname()
	st := node.OSConfigStatus{
		Distro:        release["NAME"],
		Version:       release["VERSION_ID"],
		KernelVersion: unix.ByteSliceToString(uts.Release[:]),
		Architecture:  runtime.GOARCH,
		Hostname:      hostname,
		BootTime:      metav1.NewTime(time.Now().Add(-time.Duration(info.Uptime) * time.Second).Truncate(time.Second)),
		MemoryTotal:   int64(info.Totalram) * int64(info.Unit),
		MemoryFree:    int64(info.Freeram) * int64(info.Unit),
	}
	if gpus, err := gpu.Find(); err == nil {
		for _, g := range gpus {
			st.GPUs = append(st.GPUs, node.NodeGPU{Index: int32(g.Minor), Model: g.Model, UUID: g.UUID, BusID: g.BusID})
		}
	}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || strings.HasPrefix(iface.Name, "cni") || strings.HasPrefix(iface.Name, "veth") {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.IsGlobalUnicast() {
				st.Addresses = append(st.Addresses, ipn.IP.String())
			}
		}
	}
	return st
}

func osRelease() map[string]string {
	out := map[string]string{}
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			out[k] = strings.Trim(v, `"`)
		}
	}
	return out
}

func (s *osConfigStorage) ConvertToTable(_ context.Context, obj kruntime.Object, _ kruntime.Object) (*metav1.Table, error) {
	var items []kruntime.Object
	switch o := obj.(type) {
	case *node.OSConfig:
		items = []kruntime.Object{o}
	case *node.OSConfigList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{
		col("Name", "name"), col("Distro", ""), col("Version", ""), col("Kernel", ""), col("Addresses", ""), col("Uptime", ""),
	}, items, func(o kruntime.Object) []any {
		c := o.(*node.OSConfig)
		return []any{c.Name, c.Status.Distro, c.Status.Version, c.Status.KernelVersion,
			strings.Join(c.Status.Addresses, ","), age(c.Status.BootTime.Time)}
	}), nil
}
