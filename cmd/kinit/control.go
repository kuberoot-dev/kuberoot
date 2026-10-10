package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
	"golang.org/x/sys/unix"
)

// serveControl exposes service status and actions to node-local clients.
func serveControl(reconfigure func()) {
	if err := os.MkdirAll(filepath.Dir(supervisor.Socket), 0o700); err != nil {
		log.Printf("control socket: %v", err)
		return
	}
	// The directory may exist from boot with wider permissions; only root
	// may reach the socket that reboots the node.
	_ = os.Chmod(filepath.Dir(supervisor.Socket), 0o700)
	_ = os.Remove(supervisor.Socket)
	ln, err := net.Listen("unix", supervisor.Socket)
	if err != nil {
		log.Printf("control socket: %v", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/services", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(serviceStatuses())
	})
	mux.HandleFunc("POST /v1/services/{name}/restart", func(w http.ResponseWriter, r *http.Request) {
		p, ok := running.Load(r.PathValue("name"))
		if !ok {
			http.Error(w, "service not running", http.StatusNotFound)
			return
		}
		_ = p.(*os.Process).Signal(unix.SIGTERM)
	})
	mux.HandleFunc("POST /v1/reconfigure", func(http.ResponseWriter, *http.Request) {
		go reconfigure()
	})
	mux.HandleFunc("POST /v1/reboot", func(http.ResponseWriter, *http.Request) {
		go shutdown(unix.LINUX_REBOOT_CMD_RESTART, "reboot requested")
	})
	go func() { _ = http.Serve(ln, mux) }()
}

func serviceStatuses() []supervisor.ServiceStatus {
	statusMu.Lock()
	defer statusMu.Unlock()
	out := make([]supervisor.ServiceStatus, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
