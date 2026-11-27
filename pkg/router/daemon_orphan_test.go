package router

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestDaemonProgramDiesWithTheRouter runs a Daemon in a child process
// standing in for kuberoot-router, kills that process with SIGKILL and
// checks that the program it supervised does not live on as an orphan.
func TestDaemonProgramDiesWithTheRouter(t *testing.T) {
	if os.Getenv("ROUTER_DAEMON_HELPER") == "1" {
		d := &Daemon{Name: "sleep", Args: []string{"/bin/sh", "-c", "echo $$; exec sleep 600"}}
		d.Start()
		select {} // wait to be killed
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestDaemonProgramDiesWithTheRouter$")
	helper.Env = append(os.Environ(), "ROUTER_DAEMON_HELPER=1")
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("no pid from the supervised program: %v", err)
	}
	pid, err := strconv.Atoi(line[:len(line)-1])
	if err != nil {
		t.Fatalf("pid %q: %v", line, err)
	}
	_ = helper.Process.Kill()
	_ = helper.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil || zombie(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("program %d outlived the killed router", pid)
}

func zombie(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	for i := len(b) - 1; i > 0; i-- {
		if b[i] == ')' && i+2 < len(b) {
			return b[i+2] == 'Z'
		}
	}
	return false
}
