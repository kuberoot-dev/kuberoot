package installer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/etcdstore"
)

// etcdRestore is where a snapshot of etcd waits, below /var, for kinit to
// start the store from it: carried by an installation, or restored.
const etcdRestore = "lib/kuberoot/etcd-restore.db"

// etcdRunning and etcdSnapshot: variables for tests.
var (
	etcdRunning  = etcdstore.Running
	etcdSnapshot = etcdstore.Snapshot
)

// BackupsDir keeps the control plane's state archives. It is not carried by
// installations: the archives are backups of this state, not part of it.
const BackupsDir = "/var/lib/kuberoot/backups"

// WriteStateArchive writes what an installation carries, the node's identity
// and certificates, its membership, the kubelet's credentials and a
// consistent snapshot of the cluster store, as a gzip-compressed tar with
// paths relative to /var. Restoring it on a new disk brings the node back as
// it was. tmpDir holds the store snapshot while the archive is written.
func WriteStateArchive(w io.Writer, tmpDir string) error { return writeStateArchive(w, "/var", tmpDir) }

func writeStateArchive(w io.Writer, root, tmpDir string) error {
	store, storeName := filepath.Join(tmpDir, "state.db"), kineStore
	_ = os.Remove(store)
	if etcdRunning() {
		// A member of etcd: a snapshot the restored node starts its store from.
		store, storeName = filepath.Join(tmpDir, "etcd.db"), etcdRestore
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := etcdSnapshot(ctx, store); err != nil {
			return fmt.Errorf("snapshot the cluster store: %w", err)
		}
	} else if err := snapshotStore(filepath.Join(root, kineStore), store); err != nil {
		return fmt.Errorf("snapshot the cluster store: %w", err)
	}
	defer os.Remove(store)
	if _, err := os.Stat(store); err != nil {
		return fmt.Errorf("no cluster store on this node: %w", err)
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, rel := range carriedState {
		if err := addTree(tw, root, rel); err != nil {
			return err
		}
	}
	if err := addFile(tw, store, storeName); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func addTree(tw *tar.Writer, root, rel string) error {
	src := filepath.Join(root, rel)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(p, root+"/")
		if info.IsDir() && skipped(name) {
			return filepath.SkipDir
		}
		var link string
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case !info.IsDir() && !info.Mode().IsRegular():
			return nil // sockets, fifos: runtime state
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = name
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyInto(tw, p)
	})
}

func addFile(tw *tar.Writer, src, name string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	return copyInto(tw, src)
}

func copyInto(w io.Writer, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func skipped(rel string) bool {
	for _, s := range notCarried {
		if rel == s || strings.HasPrefix(rel, s+"/") {
			return true
		}
	}
	return false
}

// extractState writes a state archive onto a new state partition mounted at
// mnt. Only the paths an archive is made of are accepted, and it must hold a
// cluster store: anything else is not a backup of a control plane.
func extractState(archive, mnt string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("state archive: %w", err)
	}
	tr := tar.NewReader(gz)
	hasStore := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("state archive: %w", err)
		}
		name := path.Clean(hdr.Name)
		if !inState(name) {
			return fmt.Errorf("state archive: unexpected path %q", hdr.Name)
		}
		target := filepath.Join(mnt, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, hdr.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			hasStore = hasStore || name == kineStore || name == etcdRestore
		default:
			return fmt.Errorf("state archive: unexpected entry type of %q", hdr.Name)
		}
	}
	if !hasStore {
		return fmt.Errorf("state archive has no cluster store: not a backup of a control plane")
	}
	return nil
}

// inState reports whether a cleaned archive path belongs to the state an
// archive is made of.
func inState(name string) bool {
	if name == kineStore {
		return true
	}
	if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	for _, rel := range carriedState {
		if name == rel || strings.HasPrefix(name, rel+"/") {
			return !skipped(name)
		}
	}
	return false
}
