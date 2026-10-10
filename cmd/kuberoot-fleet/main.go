// Command kuberoot-fleet is the center of a fleet of kuberoot nodes: it takes
// the connections the nodes open to it, keeps FleetNode and FleetRollout
// resources in its cluster, and upgrades the nodes through their
// connections. It runs on any cluster; on kuberoot, the edge control plane
// runs it when booted with kuberoot.fleet-center.
package main

import (
	"context"
	"crypto/x509"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/fleet"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "credentials for the center's cluster")
	listen := flag.String("listen", ":7443", "where nodes connect")
	servers := flag.String("servers", "", "the center's addresses as nodes reach it, host:port, comma separated; enrollments carry them")
	namespace := flag.String("namespace", "kube-system", "namespace of the center's authority and the enrollment Secrets")
	userCA := flag.String("user-client-ca-file", "", "CA of the center cluster's client certificates, for the node API proxy at /nodes/<name>/")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		klog.Fatalf("kubeconfig: %v", err)
	}
	kube := kubernetes.NewForConfigOrDie(cfg)
	dyn := dynamic.NewForConfigOrDie(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The cluster may still be coming up.
	for {
		err := fleet.ApplyCRDs(ctx, dyn)
		if err == nil {
			break
		}
		klog.Errorf("fleet API: %v", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	ca, caPEM, err := fleet.LoadOrCreateCA(ctx, kube, *namespace)
	if err != nil {
		klog.Fatalf("center authority: %v", err)
	}
	cert, err := fleet.ServingCert(ca, caPEM)
	if err != nil {
		klog.Fatalf("serving certificate: %v", err)
	}
	center := &fleet.Center{Dyn: dyn, Kube: kube, Namespace: *namespace, CAHash: fleet.Hash(ca.Certificate())}
	for _, s := range strings.Split(*servers, ",") {
		if s = strings.TrimSpace(s); s != "" {
			center.Servers = append(center.Servers, s)
		}
	}
	hub := &fleet.Hub{Cert: cert, Identity: center.Identity, Enroll: center.Enroll, Changed: center.Changed, Authorize: center.Authorize}
	if *userCA != "" {
		raw, err := os.ReadFile(*userCA)
		if err != nil {
			klog.Fatalf("user CA: %v", err)
		}
		hub.UserCAs = x509.NewCertPool()
		hub.UserCAs.AppendCertsFromPEM(raw)
	}
	center.Hub = hub
	klog.Infof("fleet center %s on %s, enrollments name %v", center.CAHash, *listen, center.Servers)
	go center.Run(ctx)
	if err := hub.Serve(ctx, *listen); err != nil {
		klog.Fatal(err)
	}
}
