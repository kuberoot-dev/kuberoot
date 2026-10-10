// Command kuberoot-aictl runs an ai cluster's language models: it places
// them on nodes, rolls new versions out behind a first node that must
// answer, and serves every model at one OpenAI-compatible endpoint.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/aictl"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "credentials for the cluster's API server")
	listen := flag.String("listen", ":8000", "address of the OpenAI-compatible endpoint")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		klog.Fatalf("kubeconfig: %v", err)
	}
	cfg.Timeout = time.Minute
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gw := &aictl.Gateway{}
	srv := &http.Server{Addr: *listen, Handler: gw, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			klog.Fatalf("endpoint: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	c := &aictl.Controller{Dynamic: dynamic.NewForConfigOrDie(cfg), Kube: kubernetes.NewForConfigOrDie(cfg), Gateway: gw}
	if err := c.Run(ctx); err != nil {
		klog.Fatal(err)
	}
}
