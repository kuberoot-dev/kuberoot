package main

import (
	"fmt"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The add-ons go in with one kubectl apply: an invalid object in the
// aggregation manifest kept every add-on out, the node API's APIService too.
func TestAggregationManifestParses(t *testing.T) {
	manifest := fmt.Sprintf(aggregationTemplate, "n1", "10.0.0.1", "n1", "Q0E=", "Q0E=")
	kinds := 0
	for _, doc := range strings.Split(manifest, "\n---\n") {
		var obj struct {
			Kind     string
			Webhooks []struct {
				Rules []struct{ Resources []string }
			}
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("%v:\n%s", err, doc)
		}
		kinds++
		for _, w := range obj.Webhooks {
			for _, r := range w.Rules {
				for _, res := range r.Resources {
					// The API refuses "*/*" next to anything else.
					if res == "*/*" && len(r.Resources) > 1 {
						t.Errorf("rule resources %v", r.Resources)
					}
				}
			}
		}
	}
	if kinds != 4 {
		t.Errorf("%d objects, want Service, EndpointSlice, APIService and the webhook", kinds)
	}
}
