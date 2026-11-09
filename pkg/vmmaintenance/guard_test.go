package vmmaintenance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func processClient(service string, pid int64) *dynamicfake.FakeDynamicClient {
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "node.kuberoot.dev/v1alpha1", "kind": "NodeService", "metadata": map[string]any{"name": "node." + service}, "status": map[string]any{
		"state": "Running", "pid": pid, "controllerInventoryObserved": true, "startedAt": "2026-11-03T09:00:00Z", "controllerProcesses": []any{map[string]any{"pid": pid, "executable": "/usr/bin/" + service, "startTicks": int64(12345), "bootID": "boot-1"}}}}}
	return dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)
}

func TestAcknowledgementWaitsForInflightWork(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	g := &Guard{Principal: "controller", Mode: ModeAlpha2, Kube: client, Dynamic: processClient("kuberoot-vmctl", 42), Node: "node", Service: "kuberoot-vmctl", PID: 42, Identity: "process"}
	if err := g.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	entered, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_ = g.Work(ctx, func(context.Context) error { close(entered); <-finish; return nil })
		close(done)
	}()
	<-entered
	_, err := client.CoreV1().ConfigMaps(Namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ConfigMap}, Data: map[string]string{"token": "cutover-1"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refresh := make(chan error, 1)
	go func() { refresh <- g.Refresh(ctx) }()
	select {
	case err := <-refresh:
		t.Fatalf("acknowledged active work: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	lease, err := client.CoordinationV1().Leases(Namespace).Get(ctx, LeaseName("node", "kuberoot-vmctl"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Annotations["token"] != "" {
		t.Fatal("published maintenance acknowledgement too early")
	}
	close(finish)
	<-done
	if err := <-refresh; err != nil {
		t.Fatal(err)
	}
	lease, err = client.CoordinationV1().Leases(Namespace).Get(ctx, LeaseName("node", "kuberoot-vmctl"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Annotations["token"] != "cutover-1" || lease.Annotations["pid"] != "42" {
		t.Fatalf("ack=%+v", lease)
	}
	called := false
	_ = g.Work(ctx, func(context.Context) error { called = true; return nil })
	if called {
		t.Fatal("work ran behind barrier")
	}
}

func TestUnknownGateClosesWork(t *testing.T) {
	client := fake.NewSimpleClientset()
	g := &Guard{Principal: "controller", Mode: ModeAlpha2, Kube: client, Dynamic: processClient("kuberoot-wsctl", 7), Node: "node", Service: "kuberoot-wsctl", PID: 7, Identity: "standby"}
	ctx := context.Background()
	if err := g.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	client.PrependReactor("get", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("API unavailable") })
	if err := g.Refresh(ctx); err == nil {
		t.Fatal("read failure accepted")
	}
	_ = g.Work(ctx, func(context.Context) error { t.Fatal("read failure left stale permission"); return nil })
}

func TestInventoryMustMatchTheOnlySupervisedProcess(t *testing.T) {
	for _, test := range []string{"old API", "extra orphan", "wrong executable", "different PID", "inventory error"} {
		t.Run(test, func(t *testing.T) {
			client := processClient("kuberoot-vmctl", 42)
			resource := schema.GroupVersionResource{Group: "node.kuberoot.dev", Version: "v1alpha1", Resource: "nodeservices"}
			u, err := client.Resource(resource).Get(context.Background(), "node.kuberoot-vmctl", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			processes, _, _ := unstructured.NestedSlice(u.Object, "status", "controllerProcesses")
			switch test {
			case "old API":
				unstructured.RemoveNestedField(u.Object, "status", "controllerProcesses")
			case "extra orphan":
				processes = append(processes, processes[0])
				_ = unstructured.SetNestedSlice(u.Object, processes, "status", "controllerProcesses")
			case "wrong executable":
				processes[0].(map[string]any)["executable"] = "/tmp/kuberoot-vmctl"
				_ = unstructured.SetNestedSlice(u.Object, processes, "status", "controllerProcesses")
			case "different PID":
				_ = unstructured.SetNestedField(u.Object, int64(43), "status", "pid")
			case "inventory error":
				_ = unstructured.SetNestedField(u.Object, "cannot inspect proc", "status", "controllerInventoryError")
			}
			if _, err := ControllerIdentity(u, "kuberoot-vmctl"); err == nil {
				t.Fatal("unverified process accepted")
			}
		})
	}
}

func TestBarrierDrainsSeedHandlersAndRefusesNewRequests(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewSimpleClientset()
	g := &Guard{Principal: "controller", Mode: ModeAlpha2, Kube: kube, Dynamic: processClient("kuberoot-vmctl", 42), Node: "node", Service: "kuberoot-vmctl", PID: 42, Identity: "process"}
	if err := g.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	handler := g.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(204) }))
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://seed/old/user-data", nil))
		close(done)
	}()
	<-entered
	_, err := kube.CoreV1().ConfigMaps(Namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ConfigMap}, Data: map[string]string{"token": "cutover"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ack := make(chan error, 1)
	go func() { ack <- g.Refresh(ctx) }()
	select {
	case err := <-ack:
		t.Fatalf("acknowledged with an active seed handler: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-done
	if err := <-ack; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "http://seed/old/user-data", nil))
	if w.Code != 503 {
		t.Fatalf("paused seed request returned %d", w.Code)
	}
}
