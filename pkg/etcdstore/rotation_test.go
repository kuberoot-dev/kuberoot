package etcdstore

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"net"
	"os"
	"testing"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/pki"
)

// newMember lays out a member's credentials as kinit does, in a temporary
// PKI directory.
func newMember(t *testing.T) *pki.Authority {
	t.Helper()
	PKIDir = t.TempDir()
	ca, certPEM, keyPEM, err := pki.NewAuthority("kuberoot-etcd-ca")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAuthority(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	for _, name := range Leaves {
		c, k, err := ca.Issue(pki.Spec{CommonName: name, DNSNames: []string{"cp-a"}, IPs: []net.IP{net.ParseIP("10.0.0.1")}, Server: name == Server})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, name+".crt", c)
		writeFile(t, name+".key", k)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	writeFile(t, "encryption.key", []byte(base64.StdEncoding.EncodeToString(key)+"\n"))
	return ca
}

func writeFile(t *testing.T, name string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path(name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func verifies(t *testing.T, leaf string, roots ...*x509.Certificate) bool {
	t.Helper()
	c, err := Certificate(leaf)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	for _, r := range roots {
		pool.AddCert(r)
	}
	_, err = c.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err == nil
}

func TestRotationOfTheAuthority(t *testing.T) {
	old := newMember(t)
	members := []string{"cp-a", "cp-b"}
	r, err := NewRotation("r1", true, old.Certificate(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	next, err := pki.ParseCertificates([]byte(r.NewCA))
	if err != nil {
		t.Fatal(err)
	}

	// Trusting: the new authority is trusted, the old one still signs.
	if r.Phase != PhaseTrusting {
		t.Fatalf("first phase %s", r.Phase)
	}
	if err := Trust(next...); err != nil {
		t.Fatal(err)
	}
	ca, trusted, err := LoadAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if !ca.Certificate().Equal(old.Certificate()) || len(trusted) != 1 || !trusted[0].Equal(next[0]) {
		t.Fatalf("trusting: signs %s, trusts %d more", ca.Certificate().Subject, len(trusted))
	}
	r.Completed = map[string]string{"cp-a": PhaseTrusting}
	if r.Advance(members, time.Now()) {
		t.Fatal("advanced with cp-b waiting")
	}
	r.Completed["cp-b"] = PhaseTrusting
	if !r.Advance(members, time.Now()) || r.Phase != PhaseReissuing {
		t.Fatalf("after trusting: %s", r.Phase)
	}

	// Reissuing: the new authority signs, the old is still trusted, and the
	// certificates come from the new one with the same names.
	keyPEM, err := Unseal(r.NewCAKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAuthority([]byte(r.NewCA), keyPEM, old.Certificate()); err != nil {
		t.Fatal(err)
	}
	ca, trusted, _ = LoadAuthority()
	if !ca.Certificate().Equal(next[0]) || len(trusted) != 1 || !trusted[0].Equal(old.Certificate()) {
		t.Fatal("reissuing: the new authority does not sign with the old trusted")
	}
	if err := ReissueLeaves(ca); err != nil {
		t.Fatal(err)
	}
	for _, leaf := range Leaves {
		if !verifies(t, leaf, next[0]) || verifies(t, leaf, old.Certificate()) {
			t.Fatalf("%s is not from the new authority", leaf)
		}
	}
	if c, _ := Certificate(Peer); c.DNSNames[0] != "cp-a" || !c.IPAddresses[0].Equal(net.ParseIP("10.0.0.1")) {
		t.Fatalf("names lost: %v %v", c.DNSNames, c.IPAddresses)
	}
	r.Completed = map[string]string{"cp-a": PhaseReissuing, "cp-b": PhaseReissuing}
	if !r.Advance(members, time.Now()) || r.Phase != PhaseDropping {
		t.Fatalf("after reissuing: %s", r.Phase)
	}

	// Dropping: only the new authority is left.
	if err := Trust(); err != nil {
		t.Fatal(err)
	}
	if ca, trusted, _ = LoadAuthority(); !ca.Certificate().Equal(next[0]) || len(trusted) != 0 {
		t.Fatal("dropping: the old authority is still trusted")
	}
	r.Completed = map[string]string{"cp-a": PhaseDropping, "cp-b": PhaseDropping}
	if !r.Advance(members, time.Now()) || r.Phase != PhaseDone || r.NewCAKey != "" || r.CompletedAt == nil {
		t.Fatalf("after dropping: %s, key kept: %t", r.Phase, r.NewCAKey != "")
	}
	if r.Active() || r.Advance(members, time.Now()) {
		t.Fatal("a done rotation is active")
	}
}

func TestRotationOfCertificates(t *testing.T) {
	ca := newMember(t)
	before, _ := Certificate(Server)
	r, err := NewRotation("r1", false, ca.Certificate(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Phase != PhaseReissuing || r.NewCA != "" || r.NewCAKey != "" {
		t.Fatalf("a rotation of certificates: %+v", r)
	}
	if err := ReissueLeaves(ca); err != nil {
		t.Fatal(err)
	}
	after, _ := Certificate(Server)
	if after.SerialNumber.Cmp(before.SerialNumber) == 0 || !verifies(t, Server, ca.Certificate()) {
		t.Fatal("not reissued by the same authority")
	}
	if after.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("usage lost: %v", after.ExtKeyUsage)
	}
	r.Completed = map[string]string{"cp-a": PhaseReissuing}
	if !r.Advance([]string{"cp-a"}, time.Now()) || r.Phase != PhaseDone {
		t.Fatalf("after reissuing: %s", r.Phase)
	}
}

func TestSealNeedsTheClusterKey(t *testing.T) {
	newMember(t)
	sealed, err := Seal([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := Unseal(sealed); err != nil || string(plain) != "secret" {
		t.Fatalf("unseal: %q %v", plain, err)
	}
	newMember(t) // another cluster's key
	if _, err := Unseal(sealed); err == nil {
		t.Fatal("unsealed with another key")
	}
}

func TestDueForRenewal(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := &x509.Certificate{NotBefore: start, NotAfter: start.AddDate(1, 0, 0)}
	if DueForRenewal(c, start.AddDate(0, 7, 0)) {
		t.Fatal("due with five months left")
	}
	if !DueForRenewal(c, start.AddDate(0, 9, 0)) {
		t.Fatal("not due with three months left")
	}
}
