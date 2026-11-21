package main

import (
	"encoding/binary"
	"testing"
)

func TestZRAMSize(t *testing.T) {
	for _, c := range []struct{ ram, want uint64 }{{0, 0}, {512 << 20, 256 << 20}, {768 << 20, 384 << 20}, {1536 << 20, 0}, {2 << 30, 0}} {
		if got := zramSize(c.ram); got != c.want {
			t.Errorf("RAM %d: %d, want %d", c.ram, got, c.want)
		}
	}
}

func TestSwapHeader(t *testing.T) {
	for _, page := range []int{4096, 65536} {
		b, err := swapHeader(256<<20, page)
		if err != nil {
			t.Fatal(err)
		}
		if string(b[page-10:]) != "SWAPSPACE2" || binary.LittleEndian.Uint32(b[1024:]) != 1 || binary.LittleEndian.Uint32(b[1028:]) != uint32((256<<20)/page-1) {
			t.Fatal("invalid Linux swap header")
		}
	}
	if _, err := swapHeader(4096, 4096); err == nil {
		t.Fatal("accepted one-page swap")
	}
}
