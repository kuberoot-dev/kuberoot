package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	addonsDir          = "/usr/share/kuberoot/addons"
	generatedAddonsDir = "/run/kuberoot/addons"
)

// renderAddons copies the bundled add-ons next to the generated ones, filling in
// the addresses that depend on the cluster's ranges.
func renderAddons() error {
	entries, err := os.ReadDir(addonsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(addonsDir, e.Name()))
		if err != nil {
			return err
		}
		out := strings.ReplaceAll(string(raw), "__CLUSTER_DNS__", activeNet.dnsIP().String())
		if err := os.WriteFile(filepath.Join(generatedAddonsDir, e.Name()), []byte(out), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeAggregation registers the node API with the cluster API server. The
// Service has no selector: its endpoint is the node itself, not a pod.
func writeAggregation(node nodeInfo) error {
	ca, err := os.ReadFile(pkiPath("ca.crt"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(generatedAddonsDir, 0o755); err != nil {
		return err
	}
	manifest := fmt.Sprintf(aggregationTemplate, node.name, node.ip, node.name, base64.StdEncoding.EncodeToString(ca))
	if err := os.WriteFile(generatedAddonsDir+"/node-api.yaml", []byte(manifest), 0o644); err != nil {
		return err
	}
	if !activeRole.Packages {
		return nil
	}
	// The package manager's image policy webhook runs in the same way, in the
	// operator on this node.
	webhook := fmt.Sprintf(imagePolicyEndpointTemplate, node.name, node.ip, node.name)
	return os.WriteFile(generatedAddonsDir+"/kubepkg-webhook.yaml", []byte(webhook), 0o644)
}

const imagePolicyEndpointTemplate = `apiVersion: v1
kind: Service
metadata:
  name: kubepkg-webhook
  namespace: kube-system
spec:
  ports:
  - name: webhook
    port: 443
    targetPort: 9443
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: kubepkg-webhook-%s
  namespace: kube-system
  labels:
    kubernetes.io/service-name: kubepkg-webhook
addressType: IPv4
ports:
- name: webhook
  port: 9443
  protocol: TCP
endpoints:
- addresses: ["%s"]
  nodeName: %s
  conditions:
    ready: true
`

const aggregationTemplate = `apiVersion: v1
kind: Service
metadata:
  name: kuberoot-node
  namespace: kube-system
spec:
  ports:
  - name: https
    port: 50000
    targetPort: 50000
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: kuberoot-node-%s
  namespace: kube-system
  labels:
    kubernetes.io/service-name: kuberoot-node
addressType: IPv4
ports:
- name: https
  port: 50000
  protocol: TCP
endpoints:
- addresses: ["%s"]
  nodeName: %s
  conditions:
    ready: true
---
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: v1alpha1.node.kuberoot.dev
spec:
  group: node.kuberoot.dev
  version: v1alpha1
  groupPriorityMinimum: 1000
  versionPriority: 15
  caBundle: %s
  service:
    namespace: kube-system
    name: kuberoot-node
    port: 50000
`

// applyAddons waits for the API server, applies the bundled cluster add-ons
// and installs the distribution. It stops with the generation that started it.
func applyAddons(ctx context.Context, cfg bootConfig, node nodeInfo) {
	for !apiServerReady() {
		if !sleepCtx(ctx, time.Second) {
			return
		}
	}
	log.Printf("kube-apiserver ready, uptime %s", uptime())
	if err := writeAggregation(node); err != nil {
		log.Printf("aggregation manifest: %v", err)
	}
	if err := renderAddons(); err != nil {
		log.Printf("add-ons: %v", err)
	}
	if activeRole.Packages {
		if err := renderPackages(cfg); err != nil {
			log.Printf("packages: %v", err)
		}
	}
	apply := []string{"/usr/bin/kubectl", "--kubeconfig", kubeDir + "/admin.kubeconfig", "apply", "--server-side",
		"--force-conflicts", "-f", generatedAddonsDir}
	if !runUntilSuccess(ctx, apply, nil, 5*time.Second) {
		return
	}
	log.Printf("add-ons applied")
	labelControlPlane(ctx, node)
	if activeRole.Packages {
		installDistro(ctx, cfg)
	}
}

// labelControlPlane marks the node as one running the control plane. The
// kubelet cannot write this label itself: NodeRestriction refuses labels in
// the node-role.kubernetes.io namespace from a node's own credentials, so it
// is written here, where the admin credentials are. The value is empty, as
// kubeadm writes it and as the convention has it -- the label means its own
// presence, and anything selecting on it should ask whether it exists rather
// than what it says.
//
// It is what `kubectl get nodes` prints under ROLES, and what a workload that
// wants to sit with the control plane matches on.
func labelControlPlane(ctx context.Context, node nodeInfo) {
	args := []string{"/usr/bin/kubectl", "--kubeconfig", kubeDir + "/admin.kubeconfig",
		"label", "--overwrite", "node", node.name, "node-role.kubernetes.io/control-plane="}
	if !runUntilSuccess(ctx, args, nil, 5*time.Second) {
		return
	}
	log.Printf("node %s labelled as control plane", node.name)
}

// runUntilSuccess runs a tool until it exits 0, each run capped at two minutes,
// logging to the add-ons log. It reports false when ctx ended first.
func runUntilSuccess(ctx context.Context, args, env []string, pause time.Duration) bool {
	for {
		if runTool(ctx, args, env, 2*time.Minute) {
			return true
		}
		if !sleepCtx(ctx, pause) {
			return false
		}
	}
}

func runTool(ctx context.Context, args, env []string, timeout time.Duration) bool {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(append([]string{}, servicePath...), env...)
	out, err := os.OpenFile("/var/log/kuberoot/addons.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false
	}
	defer out.Close()
	cmd.Stdout, cmd.Stderr = out, out
	done, err := spawn(cmd)
	if err != nil {
		return false
	}
	select {
	case st := <-done:
		return st.ExitStatus() == 0
	case <-ctx.Done():
	case <-time.After(timeout):
	}
	_ = cmd.Process.Kill()
	<-done
	return false
}

// sleepCtx waits d, or less if ctx ends; it reports whether ctx is still live.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

var (
	readyMu     sync.Mutex
	readyClient *http.Client
)

// apiServerReady reports whether the local API server answers /readyz. One
// client is reused, so polling does not pile up connections in PID 1.
func apiServerReady() bool {
	readyMu.Lock()
	if readyClient == nil {
		readyClient, _ = adminClient() // credentials appear once the PKI is written
	}
	client := readyClient
	readyMu.Unlock()
	if client == nil {
		return false
	}
	resp, err := client.Get(apiServer + "/readyz")
	if err != nil {
		// The credentials may have changed under it: a control plane that
		// joins another cluster takes that cluster's authority and admin
		// certificate, and the old client never passes TLS again.
		readyMu.Lock()
		if readyClient == client {
			readyClient = nil
			client.CloseIdleConnections()
		}
		readyMu.Unlock()
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func adminClient() (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(pkiPath("admin.crt"), pkiPath("admin.key"))
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(pkiPath("ca.crt"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool}},
	}, nil
}

// printNodeKubeconfig hands the node admin credentials to a development host over
// the serial console. The installer will take over this job.
func printNodeKubeconfig() {
	raw, err := os.ReadFile(nodeAdminKubeconfig)
	if err != nil {
		return
	}
	os.Stdout.WriteString("-----BEGIN KUBEROOT KUBECONFIG-----\n" + base64.StdEncoding.EncodeToString(raw) + "\n-----END KUBEROOT KUBECONFIG-----\n")
}
