package devices

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	v1 "github.com/tym83/kuberoot/pkg/apis/devices/v1alpha1"
)

// reader reads a device once per poll. A probe that finds its service
// down still returns what it measured, with the error.
type reader interface {
	Read() (Reading, error)
	Close()
}

// Credentials gives a device's credentials when it connects, read anew
// each time, so a changed Secret takes effect on the next connection.
type Credentials func() (map[string]string, error)

// newReader makes the reader of a device's protocol.
func newReader(spec v1.DeviceSpec, creds Credentials) reader {
	switch spec.Protocol {
	case "snmp":
		return &snmpDevice{spec: spec, creds: creds}
	case "redfish":
		return &redfishDevice{spec: spec, creds: creds, client: httpClient(spec)}
	case "http", "tcp", "icmp":
		return &probe{spec: spec, client: httpClient(spec)}
	}
	return newModbusDevice(spec)
}

func timeout(spec v1.DeviceSpec) time.Duration {
	if spec.Timeout.Duration > 0 {
		return spec.Timeout.Duration
	}
	return 2 * time.Second
}

func httpClient(spec v1.DeviceSpec) *http.Client {
	insecure := spec.TLS != nil && spec.TLS.InsecureSkipVerify
	return &http.Client{
		Timeout: timeout(spec),
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec // the device's own choice
			// Probes measure the service, not a connection kept from before.
			DisableKeepAlives: spec.Protocol == "http",
		},
	}
}

var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// SecretCredentials reads a device's credentials from its Secret.
func SecretCredentials(client dynamic.Interface, ref *v1.SecretRef) Credentials {
	return func() (map[string]string, error) {
		if ref == nil {
			return map[string]string{}, nil
		}
		ns := ref.Namespace
		if ns == "" {
			ns = "kube-system"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		u, err := client.Resource(secretsGVR).Namespace(ns).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("credentials %s/%s: %w", ns, ref.Name, err)
		}
		return secretData(u)
	}
}

func secretData(u *unstructured.Unstructured) (map[string]string, error) {
	out := map[string]string{}
	data, _, _ := unstructured.NestedStringMap(u.Object, "data")
	for k, v := range data {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("credentials: key %s: %w", k, err)
		}
		out[k] = string(b)
	}
	strs, _, _ := unstructured.NestedStringMap(u.Object, "stringData")
	for k, v := range strs {
		out[k] = v
	}
	return out, nil
}
