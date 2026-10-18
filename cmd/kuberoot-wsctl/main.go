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

	"github.com/kuberoot-dev/kuberoot/pkg/pki"
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
	gw := &wsctl.Gateway{NoVNC: *novnc, Advertise: *advertise}
	// The gateway speaks only TLS: a plain HTTP request gets an error, not
	// a desktop.
	srv := &http.Server{Addr: fmt.Sprintf(":%d", *port), Handler: gw, ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.GetCertificate}}
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
	c := &wsctl.Controller{Dynamic: dynamic.NewForConfigOrDie(cfg), Kube: kubernetes.NewForConfigOrDie(cfg),
		Gateway: gw, Advertise: *advertise, HTTPPort: *port, Certificates: certs}
	if err := c.Run(ctx); err != nil {
		klog.Fatal(err)
	}
}
