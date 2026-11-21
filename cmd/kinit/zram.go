package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

// zramSize reserves logical swap equal to half RAM. Compressed pages consume
// RAM only when swapped; a physical cap prevents incompressible pages from
// consuming the memory the kernel and applications need to make progress.
func zramSize(total uint64) uint64 {
	if total == 0 || total >= smallMemory {
		return 0
	}
	return total / 2 / 4096 * 4096
}

func swapHeader(size uint64, page int) ([]byte, error) {
	if page < 2048 || size/uint64(page) < 2 || size/uint64(page) > 1<<32 {
		return nil, fmt.Errorf("invalid swap size %d/page %d", size, page)
	}
	b := make([]byte, page)
	binary.LittleEndian.PutUint32(b[1024:], 1)
	binary.LittleEndian.PutUint32(b[1028:], uint32(size/uint64(page)-1))
	copy(b[page-10:], "SWAPSPACE2")
	return b, nil
}

func setupZRAM() error {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return err
	}
	size := zramSize(uint64(info.Totalram) * uint64(info.Unit))
	if size == 0 {
		return nil
	}
	const sys = "/sys/block/zram0/"
	for _, setting := range []struct{ name, value string }{
		{"comp_algorithm", "lz4"},
		{"mem_limit", strconv.FormatUint(size/2, 10)},
		{"disksize", strconv.FormatUint(size, 10)},
	} {
		if err := os.WriteFile(sys+setting.name, []byte(setting.value), 0o644); err != nil {
			return err
		}
	}
	header, err := swapHeader(size, os.Getpagesize())
	if err != nil {
		return err
	}
	f, err := os.OpenFile("/dev/zram0", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteAt(header, 0)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	path, err := unix.BytePtrFromString("/dev/zram0")
	if err != nil {
		return err
	}
	if _, _, errno := unix.Syscall(unix.SYS_SWAPON, uintptr(unsafe.Pointer(path)), 0, 0); errno != 0 {
		return errno
	}
	log.Printf("zram swap: %d MiB, compressed RAM cap %d MiB", size>>20, size>>21)
	return nil
}
