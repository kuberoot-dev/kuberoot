package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacesAndLeavesNoTemporaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("content = %q, %v; want new", got, err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the file", len(entries))
	}
}

func TestUnchangedFileIsNotRewritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := WriteFile(path, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := WriteFile(path, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Error("identical content was written again")
	}
	for _, c := range []struct {
		data string
		perm os.FileMode
	}{{"other", 0o600}, {"other", 0o644}} {
		if err := WriteFile(path, []byte(c.data), c.perm); err != nil {
			t.Fatal(err)
		}
		fi, _ := os.Stat(path)
		got, _ := os.ReadFile(path)
		if string(got) != c.data || fi.Mode().Perm() != c.perm {
			t.Errorf("after writing %q %o: %q %o", c.data, c.perm, got, fi.Mode().Perm())
		}
	}
}
