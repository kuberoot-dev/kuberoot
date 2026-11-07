package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"sigs.k8s.io/yaml"
)

const vmCRDFile = "vm.kuberoot.dev_virtualmachines.yaml"
const vmCRDPath = "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/virtualmachines.vm.kuberoot.dev"
const vmGatePath = "/api/v1/namespaces/kube-system/configmaps/kuberoot-vm-api-maintenance"

type vmCRDShape struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Group    string `json:"group"`
		Scope    string `json:"scope"`
		Versions []struct {
			Name    string `json:"name"`
			Served  bool   `json:"served"`
			Storage bool   `json:"storage"`
		} `json:"versions"`
	} `json:"spec"`
}

func (c vmCRDShape) mode() string {
	if c.Metadata.Name != "virtualmachines.vm.kuberoot.dev" || c.Spec.Group != "vm.kuberoot.dev" || len(c.Spec.Versions) != 1 {
		return ""
	}
	v := c.Spec.Versions[0]
	if !v.Served || !v.Storage {
		return ""
	}
	if c.Spec.Scope == "Cluster" && v.Name == "v1alpha1" {
		return "v1alpha1"
	}
	if c.Spec.Scope == "Namespaced" && v.Name == "v1alpha2" {
		return "v1alpha2"
	}
	return ""
}

// Keep the VM CRD outside the ordinary add-on directory in transition
// images. Its independent loop can wait for migration without blocking the
// other add-ons, node role label or package installation.
func stageTransitionVMAddon(bundled, generated, separate string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(bundled, vmCRDFile))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var shape vmCRDShape
	if err = yaml.Unmarshal(raw, &shape); err != nil {
		return "", err
	}
	if shape.mode() != "v1alpha2" {
		return "", fmt.Errorf("transition image must bundle the exact namespaced alpha2 VM CRD")
	}
	if err = os.MkdirAll(separate, 0755); err != nil {
		return "", err
	}
	target := filepath.Join(separate, vmCRDFile)
	if err = os.WriteFile(target, raw, 0644); err != nil {
		return "", err
	}
	if err = os.Remove(filepath.Join(generated, vmCRDFile)); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return target, nil
}

func readTransitionObject(ctx context.Context, client *http.Client, base, path string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("read transition API %s: HTTP %d", path, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) > 4*1024*1024 {
		return nil, false, fmt.Errorf("transition API response exceeds limit")
	}
	return raw, true, nil
}

// A gate check on both sides of the scope observation prevents a stale
// NotFound decision during CRD replacement from racing the importer.
func transitionVMAddonMayApply(ctx context.Context, client *http.Client, base string) (bool, error) {
	if _, gate, err := readTransitionObject(ctx, client, base, vmGatePath); err != nil || gate {
		return false, err
	}
	raw, exists, err := readTransitionObject(ctx, client, base, vmCRDPath)
	if err != nil {
		return false, err
	}
	if exists {
		var shape vmCRDShape
		if err = json.Unmarshal(raw, &shape); err != nil {
			return false, err
		}
		switch shape.mode() {
		case "v1alpha1":
			return false, nil
		case "v1alpha2":
		default:
			return false, fmt.Errorf("unknown installed VM CRD scope/version; transition add-on is deferred")
		}
	}
	if _, gate, err := readTransitionObject(ctx, client, base, vmGatePath); err != nil || gate {
		return false, err
	}
	return true, nil
}

func applyTransitionVMAddon(ctx context.Context, path string) {
	for ctx.Err() == nil {
		client, err := adminClient()
		apply := false
		if err == nil {
			apply, err = transitionVMAddonMayApply(ctx, client, apiServer)
			client.CloseIdleConnections()
		}
		if err != nil {
			log.Printf("VM API transition add-on deferred: %v", err)
		}
		if apply && runTool(ctx, []string{"/usr/bin/kubectl", "--kubeconfig", kubeDir + "/admin.kubeconfig", "apply", "--server-side", "--force-conflicts", "-f", path}, nil, 2*time.Minute) {
			log.Printf("namespaced VM API add-on applied")
			return
		}
		if !sleepCtx(ctx, 5*time.Second) {
			return
		}
	}
}
