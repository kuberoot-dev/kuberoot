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

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/internal/legacyvmctl"
	"github.com/kuberoot-dev/kuberoot/pkg/distro"
	"github.com/kuberoot-dev/kuberoot/pkg/vmctl"
	"github.com/kuberoot-dev/kuberoot/pkg/vmmaintenance"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "credentials for the cluster's API server")
	network := flag.String("network", "10.123.0.0/24", "the machines' network; its first address is the gateway here")
	stateDir := flag.String("state-dir", "/var/lib/kuberoot/vmctl", "the gateway's DHCP leases")
	leaderElect := flag.Bool("leader-elect", false, "elect one controller and gateway using the Lease kuberoot-vmctl")
	namespace := flag.String("leader-election-namespace", "kube-system", "namespace of the controller's Lease")
	identity := flag.String("identity", "", "replica name in the Lease; the host name when empty")
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
	kube, client := kubernetes.NewForConfigOrDie(cfg), dynamic.NewForConfigOrDie(cfg)
	mode, err := vmmaintenance.WaitAPIMode(ctx, client)
	if err != nil {
		klog.Fatalf("VM API startup selection: %v", err)
	}
	if mode == vmmaintenance.ModeAlpha1 && !distro.VMAPITransitionEnabled() {
		klog.Fatal("legacy VM API requires an explicitly built transition image")
	}
	guard, err := vmmaintenance.New(ctx, kube, client, "kuberoot-vmctl", mode)
	if err != nil {
		klog.Fatalf("maintenance barrier: %v", err)
	}
	var c interface {
		Run(context.Context) error
		LockGateway() (func(), error)
	}
	if mode == vmmaintenance.ModeAlpha1 {
		c = &legacyvmctl.Controller{Dynamic: client, Kube: kube, Network: prefix, StateDir: *stateDir, StableAddresses: *leaderElect, Maintenance: guard}
	} else {
		c = &vmctl.Controller{Dynamic: client, Kube: kube, Network: prefix, StateDir: *stateDir, StableAddresses: *leaderElect, Maintenance: guard}
	}
	klog.Infof("controller API mode: %s", mode)
	go guard.Run(ctx)
	release, err := c.LockGateway()
	if err != nil {
		klog.Fatalf("gateway ownership: %v", err)
	}
	defer release()
	run := c.Run
	if *leaderElect {
		if *identity == "" {
			*identity, err = os.Hostname()
			if err != nil {
				klog.Fatalf("host name: %v", err)
			}
		}
		// A replacement process must not share the old process's identity:
		// both could otherwise renew the same Lease and run as its leader.
		holder := *identity + "_" + string(uuid.NewUUID())
		run = func(ctx context.Context) error {
			return runElected(ctx, kube, *namespace, holder, c.Run)
		}
	}
	if err := run(ctx); err != nil {
		klog.Fatal(err)
	}
}
