package aictl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/ai/v1alpha1"
	nodev1 "github.com/kuberoot-dev/kuberoot/pkg/apis/node/v1alpha1"
)

const finalizer = "ai.kuberoot.dev/cleanup"

// FirstPort is where model ports start, one per model, on every node.
const FirstPort = 8100

var (
	modelsGVR  = v1.GroupVersion.WithResource("models")
	serversGVR = schema.GroupVersionResource{Group: "node.kuberoot.dev", Version: "v1alpha1", Resource: "modelservers"}
)

// Controller runs the cluster's models through the node APIs of its nodes.
type Controller struct {
	Dynamic dynamic.Interface
	Kube    kubernetes.Interface
	// Gateway learns from the controller where each model is served.
	Gateway *Gateway
	// Probe sends a trial request to a server; nil uses an HTTP request.
	Probe func(ctx context.Context, address string, port int32, model string) error

	mu       sync.Mutex
	probes   map[string]int       // "<model>/<sha>" -> answered (>0) or failures in a row (<0)
	draining map[string]time.Time // "<node>.<model>" -> taken out of the endpoint at
}

// MaxDrain bounds the wait for a replica's requests to end before it
// changes version or leaves.
var MaxDrain = 2 * time.Minute

// Run reconciles every few seconds until ctx ends.
func (c *Controller) Run(ctx context.Context) error {
	if c.Probe == nil {
		c.Probe = probe
	}
	for {
		c.reconcile(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *Controller) nodes(ctx context.Context) ([]Node, error) {
	list, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Node
	for _, n := range list.Items {
		node := Node{Name: n.Name}
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				node.Address = a.Address
			}
		}
		for _, cond := range n.Status.Conditions {
			if cond.Type == corev1.NodeReady {
				node.Ready = cond.Status == corev1.ConditionTrue
				if !node.Ready {
					node.DownSince = cond.LastTransitionTime.Time
				}
			}
		}
		out = append(out, node)
	}
	// The control plane is where the cluster's API server runs.
	if services, err := c.Dynamic.Resource(servicesGVR).List(ctx, metav1.ListOptions{}); err == nil {
		cp := map[string]bool{}
		for _, u := range services.Items {
			if node, svc, ok := strings.Cut(u.GetName(), "."); ok && svc == "kube-apiserver" {
				cp[node] = true
			}
		}
		for i := range out {
			out[i].ControlPlane = cp[out[i].Name]
		}
	}
	// The GPUs each node has, as its OS reports them.
	if configs, err := c.Dynamic.Resource(osconfigsGVR).List(ctx, metav1.ListOptions{}); err == nil {
		gpus := map[string]int{}
		for _, u := range configs.Items {
			list, _, _ := unstructured.NestedSlice(u.Object, "status", "gpus")
			gpus[u.GetName()] = len(list)
		}
		for i := range out {
			out[i].GPUs = gpus[out[i].Name]
		}
	}
	return out, nil
}

// servers lists the model servers of every node through the cluster's view
// of the node APIs, where each is named <node>.<model>.
func (c *Controller) servers(ctx context.Context) (map[string]map[string]Server, error) {
	list, err := c.Dynamic.Resource(serversGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]Server{} // model -> node -> server
	for _, u := range list.Items {
		node, model, ok := strings.Cut(u.GetName(), ".")
		if !ok {
			continue
		}
		s := Server{Node: node}
		s.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
		weights, _, _ := unstructured.NestedString(u.Object, "status", "sha256")
		engine, _, _ := unstructured.NestedString(u.Object, "status", "engineSHA256")
		if s.Phase == "Downloading" || s.Phase == "Failed" || weights == "" {
			// Not running the version asked for yet: report the one asked for.
			weights, _, _ = unstructured.NestedString(u.Object, "spec", "sha256")
			engine, _, _ = unstructured.NestedString(u.Object, "spec", "engineSHA256")
		}
		s.Version = Version(v1.Source{SHA256: weights})
		if engine != "" {
			s.Version = Version(v1.Source{SHA256: weights, Engine: &v1.Engine{SHA256: engine}})
		}
		if out[model] == nil {
			out[model] = map[string]Server{}
		}
		out[model][node] = s
	}
	return out, nil
}

func (c *Controller) reconcile(ctx context.Context) {
	nodes, err := c.nodes(ctx)
	if err != nil {
		klog.Errorf("nodes: %v", err)
		return
	}
	c.labelGPUNodes(ctx, nodes)
	list, err := c.Dynamic.Resource(modelsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("models: %v", err)
		return
	}
	servers, err := c.servers(ctx)
	if err != nil {
		klog.Errorf("model servers: %v", err)
		return
	}
	var models []v1.Model
	usedPorts, load, gpusUsed := map[int32]bool{}, map[string]int{}, map[string]int{}
	for _, u := range list.Items {
		var m v1.Model
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &m); err != nil {
			continue
		}
		models = append(models, m)
		if m.Status.Port != 0 {
			usedPorts[m.Status.Port] = true
		}
		for _, r := range m.Status.Replicas {
			load[r.Node]++
			gpusUsed[r.Node] += int(m.Spec.GPUs)
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].CreationTimestamp.Before(&models[j].CreationTimestamp) })
	c.sweep(ctx, models, nodes, servers)
	byName := map[string]Node{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	routes := map[string][]string{}
	for i := range models {
		m := &models[i]
		if m.DeletionTimestamp != nil {
			c.cleanup(ctx, m, byName)
			continue
		}
		if err := c.ensureFinalizer(ctx, m); err != nil {
			klog.Errorf("%s: %v", m.Name, err)
			continue
		}
		port := m.Status.Port
		for port == 0 {
			for p := int32(FirstPort); ; p++ {
				if !usedPorts[p] {
					port, usedPorts[p] = p, true
					break
				}
			}
		}
		// Its own replicas' GPUs are its to keep.
		for _, r := range m.Status.Replicas {
			gpusUsed[r.Node] -= int(m.Spec.GPUs)
		}
		replicas := Place(*m, nodes, load, gpusUsed, time.Now())
		for _, r := range replicas {
			load[r]++
			gpusUsed[r] += int(m.Spec.GPUs)
		}
		mine := servers[m.Name]
		want, st := Rollout(*m, replicas, mine, c.probeResult(ctx, m, mine, byName, port), time.Now())
		st.Port = port
		// Replicas about to change version or to leave are taken out of the
		// endpoint first, and changed once their requests are answered.
		var backends []string
		hold := map[string]bool{}
		for node, s := range mine {
			sha, stays := want[node]
			backend := fmt.Sprintf("%s:%d", byName[node].Address, port)
			if stays && sha == s.Version {
				c.drained(node + "." + m.Name)
				if s.Phase == "Ready" && byName[node].Ready {
					backends = append(backends, backend)
				}
				continue
			}
			if s.Phase == "Ready" && c.Gateway != nil && !c.drain(node+"."+m.Name, c.Gateway.InFlight(backend)) {
				hold[node] = true
			}
		}
		sort.Strings(backends)
		routes[m.Name] = backends
		if c.Gateway != nil {
			c.Gateway.SetModelRoutes(m.Name, backends)
		}
		c.apply(ctx, m, want, port, byName, mine, hold)
		ready := 0
		for _, r := range replicas {
			s := mine[r]
			st.Replicas = append(st.Replicas, v1.Replica{Node: r, Address: byName[r].Address, Version: s.Version, Phase: s.Phase})
			if s.Phase == "Ready" && byName[r].Ready {
				ready++
			}
		}
		if len(replicas) == 0 {
			st.Phase, st.Message = "Pending", "no ready node to serve it"
		}
		st.Ready = fmt.Sprintf("%d/%d", ready, m.Spec.Replicas)
		c.writeStatus(ctx, m, st)
	}
	if c.Gateway != nil {
		c.Gateway.SetRoutes(routes)
	}
	c.remedies(ctx, nodes)
}

// probeResult tries a new version on its first node once it is ready
// there, and remembers the answer for the version.
func (c *Controller) probeResult(ctx context.Context, m *v1.Model, mine map[string]Server, byName map[string]Node, port int32) int {
	sha := Version(m.Spec.Source)
	key := m.Name + "/" + sha
	c.mu.Lock()
	if c.probes == nil {
		c.probes = map[string]int{}
	}
	n := c.probes[key]
	c.mu.Unlock()
	canary := m.Status.Canary
	s, ok := mine[canary]
	if n > 0 || m.Status.Current == nil || sha == Version(*m.Status.Current) || !ok || s.Version != sha || s.Phase != "Ready" {
		return n
	}
	pctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := c.Probe(pctx, byName[canary].Address, port, m.Name); err != nil {
		klog.Warningf("%s: trial request to %s failed: %v", m.Name, canary, err)
		n = min(n, 0) - 1
	} else {
		klog.Infof("%s: the new version answered on %s", m.Name, canary)
		n = 1
	}
	c.mu.Lock()
	c.probes[key] = n
	c.mu.Unlock()
	return n
}

// drain reports whether a replica out of the endpoint may change: its
// requests answered, or waited for long enough.
func (c *Controller) drain(key string, inFlight int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.draining == nil {
		c.draining = map[string]time.Time{}
	}
	since, ok := c.draining[key]
	if !ok {
		// Out of the endpoint from now: requests already sent to it may
		// still be on their way.
		c.draining[key] = time.Now()
		return false
	}
	return inFlight == 0 || time.Since(since) > MaxDrain
}

func (c *Controller) drained(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.draining, key)
}

// probe asks the model a short question and wants an answer.
func probe(ctx context.Context, address string, port int32, model string) error {
	body, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 8,
		"messages": []map[string]string{{"role": "user", "content": "Reply with the word OK."}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://%s:%d/v1/chat/completions", address, port), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return fmt.Errorf("an empty answer")
	}
	return nil
}

// apply makes each replica node run the version decided for it, and no
// other node run the model.
func (c *Controller) apply(ctx context.Context, m *v1.Model, want map[string]string, port int32, byName map[string]Node, mine map[string]Server, hold map[string]bool) {
	sources := map[string]v1.Source{Version(m.Spec.Source): m.Spec.Source}
	for _, src := range []*v1.Source{m.Status.Current, m.Status.Previous} {
		if src != nil {
			sources[Version(*src)] = *src
		}
	}
	for node, version := range want {
		if !byName[node].Ready || hold[node] {
			continue
		}
		src := sources[version]
		spec := nodev1.ModelServerSpec{URL: src.URL, SHA256: src.SHA256, Model: m.Name, Port: port,
			ContextSize: m.Spec.ContextSize, Parallel: m.Spec.Parallel, Threads: m.Spec.Threads, GPUs: m.Spec.GPUs}
		if src.Engine != nil {
			spec.EngineURL, spec.EngineSHA256 = src.Engine.URL, src.Engine.SHA256
		}
		if err := c.ensure(ctx, node+"."+m.Name, spec); err != nil {
			klog.Errorf("%s on %s: %v", m.Name, node, err)
		}
	}
	for node := range mine {
		if _, ok := want[node]; !ok && byName[node].Ready && !hold[node] {
			klog.Infof("%s: leaving %s", m.Name, node)
			_ = c.Dynamic.Resource(serversGVR).Delete(ctx, node+"."+m.Name, metav1.DeleteOptions{})
		}
	}
}

// ensure creates a model server, or updates its spec when it differs.
func (c *Controller) ensure(ctx context.Context, name string, spec nodev1.ModelServerSpec) error {
	want, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
	if err != nil {
		return err
	}
	r := c.Dynamic.Resource(serversGVR)
	cur, err := r.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "node.kuberoot.dev/v1alpha1", "kind": "ModelServer",
			"metadata": map[string]any{"name": name}, "spec": want}}
		_, err = r.Create(ctx, obj, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	var have nodev1.ModelServerSpec
	if raw, ok, _ := unstructured.NestedMap(cur.Object, "spec"); ok {
		_ = runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &have)
	}
	if reflect.DeepEqual(have, spec) {
		return nil
	}
	cur.Object["spec"] = want
	_, err = r.Update(ctx, cur, metav1.UpdateOptions{})
	return err
}

// sweep removes, from nodes that are up, model servers no model accounts
// for: left by a deletion while their node was down.
func (c *Controller) sweep(ctx context.Context, models []v1.Model, nodes []Node, servers map[string]map[string]Server) {
	known := map[string]bool{}
	for _, m := range models {
		known[m.Name] = true
	}
	ready := map[string]bool{}
	for _, n := range nodes {
		ready[n.Name] = n.Ready
	}
	for model, on := range servers {
		if known[model] {
			continue
		}
		for node := range on {
			if ready[node] {
				klog.Infof("removing model server %s.%s: no model has it", node, model)
				_ = c.Dynamic.Resource(serversGVR).Delete(ctx, node+"."+model, metav1.DeleteOptions{})
			}
		}
	}
}

// cleanup removes a deleted model from every node, then lets it go.
func (c *Controller) cleanup(ctx context.Context, m *v1.Model, byName map[string]Node) {
	for _, r := range m.Status.Replicas {
		n, ok := byName[r.Node]
		if !ok {
			continue // the node left the cluster
		}
		if !n.Ready {
			return // kept until the node is back to stop its server
		}
		if err := c.Dynamic.Resource(serversGVR).Delete(ctx, r.Node+"."+m.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			klog.Errorf("%s: server on %s: %v", m.Name, r.Node, err)
			return
		}
	}
	u, err := c.Dynamic.Resource(modelsGVR).Get(ctx, m.Name, metav1.GetOptions{})
	if err != nil {
		return
	}
	var keep []string
	for _, f := range u.GetFinalizers() {
		if f != finalizer {
			keep = append(keep, f)
		}
	}
	u.SetFinalizers(keep)
	_, _ = c.Dynamic.Resource(modelsGVR).Update(ctx, u, metav1.UpdateOptions{})
}

func (c *Controller) ensureFinalizer(ctx context.Context, m *v1.Model) error {
	for _, f := range m.Finalizers {
		if f == finalizer {
			return nil
		}
	}
	u, err := c.Dynamic.Resource(modelsGVR).Get(ctx, m.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	u.SetFinalizers(append(u.GetFinalizers(), finalizer))
	_, err = c.Dynamic.Resource(modelsGVR).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

func (c *Controller) writeStatus(ctx context.Context, m *v1.Model, st v1.ModelStatus) {
	if reflect.DeepEqual(m.Status, st) {
		return
	}
	u, err := c.Dynamic.Resource(modelsGVR).Get(ctx, m.Name, metav1.GetOptions{})
	if err != nil {
		return
	}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&st)
	if err != nil {
		return
	}
	u.Object["status"] = raw
	if _, err := c.Dynamic.Resource(modelsGVR).UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
		klog.V(2).Infof("status of %s: %v", m.Name, err)
	}
}

// GPULabel marks the nodes with NVIDIA GPUs, where GPU packages (device
// plugin, metrics, sharing) run their pods.
const GPULabel = "nvidia.com/gpu.present"

// labelGPUNodes keeps GPULabel on the nodes that report GPUs, and off the
// others.
func (c *Controller) labelGPUNodes(ctx context.Context, nodes []Node) {
	list, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	gpus := map[string]int{}
	for _, n := range nodes {
		gpus[n.Name] = n.GPUs
	}
	for _, n := range list.Items {
		_, has := n.Labels[GPULabel]
		var patch string
		switch {
		case gpus[n.Name] > 0 && !has:
			patch = `{"metadata":{"labels":{"` + GPULabel + `":"true"}}}`
		case gpus[n.Name] == 0 && has:
			patch = `{"metadata":{"labels":{"` + GPULabel + `":null}}}`
		default:
			continue
		}
		if _, err := c.Kube.CoreV1().Nodes().Patch(ctx, n.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			klog.Errorf("label of %s: %v", n.Name, err)
		}
	}
}
