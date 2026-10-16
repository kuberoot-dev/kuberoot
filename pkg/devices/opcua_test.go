package devices

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
)

// selfSigned is a certificate and key in PEM, with the application URI
// OPC UA expects in it.
func selfSigned(t *testing.T, uri string) (certPEM, keyPEM string, der []byte, key *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(uri)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: uri},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment |
			x509.KeyUsageContentCommitment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true, IsCA: true,
		URIs: []*url.URL{u}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return certPEM, keyPEM, der, key
}

type opcuaServer struct {
	addr string
	ns   *server.MapNamespace
	der  []byte
	pem  string
}

// startOPCUA serves a map of values over OPC UA, with no security, or
// with Basic256Sha256 and its own certificate.
func startOPCUA(t *testing.T, secure bool, values map[string]any) *opcuaServer {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(freePort(t))
	port, _ := strconv.Atoi(portStr)
	opts := []server.Option{server.EndPoint(host, port),
		server.EnableAuthMode(ua.UserTokenTypeAnonymous), server.EnableAuthMode(ua.UserTokenTypeUserName)}
	s := &opcuaServer{addr: fmt.Sprintf("opc.tcp://%s:%d", host, port)}
	if secure {
		var key *rsa.PrivateKey
		s.pem, _, s.der, key = selfSigned(t, "urn:test:plc")
		opts = append(opts, server.EnableSecurity("Basic256Sha256", ua.MessageSecurityModeSignAndEncrypt),
			server.EnableSecurity("Basic256Sha256", ua.MessageSecurityModeSign),
			server.Certificate(s.der), server.PrivateKey(key))
	} else {
		opts = append(opts, server.EnableSecurity("None", ua.MessageSecurityModeNone))
	}
	srv := server.New(opts...)
	s.ns = server.NewMapNamespace(srv, "plant")
	for k, v := range values {
		s.ns.Data[k] = v
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return s
}

func (s *opcuaServer) node(key string) string { return fmt.Sprintf("ns=%d;s=%s", s.ns.ID(), key) }

func opcuaSpec(s *opcuaServer, set *v1.OPCUA) v1.DeviceSpec {
	return v1.DeviceSpec{Protocol: "opc-ua", Address: s.addr, OPCUA: set,
		Every: metav1.Duration{Duration: 100 * time.Millisecond}, Timeout: metav1.Duration{Duration: 3 * time.Second},
		Points: []v1.Point{
			{Name: "temperature", NodeID: s.node("Temperature"), Scale: "0.1"},
			{Name: "count", NodeID: s.node("Count")},
			{Name: "running", NodeID: s.node("Running"), Type: "bool"},
		}}
}

func noCreds() (map[string]string, error) { return map[string]string{}, nil }

func TestOPCUARead(t *testing.T) {
	s := startOPCUA(t, false, map[string]any{"Temperature": 853.0, "Count": int32(42), "Running": true})
	d := &opcuaDevice{spec: opcuaSpec(s, nil), creds: noCreds}
	defer d.Close()
	r, err := d.Read()
	if err != nil {
		t.Fatal(err)
	}
	if r["temperature"] != 85.3 || r["count"] != 42 || r["running"] != 1 {
		t.Fatalf("read %v", r)
	}
	s.ns.SetValue("Temperature", 901.0)
	if r, err = d.Read(); err != nil || r["temperature"] != 90.1 {
		t.Fatalf("after a change: %v, %v", r, err)
	}
}

func TestOPCUASubscribe(t *testing.T) {
	s := startOPCUA(t, false, map[string]any{"Temperature": 700.0, "Count": int32(1), "Running": false})
	d := &opcuaDevice{spec: opcuaSpec(s, &v1.OPCUA{Subscribe: true}), creds: noCreds}
	defer d.Close()
	r := waitReading(t, d, func(r Reading) bool { return r["temperature"] == 70 })
	if r["running"] != 0 || r["count"] != 1 {
		t.Fatalf("subscribed: %v", r)
	}
	s.ns.SetValue("Temperature", 853.0)
	waitReading(t, d, func(r Reading) bool { return r["temperature"] == 85.3 })
}

// waitReading reads until the reading is as wanted.
func waitReading(t *testing.T, d reader, ok func(Reading) bool) Reading {
	t.Helper()
	var r Reading
	var err error
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if r, err = d.Read(); err == nil && ok(r) {
			return r
		}
	}
	t.Fatalf("last read %v, %v", r, err)
	return nil
}

func TestOPCUAMissingNode(t *testing.T) {
	s := startOPCUA(t, false, map[string]any{"Temperature": 1.0, "Count": int32(1), "Running": true})
	spec := opcuaSpec(s, nil)
	spec.Points = append(spec.Points, v1.Point{Name: "pressure", NodeID: s.node("Pressure")})
	d := &opcuaDevice{spec: spec, creds: noCreds}
	defer d.Close()
	if _, err := d.Read(); err == nil || !strings.Contains(err.Error(), "pressure") {
		t.Fatalf("a node the server has not: %v", err)
	}
}

func TestOPCUASecure(t *testing.T) {
	s := startOPCUA(t, true, map[string]any{"Temperature": 853.0, "Count": int32(7), "Running": true})
	clientCert, clientKey, _, _ := selfSigned(t, "urn:kuberoot:devices")
	otherCert, _, _, _ := selfSigned(t, "urn:test:other")
	creds := func(extra map[string]string) Credentials {
		return func() (map[string]string, error) {
			m := map[string]string{"tls.crt": clientCert, "tls.key": clientKey, "username": "op", "password": "secret"}
			for k, v := range extra {
				m[k] = v
			}
			return m, nil
		}
	}
	set := &v1.OPCUA{SecurityPolicy: "Basic256Sha256", SecurityMode: "SignAndEncrypt"}

	d := &opcuaDevice{spec: opcuaSpec(s, set), creds: creds(map[string]string{"server.crt": s.pem})}
	r, err := d.Read()
	d.Close()
	if err != nil || r["temperature"] != 85.3 {
		t.Fatalf("pinned server: %v, %v", r, err)
	}

	d = &opcuaDevice{spec: opcuaSpec(s, set), creds: creds(map[string]string{"ca.crt": s.pem})}
	_, err = d.Read()
	d.Close()
	if err != nil {
		t.Fatalf("server issued by the CA: %v", err)
	}

	for name, extra := range map[string]map[string]string{
		"another pinned": {"server.crt": otherCert},
		"another CA":     {"ca.crt": otherCert},
		"nothing":        {},
	} {
		d = &opcuaDevice{spec: opcuaSpec(s, set), creds: creds(extra)}
		_, err = d.Read()
		d.Close()
		if err == nil {
			t.Fatalf("%s: a server not trusted was read", name)
		}
	}

	spec := opcuaSpec(s, set)
	spec.TLS = &v1.TLS{InsecureSkipVerify: true}
	d = &opcuaDevice{spec: spec, creds: creds(nil)}
	_, err = d.Read()
	d.Close()
	if err != nil {
		t.Fatalf("insecureSkipVerify: %v", err)
	}

	d = &opcuaDevice{spec: opcuaSpec(s, set), creds: func() (map[string]string, error) {
		return map[string]string{"server.crt": s.pem}, nil
	}}
	if _, err = d.Read(); err == nil || !strings.Contains(err.Error(), "tls.crt") {
		t.Fatalf("no client certificate: %v", err)
	}
	d.Close()

	d = &opcuaDevice{spec: opcuaSpec(s, &v1.OPCUA{SecurityPolicy: "None"}), creds: noCreds}
	if _, err = d.Read(); err == nil {
		t.Fatal("a server with no None endpoint was read without security")
	}
	d.Close()
}

func TestOPCUAServerDown(t *testing.T) {
	s := startOPCUA(t, false, map[string]any{"Temperature": 1.0, "Count": int32(1), "Running": true})
	spec := opcuaSpec(s, nil)
	spec.Address = "opc.tcp://" + freePort(t)
	d := &opcuaDevice{spec: spec, creds: noCreds}
	defer d.Close()
	if _, err := d.Read(); err == nil {
		t.Fatal("read a server that is not there")
	}
}
