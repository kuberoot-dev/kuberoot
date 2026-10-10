// Package fleet manages many independent kuberoot nodes from one central
// cluster. Each node calls the center and keeps the connection open (the
// agent, in kuberoot-node); the center (kuberoot-fleet) reaches the node API
// through that connection, keeps the inventory as FleetNode resources and
// moves releases across the fleet in waves with FleetRollout.
//
// Both ends are pinned. The node trusts only a center whose certificate
// authority hashes to what its enrollment names; the center trusts only a
// node whose client certificate was issued, for the node's name, by the
// node's own authority, pinned when the node enrolled with its one-time token.
package fleet

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// ServerName is the name the center's serving certificate carries: nodes
// verify it, whatever address they reach the center at, NAT included.
const ServerName = "kuberoot-fleet"

// TunnelProto is the protocol the node's connection is upgraded to.
const TunnelProto = "kuberoot-fleet-tunnel"

// Hash is the pin of a certificate: sha256 of its DER, as sha256:<hex>.
func Hash(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// HashPEM pins the first certificate of a PEM bundle.
func HashPEM(raw []byte) (string, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", errors.New("no PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	return Hash(cert), nil
}

// verifyPinned checks a certificate chain as presented in a TLS handshake:
// one of its certificates must hash to pin and be a CA, and the leaf must
// chain to it for usage. It returns the leaf.
func verifyPinned(rawCerts [][]byte, pin string, usage x509.ExtKeyUsage) (*x509.Certificate, error) {
	if len(rawCerts) == 0 {
		return nil, errors.New("no certificate")
	}
	var certs []*x509.Certificate
	for _, raw := range rawCerts {
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return nil, err
		}
		certs = append(certs, c)
	}
	roots := x509.NewCertPool()
	found := false
	for _, c := range certs[1:] {
		if strings.EqualFold(Hash(c), pin) && c.IsCA {
			roots.AddCert(c)
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("no certificate authority with the pinned hash %s", pin)
	}
	if _, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
		return nil, err
	}
	return certs[0], nil
}
