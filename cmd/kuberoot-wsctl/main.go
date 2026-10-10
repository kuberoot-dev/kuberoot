// Command kuberoot-wsctl runs a workstation cluster's workspaces: a desktop
// in a virtual machine for each, and the gateway that opens it in a browser
// or for an RDP client.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/tym83/kuberoot/pkg/wsctl"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "credentials for the cluster's API server")
	advertise := flag.String("advertise-address", "", "the address people reach the gateway at")
	port := flag.Int("port", 6080, "the gateway's port for browsers")
	novnc := flag.String("novnc", "/usr/share/kuberoot/novnc", "the directory noVNC is served from")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		klog.Fatalf("kubeconfig: %v", err)
	}
	cfg.Timeout = time.Minute
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gw := &wsctl.Gateway{NoVNC: *novnc}
	srv := &http.Server{Addr: fmt.Sprintf(":%d", *port), Handler: gw, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			klog.Fatalf("gateway: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	c := &wsctl.Controller{Dynamic: dynamic.NewForConfigOrDie(cfg), Kube: kubernetes.NewForConfigOrDie(cfg),
		Gateway: gw, Advertise: *advertise, HTTPPort: *port}
	if err := c.Run(ctx); err != nil {
		klog.Fatal(err)
	}
}
