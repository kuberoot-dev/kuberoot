// Command kuberoot-pause is the process of a pod sandbox, for architectures
// upstream publishes no pause image of (riscv64): it holds the pod's
// namespaces, reaps the orphans that end up with it as PID 1, and exits on
// SIGINT or SIGTERM, as the upstream pause does.
package main

import (
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
)

func main() {
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, unix.SIGINT, unix.SIGTERM, unix.SIGCHLD)
	for sig := range sigs {
		if sig != unix.SIGCHLD {
			os.Exit(0)
		}
		for {
			pid, err := unix.Wait4(-1, nil, unix.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}
