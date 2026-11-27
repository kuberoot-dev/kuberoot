package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"testing"
	"time"
)

func testAuthority(t *testing.T) *authority {
	t.Helper()
	old := pkiDir
	pkiDir = t.TempDir()
	t.Cleanup(func() { pkiDir = old })
	a, err := loadOrCreateAuthority("test-ca", "test ca")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func spec(ip string) certSpec {
	return certSpec{file: "node-api", commonName: "n1", usages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		dnsNames: []string{"n1", "localhost"}, ips: []net.IP{net.ParseIP(ip), net.ParseIP("127.0.0.1")}}
}

func TestBootKeepsACurrentCertificate(t *testing.T) {
	a := testAuthority(t)
	if err := a.issue(spec("10.0.0.5")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(pkiPath("node-api.crt"))
	beforeInfo, _ := os.Stat(pkiPath("node-api.crt"))
	if err := a.issue(spec("10.0.0.5")); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(pkiPath("node-api.crt"))
	afterInfo, _ := os.Stat(pkiPath("node-api.crt"))
	if string(before) != string(after) || !os.SameFile(beforeInfo, afterInfo) {
		t.Fatal("a certificate that still matched was issued and written again")
	}
}

func TestBootReissuesWhatNoLongerMatches(t *testing.T) {
	a := testAuthority(t)
	if err := a.issue(spec("10.0.0.5")); err != nil {
		t.Fatal(err)
	}
	if !a.current(spec("10.0.0.5")) {
		t.Fatal("a fresh certificate is not current")
	}
	if a.current(spec("10.0.0.6")) {
		t.Error("a certificate for another address counts as current")
	}
	other := spec("10.0.0.5")
	other.dnsNames = []string{"n2", "localhost"}
	if a.current(other) {
		t.Error("a certificate for another name counts as current")
	}
	b := testAuthority(t)
	if err := b.issue(spec("10.0.0.5")); err != nil {
		t.Fatal(err)
	}
	if a.current(spec("10.0.0.5")) {
		t.Error("a certificate signed by another authority counts as current")
	}
}

func TestBootRenewsACertificateCloseToExpiry(t *testing.T) {
	a := testAuthority(t)
	s := spec("10.0.0.5")
	if err := a.issue(s); err != nil {
		t.Fatal(err)
	}
	// Re-sign the same certificate with a near expiry.
	raw, _ := os.ReadFile(pkiPath("node-api.crt"))
	c := mustParse(t, raw)
	key := mustKey(t)
	tmpl := *c
	tmpl.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, a.cert, key.Public(), a.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePEM("node-api.crt", "CERTIFICATE", der); err != nil {
		t.Fatal(err)
	}
	if err := writeKey("node-api.key", key); err != nil {
		t.Fatal(err)
	}
	if a.current(s) {
		t.Fatal("a certificate expiring tomorrow counts as current")
	}
}

func mustParse(t *testing.T, raw []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(raw)
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
