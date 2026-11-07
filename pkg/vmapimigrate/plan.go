// Package vmapimigrate performs an explicit, resumable maintenance cutover
// from the cluster-scoped VM API to v1alpha2. It never deletes node Machines
// or Volumes. Its private plan contains Secrets and must not be logged.
package vmapimigrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

var (
	OldVM    = schema.GroupVersionResource{Group: "vm.kuberoot.dev", Version: "v1alpha1", Resource: "virtualmachines"}
	NewVM    = schema.GroupVersionResource{Group: "vm.kuberoot.dev", Version: "v1alpha2", Resource: "virtualmachines"}
	CRDs     = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	Machines = schema.GroupVersionResource{Group: "node.kuberoot.dev", Version: "v1alpha1", Resource: "machines"}
	Volumes  = schema.GroupVersionResource{Group: "node.kuberoot.dev", Version: "v1alpha1", Resource: "volumes"}
)

const (
	CRDName          = "virtualmachines.vm.kuberoot.dev"
	PlanAnnotation   = "vm.kuberoot.dev/import-plan"
	SourceAnnotation = "vm.kuberoot.dev/import-source-uid"
)

type Captured struct {
	Resource schema.GroupVersionResource `json:"resource"`
	Object   unstructured.Unstructured   `json:"object"`
}

type Record struct {
	Original  unstructured.Unstructured  `json:"original"`
	Namespace string                     `json:"namespace"`
	Secret    *unstructured.Unstructured `json:"secret,omitempty"`
}

type Plan struct {
	Version     int                         `json:"version"`
	ClusterUID  types.UID                   `json:"clusterUID"`
	GateUID     types.UID                   `json:"gateUID"`
	Token       string                      `json:"token"`
	APIServers  map[string]string           `json:"apiServers"`
	Executor    string                      `json:"executor"`
	OriginalCRD unstructured.Unstructured   `json:"originalCRD"`
	TargetCRD   unstructured.Unstructured   `json:"targetCRD"`
	Machines    []Record                    `json:"machines"`
	Workspaces  []unstructured.Unstructured `json:"workspaces,omitempty"`
	Dependents  []Captured                  `json:"dependents"`
	Backends    []Captured                  `json:"backends"`
}

// State is separate from the immutable, hashed backup. A process can crash
// after a server write but before this checkpoint; each phase also verifies
// server state and accepts only the same plan's already completed actions.
type State struct {
	PlanHash string                  `json:"planHash"`
	Phase    string                  `json:"phase"`
	UIDs     map[types.UID]types.UID `json:"uids"`
}

func Digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func SavePrivate(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".vm-api-plan-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func LoadPlan(path, expected string) (*Plan, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, "", fmt.Errorf("plan contains Secrets and must be readable only by its owner")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	hash := Digest(raw)
	if expected != "" && hash != expected {
		return nil, "", fmt.Errorf("plan SHA256 differs from the explicitly selected backup")
	}
	var p Plan
	if err = json.Unmarshal(raw, &p); err != nil {
		return nil, "", err
	}
	if err = p.Validate(); err != nil {
		return nil, "", err
	}
	return &p, hash, nil
}

func (p *Plan) Validate() error {
	if len(p.APIServers) == 0 {
		return fmt.Errorf("plan requires a direct HTTPS endpoint for every control-plane API server")
	}
	if p.Version != 1 || p.ClusterUID == "" || p.GateUID == "" || p.Token == "" || p.Executor == "" {
		return fmt.Errorf("plan is missing its cluster, barrier or executor identity")
	}
	oldScope, _, _ := unstructured.NestedString(p.OriginalCRD.Object, "spec", "scope")
	newScope, _, _ := unstructured.NestedString(p.TargetCRD.Object, "spec", "scope")
	if p.OriginalCRD.GetName() != CRDName || p.TargetCRD.GetName() != CRDName || oldScope != "Cluster" || newScope != "Namespaced" {
		return fmt.Errorf("plan must replace the exact cluster-scoped VM CRD with its namespaced successor")
	}
	versions, _, _ := unstructured.NestedSlice(p.TargetCRD.Object, "spec", "versions")
	if len(versions) != 1 {
		return fmt.Errorf("target CRD must serve only v1alpha2")
	}
	v, _ := versions[0].(map[string]any)
	if v["name"] != "v1alpha2" || v["served"] != true || v["storage"] != true {
		return fmt.Errorf("target CRD does not store and serve v1alpha2")
	}
	workspaceVMs := map[string]string{}
	for _, workspace := range p.Workspaces {
		if workspace.GetUID() == "" || workspace.GetDeletionTimestamp() != nil {
			return fmt.Errorf("workspace is missing identity or is deleting")
		}
		expected := "ws-" + workspace.GetName()
		machine, _, _ := unstructured.NestedString(workspace.Object, "status", "machine")
		if machine != "" && machine != expected {
			return fmt.Errorf("workspace %s has an unexpected machine binding", workspace.GetName())
		}
		workspaceVMs[expected] = workspace.GetName()
	}
	owners := map[types.UID]Record{}
	targets := map[string]bool{}
	names := map[string]bool{}
	secrets := map[string]map[string]any{}
	for _, r := range p.Machines {
		u := &r.Original
		if u.GetNamespace() != "" || u.GetUID() == "" || u.GetResourceVersion() == "" || len(validation.IsDNS1123Subdomain(u.GetName())) != 0 || len(validation.IsDNS1123Label(r.Namespace)) != 0 {
			return fmt.Errorf("invalid source VM or target namespace for %s", u.GetName())
		}
		for _, finalizer := range u.GetFinalizers() {
			if finalizer != "vm.kuberoot.dev/cleanup" {
				return fmt.Errorf("VM %s has an unknown finalizer %s", u.GetName(), finalizer)
			}
		}
		if u.GetDeletionTimestamp() != nil {
			return fmt.Errorf("VM %s is being deleted", u.GetName())
		}
		migration, exists, err := unstructured.NestedMap(u.Object, "status", "migration")
		if err != nil || exists && len(migration) > 0 {
			return fmt.Errorf("VM %s has an active migration", u.GetName())
		}
		phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
		if phase == "Migrating" || phase == "Moving" || phase == "Deleting" || phase == "Starting" {
			return fmt.Errorf("VM %s is not stable: %s", u.GetName(), phase)
		}
		if targets[r.Namespace+"/"+u.GetName()] || names[u.GetName()] {
			return fmt.Errorf("duplicate target or backend binding for %s", u.GetName())
		}
		if _, ok := owners[u.GetUID()]; ok {
			return fmt.Errorf("duplicate old VM UID")
		}
		workspace, actualWorkspace := workspaceVMs[u.GetName()]
		label := u.GetLabels()["workstation.kuberoot.dev/workspace"]
		if label != "" || actualWorkspace {
			if !actualWorkspace || label != workspace || r.Namespace != "workstation" {
				return fmt.Errorf("workspace VM %s must retain its verified workstation namespace binding", u.GetName())
			}
		}
		targets[r.Namespace+"/"+u.GetName()], names[u.GetName()] = true, true
		owners[u.GetUID()] = r
		ref, has, _ := unstructured.NestedMap(u.Object, "spec", "userDataSecret")
		if has {
			if r.Secret == nil || r.Secret.GetName() != ref["name"] || r.Secret.GetNamespace() != ref["namespace"] {
				return fmt.Errorf("VM %s has no exact Secret backup", u.GetName())
			}
			key := r.Namespace + "/" + r.Secret.GetName()
			data, _, _ := unstructured.NestedMap(r.Secret.Object, "data")
			if old, ok := secrets[key]; ok && !reflect.DeepEqual(old, data) {
				return fmt.Errorf("different source Secrets map to target %s", key)
			}
			secrets[key] = data
		}
	}
	checkRefs := func(namespace string, refs []metav1.OwnerReference) error {
		for _, ref := range refs {
			if owner, ok := owners[ref.UID]; ok && namespace != owner.Namespace {
				return fmt.Errorf("owner %s would cross namespace into %q; rehome the dependent before preparing", owner.Original.GetName(), namespace)
			}
		}
		return nil
	}
	for _, r := range p.Machines {
		if err := checkRefs(r.Namespace, r.Original.GetOwnerReferences()); err != nil {
			return err
		}
	}
	for _, d := range p.Dependents {
		if d.Object.GetUID() == "" || d.Object.GetDeletionTimestamp() != nil {
			return fmt.Errorf("dependent is deleting or has no UID")
		}
		if err := checkRefs(d.Object.GetNamespace(), d.Object.GetOwnerReferences()); err != nil {
			return err
		}
	}
	for _, b := range p.Backends {
		if b.Resource != Machines && b.Resource != Volumes {
			return fmt.Errorf("unexpected backend resource")
		}
		if b.Object.GetUID() == "" {
			return fmt.Errorf("backend has no UID")
		}
		if b.Resource == Machines {
			phase, _, _ := unstructured.NestedString(b.Object.Object, "status", "phase")
			if phase == "Sending" || phase == "Receiving" || phase == "Sent" || phase == "Deleting" {
				return fmt.Errorf("backend %s has active/terminal migration state %s", b.Object.GetName(), phase)
			}
		}
	}
	return ValidateBackendTopology(p.Machines, p.Backends)
}

func (r Record) Target(planHash string) *unstructured.Unstructured {
	u := r.Original.DeepCopy()
	u.SetAPIVersion("vm.kuberoot.dev/v1alpha2")
	u.SetNamespace(r.Namespace)
	u.SetUID("")
	u.SetResourceVersion("")
	u.SetGeneration(0)
	u.SetCreationTimestamp(metav1.Time{})
	u.SetManagedFields(nil)
	u.SetOwnerReferences(nil)
	annotations := u.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
	annotations[PlanAnnotation], annotations[SourceAnnotation] = planHash, string(r.Original.GetUID())
	u.SetAnnotations(annotations)
	unstructured.RemoveNestedField(u.Object, "spec", "userDataSecret", "namespace")
	delete(u.Object, "status")
	return u
}

func (r Record) Status() map[string]any {
	status, _, _ := unstructured.NestedMap(r.Original.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	status["backendID"], status["instanceID"] = r.Original.GetName(), string(r.Original.GetUID())
	return status
}

func RewriteOwners(refs []metav1.OwnerReference, uids map[types.UID]types.UID, detach bool) []metav1.OwnerReference {
	var out []metav1.OwnerReference
	for _, ref := range refs {
		if replacement, ok := uids[ref.UID]; ok {
			if detach {
				continue
			}
			ref.UID = replacement
			if strings.HasPrefix(ref.APIVersion, "vm.kuberoot.dev/") {
				ref.APIVersion = "vm.kuberoot.dev/v1alpha2"
			}
		}
		out = append(out, ref)
	}
	return out
}
