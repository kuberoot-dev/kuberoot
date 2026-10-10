// Command kuberoot-devices makes a node run its devices.kuberoot.dev
// resources: it reads the devices, evaluates the routes and publishes what
// they send, serves every value as a metric and sends heartbeats, with
// changes on trial under a Safeguard.
package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/tym83/kuberoot/pkg/devices"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "credentials for the node's API server")
	stateDir := flag.String("state-dir", "/var/lib/kuberoot/devices", "what survives a reboot: the confirmed configuration")
	metricsAddr := flag.String("metrics-address", "", "where to serve the devices' values as Prometheus metrics, as :9790; none when empty")
	advertise := flag.String("advertise-address", "", "the node's address, for the kube-system Service kuberoot-devices through which the cluster scrapes the metrics")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		klog.Fatalf("kubeconfig: %v", err)
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("client: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := &devices.Controller{Client: client, StateDir: *stateDir}
	if *metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", c.MetricsHandler())
		srv := &http.Server{Addr: *metricsAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				klog.Fatalf("metrics: %v", err)
			}
		}()
		defer srv.Close()
		if *advertise != "" {
			_, port, err := net.SplitHostPort(*metricsAddr)
			n, perr := strconv.Atoi(port)
			if err != nil || perr != nil {
				klog.Fatalf("--metrics-address %q: no port", *metricsAddr)
			}
			go devices.Advertise(ctx, client, *advertise, n)
		}
	}
	if err := c.Run(ctx); err != nil {
		klog.Fatal(err)
	}
}
