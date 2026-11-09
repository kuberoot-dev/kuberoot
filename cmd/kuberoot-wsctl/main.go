// Command kuberoot-wsctl runs a workstation cluster's workspaces: a desktop
// in a virtual machine for each, and the gateway that opens it in a browser
// or for an RDP client.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/internal/legacywsctl"
	"github.com/kuberoot-dev/kuberoot/pkg/distro"
	"github.com/kuberoot-dev/kuberoot/pkg/pki"
	"github.com/kuberoot-dev/kuberoot/pkg/vmmaintenance"
	"github.com/kuberoot-dev/kuberoot/pkg/wsctl"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "credentials for the cluster's API server")
	advertise := flag.String("advertise-address", "", "the address people reach the gateway at")
	port := flag.Int("port", 6080, "the gateway's port for browsers")
	novnc := flag.String("novnc", "/usr/share/kuberoot/novnc", "the directory noVNC is served from")
	caCert := flag.String("tls-ca-cert-file", "", "the cluster CA's certificate, which issues the gateway's own unless the Secret workstation/gateway-tls gives one")
	caKey := flag.String("tls-ca-key-file", "", "the cluster CA's key")
	sans := flag.String("tls-san", "", "more names and addresses for the gateway's certificate, comma-separated")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		klog.Fatalf("kubeconfig: %v", err)
	}
	cfg.Timeout = time.Minute
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ca, err := pki.LoadAuthority(*caCert, *caKey)
	if err != nil {
		klog.Fatalf("cluster CA: %v", err)
	}
	certs := &wsctl.Certificates{Authority: ca, Names: []string{"localhost"}, IPs: []net.IP{net.IPv4(127, 0, 0, 1)}}
	if host, err := os.Hostname(); err == nil {
		certs.Names = append(certs.Names, host)
	}
	for _, n := range append(strings.Split(*sans, ","), *advertise) {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if ip := net.ParseIP(n); ip != nil {
			certs.IPs = append(certs.IPs, ip)
		} else {
			certs.Names = append(certs.Names, n)
		}
	}
	kube, client := kubernetes.NewForConfigOrDie(cfg), dynamic.NewForConfigOrDie(cfg)
	mode, err := vmmaintenance.WaitAPIMode(ctx, client)
	if err != nil {
		klog.Fatalf("VM API startup selection: %v", err)
	}
	if mode == vmmaintenance.ModeAlpha1 && !distro.VMAPITransitionEnabled() {
		klog.Fatal("legacy VM API requires an explicitly built transition image")
	}
	guard, err := vmmaintenance.New(ctx, kube, client, "kuberoot-wsctl", mode)
	if err != nil {
		klog.Fatalf("maintenance barrier: %v", err)
	}
	var handler http.Handler
	var run func(context.Context) error
	var getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	if mode == vmmaintenance.ModeAlpha1 {
		legacyCerts := &legacywsctl.Certificates{Authority: certs.Authority, Names: certs.Names, IPs: certs.IPs}
		gw := &legacywsctl.Gateway{NoVNC: *novnc, Advertise: *advertise}
		controller := &legacywsctl.Controller{Dynamic: client, Kube: kube, Gateway: gw, Advertise: *advertise, HTTPPort: *port, Certificates: legacyCerts, Maintenance: guard}
		handler, run, getCertificate = gw, controller.Run, legacyCerts.GetCertificate
	} else {
		gw := &wsctl.Gateway{NoVNC: *novnc, Advertise: *advertise}
		controller := &wsctl.Controller{Dynamic: client, Kube: kube, Gateway: gw, Advertise: *advertise, HTTPPort: *port, Certificates: certs, Maintenance: guard}
		handler, run, getCertificate = gw, controller.Run, certs.GetCertificate
	}
	klog.Infof("controller API mode: %s", mode)
	go guard.Run(ctx)
	// The gateway speaks only TLS: a plain HTTP request gets an error, not
	// a desktop.
	srv := &http.Server{Addr: fmt.Sprintf(":%d", *port), Handler: handler, ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: getCertificate}}
	go func() {
		if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			klog.Fatalf("gateway: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	if err := run(ctx); err != nil {
		klog.Fatal(err)
	}
}
