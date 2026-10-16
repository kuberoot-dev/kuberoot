// Command kuberoot-fleet is the center of a fleet of kuberoot nodes: it takes
// the connections the nodes open to it, keeps FleetNode and FleetRollout
// resources in its cluster, and upgrades the nodes through their
// connections. It runs on any cluster; on kuberoot, the edge control plane
// runs it when booted with kuberoot.fleet-center.
package main

import (
	"context"
	"crypto/x509"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/fleet"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "", "credentials for the center's cluster")
	listen := flag.String("listen", ":7443", "where nodes connect")
	serversFlag := flag.String("servers", "", "the center's addresses as nodes reach it, host:port, comma separated; enrollments carry them")
	namespace := flag.String("namespace", "kube-system", "namespace of the center's authority and the enrollment Secrets")
	leaderElect := flag.Bool("leader-elect", true, "run as one of several replicas, of which the one holding the Lease kuberoot-fleet leads")
	identity := flag.String("identity", "", "this replica's name in the Lease and the replicas' ConfigMap; the host name when empty")
	settle := flag.Duration("settle", 2*time.Minute, "after starting to lead, how long nodes connected before get to connect again before they count as offline and rollouts move on")
	enrollmentTTL := flag.Duration("enrollment-ttl", 24*time.Hour, "how long an enrollment token works once issued, unless the FleetNode sets spec.enrollmentTTL; 0 for no limit")
	userCA := flag.String("user-client-ca-file", "", "CA of the center cluster's client certificates, for the node API proxy at /nodes/<name>/")
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		klog.Fatalf("kubeconfig: %v", err)
	}
	kube := kubernetes.NewForConfigOrDie(cfg)
	dyn := dynamic.NewForConfigOrDie(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The cluster may still be coming up.
	for {
		err := fleet.ApplyCRDs(ctx, dyn)
		if err == nil {
			break
		}
		klog.Errorf("fleet API: %v", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	var servers []string
	for _, s := range strings.Split(*serversFlag, ",") {
		if s = strings.TrimSpace(s); s != "" {
			servers = append(servers, s)
		}
	}
	if *identity == "" {
		*identity, _ = os.Hostname()
	}
	var users *x509.CertPool
	if *userCA != "" {
		raw, err := os.ReadFile(*userCA)
		if err != nil {
			klog.Fatalf("user CA: %v", err)
		}
		users = x509.NewCertPool()
		users.AppendCertsFromPEM(raw)
	}
	lead := func(ctx context.Context) {
		ca, caPEM, err := fleet.LoadOrCreateCA(ctx, kube, *namespace)
		if err != nil {
			klog.Fatalf("center authority: %v", err)
		}
		cert, err := fleet.ServingCert(ca, caPEM)
		if err != nil {
			klog.Fatalf("serving certificate: %v", err)
		}
		center := &fleet.Center{Dyn: dyn, Kube: kube, Namespace: *namespace, CAHash: fleet.Hash(ca.Certificate()),
			Servers: servers, EnrollmentTTL: *enrollmentTTL, Settle: *settle}
		hub := &fleet.Hub{Cert: cert, Identity: center.Identity, Enroll: center.Enroll, Changed: center.Changed,
			Authorize: center.Authorize, UserCAs: users}
		center.Hub = hub
		klog.Infof("fleet center %s on %s, leading as %s, enrollments name %v", center.CAHash, *listen, *identity, servers)
		go center.Run(ctx)
		if err := hub.Serve(ctx, *listen); err != nil {
			klog.Fatal(err)
		}
	}
	if !*leaderElect {
		lead(ctx)
		return
	}
	// Replicas wait as followers, not listening, so nodes and load balancers
	// find the leader by who answers.
	go fleet.KeepRegistered(ctx, kube, *namespace, *identity, servers)
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Name: fleet.LeaseName, Namespace: *namespace},
			Client:     kube.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: *identity},
		},
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		ReleaseOnCancel: true,
		Name:            fleet.LeaseName,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: lead,
			OnStoppedLeading: func() {
				if ctx.Err() != nil {
					return // stopping: the lease is given up for the next replica
				}
				// The connections and passes of a leader that lost its lease
				// must not go on next to the new leader's: start over.
				klog.Fatalf("fleet: %s lost the lease", *identity)
			},
			OnNewLeader: func(id string) {
				if id != *identity {
					klog.Infof("fleet: %s leads; %s waits", id, *identity)
				}
			},
		},
	})
}
