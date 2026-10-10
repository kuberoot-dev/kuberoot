package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
	"golang.org/x/sys/unix"
)

const serviceCgroups = "/sys/fs/cgroup/kuberoot"

var servicePath = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}

type service struct {
	name string
	args []string
	// console attaches the service to the system console as its controlling terminal.
	console bool
	// env is added to the service's environment.
	env []string
	// after holds the first start until it returns true, so a service does not
	// crash just because what it depends on is still coming up.
	after func() bool
}

// children maps the PIDs kinit started to the channel waiting for their exit.
// As PID 1 kinit reaps everything itself, so exec.Cmd.Wait is never used.
var (
	childrenMu sync.Mutex
	children   = map[int]chan unix.WaitStatus{}
	running    sync.Map // service name -> *os.Process

	// A generation is one set of running services; reconfiguring replaces it.
	genMu     sync.Mutex
	genCtx    = context.Background()
	genCancel context.CancelFunc
	genDone   sync.WaitGroup

	statusMu sync.Mutex
	statuses = map[string]*supervisor.ServiceStatus{}
)

func setStatus(name string, update func(*supervisor.ServiceStatus)) {
	statusMu.Lock()
	defer statusMu.Unlock()
	s, ok := statuses[name]
	if !ok {
		s = &supervisor.ServiceStatus{Name: name, Cgroup: filepath.Join(serviceCgroups, name), LogFile: logPath(name)}
		statuses[name] = s
	}
	update(s)
}

func logPath(name string) string { return filepath.Join("/var/log/kuberoot", name+".log") }

func startServices(services []service, cfg bootConfig) {
	if cfg.install {
		services = append(services, service{name: "installer", args: []string{"/usr/bin/kuberoot-installer"}, console: true})
		// From here on the console belongs to the installer screen: kernel
		// messages and kinit's own log stay off it.
		_ = os.WriteFile("/proc/sys/kernel/printk", []byte("1"), 0o644)
		if f, err := os.OpenFile(logPath("kinit"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			log.SetOutput(f)
		}
	}
	// Inherited by every service and, through containerd, by containers.
	limit := &unix.Rlimit{Cur: 1 << 20, Max: 1 << 20}
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, limit); err != nil {
		log.Printf("raise open files limit: %v", err)
	}
	controllers := []byte("+cpu +cpuset +memory +pids +io +hugetlb")
	_ = os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", controllers, 0o644)
	_ = os.MkdirAll(serviceCgroups, 0o755)
	_ = os.WriteFile(filepath.Join(serviceCgroups, "cgroup.subtree_control"), controllers, 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	genMu.Lock()
	genCtx, genCancel = ctx, cancel
	genMu.Unlock()
	for _, s := range services {
		genDone.Add(1)
		go func() {
			defer genDone.Done()
			supervise(ctx, s, cfg.verbose)
		}()
	}
}

// generation is the context of the running set of services; work tied to the
// node's current role stops with it.
func generation() context.Context {
	genMu.Lock()
	defer genMu.Unlock()
	return genCtx
}

// stopServices ends the current generation: every service gets SIGTERM, then
// SIGKILL if it outstays its grace period.
func stopServices() {
	genMu.Lock()
	if genCancel != nil {
		genCancel()
	}
	genMu.Unlock()
	genDone.Wait()
	statusMu.Lock()
	statuses = map[string]*supervisor.ServiceStatus{}
	statusMu.Unlock()
}

// supervise keeps a service running, restarting it with backoff when it exits.
func supervise(ctx context.Context, s service, verbose bool) {
	if s.after != nil {
		setStatus(s.name, func(st *supervisor.ServiceStatus) { st.State = supervisor.StateWaiting })
		for !s.after() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		status, err := runService(ctx, s, verbose)
		if ctx.Err() != nil {
			return
		}
		exit := describe(status)
		if err != nil {
			exit = err.Error()
			log.Printf("%s: %v", s.name, err)
		} else {
			log.Printf("%s exited (%s), restarting in %s", s.name, exit, backoff)
		}
		setStatus(s.name, func(st *supervisor.ServiceStatus) {
			st.State, st.PID, st.LastExit = supervisor.StateRestarting, 0, exit
			st.Restarts++
		})
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func runService(ctx context.Context, s service, verbose bool) (unix.WaitStatus, error) {
	logFile, err := os.OpenFile(logPath(s.name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	var out io.Writer = logFile
	if verbose {
		out = io.MultiWriter(logFile, &prefixWriter{prefix: "[" + s.name + "] ", w: os.Stdout})
	}
	cmd := exec.Command(s.args[0], s.args[1:]...)
	cmd.Env = append(append([]string{}, servicePath...), s.env...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &unix.SysProcAttr{Setsid: true}
	if s.console {
		tty, err := os.OpenFile("/dev/console", os.O_RDWR, 0)
		if err != nil {
			return 0, err
		}
		defer tty.Close()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
		cmd.Env = append(cmd.Env, "TERM=linux")
		cmd.SysProcAttr = &unix.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	}
	done, err := spawn(cmd)
	if err != nil {
		return 0, err
	}
	running.Store(s.name, cmd.Process)
	joinCgroup(s.name, cmd.Process.Pid)
	setStatus(s.name, func(st *supervisor.ServiceStatus) {
		st.State, st.PID, st.StartedAt = supervisor.StateRunning, cmd.Process.Pid, time.Now()
	})
	defer running.Delete(s.name)
	select {
	case status := <-done:
		return status, nil
	case <-ctx.Done():
	}
	_ = cmd.Process.Signal(unix.SIGTERM)
	select {
	case status := <-done:
		return status, nil
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		return <-done, nil
	}
}

// spawn starts cmd and returns a channel that receives its exit status from the reaper.
func spawn(cmd *exec.Cmd) (chan unix.WaitStatus, error) {
	childrenMu.Lock()
	defer childrenMu.Unlock()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan unix.WaitStatus, 1)
	children[cmd.Process.Pid] = done
	return done, nil
}

// reap collects every exited child: the ones kinit tracks and orphans reparented to PID 1.
func reap() {
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
		childrenMu.Lock()
		if done, ok := children[pid]; ok {
			done <- ws
			delete(children, pid)
		}
		childrenMu.Unlock()
	}
}

// joinCgroup moves a service into its own cgroup so its usage is accounted separately.
func joinCgroup(name string, pid int) {
	dir := filepath.Join(serviceCgroups, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644)
}

func describe(ws unix.WaitStatus) string {
	if ws.Signaled() {
		return "signal " + ws.Signal().String()
	}
	return fmt.Sprintf("code %d", ws.ExitStatus())
}

type prefixWriter struct {
	prefix string
	w      io.Writer
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	_, err := p.w.Write(append([]byte(p.prefix), b...))
	return len(b), err
}
