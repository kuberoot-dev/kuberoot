package main

import (
	"sync"

	"golang.org/x/sys/unix"
)

// smallMemory is the memory below which a node's services trade CPU for
// memory: an emulated riscv64 node of 1 GiB runs a dozen Go programs, and
// with their heaps left to grow to twice what they hold, the page cache of
// their own code is squeezed out and the node thrashes.
const smallMemory = 1536 << 20

// smallMemoryEnv is what every service gets on such a node: the Go garbage
// collector runs when a heap has grown by a quarter rather than doubled. A
// profile's own GOGC for a service comes after it and wins.
var smallMemoryEnv = []string{"GOGC=25"}

var memoryEnv = sync.OnceValue(func() []string {
	var info unix.Sysinfo_t
	if unix.Sysinfo(&info) != nil {
		return nil
	}
	return envForMemory(uint64(info.Totalram) * uint64(info.Unit))
})

func envForMemory(total uint64) []string {
	if total == 0 || total >= smallMemory {
		return nil
	}
	return smallMemoryEnv
}
