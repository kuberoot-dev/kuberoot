package nodeapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agecrypt "filippo.io/age"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/installer"
	"github.com/kuberoot-dev/kuberoot/pkg/s3put"
)

// stateBackupConfig is the Secret that schedules state backups and says where
// to upload them. Without it the node keeps hourly backups locally.
const stateBackupConfig = "kuberoot-state-backup"

type backupConfig struct {
	interval time.Duration
	keep     int
	s3       *s3Target
	// recipients the archives are encrypted to; none: archives in the clear.
	recipients []agecrypt.Recipient
}

type s3Target struct {
	s3put.Target
	prefix string
}

func (t *s3Target) location(key string) string { return "s3://" + t.Bucket + "/" + key }

// parseBackupConfig reads the Secret's keys: interval (a duration, at least
// five minutes; one hour by default), keep (local archives, 12 by default),
// encryptionRecipient (age public keys, one per line, that archives are
// encrypted to) and, to upload, s3Endpoint, s3Bucket, s3AccessKey and
// s3SecretKey, with optional s3Region and s3Prefix. Archives hold the
// cluster's certificate authorities and every Secret, so they leave the
// node only encrypted unless allowUnencrypted is "true".
func parseBackupConfig(data map[string][]byte, nodeName string) (backupConfig, error) {
	c := backupConfig{interval: time.Hour, keep: 12}
	get := func(k string) string { return strings.TrimSpace(string(data[k])) }
	if v := get("interval"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 5*time.Minute {
			return c, fmt.Errorf("interval %q: a duration of at least 5m", v)
		}
		c.interval = d
	}
	if v := get("keep"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return c, fmt.Errorf("keep %q: a number of archives, at least 1", v)
		}
		c.keep = n
	}
	if v := get("encryptionRecipient"); v != "" {
		r, err := agecrypt.ParseRecipients(strings.NewReader(v))
		if err != nil {
			return c, fmt.Errorf("encryptionRecipient: %w", err)
		}
		c.recipients = r
	}
	if get("s3Endpoint") == "" {
		return c, nil
	}
	if len(c.recipients) == 0 && get("allowUnencrypted") != "true" {
		return c, fmt.Errorf("uploads need encryptionRecipient: archives hold the cluster's keys (or set allowUnencrypted: \"true\")")
	}
	u, err := url.Parse(get("s3Endpoint"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return c, fmt.Errorf("s3Endpoint %q: an http(s) URL", get("s3Endpoint"))
	}
	t := &s3Target{Target: s3put.Target{Endpoint: u, Bucket: get("s3Bucket"), Region: get("s3Region"),
		AccessKey: get("s3AccessKey"), SecretKey: get("s3SecretKey")}, prefix: get("s3Prefix")}
	if t.Bucket == "" || t.AccessKey == "" || t.SecretKey == "" {
		return c, fmt.Errorf("s3Bucket, s3AccessKey and s3SecretKey are required with s3Endpoint")
	}
	if t.Region == "" {
		t.Region = "us-east-1"
	}
	if t.prefix == "" {
		t.prefix = "kuberoot/" + nodeName + "/"
	}
	c.s3 = t
	return c, nil
}

// stateBackupStorage takes, lists and uploads the control plane's state
// archives. Each archive has a JSON file beside it with its status.
type stateBackupStorage struct {
	nodeName string
	cluster  kubernetes.Interface
	dir      string
	write    func(w io.Writer, tmpDir string) error

	mu sync.Mutex // one backup or upload at a time
}

func newStateBackups(nodeName string, cluster kubernetes.Interface) *stateBackupStorage {
	return &stateBackupStorage{nodeName: nodeName, cluster: cluster, dir: installer.BackupsDir, write: installer.WriteStateArchive}
}

var (
	_ rest.Creater         = &stateBackupStorage{}
	_ rest.GracefulDeleter = &stateBackupStorage{}
)

func (s *stateBackupStorage) New() runtime.Object     { return &node.StateBackup{} }
func (s *stateBackupStorage) NewList() runtime.Object { return &node.StateBackupList{} }
func (s *stateBackupStorage) Destroy()                {}
func (s *stateBackupStorage) NamespaceScoped() bool   { return false }
func (s *stateBackupStorage) GetSingularName() string { return "statebackup" }

// archive is where a backup's archive is: name.tar.gz, with .age when encrypted.
func (s *stateBackupStorage) archive(name string, encrypted bool) string {
	return filepath.Join(s.dir, archiveFile(name, encrypted))
}

func archiveFile(name string, encrypted bool) string {
	if encrypted {
		return name + ".tar.gz.age"
	}
	return name + ".tar.gz"
}
func (s *stateBackupStorage) meta(name string) string { return filepath.Join(s.dir, name+".json") }

func (s *stateBackupStorage) load(name string) (*node.StateBackup, error) {
	raw, err := os.ReadFile(s.meta(name))
	if err != nil {
		return nil, err
	}
	b := &node.StateBackup{}
	if err := json.Unmarshal(raw, &b.Status); err != nil {
		return nil, err
	}
	b.Name, b.UID = name, types.UID(string(nodeUID())+"-statebackup-"+name)
	b.CreationTimestamp = b.Status.CreatedAt
	b.ResourceVersion = resourceVersion(b.Status)
	return b, nil
}

func (s *stateBackupStorage) save(name string, st node.StateBackupStatus) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := s.meta(name) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.meta(name))
}

// names lists the backups, oldest first: by when they were taken, whatever
// they are called.
func (s *stateBackupStorage) names() []string {
	files, _ := filepath.Glob(filepath.Join(s.dir, "*.json"))
	taken := map[string]time.Time{}
	var out []string
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if b, err := s.load(name); err == nil {
			taken[name] = b.Status.CreatedAt.Time
		}
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		if !taken[out[i]].Equal(taken[out[j]]) {
			return taken[out[i]].Before(taken[out[j]])
		}
		return out[i] < out[j]
	})
	return out
}

func (s *stateBackupStorage) snapshot(context.Context) (map[string]runtime.Object, error) {
	out := map[string]runtime.Object{}
	for _, name := range s.names() {
		if b, err := s.load(name); err == nil {
			out[name] = b
		}
	}
	return out, nil
}

func (s *stateBackupStorage) Get(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	b, err := s.load(name)
	if err != nil {
		return nil, apierrors.NewNotFound(node.Resource("statebackups"), name)
	}
	return b, nil
}

func (s *stateBackupStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	list := &node.StateBackupList{}
	for _, name := range s.names() {
		if b, err := s.load(name); err == nil {
			list.Items = append(list.Items, *b)
		}
	}
	return list, nil
}

func (s *stateBackupStorage) Watch(ctx context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return pollWatch(ctx, s.snapshot), nil
}

// Create takes a backup now, named after the time unless a name is given.
func (s *stateBackupStorage) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	name := obj.(*node.StateBackup).Name
	if _, err := os.Stat(s.meta(name)); name != "" && err == nil {
		return nil, apierrors.NewAlreadyExists(node.Resource("statebackups"), name)
	}
	cfg, err := s.config(ctx)
	if err != nil {
		return nil, apierrors.NewBadRequest(err.Error())
	}
	name, err = s.take(name, cfg.recipients)
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	s.upload(ctx, cfg)
	return s.Get(ctx, name, nil)
}

func (s *stateBackupStorage) Delete(ctx context.Context, name string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	b, err := s.Get(ctx, name, nil)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remove(name)
	if _, err := os.Stat(s.meta(name)); err == nil {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("backup %s could not be removed", name))
	}
	return b, true, nil
}

// remove deletes a backup's archive and status.
func (s *stateBackupStorage) remove(name string) {
	_ = os.Remove(s.archive(name, false))
	_ = os.Remove(s.archive(name, true))
	_ = os.Remove(s.meta(name))
}

// take writes a new archive, encrypted to recipients when there are any, and
// its status.
func (s *stateBackupStorage) take(name string, recipients []agecrypt.Recipient) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if name == "" {
		name = "state-" + now.Format("20060102-150405")
	}
	tmpDir := filepath.Join(s.dir, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", err
	}
	encrypted := len(recipients) > 0
	part := filepath.Join(tmpDir, archiveFile(name, encrypted))
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	var out io.Writer = io.MultiWriter(f, h)
	var sealed io.WriteCloser
	if encrypted {
		if sealed, err = agecrypt.Encrypt(out, recipients...); err != nil {
			f.Close()
			_ = os.Remove(part)
			return "", err
		}
		out = sealed
	}
	err = s.write(out, tmpDir)
	if err == nil && sealed != nil {
		err = sealed.Close()
	}
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		_ = os.Remove(part)
		return "", err
	}
	info, err := os.Stat(part)
	if err != nil {
		return "", err
	}
	if err := os.Rename(part, s.archive(name, encrypted)); err != nil {
		return "", err
	}
	st := node.StateBackupStatus{CreatedAt: metav1.NewTime(now), SizeBytes: info.Size(), Sha256: hex.EncodeToString(h.Sum(nil)), Encrypted: encrypted}
	return name, s.save(name, st)
}

// upload sends every archive not yet in object storage there, recording the
// location or why it failed; without a target it does nothing.
func (s *stateBackupStorage) upload(ctx context.Context, cfg backupConfig) {
	if cfg.s3 == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := cfg.s3
	for _, name := range s.names() {
		b, err := s.load(name)
		if err != nil || b.Status.Location != "" {
			continue
		}
		if !b.Status.Encrypted && len(cfg.recipients) > 0 {
			continue // taken before encryption was set up: it stays on the node
		}
		key := t.prefix + archiveFile(name, b.Status.Encrypted)
		err = t.Put(ctx, uploadClient, key, s.archive(name, b.Status.Encrypted), b.Status.Sha256)
		st := b.Status
		if err != nil {
			st.Message = "upload: " + err.Error()
		} else {
			st.Location, st.Message = t.location(key), ""
		}
		_ = s.save(name, st)
	}
}

// uploadClient gives up on a stalled object store instead of holding the
// backups back forever.
var uploadClient = &http.Client{Timeout: 30 * time.Minute, Transport: &http.Transport{ResponseHeaderTimeout: 2 * time.Minute}}

// prune keeps the newest keep archives.
func (s *stateBackupStorage) prune(keep int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := s.names()
	for len(names) > keep {
		s.remove(names[0])
		names = names[1:]
	}
}

func (s *stateBackupStorage) config(ctx context.Context) (backupConfig, error) {
	secret, err := s.cluster.CoreV1().Secrets("kube-system").Get(ctx, stateBackupConfig, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return parseBackupConfig(nil, s.nodeName)
	}
	if err != nil {
		return backupConfig{}, fmt.Errorf("read kube-system/%s: %w", stateBackupConfig, err)
	}
	return parseBackupConfig(secret.Data, s.nodeName)
}

// run takes a backup whenever the newest is older than the interval, uploads
// what is not uploaded yet and prunes old archives.
func (s *stateBackupStorage) run(ctx context.Context) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		klog.Errorf("state backups: %v", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
		cfg, err := s.config(ctx)
		if err != nil {
			klog.Errorf("state backups: %v", err)
			continue
		}
		if s.due(cfg.interval) {
			if name, err := s.take("", cfg.recipients); err != nil {
				klog.Errorf("state backup: %v", err)
			} else {
				klog.Infof("state backup %s taken", name)
			}
		}
		s.upload(ctx, cfg)
		s.prune(cfg.keep)
	}
}

func (s *stateBackupStorage) due(interval time.Duration) bool {
	names := s.names()
	if len(names) == 0 {
		return true
	}
	b, err := s.load(names[len(names)-1])
	return err != nil || time.Since(b.Status.CreatedAt.Time) >= interval
}

func (s *stateBackupStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.StateBackup:
		items = []runtime.Object{o}
	case *node.StateBackupList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Created", ""), col("Size", ""), col("SHA256", ""), col("Location", "")},
		items, func(o runtime.Object) []any {
			b := o.(*node.StateBackup)
			where := b.Status.Location
			if where == "" {
				where = "local"
				if b.Status.Message != "" {
					where += " (" + b.Status.Message + ")"
				}
			}
			return []any{b.Name, b.Status.CreatedAt.UTC().Format(time.RFC3339), bytesHuman(b.Status.SizeBytes), b.Status.Sha256, where}
		}), nil
}
