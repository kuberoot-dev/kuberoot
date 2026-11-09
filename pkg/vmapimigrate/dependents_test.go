package vmapimigrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

type inventoryDiscovery struct {
	*discoveryfake.FakeDiscovery
	resources []*metav1.APIResourceList
}

func (d inventoryDiscovery) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	return d.resources, nil
}

func credentialDiscovery() *metav1.APIResourceList {
	return &metav1.APIResourceList{GroupVersion: "node.kuberoot.dev/v1alpha1", APIResources: []metav1.APIResource{{
		Name: "kubeconfigs", SingularName: "kubeconfig", Kind: "Kubeconfig", Verbs: []string{"get", "list"},
	}}}
}

func inventoryWithCredentialResource(c *Client, extra *metav1.APIResourceList) {
	resources, _ := c.Discovery.ServerPreferredResources()
	c.Discovery = inventoryDiscovery{resources: append(resources, extra)}
}

func TestDependentInventoryNeverListsSyntheticCredentials(t *testing.T) {
	c, p, dyn := migrationFixture(t)
	inventoryWithCredentialResource(c, credentialDiscovery())
	dyn.PrependReactor("list", "kubeconfigs", func(ktesting.Action) (bool, runtime.Object, error) {
		t.Fatal("synthetic credential payload must never be requested")
		return true, nil, errors.New("credential disclosure")
	})
	objects, err := c.dependentInventory(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Object.GetUID() != p.Machines[0].Secret.GetUID() {
		t.Fatalf("real Secret owner reference disappeared: %#v", objects)
	}
	for _, action := range dyn.Actions() {
		if action.GetResource().Resource == "kubeconfigs" {
			t.Fatalf("credential API called: %s", action.GetVerb())
		}
	}
}

func TestDependentInventoryRejectsSyntheticCredentialShapeDrift(t *testing.T) {
	for _, variant := range []string{"version", "kind", "namespaced", "create", "watch", "missing list", "duplicate verb", "group override", "version override"} {
		t.Run(variant, func(t *testing.T) {
			c, p, dyn := migrationFixture(t)
			resource := credentialDiscovery()
			r := &resource.APIResources[0]
			switch variant {
			case "version":
				resource.GroupVersion = "node.kuberoot.dev/v1alpha2"
			case "kind":
				r.Kind = "StoredKubeconfig"
			case "namespaced":
				r.Namespaced = true
			case "create":
				r.Verbs = append(r.Verbs, "create")
			case "watch":
				r.Verbs = append(r.Verbs, "watch")
			case "missing list":
				r.Verbs = []string{"get"}
			case "duplicate verb":
				r.Verbs = []string{"get", "get"}
			case "group override":
				r.Group = "example.invalid"
			case "version override":
				r.Version = "v1alpha2"
			}
			inventoryWithCredentialResource(c, resource)
			_, err := c.dependentInventory(context.Background(), p)
			if err == nil || !strings.Contains(err.Error(), "discovery changed") {
				t.Fatalf("changed credential handler accepted: %v", err)
			}
			for _, action := range dyn.Actions() {
				if action.GetResource().Resource == "kubeconfigs" {
					t.Fatal("shape failure requested credential payload")
				}
			}
		})
	}
}

func TestDependentInventoryStillRequiresUnknownAndSecretLists(t *testing.T) {
	for _, variant := range []string{"unknown read-only", "foreign kubeconfigs", "secrets"} {
		t.Run(variant, func(t *testing.T) {
			c, p, dyn := migrationFixture(t)
			inventoryWithCredentialResource(c, credentialDiscovery())
			gvr := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
			if variant != "secrets" {
				gvr = schema.GroupVersionResource{Group: "example.invalid", Version: "v1", Resource: "readonlyobjects"}
				if variant == "foreign kubeconfigs" {
					gvr.Resource = "kubeconfigs"
				}
				inventoryWithCredentialResource(c, &metav1.APIResourceList{GroupVersion: gvr.GroupVersion().String(), APIResources: []metav1.APIResource{{Name: gvr.Resource, Kind: "Kubeconfig", Verbs: []string{"get", "list"}}}})
				dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
					{Version: "v1", Resource: "secrets"}:    "SecretList",
					{Version: "v1", Resource: "configmaps"}: "ConfigMapList",
					gvr:                                     "KubeconfigList",
				}, p.Machines[0].Secret.DeepCopy())
				c.Dynamic = dyn
			}
			listed := false
			dyn.PrependReactor("list", gvr.Resource, func(action ktesting.Action) (bool, runtime.Object, error) {
				if action.GetResource() != gvr {
					return false, nil, nil
				}
				listed = true
				return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("intentional inventory denial"))
			})
			_, err := c.dependentInventory(context.Background(), p)
			if !listed || err == nil || !strings.Contains(err.Error(), "inventory owner references") {
				t.Fatalf("inventory denial bypassed: listed=%v err=%v", listed, err)
			}
		})
	}
}
