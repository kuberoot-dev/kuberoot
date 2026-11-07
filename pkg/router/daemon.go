package router

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/klog/v2"
)

// Daemon runs a program the router needs (dnsmasq, bird) and starts it again
// when it exits, until it is stopped.
type Daemon struct {
	Name string
	Args []string
	// StopWithParent kills the child if this process dies, including by
	// SIGKILL. Use for exclusive services such as an elected gateway's
	// DHCP server, which must not survive its controller.
	StopWithParent bool

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
	// The last time the program exited on its own, and what it said.
	exitedAt time.Time
	exitMsg  string
}

// Failure says why the program keeps exiting, if it exited on its own in
// the last minute and a half; empty while it runs.
func (d *Daemon) Failure() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.exitMsg == "" || time.Since(d.exitedAt) > 90*time.Second {
		return ""
	}
	return d.Name + " exited: " + d.exitMsg
}

// Start runs the program unless it already runs.
func (d *Daemon) Start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.done, d.running = cancel, make(chan struct{}), true
	d.exitMsg = "" // a new start, with a new configuration, is judged afresh
	go d.loop(ctx, d.done)
}

// Stop ends the program and waits for it.
func (d *Daemon) Stop() {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return
	}
	cancel, done := d.cancel, d.done
	d.running = false
	d.mu.Unlock()
	cancel()
	<-done
}

// Restart stops the program and runs it again, as after a configuration
// change it does not reload on its own.
func (d *Daemon) Restart() {
	d.Stop()
	d.Start()
}

// Running reports whether the program is meant to run.
func (d *Daemon) Running() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

func (d *Daemon) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	if d.StopWithParent {
		// Linux attaches Pdeathsig to the creating OS thread. Keep that
		// thread alive until every child has been waited for, otherwise
		// Go retiring the thread could kill a healthy daemon.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	backoff := time.Second
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, d.Args[0], d.Args[1:]...)
		if d.StopWithParent {
			cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		}
		var tail lastLine
		cmd.Stdout, cmd.Stderr = os.Stdout, io.MultiWriter(os.Stderr, &tail)
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 10 * time.Second
		start := time.Now()
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		d.mu.Lock()
		d.exitedAt, d.exitMsg = time.Now(), tail.String()
		if d.exitMsg == "" && err != nil {
			d.exitMsg = err.Error()
		}
		d.mu.Unlock()
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		klog.Errorf("%s exited (%v), starting again in %s", d.Name, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// lastLine keeps the last non-empty line written to it.
type lastLine struct {
	mu   sync.Mutex
	line string
}

func (l *lastLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range strings.Split(string(p), "\n") {
		if s = strings.TrimSpace(s); s != "" {
			l.line = s
		}
	}
	return len(p), nil
}

func (l *lastLine) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.line
}
