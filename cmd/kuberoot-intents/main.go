// Command kuberoot-intents translates intents into primitives and runs Overrides.
package main

import (
	"context"
	"flag"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/intents"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "credentials of the translator identity")
	interval := flag.Duration("interval", 5*time.Second, "how often intents are reconciled")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		klog.Fatal(err)
	}
	t := &intents.Translator{
		Dynamic: dynamic.NewForConfigOrDie(cfg),
		Client:  kubernetes.NewForConfigOrDie(cfg),
		Now:     time.Now,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), unix.SIGTERM, unix.SIGINT)
	defer cancel()
	t.Run(ctx, *interval)
}
