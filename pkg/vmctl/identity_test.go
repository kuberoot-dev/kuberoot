package vmctl

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha2"
)

func tenantVM(namespace, name, uid string) v1.VirtualMachine {
	return v1.VirtualMachine{TypeMeta: metav1.TypeMeta{APIVersion: v1.GroupVersion.String(), Kind: "VirtualMachine"},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(uid)}}
}

func identityClient(vms ...v1.VirtualMachine) *fake.FakeDynamicClient {
	objects := make([]runtime.Object, 0, len(vms))
	for i := range vms {
		objects = append(objects, &unstructured.Unstructured{Object: toMap(&vms[i])})
	}
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{vmsGVR: "VirtualMachineList"}, objects...)
}

func TestTenantNamesBindToDifferentBackends(t *testing.T) {
	dyn := identityClient(tenantVM("alice", "web", "uid-alice"), tenantVM("bob", "web", "uid-bob"))
	c := &Controller{Dynamic: dyn}
	machines, err := c.boundMachines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 2 || backend(&machines[0]) == backend(&machines[1]) {
		t.Fatalf("bindings=%+v", machines)
	}
	reservations, err := addressReservations(netip.MustParsePrefix("10.123.0.0/24"), machines)
	if err != nil || reservations["alice/web"].MAC == reservations["bob/web"].MAC || reservations["alice/web"].Address == reservations["bob/web"].Address {
		t.Fatalf("tenant collision: %v, %v", reservations, err)
	}
	for i := range machines {
		m := &machines[i]
		u, err := dyn.Resource(vmsGVR).Namespace(m.Namespace).Get(context.Background(), m.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		id, _, _ := unstructured.NestedString(u.Object, "status", "backendID")
		if id != "vm-"+string(m.UID) || machineSpec(m, Plan{}, false).Volumes[0] != id {
			t.Fatalf("unpersisted/wrong backend: %v", u)
		}
	}
	count := len(dyn.Actions())
	if _, err := c.boundMachines(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, action := range dyn.Actions()[count:] {
		if action.GetVerb() != "list" {
			t.Fatalf("binding was not idempotent: %v", action)
		}
	}
}

func TestBindingFailuresNeverPublishOrAdoptByName(t *testing.T) {
	for _, test := range []string{"placed without binding", "duplicate backend", "duplicate guest", "namespace absent", "failed checkpoint"} {
		t.Run(test, func(t *testing.T) {
			a, b := tenantVM("alice", "web", "uid-a"), tenantVM("bob", "web", "uid-b")
			a.Status = v1.VirtualMachineStatus{BackendID: "old-web", InstanceID: "old-uid", Node: "node-a", ReplicaNodes: []string{"node-a"}, ImageWritten: true}
			switch test {
			case "placed without binding":
				a.Status.BackendID = ""
			case "duplicate backend":
				b.Status = v1.VirtualMachineStatus{BackendID: a.Status.BackendID, InstanceID: "other"}
			case "duplicate guest":
				b.Status = v1.VirtualMachineStatus{BackendID: "other", InstanceID: a.Status.InstanceID}
			case "namespace absent":
				b.Namespace = ""
			}
			dyn := identityClient(a, b)
			if test == "failed checkpoint" {
				dyn.PrependReactor("update", "virtualmachines", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("checkpoint failed") })
			}
			c := &Controller{Dynamic: dyn, Network: netip.MustParsePrefix("10.123.0.0/24"), StateDir: t.TempDir()}
			if reservations, err := c.reserveAddresses(context.Background()); err == nil || reservations != nil {
				t.Fatalf("unsafe bindings accepted: %v, %v", reservations, err)
			}
			for _, action := range dyn.Actions() {
				if action.GetResource().Group == "node.kuberoot.dev" {
					t.Fatalf("node resource touched: %v", action)
				}
			}
		})
	}
}

func TestImportedIdentityKeepsDiskSerialAndInstance(t *testing.T) {
	m := tenantVM("tenant", "desk", "new-api-uid")
	m.Status = v1.VirtualMachineStatus{BackendID: "desk", InstanceID: "old-api-uid", MAC: MAC("desk"), Address: "10.123.0.3", Node: "a", ReplicaNodes: []string{"a"}, Minor: 100, Port: 7800, ImageWritten: true}
	m.Spec.UserData = "private"
	seedBase.Store("http://10.123.0.1:8091")
	defer seedBase.Store("")
	dyn := identityClient(m)
	c := &Controller{Dynamic: dyn, StableAddresses: true}
	bound, err := c.boundMachines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := machineSpec(&bound[0], Plan{MAC: m.Status.MAC}, true)
	if got.Volumes[0] != "desk" || got.SystemSerial != "ds=nocloud;s=http://10.123.0.1:8091/desk/" || got.MAC != MAC("desk") {
		t.Fatalf("import changed launch identity: %+v", got)
	}
	req := httptest.NewRequest("GET", "http://gateway/desk/meta-data", nil)
	req.RemoteAddr = "10.123.0.3:1234"
	w := httptest.NewRecorder()
	c.seedHandler().ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "instance-id: old-api-uid\n") {
		t.Fatalf("guest identity changed: %d %s", w.Code, w.Body.String())
	}
}

func TestSeedSecretsAreLocalAndBackendPathsUnambiguous(t *testing.T) {
	a, b := tenantVM("alice", "web", "uid-a"), tenantVM("bob", "web", "uid-b")
	a.Status = v1.VirtualMachineStatus{BackendID: "vm-uid-a", InstanceID: "uid-a", MAC: MAC("vm-uid-a"), Address: "10.123.0.3"}
	b.Status = v1.VirtualMachineStatus{BackendID: "vm-uid-b", InstanceID: "uid-b", MAC: MAC("vm-uid-b"), Address: "10.123.0.4"}
	a.Spec.UserDataSecret = &v1.SecretRef{Name: "bootstrap"}
	b.Spec.UserDataSecret = &v1.SecretRef{Name: "bootstrap"}
	kube := kubefake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bootstrap", Namespace: "alice"}, Data: map[string][]byte{"userData": []byte("alice-data")}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bootstrap", Namespace: "bob"}, Data: map[string][]byte{"userData": []byte("bob-data")}})
	c := &Controller{Dynamic: identityClient(a, b), Kube: kube, StableAddresses: true}
	for _, test := range []struct {
		path, host, want string
		status           int
	}{{"vm-uid-a", "10.123.0.3", "alice-data", 200}, {"vm-uid-b", "10.123.0.3", "", 403}, {"web", "10.123.0.3", "", 404}} {
		req := httptest.NewRequest("GET", "http://gateway/"+test.path+"/user-data", nil)
		req.RemoteAddr = test.host + ":1234"
		w := httptest.NewRecorder()
		c.seedHandler().ServeHTTP(w, req)
		if w.Code != test.status || test.want != "" && w.Body.String() != test.want {
			t.Fatalf("%+v: %d %s", test, w.Code, w.Body.String())
		}
	}
	b.Status.BackendID = a.Status.BackendID
	c.Dynamic = identityClient(a, b)
	req := httptest.NewRequest("GET", "http://gateway/vm-uid-a/user-data", nil)
	req.RemoteAddr = "10.123.0.3:1234"
	w := httptest.NewRecorder()
	c.seedHandler().ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("ambiguous backend served credentials: %d", w.Code)
	}
}
