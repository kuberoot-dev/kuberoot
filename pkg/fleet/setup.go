package fleet

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"path"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/kuberoot-dev/kuberoot/pkg/pki"
)

// The fleet API's CRDs, generated from pkg/apis/fleet; the center applies
// them itself, so any cluster can be the center.
//
//go:embed crds/*.yaml
var crds embed.FS

var crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

// ApplyCRDs installs or updates the fleet API in the center's cluster.
func ApplyCRDs(ctx context.Context, dyn dynamic.Interface) error {
	files, err := crds.ReadDir("crds")
	if err != nil {
		return err
	}
	for _, f := range files {
		raw, err := crds.ReadFile(path.Join("crds", f.Name()))
		if err != nil {
			return err
		}
		obj := map[string]any{}
		if err := yaml.Unmarshal(raw, &obj); err != nil {
			return fmt.Errorf("%s: %w", f.Name(), err)
		}
		body, _ := json.Marshal(obj)
		name, _ := obj["metadata"].(map[string]any)["name"].(string)
		force := true
		if _, err := dyn.Resource(crdGVR).Patch(ctx, name, types.ApplyPatchType, body,
			metav1.PatchOptions{FieldManager: "kuberoot-fleet", Force: &force}); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}

// CAName names the Secret with the center's authority, and the ConfigMap
// with its certificate and pin, which anyone may read.
const CAName = "kuberoot-fleet-ca"

// LoadOrCreateCA returns the center's authority, kept in the center's
// cluster, and its certificate in PEM. Nodes pin it: replacing it means
// enrolling every node again.
func LoadOrCreateCA(ctx context.Context, kube kubernetes.Interface, ns string) (*pki.Authority, []byte, error) {
	secrets := kube.CoreV1().Secrets(ns)
	sec, err := secrets.Get(ctx, CAName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, certPEM, keyPEM, err := pki.NewAuthority("kuberoot-fleet-ca")
		if err != nil {
			return nil, nil, err
		}
		sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: CAName, Namespace: ns}, Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM}}
		if sec, err = secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, err
	}
	certPEM := sec.Data[corev1.TLSCertKey]
	ca, err := pki.ParseAuthority(certPEM, sec.Data[corev1.TLSPrivateKeyKey])
	if err != nil {
		return nil, nil, err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: CAName, Namespace: ns},
		Data: map[string]string{"ca.crt": string(certPEM), "caHash": Hash(ca.Certificate())}}
	if _, err := kube.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
		_, err = kube.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, err
	}
	return ca, certPEM, nil
}

// ServingCert issues the center's serving certificate, sent with its
// authority so nodes can check it against their pin.
func ServingCert(ca *pki.Authority, caPEM []byte) (tls.Certificate, error) {
	return issueChain(ca, caPEM, pki.Spec{CommonName: ServerName, DNSNames: []string{ServerName}, Server: true})
}
