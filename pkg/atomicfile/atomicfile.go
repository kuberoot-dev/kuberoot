// Package atomicfile writes files so that a power cut leaves either the old
// content or the new one, never a truncated file.
package atomicfile

import (
	"bytes"
	"os"
	"path/filepath"
)

// WriteFile writes data to a temporary file next to path, syncs it, renames it
// over path and syncs the directory. A file that already holds data with the
// same permissions is left alone: rewriting it would only wear the flash,
// and on a full filesystem the temporary copy would not fit.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if same(path, data, perm) {
		return nil
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// same reports whether path is a regular file holding data with permissions perm.
func same(path string, data []byte, perm os.FileMode) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != perm.Perm() || fi.Size() != int64(len(data)) {
		return false
	}
	old, err := os.ReadFile(path)
	return err == nil && bytes.Equal(old, data)
}
