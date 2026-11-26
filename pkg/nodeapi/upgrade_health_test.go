package nodeapi

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

func TestUpgradeRejectsInvalidApplicationPolicyBeforeStaging(t *testing.T) {
	storage := &upgradeStorage{}
	request := &node.Upgrade{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-policy"},
		Spec:       node.UpgradeSpec{URL: "https://example.invalid/release.tar", HealthChecks: []node.UpgradeHealthCheck{{URL: "file:///etc/passwd"}}},
	}
	_, err := storage.Create(context.Background(), request, nil, nil)
	if !apierrors.IsBadRequest(err) || !strings.Contains(err.Error(), "healthChecks") {
		t.Fatalf("wanted policy validation before installed-system check or download, got %v", err)
	}
	if len(storage.items) != 0 {
		t.Fatal("invalid policy started an upgrade")
	}
}
