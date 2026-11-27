package router

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The kernel sizes the connection tracking table from memory in steps: a
// node with 2 GiB gets 65536 entries, the same as one with 1 GiB, and NAT
// for a busy network fills that. An entry takes about 320 bytes; one entry
// per 8 KiB of memory spends 3 to 6 % of it on a full table.
const (
	conntrackBytesPerEntry = 8 << 10
	conntrackMin           = 65536
	conntrackMax           = 4 << 20
)

// ConntrackSize is the size of the connection tracking table for a node
// with memTotal bytes of memory: the power of two nearest to one entry per
// 8 KiB, so the hash table has as many buckets, and MemTotal, a little
// under what the machine was given, does not halve it.
func ConntrackSize(memTotal uint64) int {
	want := memTotal / conntrackBytesPerEntry
	n := conntrackMin
	for uint64(n) < want && n < conntrackMax {
		n *= 2
	}
	if n > conntrackMin && uint64(n)*uint64(n) > 2*want*want {
		n /= 2
	}
	return n
}

// SizeConntrack grows the connection tracking table and its hash table to
// ConntrackSize for the node's memory under procRoot and sysRoot. It never
// shrinks them: a larger size set by hand stays.
func SizeConntrack(procRoot, sysRoot string) (int, error) {
	mem, err := memTotal(filepath.Join(procRoot, "meminfo"))
	if err != nil {
		return 0, err
	}
	n := ConntrackSize(mem)
	for _, f := range []string{
		filepath.Join(procRoot, "sys/net/netfilter/nf_conntrack_max"),
		// One bucket per entry, as the kernel sizes it itself.
		filepath.Join(sysRoot, "module/nf_conntrack/parameters/hashsize"),
	} {
		cur, err := readInt(f)
		if err != nil {
			return 0, err
		}
		if cur >= n {
			continue
		}
		if err := os.WriteFile(f, []byte(strconv.Itoa(n)+"\n"), 0o644); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func memTotal(meminfo string) (uint64, error) {
	f, err := os.Open(meminfo)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kib, err := strconv.ParseUint(fields[1], 10, 64)
			return kib << 10, err
		}
	}
	return 0, fmt.Errorf("%s: no MemTotal", meminfo)
}

func readInt(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}
