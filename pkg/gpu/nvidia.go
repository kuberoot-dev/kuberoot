// Package gpu makes a node's NVIDIA GPUs usable: it finds them, creates
// their device files, as no udev does it here, and describes them to the
// container runtime as CDI devices (nvidia.com/gpu=<index>, and =all), so a
// device plugin can hand them to pods.
package gpu

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"sigs.k8s.io/yaml"
)

var (
	// ProcGPUs lists the GPUs the driver drives, one directory each.
	ProcGPUs = "/proc/driver/nvidia/gpus"
	// ProcDevices names the character device majors.
	ProcDevices = "/proc/devices"
	Dev         = "/dev"
	// CDIFile is where the runtime reads the GPUs' CDI description.
	CDIFile = "/var/run/cdi/nvidia.yaml"
	// LibDir holds the driver's libraries, on the node and in containers.
	LibDir = "/lib/x86_64-linux-gnu"
	// CDIHook refreshes a container's loader cache for the libraries given.
	CDIHook = "/usr/bin/nvidia-cdi-hook"
)

// Supported says whether kuberoot ships the NVIDIA driver for the node's
// architecture: it does for amd64 only.
var Supported = runtime.GOARCH == "amd64"

// ErrUnsupported is what asking for GPUs gets where no driver is shipped.
var ErrUnsupported = errors.New("NVIDIA GPUs are not supported on " + runtime.GOARCH + ": kuberoot ships the NVIDIA driver for amd64 only")

// GPU is one GPU as the driver reports it.
type GPU struct {
	Minor int    `json:"minor"`
	Model string `json:"model"`
	UUID  string `json:"uuid"`
	BusID string `json:"busID"`
}

// Find lists the GPUs the driver drives, by device minor.
func Find() ([]GPU, error) {
	dirs, err := os.ReadDir(ProcGPUs)
	if os.IsNotExist(err) {
		return nil, nil // no driver loaded: no NVIDIA GPU
	}
	if err != nil {
		return nil, err
	}
	var out []GPU
	for _, d := range dirs {
		raw, err := os.ReadFile(filepath.Join(ProcGPUs, d.Name(), "information"))
		if err != nil {
			continue
		}
		g := GPU{BusID: d.Name(), Minor: -1}
		for _, line := range strings.Split(string(raw), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch strings.TrimSpace(k) {
			case "Model":
				g.Model = v
			case "GPU UUID":
				g.UUID = v
			case "Device Minor":
				g.Minor, _ = strconv.Atoi(v)
			}
		}
		if g.Minor >= 0 {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Minor < out[j].Minor })
	return out, nil
}

// majors reads the majors of the driver's character devices.
func majors() (map[string]uint32, error) {
	f, err := os.Open(ProcDevices)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]uint32{}
	chars := false
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		switch {
		case line == "Character devices:":
			chars = true
			continue
		case line == "Block devices:":
			chars = false
		}
		if f := strings.Fields(line); chars && len(f) == 2 {
			if n, err := strconv.ParseUint(f[0], 10, 32); err == nil {
				out[f[1]] = uint32(n)
			}
		}
	}
	return out, s.Err()
}

// node is a device file to create.
type node struct {
	path         string
	major, minor uint32
}

// nodes are the device files the driver's devices need: the control
// device, one per GPU, and those of unified memory and mode setting.
func nodes(gpus []GPU, m map[string]uint32) []node {
	front, ok := m["nvidia-frontend"]
	if !ok {
		front, ok = m["nvidia"]
	}
	if !ok {
		return nil
	}
	out := []node{{filepath.Join(Dev, "nvidiactl"), front, 255}}
	for _, g := range gpus {
		out = append(out, node{filepath.Join(Dev, fmt.Sprintf("nvidia%d", g.Minor)), front, uint32(g.Minor)})
	}
	if _, ok := m["nvidia-modeset"]; ok {
		out = append(out, node{filepath.Join(Dev, "nvidia-modeset"), front, 254})
	}
	if uvm, ok := m["nvidia-uvm"]; ok {
		out = append(out, node{filepath.Join(Dev, "nvidia-uvm"), uvm, 0}, node{filepath.Join(Dev, "nvidia-uvm-tools"), uvm, 1})
	}
	return out
}

// EnsureDeviceNodes creates the device files of the GPUs found.
func EnsureDeviceNodes(gpus []GPU) ([]string, error) {
	m, err := majors()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, n := range nodes(gpus, m) {
		dev := int(unix.Mkdev(n.major, n.minor))
		var st unix.Stat_t
		if err := unix.Stat(n.path, &st); err == nil && int(st.Rdev) == dev {
			paths = append(paths, n.path)
			continue
		}
		_ = os.Remove(n.path)
		if err := unix.Mknod(n.path, unix.S_IFCHR|0o666, dev); err != nil {
			return paths, fmt.Errorf("%s: %w", n.path, err)
		}
		// mknod is subject to the umask: set the mode it was meant to have.
		if err := os.Chmod(n.path, 0o666); err != nil {
			return paths, err
		}
		paths = append(paths, n.path)
	}
	return paths, nil
}

// CDI describes the GPUs to the container runtime: each by its index and
// its UUID, and all of them together, with the device files and the driver
// libraries a CUDA program needs.
func CDI(gpus []GPU, devices []string) ([]byte, error) {
	var common []map[string]any
	perGPU := map[string]bool{}
	for _, g := range gpus {
		perGPU[filepath.Join(Dev, fmt.Sprintf("nvidia%d", g.Minor))] = true
	}
	for _, d := range devices {
		if !perGPU[d] {
			common = append(common, map[string]any{"path": d})
		}
	}
	var mounts []map[string]any
	libs, _ := filepath.Glob(filepath.Join(LibDir, "libcuda.so*"))
	for _, pattern := range []string{"libnvidia-ml.so*", "libnvidia-ptxjitcompiler.so*", "libnvidia-nvvm.so*", "libnvidia-gpucomp.so*"} {
		more, _ := filepath.Glob(filepath.Join(LibDir, pattern))
		libs = append(libs, more...)
	}
	sort.Strings(libs)
	for _, l := range append(libs, "/usr/bin/nvidia-smi") {
		if _, err := os.Stat(l); err == nil {
			mounts = append(mounts, map[string]any{"hostPath": l, "containerPath": l, "options": []string{"ro", "nosuid", "nodev", "bind"}})
		}
	}
	var devs []map[string]any
	var all []map[string]any
	for _, g := range gpus {
		dn := []map[string]any{{"path": filepath.Join(Dev, fmt.Sprintf("nvidia%d", g.Minor))}}
		all = append(all, dn...)
		devs = append(devs, map[string]any{"name": strconv.Itoa(g.Minor), "containerEdits": map[string]any{"deviceNodes": dn}})
		if g.UUID != "" {
			devs = append(devs, map[string]any{"name": g.UUID, "containerEdits": map[string]any{"deviceNodes": dn}})
		}
	}
	if len(gpus) > 0 {
		devs = append(devs, map[string]any{"name": "all", "containerEdits": map[string]any{"deviceNodes": all}})
	}
	spec := map[string]any{
		"cdiVersion": "0.6.0",
		"kind":       "nvidia.com/gpu",
		"devices":    devs,
		"containerEdits": map[string]any{
			"deviceNodes": common,
			"mounts":      mounts,
			// The image's loader cache knows nothing of the libraries given.
			"hooks": []map[string]any{{"hookName": "createContainer", "path": CDIHook,
				"args": []string{"nvidia-cdi-hook", "update-ldcache", "--folder", LibDir}}},
		},
	}
	return yaml.Marshal(spec)
}

// Setup finds the GPUs, creates their device files and writes their CDI
// description; with none, it removes a description left from before.
func Setup() ([]GPU, error) {
	if !Supported {
		return nil, ErrUnsupported
	}
	gpus, err := Find()
	if err != nil || len(gpus) == 0 {
		_ = os.Remove(CDIFile)
		return nil, err
	}
	devices, err := EnsureDeviceNodes(gpus)
	if err != nil {
		return gpus, err
	}
	spec, err := CDI(gpus, devices)
	if err != nil {
		return gpus, err
	}
	if err := os.MkdirAll(filepath.Dir(CDIFile), 0o755); err != nil {
		return gpus, err
	}
	tmp := CDIFile + ".tmp"
	if err := os.WriteFile(tmp, spec, 0o644); err != nil {
		return gpus, err
	}
	return gpus, os.Rename(tmp, CDIFile)
}
