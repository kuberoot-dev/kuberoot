// Package installer discovers the disks of a node and writes kuberoot onto one.
package installer

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kuberoot-dev/kuberoot/pkg/bootdisk"
)

const (
	RoleBootMedia = "BootMedia"
	RoleSystem    = "System"
)

type Partition struct {
	Name      string
	Label     string
	SizeBytes int64
}

type Disk struct {
	Name       string
	SizeBytes  int64
	Model      string
	Removable  bool
	Role       string
	Partitions []Partition
}

// Disks lists the whole disks of the node, skipping loop, RAM and empty devices.
func Disks() []Disk {
	entries, _ := os.ReadDir("/sys/block")
	var out []Disk
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") {
			continue
		}
		sys := filepath.Join("/sys/block", name)
		d := Disk{
			Name:      name,
			SizeBytes: sectors(filepath.Join(sys, "size")),
			Model:     strings.TrimSpace(readFile(filepath.Join(sys, "device/model"))),
			Removable: strings.TrimSpace(readFile(filepath.Join(sys, "removable"))) == "1",
		}
		if d.SizeBytes == 0 {
			continue
		}
		parts, _ := filepath.Glob(filepath.Join(sys, name+"*"))
		for _, p := range parts {
			pname := filepath.Base(p)
			label := ueventValue(filepath.Join(p, "uevent"), "PARTNAME")
			d.Partitions = append(d.Partitions, Partition{Name: pname, Label: label, SizeBytes: sectors(filepath.Join(p, "size"))})
			switch {
			case label == bootdisk.MediaLabel:
				d.Role = RoleBootMedia
			case label == bootdisk.StateLabel && d.Role == "":
				d.Role = RoleSystem
			}
		}
		sort.Slice(d.Partitions, func(i, j int) bool { return d.Partitions[i].Name < d.Partitions[j].Name })
		out = append(out, d)
	}
	return out
}

func sectors(path string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(readFile(path)), 10, 64)
	return n * 512
}

func readFile(path string) string {
	raw, _ := os.ReadFile(path)
	return string(raw)
}

func ueventValue(path, key string) string {
	for _, line := range strings.Split(readFile(path), "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}
