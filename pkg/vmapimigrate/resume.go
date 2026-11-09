package vmapimigrate

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kuberoot-dev/kuberoot/pkg/vmmaintenance"
)

func (c *Client) Verify(ctx context.Context, p *Plan, hash, statePath string) error {
	if statePath == "" {
		return c.VerifyUnchanged(ctx, p)
	}
	state, err := loadState(statePath, hash)
	if err != nil {
		return err
	}
	switch state.Phase {
	case "":
		return c.VerifyUnchanged(ctx, p)
	case "verified":
		return c.VerifyImported(ctx, p, state, hash)
	case "complete":
		return c.verifyRestoredUIDs(ctx, p, state)
	default:
		return fmt.Errorf("cutover is incomplete at checkpoint %q; resume its explicit operation with the same plan", state.Phase)
	}
}

// Resume releases only a fully verified import. The durable resuming phase
// records that exact verification preceded gate deletion. Admission fences
// are removed only after this release boundary, and cleanup is resumable
// without pretending controller writes remained paused after the gate vanished.
func (c *Client) Resume(ctx context.Context, p *Plan, hash, statePath string) error {
	user, err := c.username(ctx)
	if err != nil {
		return err
	}
	if user != p.Executor {
		return fmt.Errorf("resume and post-release cleanup require the original dedicated executor identity")
	}

	state, err := loadState(statePath, hash)
	if err != nil {
		return err
	}
	if state.Phase == "complete" {
		return c.verifyRestoredUIDs(ctx, p, state)
	}
	if state.Phase == "verified" {
		if err = c.VerifyImported(ctx, p, state, hash); err != nil {
			return err
		}
		state.Phase = "resuming"
		if err = SavePrivate(statePath, state); err != nil {
			return err
		}
	}
	if state.Phase != "resuming" {
		return fmt.Errorf("import is not verified; maintenance must stay active")
	}
	if err = c.verifyRestoredUIDs(ctx, p, state); err != nil {
		return err
	}
	cm, err := c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		// Gate deletion is the release boundary. Keep the write fences until
		// that point so a failure before release can still verify exact data.
		if err = c.VerifyImported(ctx, p, state, hash); err != nil {
			return err
		}
		if err = c.verifyBarrierWithMode(ctx, p, true, vmmaintenance.ModeAlpha2); err != nil {
			return err
		}
		uid, rv := cm.UID, cm.ResourceVersion
		if err = c.Kube.CoreV1().ConfigMaps(vmmaintenance.Namespace).Delete(ctx, vmmaintenance.ConfigMap, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}); err != nil {
			return err
		}
	} else {
		// A crash after release may leave admission cleanup pending. Ordinary
		// controller changes are legal now; verify only UID/backend identity.
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{UID: p.GateUID}, Data: map[string]string{"token": p.Token}}
	}
	for _, resource := range []schema.GroupVersionResource{bindings, policies} {
		u, err := c.Dynamic.Resource(resource).Get(ctx, vmmaintenance.ConfigMap, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if u.GetAnnotations()["vm.kuberoot.dev/barrier-token"] != p.Token {
			return fmt.Errorf("admission fence belongs to another session")
		}
		uid := u.GetUID()
		if err = c.Dynamic.Resource(resource).Delete(ctx, u.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
			return err
		}
	}
	if err = c.deleteProbeRBAC(ctx, cm); err != nil {
		return err
	}
	state.Phase = "complete"
	return SavePrivate(statePath, state)
}

func (c *Client) verifyRestoredUIDs(ctx context.Context, p *Plan, state *State) error {
	if len(state.UIDs) != len(p.Machines) {
		return fmt.Errorf("restored UID map is incomplete")
	}
	if err := vmmaintenance.APIReady(ctx, c.Dynamic); err != nil {
		return err
	}
	for _, record := range p.Machines {
		u, err := c.Dynamic.Resource(NewVM).Namespace(record.Namespace).Get(ctx, record.Original.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if u.GetUID() != state.UIDs[record.Original.GetUID()] {
			return fmt.Errorf("restored VM UID differs from the verified checkpoint")
		}
		status, ok := u.Object["status"].(map[string]any)
		if !ok || status["backendID"] != record.Original.GetName() || status["instanceID"] != string(record.Original.GetUID()) {
			return fmt.Errorf("restored backend/guest identity differs from the plan")
		}
	}
	return nil
}
