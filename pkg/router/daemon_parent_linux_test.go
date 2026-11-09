package router

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const daemonChildPIDEnv = "KUBEROOT_TEST_DAEMON_CHILD_PID"

// Two helper subprocesses exercise actual parent death rather than an
// orderly Context cancellation: test -> daemon parent -> daemon child.
func TestDaemonParentHelper(t *testing.T) {
	if os.Getenv(daemonChildPIDEnv) == "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{Name: "test-child", Args: []string{exe, "-test.run=^TestDaemonChildHelper$"}, StopWithParent: true}
	d.Start()
	defer d.Stop()
	for {
		time.Sleep(time.Hour)
	}
}

func TestDaemonChildHelper(t *testing.T) {
	path := os.Getenv(daemonChildPIDEnv)
	if path == "" {
		return
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestDaemonDiesWhenParentIsKilled(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "child.pid")
	parent := exec.Command(exe, "-test.run=^TestDaemonParentHelper$")
	parent.Env = append(os.Environ(), daemonChildPIDEnv+"="+path)
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = parent.Process.Kill()
		_ = parent.Wait()
	}()
	var child int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			child, _ = strconv.Atoi(string(raw))
			if child > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("child did not start")
	}
	defer func() {
		if p, err := os.FindProcess(child); err == nil {
			_ = p.Kill()
		}
	}()
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child))
		if os.IsNotExist(err) {
			return
		}
		// A minimal container's PID 1 may not reap an orphan immediately.
		// A zombie has exited and cannot keep serving DHCP.
		if err == nil {
			_, rest, _ := strings.Cut(string(raw), ") ")
			if strings.HasPrefix(rest, "Z ") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon survived SIGKILL of its parent")
}
