// Command cluster-api-provider-kuberoot runs the native Kuberoot CAPI
// infrastructure provider in the management cluster.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/capikuberoot"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "management kubeconfig; omitted uses the Pod service account")
	contextName := flag.String("context", "", "explicit context for an external kubeconfig")
	namespace := flag.String("namespace", "", "namespace to reconcile; empty watches all namespaces")
	period := flag.Duration("poll-interval", 5*time.Second, "authoritative reconciliation interval")
	leaderElect := flag.Bool("leader-elect", true, "elect a single active infrastructure controller")
	leaderNamespace := flag.String("leader-election-namespace", "kube-system", "namespace of the controller Lease")
	identity := flag.String("identity", "", "replica identity prefix; defaults to host name")
	healthAddress := flag.String("health-address", ":8081", "HTTP health and readiness listener")
	klog.InitFlags(nil)
	flag.Parse()
	if *period <= 0 || *leaderNamespace == "" {
		klog.Fatal("poll interval must be positive and Lease namespace must be nonempty")
	}
	var config *rest.Config
	var err error
	if *kubeconfig == "" {
		if *contextName != "" {
			klog.Fatal("--context requires --kubeconfig")
		}
		config, err = rest.InClusterConfig()
	} else {
		if *contextName == "" {
			klog.Fatal("--kubeconfig requires explicit --context")
		}
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfig}, &clientcmd.ConfigOverrides{CurrentContext: *contextName}).ClientConfig()
	}
	if err != nil {
		klog.Fatal("management API credentials could not be loaded")
	}
	config.Timeout = 20 * time.Second
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatal("management API client could not be created")
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		klog.Fatal("management dynamic client could not be created")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", *healthAddress)
	if err != nil {
		klog.Fatalf("health listener: %v", err)
	}
	mux := http.NewServeMux()
	probe := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if ctx.Err() != nil {
			http.Error(w, "stopping", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintln(w, "ok")
	}
	mux.HandleFunc("/healthz", probe)
	mux.HandleFunc("/readyz", probe)
	health := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	healthErr := make(chan error, 1)
	go func() {
		err := health.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			healthErr <- err
			stop()
		}
	}()
	controller := &capikuberoot.Controller{Dynamic: dyn, Kube: kube, Namespace: *namespace, PollInterval: *period}
	if *leaderElect {
		if *identity == "" {
			*identity, err = os.Hostname()
			if err != nil {
				klog.Fatalf("host identity: %v", err)
			}
		}
		err = runElected(ctx, kube, *leaderNamespace, *identity+"_"+string(uuid.NewUUID()), controller.Run)
	} else {
		err = controller.Run(ctx)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = errors.Join(err, health.Shutdown(shutdown))
	select {
	case serveErr := <-healthErr:
		err = errors.Join(err, serveErr)
	default:
	}
	if err != nil {
		klog.Fatal(err)
	}
}
