package vmctl

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	v1 "github.com/tym83/kuberoot/pkg/apis/vm/v1alpha1"
)

// SeedPort is where the machines' gateway serves cloud-init's data.
const SeedPort = 8091

// seedBase is the URL machines fetch their cloud-init data under, set once
// the gateway's address is known; empty, machines get none.
var seedBase atomic.Value // string

// systemSerial points cloud-init in a machine to its data at the gateway:
// the NoCloud data source, read from the machine's DMI serial number.
func systemSerial(m *v1.VirtualMachine) string {
	base, _ := seedBase.Load().(string)
	if base == "" || (m.Spec.UserData == "" && m.Spec.UserDataSecret == nil) {
		return ""
	}
	return fmt.Sprintf("ds=nocloud;s=%s/%s/", base, m.Name)
}

// serveSeeds serves each machine its cloud-init data: meta-data, user-data
// and an empty vendor-data, to the machine's own address and nobody else's,
// as user data may hold credentials.
func (c *Controller) serveSeeds(ctx context.Context, gw string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name, file, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		u, err := c.Dynamic.Resource(vmsGVR).Get(r.Context(), name, metav1.GetOptions{})
		if err != nil {
			http.NotFound(w, r)
			return
		}
		var m v1.VirtualMachine
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &m); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if m.Status.MAC == "" || c.leases()[strings.ToLower(m.Status.MAC)] != host {
			klog.Warningf("cloud-init data of %s refused to %s: not the machine's address", name, host)
			http.Error(w, "not this machine's", http.StatusForbidden)
			return
		}
		switch file {
		case "meta-data":
			fmt.Fprintf(w, "instance-id: %s\nlocal-hostname: %s\n", m.UID, m.Name)
		case "vendor-data":
		case "user-data":
			data, err := c.userData(r.Context(), &m)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(data))
		default:
			http.NotFound(w, r)
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	addr := net.JoinHostPort(gw, fmt.Sprint(SeedPort))
	for ctx.Err() == nil {
		// The address comes with the bridge, which the gateway sets up.
		l, err := net.Listen("tcp", addr)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		seedBase.Store("http://" + addr)
		klog.Infof("serving cloud-init data at http://%s", addr)
		if err := srv.Serve(l); err != nil && ctx.Err() == nil {
			klog.Errorf("cloud-init data: %v", err)
		}
		return
	}
}

// userData is the machine's user data: its own, or its Secret's.
func (c *Controller) userData(ctx context.Context, m *v1.VirtualMachine) (string, error) {
	if ref := m.Spec.UserDataSecret; ref != nil {
		s, err := c.Kube.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		return string(s.Data["userData"]), nil
	}
	return m.Spec.UserData, nil
}
