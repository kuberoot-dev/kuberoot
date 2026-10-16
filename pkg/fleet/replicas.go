package fleet

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// The center runs as several replicas, one of them the leader: only the
// leader takes the nodes' connections and acts, the others wait to take
// over. Every replica writes where nodes reach it into this ConfigMap, so
// the leader hands the nodes every address, and a node whose leader is
// gone finds the next one.
const (
	CentersName = "kuberoot-fleet-centers"
	// LeaseName is the Lease the replicas elect the leader with.
	LeaseName = "kuberoot-fleet"
	// replicaKept: a replica not heard from for this long is forgotten.
	replicaKept = 24 * time.Hour
)

// replica is a center replica as it writes itself into the ConfigMap.
type replica struct {
	Servers []string  `json:"servers"`
	Seen    time.Time `json:"seen"`
}

// Register writes a replica's addresses into the ConfigMap of centers,
// creating it when it is the first.
func Register(ctx context.Context, kube kubernetes.Interface, ns, identity string, servers []string) error {
	raw, _ := json.Marshal(replica{Servers: servers, Seen: time.Now().UTC().Truncate(time.Second)})
	patch, _ := json.Marshal(map[string]any{"data": map[string]string{identity: string(raw)}})
	cms := kube.CoreV1().ConfigMaps(ns)
	_, err := cms.Patch(ctx, CentersName, types.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: CentersName, Namespace: ns},
			Data: map[string]string{identity: string(raw)}}
		if _, err = cms.Create(ctx, cm, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
			_, err = cms.Patch(ctx, CentersName, types.MergePatchType, patch, metav1.PatchOptions{})
		}
	}
	return err
}

// KeepRegistered writes the replica's addresses now and every minute,
// until ctx ends.
func KeepRegistered(ctx context.Context, kube kubernetes.Interface, ns, identity string, servers []string) {
	for {
		if err := Register(ctx, kube, ns, identity, servers); err != nil && ctx.Err() == nil {
			klog.Errorf("fleet: register this replica: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// servers are every replica's addresses, sorted, its own when the
// ConfigMap cannot be read. The leader forgets replicas gone for a day.
func (c *Center) servers(ctx context.Context) []string {
	cm, err := c.Kube.CoreV1().ConfigMaps(c.Namespace).Get(ctx, CentersName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Errorf("fleet: read the replicas: %v", err)
		}
		return c.Servers
	}
	out := slices.Clone(c.Servers)
	var gone []string
	for id, raw := range cm.Data {
		var r replica
		if json.Unmarshal([]byte(raw), &r) != nil || time.Since(r.Seen) > replicaKept {
			gone = append(gone, id)
			continue
		}
		out = append(out, r.Servers...)
	}
	if len(gone) > 0 {
		patch := map[string]any{}
		for _, id := range gone {
			patch[id] = nil
		}
		raw, _ := json.Marshal(map[string]any{"data": patch})
		if _, err := c.Kube.CoreV1().ConfigMaps(c.Namespace).Patch(ctx, CentersName, types.MergePatchType, raw, metav1.PatchOptions{}); err == nil {
			klog.Infof("fleet: forgot replicas not heard from for %s: %v", replicaKept, gone)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// sameServers: the same addresses, in any order.
func sameServers(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}
