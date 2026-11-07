package vmctl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha2"
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
	protocol := "nocloud"
	if m.Spec.SeedMode == v1.SeedModeNoCloudNet {
		protocol = "nocloud-net"
	}
	return fmt.Sprintf("ds=%s;s=%s/%s/", protocol, base, backend(m))
}

// serveSeeds serves each machine its cloud-init data: meta-data, user-data
// and an empty vendor-data, to the machine's own address and nobody else's,
// as user data may hold credentials.
func (c *Controller) serveSeeds(ctx context.Context, gw string) error {
	addr := net.JoinHostPort(gw, fmt.Sprint(SeedPort))
	for ctx.Err() == nil {
		// The address comes with the bridge, which the gateway sets up.
		l, err := net.Listen("tcp", addr)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
				continue
			}
		}
		klog.Infof("serving cloud-init data at http://%s", addr)
		return serveSeedHTTP(ctx, l, c.Maintenance.Handler(c.seedHandler()))
	}
	return nil
}

func (c *Controller) seedHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name, file, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		// Look up the globally unique backend path, never a tenant-supplied
		// name. Imports retain their original path and DMI serial.
		list, err := c.Dynamic.Resource(vmsGVR).List(r.Context(), metav1.ListOptions{})
		if err != nil {
			http.Error(w, "machine lookup unavailable", http.StatusServiceUnavailable)
			return
		}
		var m *v1.VirtualMachine
		for _, u := range list.Items {
			var candidate v1.VirtualMachine
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &candidate); err != nil {
				http.Error(w, "invalid machine binding", http.StatusServiceUnavailable)
				return
			}
			if backend(&candidate) != name {
				continue
			}
			if m != nil {
				http.Error(w, "ambiguous machine binding", http.StatusServiceUnavailable)
				return
			}
			m = &candidate
		}
		if m == nil || m.Namespace == "" || m.Status.InstanceID == "" {
			http.NotFound(w, r)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		address := c.leases()[strings.ToLower(m.Status.MAC)]
		if c.StableAddresses {
			address = m.Status.Address
		}
		if m.Status.MAC == "" || address != host {
			klog.Warningf("cloud-init data of %s refused to %s: not the machine's address", name, host)
			http.Error(w, "not this machine's", http.StatusForbidden)
			return
		}
		switch file {
		case "meta-data":
			fmt.Fprintf(w, "instance-id: %s\nlocal-hostname: %s\n", m.Status.InstanceID, m.Name)
		case "vendor-data":
		case "network-config":
			if m.Spec.SeedMode != v1.SeedModeNoCloudNet {
				http.NotFound(w, r)
				return
			}
			mac, err := net.ParseMAC(m.Status.MAC)
			if err != nil || len(mac) != 6 {
				http.Error(w, "invalid machine MAC", http.StatusServiceUnavailable)
				return
			}
			// Both cloud-init and Talos accept plain network-config v2. Match
			// by the bound NIC's MAC; interface names vary between guest OSes.
			fmt.Fprintf(w, "version: 2\nethernets:\n  vm:\n    match:\n      macaddress: %q\n    dhcp4: true\n", mac.String())
		case "user-data":
			data, err := c.userData(r.Context(), m)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(data))
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

// serveSeedHTTP closes the listener, cancels in-flight API calls through
// each request's base context, and waits for the handlers before return.
func serveSeedHTTP(ctx context.Context, listener net.Listener, handler http.Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	stopped := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shut, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		err := srv.Shutdown(shut)
		if err != nil {
			_ = srv.Close()
		}
		stopped <- err
	}()
	err := srv.Serve(listener)
	cancel()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, <-stopped)
}

// userData is the machine's user data: its own, or its Secret's.
func (c *Controller) userData(ctx context.Context, m *v1.VirtualMachine) (string, error) {
	if ref := m.Spec.UserDataSecret; ref != nil {
		s, err := c.Kube.CoreV1().Secrets(m.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		return string(s.Data["userData"]), nil
	}
	return m.Spec.UserData, nil
}
