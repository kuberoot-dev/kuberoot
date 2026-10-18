package etcdstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/kuberoot-dev/kuberoot/pkg/atomicfile"
	"github.com/kuberoot-dev/kuberoot/pkg/pki"
)

// Phases of a rotation. Every member completes one before any starts the
// next, so no member ever presents a certificate another does not trust.
const (
	// PhaseTrusting: members trust the new authority beside the old one.
	PhaseTrusting = "Trusting"
	// PhaseReissuing: members take certificates issued anew, by the new
	// authority when the rotation replaces it.
	PhaseReissuing = "Reissuing"
	// PhaseDropping: members stop trusting the old authority.
	PhaseDropping = "Dropping"
	PhaseDone     = "Done"
)

// RotationKey is where the store keeps the rotation, beside the cluster's
// objects (under /registry), which the API server never reads past.
const RotationKey = "/kuberoot/etcd-rotation"

// Leaves are the credentials the store's authority issues on each member.
var Leaves = []string{Server, Peer, APIServer, Admin}

// Rotation replaces the certificates of the store's members, and with
// Authority the authority that issues them, one member at a time.
type Rotation struct {
	Name      string `json:"name"`
	Authority bool   `json:"authority,omitempty"`
	Phase     string `json:"phase"`
	// Completed is the phase each member has completed, by member name.
	Completed map[string]string `json:"completed,omitempty"`
	// Messages say why a member has not completed the phase yet.
	Messages    map[string]string `json:"messages,omitempty"`
	StartedAt   time.Time         `json:"startedAt"`
	CompletedAt *time.Time        `json:"completedAt,omitempty"`
	// OldCA and NewCA are the authorities' certificates in PEM; NewCAKey is
	// the new one's key, sealed with the cluster's encryption key, and kept
	// only until every member holds it.
	OldCA    string `json:"oldCA,omitempty"`
	NewCA    string `json:"newCA,omitempty"`
	NewCAKey string `json:"newCAKey,omitempty"`
}

// Phases a rotation goes through, in order.
func (r *Rotation) Phases() []string {
	if r.Authority {
		return []string{PhaseTrusting, PhaseReissuing, PhaseDropping}
	}
	return []string{PhaseReissuing}
}

// Active: not done yet.
func (r *Rotation) Active() bool { return r != nil && r.Phase != PhaseDone }

// Waiting lists the members that have not completed the current phase.
func (r *Rotation) Waiting(members []string) []string {
	var out []string
	for _, m := range members {
		if r.Completed[m] != r.Phase {
			out = append(out, m)
		}
	}
	return out
}

// Advance moves to the next phase once every member completed this one,
// and reports whether it did.
func (r *Rotation) Advance(members []string, now time.Time) bool {
	if !r.Active() || len(members) == 0 || len(r.Waiting(members)) > 0 {
		return false
	}
	phases := r.Phases()
	i := slices.Index(phases, r.Phase)
	if i < len(phases)-1 {
		r.Phase = phases[i+1]
		return true
	}
	r.Phase, r.Messages, r.NewCAKey = PhaseDone, nil, ""
	r.CompletedAt = &now
	return true
}

// NewRotation starts a rotation; one of the authority makes the new one now,
// from current, the authority every member holds.
func NewRotation(name string, authority bool, current *x509.Certificate, now time.Time) (*Rotation, error) {
	r := &Rotation{Name: name, Authority: authority, StartedAt: now}
	r.Phase = r.Phases()[0]
	if !authority {
		return r, nil
	}
	_, certPEM, keyPEM, err := pki.NewAuthority("kuberoot-etcd-ca")
	if err != nil {
		return nil, err
	}
	sealed, err := Seal(keyPEM)
	if err != nil {
		return nil, err
	}
	r.OldCA, r.NewCA, r.NewCAKey = string(pki.EncodeCertificates(current)), string(certPEM), sealed
	return r, nil
}

// LoadRotation reads the store's rotation and the revision it was written
// at; nil and 0 when there has been none.
func LoadRotation(ctx context.Context, c *clientv3.Client) (*Rotation, int64, error) {
	resp, err := c.Get(ctx, RotationKey)
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, nil
	}
	r := &Rotation{}
	if err := json.Unmarshal(resp.Kvs[0].Value, r); err != nil {
		return nil, 0, err
	}
	return r, resp.Kvs[0].ModRevision, nil
}

// SaveRotation writes the rotation if nobody wrote it since revision rev
// (0: there was none), and reports whether it did.
func SaveRotation(ctx context.Context, c *clientv3.Client, r *Rotation, rev int64) (bool, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	resp, err := c.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(RotationKey), "=", rev)).
		Then(clientv3.OpPut(RotationKey, string(raw))).Commit()
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

// DueForRenewal: a third of the certificate's validity or less is left.
func DueForRenewal(c *x509.Certificate, now time.Time) bool {
	return c.NotAfter.Sub(now) < c.NotAfter.Sub(c.NotBefore)/3
}

// LoadAuthority reads this member's authority: the certificate first in
// etcd-ca.crt signs, with the key in etcd-ca.key; the others after it are
// trusted with it while a rotation replaces it.
func LoadAuthority() (*pki.Authority, []*x509.Certificate, error) {
	certPEM, err := os.ReadFile(path(CA + ".crt"))
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(path(CA + ".key"))
	if err != nil {
		return nil, nil, err
	}
	ca, err := pki.ParseAuthority(certPEM, keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", CA, err)
	}
	certs, err := pki.ParseCertificates(certPEM)
	if err != nil {
		return nil, nil, err
	}
	return ca, certs[1:], nil
}

// WriteAuthority makes the authority in certPEM and keyPEM the one that
// signs, trusted with others. etcd, its clients and kinit read the file
// as they are: the signing certificate first, then the rest of the trust.
func WriteAuthority(certPEM, keyPEM []byte, others ...*x509.Certificate) error {
	ca, err := pki.ParseAuthority(certPEM, keyPEM)
	if err != nil {
		return err
	}
	trust := []*x509.Certificate{ca.Certificate()}
	for _, o := range others {
		if !slices.ContainsFunc(trust, func(t *x509.Certificate) bool { return t.Equal(o) }) {
			trust = append(trust, o)
		}
	}
	// The key first: whatever reads the pair in between finds the old
	// certificate's key gone only for as long as one rename takes.
	if err := atomicfile.WriteFile(path(CA+".key"), keyPEM, 0o600); err != nil {
		return err
	}
	return atomicfile.WriteFile(path(CA+".crt"), pki.EncodeCertificates(trust...), 0o600)
}

// Trust keeps the authority that signs and trusts others beside it, or
// only it when there are none.
func Trust(others ...*x509.Certificate) error {
	ca, _, err := LoadAuthority()
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(path(CA + ".key"))
	if err != nil {
		return err
	}
	return WriteAuthority(pki.EncodeCertificates(ca.Certificate()), keyPEM, others...)
}

// ReissueLeaves issues this member's credentials again from ca, to the same
// names and for the same uses. etcd and its clients read their certificates
// on every new connection, so the new ones take effect without a restart.
func ReissueLeaves(ca *pki.Authority) error {
	for _, name := range Leaves {
		old, err := Certificate(name)
		if err != nil {
			return err
		}
		certPEM, keyPEM, err := ca.Reissue(old)
		if err != nil {
			return fmt.Errorf("reissue %s: %w", name, err)
		}
		if err := atomicfile.WriteFile(path(name+".key"), keyPEM, 0o600); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(path(name+".crt"), certPEM, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Certificate reads one of this member's credentials' certificate.
func Certificate(name string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path(name + ".crt"))
	if err != nil {
		return nil, err
	}
	certs, err := pki.ParseCertificates(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return certs[0], nil
}

// Seal encrypts with the cluster's encryption key, which every control plane
// holds the same: what only control planes may read can wait in the store.
func Seal(plain []byte) (string, error) {
	aead, err := sealer()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, []byte(RotationKey))), nil
}

// Unseal decrypts what Seal encrypted.
func Unseal(sealed string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	aead, err := sealer()
	if err != nil {
		return nil, err
	}
	if len(raw) < aead.NonceSize() {
		return nil, errors.New("sealed data too short")
	}
	return aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(RotationKey))
}

func sealer() (cipher.AEAD, error) {
	encoded, err := os.ReadFile(path("encryption.key"))
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("encryption.key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
