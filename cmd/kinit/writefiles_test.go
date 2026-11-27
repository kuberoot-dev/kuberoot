package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFilesLeavesCurrentFilesAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pki", "encryption.yaml")
	if err := writeFiles(map[string]string{path: "key: a\n"}, filepath.Join(dir, "kine")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := writeFiles(map[string]string{path: "key: a\n"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Error("a generated file with unchanged content was written again")
	}
	if err := writeFiles(map[string]string{path: "key: b\n"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "key: b\n" {
		t.Errorf("changed content not written: %q", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, "kine")); err != nil || !fi.IsDir() {
		t.Errorf("directory not created: %v", err)
	}
}
