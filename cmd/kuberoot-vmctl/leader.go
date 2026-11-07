package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

const leaseName = "kuberoot-vmctl"

// leaderWork joins the controller before the Lease can be released. The
// election library starts its callback asynchronously, so stopping also
// bars a callback that has not started yet.
type leaderWork struct {
	mu      sync.Mutex
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
}

func (w *leaderWork) start(ctx context.Context, run func(context.Context) error) {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	w.mu.Unlock()
	err := run(ctx)
	w.mu.Lock()
	w.err = err
	close(w.done)
	w.mu.Unlock()
}

func (w *leaderWork) stop() error {
	w.mu.Lock()
	w.stopped = true
	if w.cancel != nil {
		w.cancel()
	}
	done := w.done
	w.mu.Unlock()
	if done != nil {
		<-done
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func runElected(ctx context.Context, kube kubernetes.Interface, namespace, identity string, run func(context.Context) error) error {
	// Keep renewing on SIGTERM while the controller drains. Canceling the
	// election first would let another controller start before cleanup.
	electCtx, cancelElection := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelElection()
	var work leaderWork
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Name: leaseName, Namespace: namespace},
			Client:     kube.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: identity},
		},
		// The gap leaves time to stop dnsmasq and remove the gateway after
		// renewal fails. A hung or partitioned host still needs fencing;
		// a Kubernetes Lease cannot fence a kernel's network state.
		LeaseDuration: 45 * time.Second,
		RenewDeadline: 20 * time.Second,
		RetryPeriod:   2 * time.Second,
		// The library releases before canceling/joining OnStartedLeading.
		// Release explicitly below, only after all work has stopped.
		ReleaseOnCancel: false,
		Name:            leaseName,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				work.start(ctx, run)
				cancelElection()
			},
			OnStoppedLeading: func() { _ = work.stop() },
			OnNewLeader: func(id string) {
				if id != identity {
					klog.Infof("vmctl: %s leads; %s waits", id, identity)
				}
			},
		},
	})
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		elector.Run(electCtx)
		close(done)
	}()
	select {
	case <-ctx.Done():
		// The signal cancels work, but election renewal continues until
		// reconciliation, HTTP requests, DHCP and gateway cleanup finish.
		err = work.stop()
		cancelElection()
		<-done
	case <-done:
		err = work.stop()
		if ctx.Err() == nil {
			err = errors.Join(err, fmt.Errorf("vmctl: %s stopped leading", identity))
		}
	}
	if err != nil {
		// Failed cleanup or a lost Lease: exit and leave expiry to the
		// election protocol instead of inviting an immediate takeover.
		return err
	}
	return releaseLease(kube, namespace, identity)
}

func releaseLease(kube kubernetes.Interface, namespace, identity string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		lease, err := kube.CoordinationV1().Leases(namespace).Get(ctx, leaseName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != identity {
			return nil // a follower or an already replaced leader releases nothing
		}
		empty, duration := "", int32(1)
		lease.Spec.HolderIdentity, lease.Spec.LeaseDurationSeconds = &empty, &duration
		now := metav1.NowMicro()
		lease.Spec.RenewTime, lease.Spec.AcquireTime = &now, &now
		_, err = kube.CoordinationV1().Leases(namespace).Update(ctx, lease, metav1.UpdateOptions{})
		return err
	})
}
