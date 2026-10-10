// Command kuberoot-vmctl runs a hypervisor cluster's virtual machines: it
// places them on nodes, keeps their disks replicated, starts them elsewhere
// when a node fails, and serves the machines' network its gateway.
package main

import (
	"context"
	"flag"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/vmctl"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "credentials for the cluster's API server")
	network := flag.String("network", "10.123.0.0/24", "the machines' network; its first address is the gateway here")
	stateDir := flag.String("state-dir", "/var/lib/kuberoot/vmctl", "the gateway's DHCP leases")
	klog.InitFlags(nil)
	flag.Parse()

	prefix, err := netip.ParsePrefix(*network)
	if err != nil || !prefix.Addr().Is4() {
		klog.Fatalf("--network %q: an IPv4 network", *network)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		klog.Fatalf("kubeconfig: %v", err)
	}
	// A node that answers slowly delays its machines, not the others'.
	cfg.Timeout = time.Minute
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := &vmctl.Controller{Dynamic: dynamic.NewForConfigOrDie(cfg), Kube: kubernetes.NewForConfigOrDie(cfg),
		Network: prefix, StateDir: *stateDir}
	if err := c.Run(ctx); err != nil {
		klog.Fatal(err)
	}
}
