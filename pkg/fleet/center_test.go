package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/fleet/v1alpha1"
	"github.com/kuberoot-dev/kuberoot/pkg/pki"
)

func testCenter(t *testing.T, nodes ...*v1.FleetNode) *Center {
	t.Helper()
	var objs []runtime.Object
	for _, n := range nodes {
		n.TypeMeta = metav1.TypeMeta{APIVersion: v1.GroupVersion.String(), Kind: "FleetNode"}
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(n)
		if err != nil {
			t.Fatal(err)
		}
		objs = append(objs, &unstructured.Unstructured{Object: raw})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{nodesGVR: "FleetNodeList", rolloutsGVR: "FleetRolloutList"}, objs...)
	return &Center{Dyn: dyn, Kube: kubefake.NewSimpleClientset(), Namespace: "kube-system",
		Servers: []string{"center:7443"}, CAHash: "sha256:00"}
}

func nodeCAPEM(t *testing.T) string {
	t.Helper()
	_, certPEM, _, err := pki.NewAuthority("node-ca")
	if err != nil {
		t.Fatal(err)
	}
	return string(certPEM)
}

// An expired token is refused, deleting its Secret issues a new one, and
// that one enrolls the node.
func TestEnrollmentExpires(t *testing.T) {
	ctx := context.Background()
	c := testCenter(t, &v1.FleetNode{ObjectMeta: metav1.ObjectMeta{Name: "shop-12", UID: "u1"},
		Spec: v1.FleetNodeSpec{EnrollmentTTL: &metav1.Duration{Duration: time.Second}}})
	n, err := c.getNode(ctx, "shop-12")
	if err != nil {
		t.Fatal(err)
	}
	name, exp, err := c.enrollment(ctx, n)
	if err != nil || exp.IsZero() || exp.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("enrollment: %s, %s, %v", name, exp, err)
	}
	sec, _ := c.Kube.CoreV1().Secrets("kube-system").Get(ctx, name, metav1.GetOptions{})
	token := string(sec.Data["token"])

	// Expired, by rewriting when it expires rather than by waiting.
	sec.Annotations[AnnotationExpires] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	if _, err := c.Kube.CoreV1().Secrets("kube-system").Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	err = c.Enroll(ctx, EnrollRequest{Name: "shop-12", Token: token, CA: nodeCAPEM(t)}, "test")
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("an expired token enrolled: %v", err)
	}
	// A wrong token says nothing of expiry.
	if err := c.Enroll(ctx, EnrollRequest{Name: "shop-12", Token: "nope", CA: nodeCAPEM(t)}, "test"); err == nil || strings.Contains(err.Error(), "expired") {
		t.Fatalf("wrong token: %v", err)
	}

	// Revoked: the Secret is deleted, the next pass issues a new token.
	if err := c.Kube.CoreV1().Secrets("kube-system").Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	n.Spec.EnrollmentTTL = &metav1.Duration{Duration: time.Hour}
	if _, _, err := c.enrollment(ctx, n); err != nil {
		t.Fatal(err)
	}
	sec, _ = c.Kube.CoreV1().Secrets("kube-system").Get(ctx, name, metav1.GetOptions{})
	if string(sec.Data["token"]) == token {
		t.Fatal("the revoked token was issued again")
	}
	if err := c.Enroll(ctx, EnrollRequest{Name: "shop-12", Token: token, CA: nodeCAPEM(t)}, "test"); err == nil {
		t.Fatal("the revoked token enrolled")
	}
	if err := c.Enroll(ctx, EnrollRequest{Name: "shop-12", Token: string(sec.Data["token"]), CA: nodeCAPEM(t)}, "test"); err != nil {
		t.Fatalf("the new token: %v", err)
	}
	if n, _ := c.getNode(ctx, "shop-12"); n.Status.Identity == "" {
		t.Error("the node was not pinned")
	}
}

// Without a TTL from either the node or the center, the token works until used.
func TestEnrollmentNoExpiry(t *testing.T) {
	ctx := context.Background()
	c := testCenter(t, &v1.FleetNode{ObjectMeta: metav1.ObjectMeta{Name: "n", UID: "u"}})
	n, _ := c.getNode(ctx, "n")
	if _, exp, err := c.enrollment(ctx, n); err != nil || !exp.IsZero() {
		t.Fatalf("%s, %v", exp, err)
	}
}
