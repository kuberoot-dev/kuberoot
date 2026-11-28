package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("read-only file system") }

// A log file that cannot be written must not stop the copy to the console.
func TestFailedLogWriteKeepsTheOutputGoing(t *testing.T) {
	var console bytes.Buffer
	w := io.MultiWriter(keepGoing{failing{}}, &prefixWriter{prefix: "[svc] ", w: &console})
	for _, line := range []string{"one\n", "two\n"} {
		if n, err := w.Write([]byte(line)); err != nil || n != len(line) {
			t.Fatalf("write %q: %d, %v", line, n, err)
		}
	}
	if console.String() != "[svc] one\n[svc] two\n" {
		t.Errorf("console got %q", console.String())
	}
}
