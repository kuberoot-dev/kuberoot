package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for controller lifecycle")
		var zero T
		return zero
	}
}

func TestElectionDrainsBeforeReleasingLease(t *testing.T) {
	kube := fake.NewClientset()
	var cleaned atomic.Bool
	kube.PrependReactor("update", "leases", func(action ktesting.Action) (bool, runtime.Object, error) {
		lease := action.(ktesting.UpdateAction).GetObject().(*coordinationv1.Lease)
		if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == "" && !cleaned.Load() {
			t.Error("Lease released while the controller still had active work")
		}
		return false, nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, draining, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- runElected(ctx, kube, "kube-system", "node-a_process-1", func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(draining)
			<-finish // listener/DHCP/kernel cleanup is still running
			cleaned.Store(true)
			return nil
		})
	}()
	receive(t, started)
	cancel()
	receive(t, draining)
	lease, err := kube.CoordinationV1().Leases("kube-system").Get(context.Background(), leaseName, metav1.GetOptions{})
	if err != nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "node-a_process-1" {
		t.Fatalf("Lease lost before cleanup: %v, %v", lease, err)
	}
	select {
	case err := <-done:
		t.Fatalf("election returned before cleanup: %v", err)
	default:
	}
	close(finish)
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	lease, err = kube.CoordinationV1().Leases("kube-system").Get(context.Background(), leaseName, metav1.GetOptions{})
	if err != nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "" {
		t.Fatalf("cleaned controller did not release: %v, %v", lease, err)
	}
}

func TestElectionDoesNotReleaseAfterFailedCleanup(t *testing.T) {
	kube := fake.NewClientset()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	failure := errors.New("gateway address could not be removed")
	go func() {
		done <- runElected(ctx, kube, "kube-system", "node-a", func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return failure
		})
	}()
	receive(t, started)
	cancel()
	if err := receive(t, done); !errors.Is(err, failure) {
		t.Fatalf("lost cleanup error: %v", err)
	}
	lease, err := kube.CoordinationV1().Leases("kube-system").Get(context.Background(), leaseName, metav1.GetOptions{})
	if err != nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "node-a" {
		t.Fatalf("failed cleanup must leave the Lease to expire: %v, %v", lease, err)
	}
}

func TestStoppedWorkCannotStartLate(t *testing.T) {
	var work leaderWork
	if err := work.stop(); err != nil {
		t.Fatal(err)
	}
	work.start(context.Background(), func(context.Context) error {
		t.Fatal("late OnStartedLeading callback started work after shutdown")
		return nil
	})
}

func TestFollowerReleasesNothing(t *testing.T) {
	holder, duration := "another-process", int32(45)
	kube := fake.NewClientset(&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: leaseName, Namespace: "kube-system"},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &duration}})
	if err := releaseLease(kube, "kube-system", "node-a"); err != nil {
		t.Fatal(err)
	}
	for _, a := range kube.Actions() {
		if a.GetVerb() != "get" {
			t.Fatalf("follower mutated the leader's Lease: %s", a.GetVerb())
		}
	}
}
