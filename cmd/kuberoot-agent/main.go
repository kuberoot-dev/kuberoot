// Command kuberoot-agent is the operator agent of an ai cluster: it looks
// for problems, asks a language model what to do about each, and proposes
// the answer as a Remedy for a person, or the Agent's policy, to approve.
// Its credentials read the cluster and create remedies; nothing more.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/agent"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "the agent's credentials for the cluster's API server")
	interval := flag.Duration("interval", time.Minute, "how often to look for problems")
	quiet := flag.Duration("quiet", 30*time.Minute, "how long a problem with a remedy is left be")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		klog.Fatalf("kubeconfig: %v", err)
	}
	cfg.Timeout = 2 * time.Minute
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	kube := kubernetes.NewForConfigOrDie(cfg)
	dyn := dynamic.NewForConfigOrDie(cfg)
	a := &agent.Agent{Dynamic: dyn, Kube: kube, Interval: *interval, Quiet: *quiet, PerCheck: 3,
		Collector: &agent.Collector{Dynamic: dyn, Kube: kube, Raw: kube.CoreV1().RESTClient()}}
	if err := a.Run(ctx); err != nil {
		klog.Fatal(err)
	}
}
