package wsctl

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net"
	"sync"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/pki"
)

// Certificates is the gateway's serving certificate: one from the Secret
// workstation/gateway-tls when there is one, otherwise one the cluster's CA
// issues for the gateway's names, renewed before it runs out.
type Certificates struct {
	Authority *pki.Authority
	Names     []string
	IPs       []net.IP

	mu       sync.Mutex
	issued   *tls.Certificate
	custom   *tls.Certificate
	customPE []byte
}

// SetCustom uses the Secret's certificate and key, or, given none, goes
// back to the issued one.
func (c *Certificates) SetCustom(certPEM, keyPEM []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(certPEM) == 0 {
		c.custom, c.customPE = nil, nil
		return nil
	}
	pe := append(append([]byte{}, certPEM...), keyPEM...)
	if bytes.Equal(pe, c.customPE) {
		return nil
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	c.custom, c.customPE = &pair, pe
	return nil
}

// GetCertificate serves as tls.Config.GetCertificate.
func (c *Certificates) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.custom != nil {
		return c.custom, nil
	}
	if c.issued != nil && time.Until(c.issued.Leaf.NotAfter) > 30*24*time.Hour {
		return c.issued, nil
	}
	certPEM, keyPEM, err := c.Authority.Issue(pki.Spec{CommonName: "kuberoot-wsctl", DNSNames: c.Names, IPs: c.IPs, Server: true})
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
		return nil, err
	}
	c.issued = &pair
	return c.issued, nil
}
