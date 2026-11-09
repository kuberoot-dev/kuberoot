package vmmaintenance

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestControllerStartupModeNeverConfusesScopes(t *testing.T) {
	for _, test := range []struct{ scope, version, want string }{
		{"Cluster", ModeAlpha1, ModeAlpha1},
		{"Namespaced", ModeAlpha2, ModeAlpha2},
		{"Cluster", ModeAlpha2, ""},
		{"Namespaced", ModeAlpha1, ""},
		{"Namespaced", "v1alpha3", ""},
	} {
		t.Run(test.scope+"/"+test.version, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]any{"name": "virtualmachines.vm.kuberoot.dev"}, "spec": map[string]any{"scope": test.scope, "versions": []any{map[string]any{"name": test.version, "served": true, "storage": true}}}}}
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), u)
			mode, err := APIMode(context.Background(), client)
			if mode != test.want || (err == nil) != (test.want != "") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			if (APIReady(context.Background(), client) == nil) != (test.want == ModeAlpha2) {
				t.Fatal("alpha2 guard accepted a different scope")
			}
			if (LegacyAPIReady(context.Background(), client) == nil) != (test.want == ModeAlpha1) {
				t.Fatal("legacy guard accepted a different scope")
			}
		})
	}
}
