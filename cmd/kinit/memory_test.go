package main

import "testing"

func TestEnvForMemory(t *testing.T) {
	for _, c := range []struct {
		total uint64
		want  int
	}{{1 << 30, 1}, {2 << 30, 0}, {0, 0}} {
		if got := envForMemory(c.total); len(got) != c.want {
			t.Errorf("%d bytes: %v", c.total, got)
		}
	}
}
