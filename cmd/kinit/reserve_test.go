package main

import "testing"

func TestSpaceReserveSize(t *testing.T) {
	for _, c := range []struct{ total, want int64 }{
		{454 << 20, 22 << 20},
		{16 << 30, 64 << 20},
		{10 << 20, 0},
	} {
		if got := spaceReserveSize(c.total); got != c.want {
			t.Errorf("spaceReserveSize(%d MiB) = %d MiB, want %d MiB", c.total>>20, got>>20, c.want>>20)
		}
	}
}
