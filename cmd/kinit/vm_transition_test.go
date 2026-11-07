package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func transitionCRD(scope, version string) string {
	return fmt.Sprintf(`{"apiVersion":"apiextensions.k8s.io/v1","kind":"CustomResourceDefinition","metadata":{"name":"virtualmachines.vm.kuberoot.dev"},"spec":{"group":"vm.kuberoot.dev","scope":%q,"versions":[{"name":%q,"served":true,"storage":true}]}}`, scope, version)
}

func TestTransitionAddonStagesOnlyVMOutsideBulkDirectory(t *testing.T) {
	root := t.TempDir()
	bundled, generated, separate := filepath.Join(root, "bundled"), filepath.Join(root, "bulk"), filepath.Join(root, "vm-only")
	for _, dir := range []string{bundled, generated} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	raw := []byte(transitionCRD("Namespaced", "v1alpha2"))
	for _, dir := range []string{bundled, generated} {
		if err := os.WriteFile(filepath.Join(dir, vmCRDFile), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(generated, "node-api.yaml")
	if err := os.WriteFile(other, []byte("keep other addons"), 0644); err != nil {
		t.Fatal(err)
	}
	path, err := stageTransitionVMAddon(bundled, generated, separate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(generated, vmCRDFile)); !os.IsNotExist(err) {
		t.Fatal("VM CRD still participates in ordinary bulk apply")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(raw) {
		t.Fatalf("separate CRD differs: %s %v", got, err)
	}
	if got, err := os.ReadFile(other); err != nil || string(got) != "keep other addons" {
		t.Fatal("unrelated addon was removed")
	}
	if err = os.WriteFile(filepath.Join(bundled, vmCRDFile), []byte(transitionCRD("Cluster", "v1alpha1")), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = stageTransitionVMAddon(bundled, generated, separate); err == nil {
		t.Fatal("transition image could reapply an alpha1 CRD")
	}
}

func TestTransitionAddonDefersLegacyAndEveryMaintenanceWindow(t *testing.T) {
	for _, test := range []struct {
		name, crd             string
		firstGate, secondGate int
		want                  bool
		wantError             bool
	}{
		{name: "legacy", crd: transitionCRD("Cluster", "v1alpha1"), firstGate: 404, secondGate: 404},
		{name: "new after cutover", crd: transitionCRD("Namespaced", "v1alpha2"), firstGate: 404, secondGate: 404, want: true},
		{name: "fresh cluster", firstGate: 404, secondGate: 404, want: true},
		{name: "gate before old CRD deletion", crd: transitionCRD("Cluster", "v1alpha1"), firstGate: 200, secondGate: 200},
		{name: "gate while CRD absent", firstGate: 200, secondGate: 200},
		{name: "gate after alpha2 import", crd: transitionCRD("Namespaced", "v1alpha2"), firstGate: 200, secondGate: 200},
		{name: "gate appeared during NotFound observation", firstGate: 404, secondGate: 200},
		{name: "unreadable first gate", firstGate: 403, secondGate: 404, wantError: true},
		{name: "unreadable second gate", firstGate: 404, secondGate: 503, wantError: true},
		{name: "unknown installed scope", crd: transitionCRD("Cluster", "v1alpha2"), firstGate: 404, secondGate: 404, wantError: true},
		{name: "malformed CRD", crd: "not-json", firstGate: 404, secondGate: 404, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var gates, crds atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("decision attempted an API mutation")
					w.WriteHeader(405)
					return
				}
				switch r.URL.Path {
				case vmGatePath:
					gates.Add(1)
					status := test.firstGate
					if gates.Load() > 1 {
						status = test.secondGate
					}
					w.WriteHeader(status)
				case vmCRDPath:
					crds.Add(1)
					if test.crd == "" {
						w.WriteHeader(404)
					} else {
						_, _ = w.Write([]byte(test.crd))
					}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			apply, err := transitionVMAddonMayApply(context.Background(), server.Client(), server.URL)
			if apply != test.want || (err != nil) != test.wantError {
				t.Fatalf("apply=%v err=%v", apply, err)
			}
			if test.firstGate == 200 && crds.Load() != 0 {
				t.Fatal("maintenance gate did not prevent scope observation/application")
			}
			if test.want && gates.Load() != 2 {
				t.Fatal("apply decision did not recheck maintenance gate")
			}
			if strings.Contains(test.name, "appeared") && gates.Load() != 2 {
				t.Fatal("stale decision was not rechecked")
			}
		})
	}
}
