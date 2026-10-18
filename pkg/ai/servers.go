// Package ai runs language models on a node: the weights downloaded and
// checked against their hash, and llama-server serving them as a process of
// the node, with no pod and no container image.
package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
	"github.com/kuberoot-dev/kuberoot/pkg/gpu"
)

var (
	// StateDir keeps the servers' specs; BlobDir the weights, by hash.
	StateDir = "/var/lib/kuberoot/modelservers"
	BlobDir  = "/var/lib/kuberoot/models"
	RunDir   = "/run/kuberoot/models"
	LogDir   = "/var/log/kuberoot/models"
	Binary   = "/usr/bin/llama-server"
	// CgroupDir holds the servers' memory: all of the node's but Reserve,
	// so that a server asking for more is stopped, and not the node.
	CgroupDir = "/sys/fs/cgroup/kuberoot-models"
	// RestartDelay: a server that exits is started again after this.
	RestartDelay = 30 * time.Second
)

// Servers are the model servers of the node.
type Servers struct {
	// Reserve is the memory kept from the servers for the node itself.
	Reserve int64

	mu       sync.Mutex
	progress map[string]int64 // sha256 -> bytes downloaded so far
}

func (s *Servers) specFile(n string) string { return filepath.Join(StateDir, n+".json") }
func (s *Servers) pidFile(n string) string  { return filepath.Join(RunDir, n+".pid") }

// LogFile is the server's output.
func (s *Servers) LogFile(n string) string { return filepath.Join(LogDir, n+".log") }

// Blob is where weights with this hash are kept.
func Blob(sha string) string { return filepath.Join(BlobDir, sha+".gguf") }

// EngineBlob is where an engine with this hash is kept.
func EngineBlob(sha string) string { return filepath.Join(BlobDir, sha+".engine") }

// Save keeps a server's spec; Apply then makes the node match it. The
// weights it served before are remembered, and kept, for a rollback to find
// them still on the node.
func (s *Servers) Save(name string, spec node.ModelServerSpec) error {
	if err := os.MkdirAll(StateDir, 0o700); err != nil {
		return err
	}
	if old, err := s.Load(name); err == nil && (old.SHA256 != spec.SHA256 || old.EngineSHA256 != spec.EngineSHA256) {
		if err := os.WriteFile(s.specFile(name)+".previous", []byte(old.SHA256+" "+old.EngineSHA256), 0o600); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(s.specFile(name), raw, 0o600)
}

func (s *Servers) Load(name string) (node.ModelServerSpec, error) {
	var spec node.ModelServerSpec
	raw, err := os.ReadFile(s.specFile(name))
	if err != nil {
		return spec, err
	}
	return spec, json.Unmarshal(raw, &spec)
}

// Names lists the node's model servers.
func (s *Servers) Names() []string {
	files, _ := filepath.Glob(filepath.Join(StateDir, "*.json"))
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimSuffix(filepath.Base(f), ".json"))
	}
	sort.Strings(out)
	return out
}

// Args are llama-server's arguments for a server.
func Args(spec node.ModelServerSpec) []string {
	args := []string{"--model", Blob(spec.SHA256), "--alias", spec.Model,
		"--host", "0.0.0.0", "--port", strconv.Itoa(int(spec.Port)), "--metrics"}
	if spec.ContextSize > 0 {
		args = append(args, "--ctx-size", strconv.Itoa(int(spec.ContextSize)))
	}
	if spec.Parallel > 0 {
		args = append(args, "--parallel", strconv.Itoa(int(spec.Parallel)))
	}
	if spec.Threads > 0 {
		args = append(args, "--threads", strconv.Itoa(int(spec.Threads)))
	}
	if spec.GPUs > 0 {
		args = append(args, "--n-gpu-layers", "999")
	}
	return args
}

// binary serves the spec: its engine, or the llama-server of the image.
func binary(spec node.ModelServerSpec) string {
	if spec.EngineSHA256 != "" {
		return EngineBlob(spec.EngineSHA256)
	}
	return Binary
}

func configKey(spec node.ModelServerSpec, gpus []int32) string {
	key := binary(spec) + " " + strings.Join(Args(spec), " ")
	if len(gpus) > 0 {
		key += " gpus=" + joinInts(gpus)
	}
	return key
}

func joinInts(v []int32) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(int(n))
	}
	return strings.Join(parts, ",")
}

// GPUsOf are the GPUs a server was given.
func (s *Servers) GPUsOf(name string) []int32 {
	raw, err := os.ReadFile(s.specFile(name) + ".gpus")
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var out []int32
	for _, f := range strings.Split(strings.TrimSpace(string(raw)), ",") {
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, int32(n))
		}
	}
	return out
}

// GPUsPresent lists the node's GPUs by index; a variable for tests.
var GPUsPresent = func() []int32 {
	gpus, _ := gpu.Find()
	out := make([]int32, 0, len(gpus))
	for _, g := range gpus {
		out = append(out, int32(g.Minor))
	}
	return out
}

// assignGPUs gives a server as many GPUs as its spec asks for: those it
// has, if they still are enough, else free ones no other server holds.
func (s *Servers) assignGPUs(name string, spec node.ModelServerSpec) ([]int32, error) {
	file := s.specFile(name) + ".gpus"
	if spec.GPUs == 0 {
		_ = os.Remove(file)
		return nil, nil
	}
	if !gpu.Supported {
		return nil, gpu.ErrUnsupported
	}
	present := map[int32]bool{}
	for _, g := range GPUsPresent() {
		present[g] = true
	}
	held := map[int32]bool{}
	for _, other := range s.Names() {
		if other == name {
			continue
		}
		for _, g := range s.GPUsOf(other) {
			held[g] = true
		}
	}
	var mine []int32
	for _, g := range s.GPUsOf(name) {
		if present[g] && !held[g] && len(mine) < int(spec.GPUs) {
			mine = append(mine, g)
		}
	}
	for _, g := range GPUsPresent() {
		if len(mine) >= int(spec.GPUs) {
			break
		}
		if !held[g] && !slices.Contains(mine, g) {
			mine = append(mine, g)
		}
	}
	if len(mine) < int(spec.GPUs) {
		return nil, fmt.Errorf("%d GPUs asked for, %d free", spec.GPUs, len(mine))
	}
	slices.Sort(mine)
	return mine, os.WriteFile(file, []byte(joinInts(mine)), 0o600)
}

// Apply downloads the weights the server needs, and runs the server with
// them; a server running with other weights or settings is restarted.
func (s *Servers) Apply(ctx context.Context, name string, spec node.ModelServerSpec) error {
	if spec.GPUs > 0 && spec.EngineSHA256 == "" {
		return fmt.Errorf("a model on GPUs needs an engine built for CUDA")
	}
	if err := s.fetch(ctx, spec.URL, spec.SHA256, Blob(spec.SHA256)); err != nil {
		return err
	}
	if spec.EngineSHA256 != "" {
		if err := s.fetch(ctx, spec.EngineURL, spec.EngineSHA256, EngineBlob(spec.EngineSHA256)); err != nil {
			return fmt.Errorf("engine: %w", err)
		}
		if err := os.Chmod(EngineBlob(spec.EngineSHA256), 0o755); err != nil {
			return err
		}
	}
	gpus, err := s.assignGPUs(name, spec)
	if err != nil {
		return err
	}
	pid := s.pid(name)
	if pid != 0 {
		if s.startedWith(name) == configKey(spec, gpus) {
			if ok, _ := healthy(spec.Port); ok {
				// Proven: no longer the first one the kernel stops.
				_ = os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", pid), []byte("0"), 0o644)
			}
			return nil
		}
		if err := s.stop(name, pid); err != nil {
			return err
		}
	} else if s.startedWith(name) == configKey(spec, gpus) {
		// It ran with these settings and exited: not again at once.
		if info, err := os.Stat(s.pidFile(name) + ".config"); err == nil && time.Since(info.ModTime()) < RestartDelay {
			return nil
		}
	}
	return s.start(name, spec, gpus)
}

// cgroup is the server's own group under the servers' limit; the whole
// server goes when it runs out.
func (s *Servers) cgroup(name string) (*os.File, error) {
	if err := os.MkdirAll(CgroupDir, 0o755); err != nil {
		return nil, err
	}
	if total := memTotal(); total > 0 {
		limit := total - s.Reserve
		if limit < total/4 {
			limit = total / 4
		}
		if err := os.WriteFile(filepath.Join(CgroupDir, "memory.max"), []byte(strconv.FormatInt(limit, 10)), 0o644); err != nil {
			return nil, fmt.Errorf("memory limit: %w", err)
		}
	}
	_ = os.WriteFile(filepath.Join(CgroupDir, "memory.swap.max"), []byte("0"), 0o644)
	_ = os.WriteFile(filepath.Join(CgroupDir, "cgroup.subtree_control"), []byte("+memory +cpu"), 0o644)
	dir := filepath.Join(CgroupDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(dir, "memory.oom.group"), []byte("1"), 0o644)
	return os.Open(dir)
}

func memTotal() int64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			return kb << 10
		}
	}
	return 0
}

func (s *Servers) start(name string, spec node.ModelServerSpec, gpus []int32) error {
	for _, d := range []string{RunDir, LogDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	log, err := os.OpenFile(s.LogFile(name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cg, err := s.cgroup(name)
	if err != nil {
		return err
	}
	defer cg.Close()
	cmd := exec.Command(binary(spec), Args(spec)...)
	cmd.Stdout, cmd.Stderr = log, log
	if len(gpus) > 0 {
		// The server sees its GPUs only, as 0, 1 and on.
		cmd.Env = append(os.Environ(), "CUDA_VISIBLE_DEVICES="+joinInts(gpus))
	}
	// A session of its own: the server outlives the node API. It starts in
	// its cgroup, under the limit from its first instruction.
	cmd.SysProcAttr = procAttr(cg)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start llama-server: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	// Until it serves, a new server is the first the kernel stops when the
	// servers run out of memory, not one that serves already.
	_ = os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", cmd.Process.Pid), []byte("500"), 0o644)
	if err := os.WriteFile(s.pidFile(name)+".config", []byte(configKey(spec, gpus)), 0o600); err != nil {
		return err
	}
	return os.WriteFile(s.pidFile(name), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
}

func (s *Servers) startedWith(name string) string {
	raw, _ := os.ReadFile(s.pidFile(name) + ".config")
	return string(raw)
}

func (s *Servers) stop(name string, pid int) error {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 20 && s.pid(name) != 0; i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if s.pid(name) != 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		for i := 0; i < 10 && s.pid(name) != 0; i++ {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if s.pid(name) != 0 {
		return fmt.Errorf("the server %s does not exit", name)
	}
	_ = os.Remove(s.pidFile(name))
	return nil
}

// pid is the server's process when it runs, 0 otherwise.
func (s *Servers) pid(name string) int {
	raw, err := os.ReadFile(s.pidFile(name))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || !strings.Contains(string(cmdline), "--alias") {
		return 0
	}
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil && strings.Contains(string(stat), ") Z ") {
		return 0
	}
	return pid
}

// fetch downloads weights once, into a file named by their hash; a
// download that does not match the hash is thrown away.
func (s *Servers) fetch(ctx context.Context, url, sha, dest string) error {
	if len(sha) != 64 {
		return fmt.Errorf("sha256 %q: 64 hex digits", sha)
	}
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	if err := os.MkdirAll(BlobDir, 0o700); err != nil {
		return err
	}
	part := dest + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	defer os.Remove(part)
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	h := sha256.New()
	w := &counter{w: io.MultiWriter(f, h), add: func(n int) { s.addProgress(sha, n) }}
	defer s.clearProgress(sha)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sha {
		return fmt.Errorf("download %s: sha256 %s, not the %s asked for", url, got, sha)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return os.Rename(part, dest)
}

type counter struct {
	w   io.Writer
	add func(int)
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.add(n)
	return n, err
}

func (s *Servers) addProgress(sha string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progress == nil {
		s.progress = map[string]int64{}
	}
	s.progress[sha] += int64(n)
}

func (s *Servers) clearProgress(sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.progress, sha)
}

func (s *Servers) downloading(sha string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.progress[sha]
	return n, ok
}

// healthy asks the server itself: it answers 200 once the model is loaded.
var healthy = func(port int32) (bool, error) {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// Status tells where the server is: its weights downloading, the model
// loading, the server ready, or not running.
func (s *Servers) Status(name string, spec node.ModelServerSpec) node.ModelServerStatus {
	if n, ok := s.downloading(spec.SHA256); ok {
		return node.ModelServerStatus{Phase: "Downloading", Downloaded: n}
	}
	pid := s.pid(name)
	if pid == 0 {
		st := node.ModelServerStatus{Phase: "Starting", Message: lastLine(s.LogFile(name))}
		if s.startedWith(name) == configKey(spec, s.GPUsOf(name)) {
			// Started with these weights and settings, and gone: they do
			// not run here. The next pass tries again.
			st.Phase = "Failed"
		}
		return st
	}
	st := node.ModelServerStatus{Phase: "Loading", PID: int32(pid)}
	if f := strings.Fields(s.startedWith(name)); len(f) > 0 {
		if i := slices.Index(f, "--model"); i >= 0 && i+1 < len(f) {
			st.SHA256 = strings.TrimSuffix(filepath.Base(f[i+1]), ".gguf")
		}
	}
	st.GPUIndexes = s.GPUsOf(name)
	if f := strings.Fields(s.startedWith(name)); len(f) > 0 && strings.HasSuffix(f[0], ".engine") {
		st.EngineSHA256 = strings.TrimSuffix(filepath.Base(f[0]), ".engine")
	}
	if info, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
		t := metav1.NewTime(info.ModTime())
		st.StartedAt = &t
	}
	if ok, _ := healthy(spec.Port); ok && st.SHA256 == spec.SHA256 && st.EngineSHA256 == spec.EngineSHA256 {
		st.Phase = "Ready"
	}
	return st
}

// Delete stops a server and forgets it, and the weights no server needs.
func (s *Servers) Delete(name string) error {
	if pid := s.pid(name); pid != 0 {
		if err := s.stop(name, pid); err != nil {
			return err
		}
	}
	for _, f := range []string{s.specFile(name), s.specFile(name) + ".previous", s.specFile(name) + ".gpus", s.pidFile(name) + ".config"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return s.Prune()
}

// Prune removes weights and engines that no server runs or ran last.
func (s *Servers) Prune() error {
	keep := map[string]bool{}
	for _, n := range s.Names() {
		if spec, err := s.Load(n); err == nil {
			keep[Blob(spec.SHA256)] = true
			if spec.EngineSHA256 != "" {
				keep[EngineBlob(spec.EngineSHA256)] = true
			}
		}
		if raw, err := os.ReadFile(s.specFile(n) + ".previous"); err == nil {
			for _, f := range strings.Fields(string(raw)) {
				keep[Blob(f)], keep[EngineBlob(f)] = true, true
			}
		}
	}
	for _, pattern := range []string{"*.gguf", "*.engine"} {
		blobs, _ := filepath.Glob(filepath.Join(BlobDir, pattern))
		for _, b := range blobs {
			if !keep[b] {
				if err := os.Remove(b); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func lastLine(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	return lines[len(lines)-1]
}
