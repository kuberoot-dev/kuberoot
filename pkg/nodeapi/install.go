package nodeapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	agecrypt "filippo.io/age"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/bootdisk"
	"github.com/kuberoot-dev/kuberoot/pkg/installer"
	"github.com/kuberoot-dev/kuberoot/pkg/release"
	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
	"github.com/kuberoot-dev/kuberoot/pkg/upgrade"
)

// diskStorage lists the block devices of the node.
type diskStorage struct{}

func (diskStorage) New() runtime.Object     { return &node.Disk{} }
func (diskStorage) NewList() runtime.Object { return &node.DiskList{} }
func (diskStorage) Destroy()                {}
func (diskStorage) NamespaceScoped() bool   { return false }
func (diskStorage) GetSingularName() string { return "disk" }

func (diskStorage) snapshot() map[string]runtime.Object {
	out := map[string]runtime.Object{}
	for _, d := range installer.Disks() {
		obj := &node.Disk{Status: node.DiskStatus{SizeBytes: d.SizeBytes, Model: d.Model, Removable: d.Removable, Role: d.Role}}
		for _, p := range d.Partitions {
			obj.Status.Partitions = append(obj.Status.Partitions, node.DiskPartition{Name: p.Name, Label: p.Label, SizeBytes: p.SizeBytes})
		}
		obj.ObjectMeta = metav1.ObjectMeta{Name: d.Name, UID: types.UID(string(nodeUID()) + "-disk-" + d.Name), ResourceVersion: resourceVersion(obj.Status)}
		out[d.Name] = obj
	}
	return out
}

func (s diskStorage) Get(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	if obj, ok := s.snapshot()[name]; ok {
		return obj, nil
	}
	return nil, apierrors.NewNotFound(node.Resource("disks"), name)
}

func (s diskStorage) List(context.Context, *metainternalversion.ListOptions) (runtime.Object, error) {
	list := &node.DiskList{}
	for _, obj := range s.snapshot() {
		list.Items = append(list.Items, *obj.(*node.Disk))
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list, nil
}

func (s diskStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, func(context.Context) (map[string]runtime.Object, error) { return s.snapshot(), nil }), nil
}

func (diskStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.Disk:
		items = []runtime.Object{o}
	case *node.DiskList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Size", ""), col("Model", ""), col("Removable", ""), col("Role", ""), col("Partitions", "")},
		items, func(o runtime.Object) []any {
			d := o.(*node.Disk)
			var parts []string
			for _, p := range d.Status.Partitions {
				parts = append(parts, p.Label)
			}
			return []any{d.Name, bytesHuman(d.Status.SizeBytes), d.Status.Model, d.Status.Removable, d.Status.Role, strings.Join(parts, ",")}
		}), nil
}

// installationStorage runs installations one at a time and keeps their status in memory.
type installationStorage struct {
	kinit *supervisor.Client
	mu    sync.Mutex
	items map[string]*node.Installation
}

var _ rest.Creater = &installationStorage{}

func (s *installationStorage) New() runtime.Object     { return &node.Installation{} }
func (s *installationStorage) NewList() runtime.Object { return &node.InstallationList{} }
func (s *installationStorage) Destroy()                {}
func (s *installationStorage) NamespaceScoped() bool   { return false }
func (s *installationStorage) GetSingularName() string { return "installation" }

func (s *installationStorage) Create(_ context.Context, obj runtime.Object, validate rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	inst := obj.(*node.Installation).DeepCopy()
	if inst.Name == "" {
		return nil, apierrors.NewBadRequest("metadata.name is required")
	}
	if inst.Spec.Disk == "" {
		return nil, apierrors.NewBadRequest("spec.disk is required")
	}
	if r := inst.Spec.Restore; r != nil {
		if sum, err := hex.DecodeString(r.Sha256); r.URL == "" || err != nil || len(sum) != sha256.Size {
			return nil, apierrors.NewBadRequest("spec.restore needs url and the archive's sha256")
		}
		if r.Identity != "" {
			if _, err := agecrypt.ParseIdentities(strings.NewReader(r.Identity)); err != nil {
				return nil, apierrors.NewBadRequest("spec.restore.identity: " + err.Error())
			}
		}
	}
	artifacts, err := installer.FromMedia()
	if err != nil {
		return nil, apierrors.NewBadRequest(err.Error())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]*node.Installation{}
	}
	if _, ok := s.items[inst.Name]; ok {
		return nil, apierrors.NewAlreadyExists(node.Resource("installations"), inst.Name)
	}
	for _, other := range s.items {
		if other.Status.Phase != installer.PhaseCompleted && other.Status.Phase != installer.PhaseFailed {
			return nil, apierrors.NewConflict(node.Resource("installations"), inst.Name, fmt.Errorf("installation %s is still running", other.Name))
		}
	}
	now := metav1.Now()
	inst.UID = uuid.NewUUID()
	inst.CreationTimestamp = now
	inst.Status = node.InstallationStatus{Phase: installer.PhasePending, StartedAt: &now}
	// The identity opens the archive once; it is kept in no object.
	spec := *inst.Spec.DeepCopy()
	if inst.Spec.Restore != nil {
		inst.Spec.Restore.Identity = ""
	}
	inst.ResourceVersion = resourceVersion(inst.Status)
	s.items[inst.Name] = inst
	go s.run(inst.Name, spec, artifacts)
	return inst.DeepCopy(), nil
}

func (s *installationStorage) run(name string, spec node.InstallationSpec, a bootdisk.Artifacts) {
	update := func(phase, message string, progress int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		st := &s.items[name].Status
		st.Phase, st.Message, st.Progress = phase, message, int32(progress)
		s.items[name].ResourceVersion = resourceVersion(*st)
	}
	var err error
	restore := ""
	if spec.Restore != nil {
		restore, err = fetchStateArchive(spec.Restore, update)
		defer os.Remove(restore)
	}
	if err == nil {
		err = installer.Install(context.Background(), spec.Disk, a, restore, update)
	}
	now := metav1.Now()
	s.mu.Lock()
	st := &s.items[name].Status
	st.CompletedAt = &now
	if err != nil {
		st.Phase, st.Message = installer.PhaseFailed, err.Error()
	} else {
		st.Phase, st.Message, st.Progress = installer.PhaseCompleted, "installed on "+spec.Disk, 100
	}
	s.items[name].ResourceVersion = resourceVersion(*st)
	s.mu.Unlock()
	if err == nil && spec.Reboot {
		time.Sleep(3 * time.Second) // let watchers see the final status
		_ = s.kinit.Reboot(context.Background())
	}
}

func (s *installationStorage) snapshot(context.Context) (map[string]runtime.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]runtime.Object{}
	for name, inst := range s.items {
		out[name] = inst.DeepCopy()
	}
	return out, nil
}

func (s *installationStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	all, _ := s.snapshot(ctx)
	if obj, ok := all[name]; ok {
		return obj, nil
	}
	return nil, apierrors.NewNotFound(node.Resource("installations"), name)
}

func (s *installationStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	all, _ := s.snapshot(ctx)
	list := &node.InstallationList{}
	for _, obj := range all {
		list.Items = append(list.Items, *obj.(*node.Installation))
	}
	return list, nil
}

func (s *installationStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

func (s *installationStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.Installation:
		items = []runtime.Object{o}
	case *node.InstallationList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Disk", ""), col("Phase", ""), col("Progress", ""), col("Message", "")},
		items, func(o runtime.Object) []any {
			i := o.(*node.Installation)
			return []any{i.Name, i.Spec.Disk, i.Status.Phase, fmt.Sprintf("%d%%", i.Status.Progress), i.Status.Message}
		}), nil
}

// fetchStateArchive downloads the state archive to restore and checks it is
// the one asked for: it holds the cluster's certificate authorities.
func fetchStateArchive(r *node.InstallationRestore, update func(phase, message string, progress int)) (string, error) {
	const download, path = "/run/kuberoot/restore.download", "/run/kuberoot/restore.tar.gz"
	defer os.Remove(download)
	update(installer.PhasePending, "downloading the state archive", 1)
	if err := upgrade.Download(context.Background(), r.URL, download, func(done, _ int64) {
		update(installer.PhasePending, fmt.Sprintf("downloading the state archive: %d MiB", done>>20), 2)
	}); err != nil {
		return path, fmt.Errorf("state archive: %w", err)
	}
	sum, err := release.FileSum(download)
	if err != nil {
		return path, err
	}
	if !strings.EqualFold(hex.EncodeToString(sum), r.Sha256) {
		return path, fmt.Errorf("state archive: sha256 %x is not the one given", sum)
	}
	return path, openStateArchive(download, path, r.Identity)
}

// openStateArchive writes the archive at src to dst, decrypting it with the
// age identity when it is encrypted.
func openStateArchive(src, dst, identity string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	head := make([]byte, len(ageHeader))
	n, _ := io.ReadFull(in, head)
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var r io.Reader = in
	if string(head[:n]) == ageHeader {
		if identity == "" {
			return fmt.Errorf("state archive is encrypted: give spec.restore.identity")
		}
		ids, err := agecrypt.ParseIdentities(strings.NewReader(identity))
		if err != nil {
			return err
		}
		if r, err = agecrypt.Decrypt(in, ids...); err != nil {
			return fmt.Errorf("state archive: %w", err)
		}
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return fmt.Errorf("state archive: %w", err)
	}
	return out.Close()
}

// ageHeader starts every age-encrypted file.
const ageHeader = "age-encryption.org/v1"
