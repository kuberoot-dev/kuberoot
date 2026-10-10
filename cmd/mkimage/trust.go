package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/diskfs/go-diskfs/filesystem"

	"github.com/kuberoot-dev/kuberoot/pkg/bootdisk"
	"github.com/kuberoot-dev/kuberoot/pkg/pki"
)

// trust is what --trust keeps in its directory: a serving CA, whose key goes
// onto the media so nodes can sign their API certificates, and an admin CA,
// whose key never leaves the directory. Its admin.kubeconfig reaches every
// node installed from media made with the same directory.
type trust struct{ dir string }

func (t trust) path(name string) string { return filepath.Join(t.dir, name) }

// ensure creates the CAs and the admin credentials the first time.
func (t trust) ensure() error {
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return err
	}
	for _, ca := range []string{"serving-ca", "admin-ca"} {
		if _, err := os.Stat(t.path(ca + ".crt")); err == nil {
			continue
		}
		_, certPEM, keyPEM, err := pki.NewAuthority("kuberoot " + ca)
		if err != nil {
			return err
		}
		if err := os.WriteFile(t.path(ca+".key"), keyPEM, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(t.path(ca+".crt"), certPEM, 0o644); err != nil {
			return err
		}
	}
	if _, err := os.Stat(t.path("admin.kubeconfig")); err == nil {
		return nil
	}
	admin, err := pki.LoadAuthority(t.path("admin-ca.crt"), t.path("admin-ca.key"))
	if err != nil {
		return err
	}
	certPEM, keyPEM, err := admin.Issue(pki.Spec{CommonName: "kuberoot-admin", Orgs: []string{"kuberoot:node-admins"}})
	if err != nil {
		return err
	}
	serving, err := os.ReadFile(t.path("serving-ca.crt"))
	if err != nil {
		return err
	}
	b64 := base64.StdEncoding.EncodeToString
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
# Pass the node's address with --server https://<address>:50000 or edit it here.
clusters:
- name: kuberoot
  cluster:
    server: https://NODE-ADDRESS:50000
    tls-server-name: kuberoot-node
    certificate-authority-data: %s
users:
- name: kuberoot-admin
  user:
    client-certificate-data: %s
    client-key-data: %s
contexts:
- name: kuberoot
  context:
    cluster: kuberoot
    user: kuberoot-admin
current-context: kuberoot
`, b64(serving), b64(certPEM), b64(keyPEM))
	return os.WriteFile(t.path("admin.kubeconfig"), []byte(kubeconfig), 0o600)
}

// install puts the public half of the trust, and the serving CA, onto the media.
func (t trust) install(fs filesystem.FileSystem) error {
	for _, f := range []string{"serving-ca.crt", "serving-ca.key", "admin-ca.crt"} {
		if err := bootdisk.CopyFile(fs, "/kuberoot/trust/"+f, t.path(f)); err != nil {
			return err
		}
	}
	return nil
}
