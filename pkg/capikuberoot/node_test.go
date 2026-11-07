package capikuberoot

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func kubeconfigData(t *testing.T, change func(*clientcmdapi.Config)) []byte {
	t.Helper()
	config := clientcmdapi.NewConfig()
	config.CurrentContext = "workload"
	config.Contexts["workload"] = &clientcmdapi.Context{Cluster: "workload", AuthInfo: "admin"}
	config.Clusters["workload"] = &clientcmdapi.Cluster{Server: "https://workload.invalid:6443", CertificateAuthorityData: []byte("test CA; fake transport never opens a connection")}
	config.AuthInfos["admin"] = &clientcmdapi.AuthInfo{Token: "unit-test-only-embedded-token"}
	if change != nil {
		change(config)
	}
	data, err := clientcmd.Write(*config)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (f *fixture) workloadSecret() *corev1.Secret {
	f.t.Helper()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "workload-kubeconfig", Namespace: testNS, UID: "workload-credentials", ResourceVersion: "1",
		Labels: map[string]string{ClusterLabel: "workload"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: Clusters.GroupVersion().String(), Kind: "Cluster", Name: "workload", UID: "cluster-a"}}},
		Type: "cluster.x-k8s.io/secret", Data: map[string][]byte{"value": kubeconfigData(f.t, nil)}}
	created, err := f.kube.CoreV1().Secrets(testNS).Create(context.Background(), secret, metav1.CreateOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	return created
}

func (f *fixture) workloadNode() *corev1.Node {
	f.t.Helper()
	vm := f.get(VirtualMachines, "worker")
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "node-a", ResourceVersion: "1", CreationTimestamp: metav1.NewTime(vm.GetCreationTimestamp().Add(time.Second))},
		Spec:   corev1.NodeSpec{Taints: []corev1.Taint{{Key: "keep-user-taint", Effect: corev1.TaintEffectNoSchedule}}},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.123.0.20"}}}}
	created, err := f.workload.CoreV1().Nodes().Create(context.Background(), node, metav1.CreateOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	return created
}

func (f *fixture) nodeCondition() corev1.ConditionStatus {
	f.t.Helper()
	for _, condition := range f.machine().Status.Conditions {
		if condition.Type == "NodeProviderIDReady" {
			return condition.Status
		}
	}
	f.t.Fatal("missing separate Node providerID condition")
	return corev1.ConditionUnknown
}

func TestRunningVMWaitsForNodeThenBindsOnlyItsIncarnation(t *testing.T) {
	f := newFixture(t)
	f.running()
	f.reconcile(1)
	if !f.machine().Status.Ready || f.nodeCondition() != corev1.ConditionFalse {
		t.Fatal("VM readiness incorrectly depends on an already registered Node")
	}
	f.workloadSecret()
	node := f.workloadNode()
	f.reconcile(2)
	machine := f.machine()
	if !machine.Status.Ready || machine.Status.NodeRef == nil || machine.Status.NodeRef.UID != node.UID || f.nodeCondition() != corev1.ConditionTrue {
		t.Fatalf("Node identity not bound: %#v", machine.Status)
	}
	actual, _ := f.workload.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
	if actual.Spec.ProviderID != "kuberoot://worker" || actual.Annotations[nodeMachineUID] != string(machine.UID) || actual.Annotations[nodeVMUID] != string(machine.Status.VMRef.UID) {
		t.Fatal("workload providerID or UID markers not set")
	}
	if len(actual.Spec.Taints) != 1 || actual.Spec.Taints[0].Key != "keep-user-taint" {
		t.Fatal("bridge changed user taints")
	}
	actual.UID = "new-node-same-name-and-ip"
	if _, err := f.workload.CoreV1().Nodes().Update(context.Background(), actual, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	f.reconcile(1)
	if f.nodeCondition() != corev1.ConditionFalse || f.machine().Status.NodeRef.UID != node.UID {
		t.Fatal("rebound the same name to a replacement Node UID")
	}
}

func TestNodeIdentityIsDurableBeforeWorkloadMutation(t *testing.T) {
	f := newFixture(t)
	f.running()
	f.workloadSecret()
	node := f.workloadNode()
	f.reconcile(1)
	if f.machine().Status.NodeRef == nil || f.machine().Status.NodeRef.UID != node.UID {
		t.Fatal("Node identity was not persisted first")
	}
	actual, err := f.workload.CoreV1().Nodes().Get(context.Background(), node.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if actual.Spec.ProviderID != "" || len(actual.Annotations) != 0 {
		t.Fatal("workload Node was modified before the management identity checkpoint")
	}
	actual.UID = "replacement-after-controller-crash"
	if _, err = f.workload.CoreV1().Nodes().Update(context.Background(), actual, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	before := len(f.workload.Actions())
	f.reconcile(1)
	if f.nodeCondition() != corev1.ConditionFalse || f.machine().Status.NodeRef.UID != node.UID {
		t.Fatal("Node replacement crossed the persisted identity barrier")
	}
	for _, action := range f.workload.Actions()[before:] {
		if action.GetVerb() == "update" {
			t.Fatal("modified a replacement Node")
		}
	}
}

func TestNodeBridgeRejectsWrongIdentity(t *testing.T) {
	for _, scenario := range []string{"foreignProvider", "foreignMachine", "foreignVM", "wrongAddress", "oldNode"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			f.running()
			f.workloadSecret()
			node := f.workloadNode()
			switch scenario {
			case "foreignProvider":
				node.Spec.ProviderID = "other://worker"
			case "foreignMachine":
				node.Annotations = map[string]string{nodeMachineUID: "somebody-else"}
			case "foreignVM":
				node.Annotations = map[string]string{nodeVMUID: "old-vm"}
			case "wrongAddress":
				node.Status.Addresses[0].Address = "10.123.0.21"
			case "oldNode":
				node.CreationTimestamp = metav1.NewTime(time.Unix(1, 0))
			}
			if _, err := f.workload.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			before := len(f.workload.Actions())
			f.reconcile(1)
			if f.nodeCondition() != corev1.ConditionFalse || f.machine().Status.NodeRef != nil {
				t.Fatal("foreign Node was accepted")
			}
			for _, action := range f.workload.Actions()[before:] {
				if action.GetVerb() == "update" {
					t.Fatal("modified an unverified workload Node")
				}
			}
		})
	}
}

func TestKamajiKubeconfigUsesControlPlaneOwnerChain(t *testing.T) {
	f := newFixture(t)
	cluster := f.get(Clusters, "workload")
	_ = unstructured.SetNestedMap(cluster.Object, map[string]any{"apiVersion": KamajiControlPlanes.GroupVersion().String(), "kind": "KamajiControlPlane", "name": "kamaji"}, "spec", "controlPlaneRef")
	f.update(Clusters, cluster)
	control := &unstructured.Unstructured{Object: map[string]any{"apiVersion": KamajiControlPlanes.GroupVersion().String(), "kind": "KamajiControlPlane", "metadata": map[string]any{"name": "kamaji", "namespace": testNS, "uid": "control-plane-a"}}}
	control.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: Clusters.GroupVersion().String(), Kind: "Cluster", Name: "workload", UID: "cluster-a"}})
	if err := f.dynamic.Tracker().Create(KamajiControlPlanes, control, testNS); err != nil {
		t.Fatal(err)
	}
	secret := f.workloadSecret()
	controller := true
	secret.OwnerReferences = []metav1.OwnerReference{{APIVersion: KamajiControlPlanes.GroupVersion().String(), Kind: "KamajiControlPlane", Name: "kamaji", UID: "control-plane-a", Controller: &controller}}
	if err := f.c.validateKubeconfigOwner(context.Background(), secret, cluster); err != nil {
		t.Fatal(err)
	}
	secret.OwnerReferences[0].UID = "old-control-plane"
	if err := f.c.validateKubeconfigOwner(context.Background(), secret, cluster); err == nil {
		t.Fatal("accepted kubeconfig from another control plane incarnation")
	}
	secret.OwnerReferences[0].UID = "control-plane-a"
	secret.Namespace = "tenant-b"
	if err := f.c.validateKubeconfigOwner(context.Background(), secret, cluster); err == nil {
		t.Fatal("accepted cross-namespace kubeconfig")
	}
}

func TestEmbeddedWorkloadCredentialsDoNotExecuteOrReadHostFiles(t *testing.T) {
	mutations := map[string]func(*clientcmdapi.Config){
		"exec": func(c *clientcmdapi.Config) {
			c.AuthInfos["admin"].Exec = &clientcmdapi.ExecConfig{Command: "never-run-this"}
		},
		"plugin": func(c *clientcmdapi.Config) {
			c.AuthInfos["admin"].AuthProvider = &clientcmdapi.AuthProviderConfig{Name: "gcp"}
		},
		"tokenFile":     func(c *clientcmdapi.Config) { c.AuthInfos["admin"].TokenFile = "/host/credential" },
		"clientFile":    func(c *clientcmdapi.Config) { c.AuthInfos["admin"].ClientKey = "/host/key" },
		"caFile":        func(c *clientcmdapi.Config) { c.Clusters["workload"].CertificateAuthority = "/host/ca" },
		"insecure":      func(c *clientcmdapi.Config) { c.Clusters["workload"].InsecureSkipTLSVerify = true },
		"http":          func(c *clientcmdapi.Config) { c.Clusters["workload"].Server = "http://workload.invalid" },
		"proxy":         func(c *clientcmdapi.Config) { c.Clusters["workload"].ProxyURL = "http://proxy.invalid" },
		"impersonation": func(c *clientcmdapi.Config) { c.AuthInfos["admin"].Impersonate = "other-user" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			secret := &corev1.Secret{Data: map[string][]byte{"value": kubeconfigData(t, mutate)}}
			_, err := embeddedWorkloadConfig(secret)
			if err == nil {
				t.Fatal("unsafe kubeconfig accepted")
			}
			if strings.Contains(err.Error(), "unit-test-only") {
				t.Fatal("credentials leaked into an error")
			}
		})
	}
	config, err := embeddedWorkloadConfig(&corev1.Secret{Data: map[string][]byte{"value": kubeconfigData(t, nil)}})
	if err != nil || config.Insecure || config.ExecProvider != nil || config.Proxy == nil {
		t.Fatalf("valid embedded credentials rejected: %v", err)
	}
}
