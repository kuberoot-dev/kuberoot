package main

import (
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// stateMountOptions are the state partition's mount options.
const stateMountOptions = "data=ordered,nodelalloc,errors=remount-ro"

// spaceReserveFile holds space on the state partition for the node itself.
// When the partition fills up, the cluster store cannot even start and the
// node cannot be reached to make room; the reserve, given up at the next
// boot, lets it start, and the API then serves what makes room again.
const spaceReserveFile = "/var/lib/kuberoot/space-reserve"

const ext4Magic = 0xef53

// spaceReserveSize is 5 % of the partition, at most 64 MiB, in whole MiB.
func spaceReserveSize(total int64) int64 {
	return min(total/20, 64<<20) &^ (1<<20 - 1)
}

// keepSpaceReserve frees the reserve when less than its size is free and
// lays it out again, at a later boot, once four times its size is free.
// It reports whether the reserve was freed.
func keepSpaceReserve(path, released string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(filepath.Dir(filepath.Dir(path)), &st); err != nil || st.Type != ext4Magic {
		return false
	}
	total, free := int64(st.Blocks)*st.Bsize, int64(st.Bfree)*st.Bsize
	size := spaceReserveSize(total)
	if size == 0 {
		return false
	}
	_, err := os.Stat(path)
	switch held := err == nil; {
	case held && free < size:
		if err := os.Remove(path); err != nil {
			log.Printf("state partition full (%d bytes free): freeing the space reserve: %v", free, err)
			return false
		}
		log.Printf("state partition full (%d bytes free): freed the %d MiB space reserve so the node's services can start; make room through the API", free, size>>20)
		if err := os.MkdirAll(filepath.Dir(released), 0o755); err == nil {
			_ = os.WriteFile(released, nil, 0o644)
		}
		return true
	case !held && free >= 4*size:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return false
		}
		// Allocated, not written: the blocks are taken without wearing the flash.
		err = unix.Fallocate(int(f.Fd()), 0, 0, size)
		f.Close()
		if err != nil {
			_ = os.Remove(path)
			log.Printf("space reserve: %v", err)
			return false
		}
	}
	return false
}
