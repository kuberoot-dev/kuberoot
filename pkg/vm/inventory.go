package vm

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// NamesChecked is the API inventory path. Unlike the reconciliation helper's
// Glob, it reports unreadable directories and malformed entries. A missing
// collection is explicitly empty on a node that has never held that kind.
func (m *Machines) NamesChecked() ([]string, error) { return checkedNames(m.dir()) }
func (v *Volumes) NamesChecked() ([]string, error)  { return checkedNames(v.dir()) }

func checkedNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("spec entry %s is not a regular file", entry.Name())
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		if name == "" {
			return nil, fmt.Errorf("empty spec name in %s", dir)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
