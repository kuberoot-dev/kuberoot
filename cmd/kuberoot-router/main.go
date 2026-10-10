// Command kuberoot-router makes a router node match its router.kuberoot.dev
// resources: links and addresses, routes, the firewall and NAT, DHCP and DNS
// for its networks, and BGP.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/router"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "credentials for the node's API server")
	runDir := flag.String("run-dir", "/run/kuberoot/router", "the daemons' configuration and sockets")
	stateDir := flag.String("state-dir", "/var/lib/kuberoot/router", "what survives a reboot: DHCP leases")
	routerID := flag.String("router-id", "", "BGP router ID when the BGPRouter names none; the first IPv4 address otherwise")
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
	c := &router.Controller{Client: client, RunDir: *runDir, StateDir: *stateDir, RouterID: *routerID}
	if err := c.Run(ctx); err != nil {
		klog.Fatal(err)
	}
}
