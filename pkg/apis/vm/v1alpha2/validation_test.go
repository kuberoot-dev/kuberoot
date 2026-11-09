package v1alpha2

import (
	"context"
	"os"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"
)

// Exercise the generated rules with Kubernetes' validator: comments alone do
// not prove that quantity CEL expressions compile or protect optional fields.
func TestResourceCeilingAdmission(t *testing.T) {
	raw, err := os.ReadFile("../../../../distros/hypervisor/addons/vm.kuberoot.dev_virtualmachines.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	var props apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &props, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := schema.NewStructural(&props)
	if err != nil {
		t.Fatal(err)
	}
	validator := cel.NewValidator(structural, true, celconfig.PerCallLimit)
	if validator == nil {
		t.Fatal("generated CRD has no CEL validator")
	}
	bounded := func() map[string]any {
		return map[string]any{"cpus": int64(1), "maxCPUs": int64(4), "memory": "1Gi", "maxMemory": "4Gi"}
	}
	legacy := func() map[string]any { return map[string]any{"cpus": int64(1), "memory": "513Mi"} }
	seeded := func() map[string]any {
		s := bounded()
		s["seedMode"] = "NoCloudNet"
		return s
	}
	object := func(s map[string]any) any {
		if s == nil {
			return nil
		}
		return map[string]any{"apiVersion": "vm.kuberoot.dev/v1alpha2", "kind": "VirtualMachine", "metadata": map[string]any{"name": "test"}, "spec": s}
	}
	for _, test := range []struct {
		name    string
		old     map[string]any
		current map[string]any
		change  func(map[string]any)
		valid   bool
	}{
		{name: "create", current: bounded(), valid: true},
		{name: "grow", old: bounded(), current: bounded(), change: func(s map[string]any) { s["cpus"] = int64(2); s["memory"] = "2Gi" }, valid: true},
		{name: "same quantity", old: bounded(), current: bounded(), change: func(s map[string]any) { s["maxMemory"] = "4096Mi" }, valid: true},
		{name: "CPU exceeds ceiling", current: bounded(), change: func(s map[string]any) { s["cpus"] = int64(5) }},
		{name: "RAM exceeds ceiling", current: bounded(), change: func(s map[string]any) { s["memory"] = "8Gi" }},
		{name: "RAM alignment", current: bounded(), change: func(s map[string]any) { s["memory"] = "1025Mi" }},
		{name: "RAM overflow", current: bounded(), change: func(s map[string]any) { s["maxMemory"] = "100Ei" }},
		{name: "change CPU ceiling", old: bounded(), current: bounded(), change: func(s map[string]any) { s["maxCPUs"] = int64(8) }},
		{name: "change RAM ceiling", old: bounded(), current: bounded(), change: func(s map[string]any) { s["maxMemory"] = "8Gi" }},
		{name: "remove CPU ceiling", old: bounded(), current: bounded(), change: func(s map[string]any) { delete(s, "maxCPUs") }},
		{name: "remove RAM ceiling", old: bounded(), current: bounded(), change: func(s map[string]any) { delete(s, "maxMemory") }},
		{name: "add CPU ceiling", old: legacy(), current: legacy(), change: func(s map[string]any) { s["maxCPUs"] = int64(4) }},
		{name: "legacy arbitrary RAM", current: legacy(), valid: true},
		{name: "legacy resize", old: legacy(), current: legacy(), change: func(s map[string]any) { s["cpus"] = int64(2); s["memory"] = "1025Mi" }, valid: true},
		{name: "create network seed", current: seeded(), valid: true},
		{name: "keep network seed", old: seeded(), current: seeded(), valid: true},
		{name: "change seed protocol", old: seeded(), current: seeded(), change: func(s map[string]any) { s["seedMode"] = "NoCloud" }},
		{name: "remove seed protocol", old: seeded(), current: seeded(), change: func(s map[string]any) { delete(s, "seedMode") }},
		{name: "add seed protocol", old: bounded(), current: seeded()},
		{name: "explicit legacy seed is also immutable", old: bounded(), current: bounded(), change: func(s map[string]any) { s["seedMode"] = "NoCloud" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.change != nil {
				test.change(test.current)
			}
			errs, _ := validator.Validate(context.Background(), field.NewPath("root"), structural, object(test.current), object(test.old), celconfig.RuntimeCELCostBudget)
			if (len(errs) == 0) != test.valid {
				t.Fatalf("valid=%v errors=%v", test.valid, errs)
			}
		})
	}
	for _, variant := range []string{"unchanged", "remove backend", "remove instance", "remove status", "null status"} {
		t.Run("binding "+variant, func(t *testing.T) {
			old := object(bounded()).(map[string]any)
			old["status"] = map[string]any{"backendID": "kept-backend", "instanceID": "kept-instance"}
			current := object(bounded()).(map[string]any)
			current["status"] = map[string]any{"backendID": "kept-backend", "instanceID": "kept-instance"}
			switch variant {
			case "remove backend":
				delete(current["status"].(map[string]any), "backendID")
			case "remove instance":
				delete(current["status"].(map[string]any), "instanceID")
			case "remove status":
				delete(current, "status")
			case "null status":
				current["status"] = nil
			}
			errs, _ := validator.Validate(context.Background(), field.NewPath("root"), structural, current, old, celconfig.RuntimeCELCostBudget)
			if (len(errs) == 0) != (variant == "unchanged") {
				t.Fatalf("whole binding invariant variant=%s errors=%v", variant, errs)
			}
		})
	}

}
