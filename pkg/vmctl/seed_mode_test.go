package vmctl

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/vm/v1alpha2"
	"sigs.k8s.io/yaml"
)

func TestSeedModePreservesLegacyLaunchAndOptsIntoNetwork(t *testing.T) {
	seedBase.Store("http://10.123.0.1:8091")
	defer seedBase.Store("")
	m := tenantVM("alice", "web", "uid-a")
	m.Status = v1.VirtualMachineStatus{BackendID: "old-web", InstanceID: "old-uid"}
	m.Spec.UserData = "bootstrap"
	legacy := machineSpec(&m, Plan{MAC: MAC("old-web")}, true)
	if legacy.SystemSerial != "ds=nocloud;s=http://10.123.0.1:8091/old-web/" {
		t.Fatalf("legacy serial changed: %q", legacy.SystemSerial)
	}
	m.Spec.SeedMode = v1.SeedModeNoCloud
	if current := machineSpec(&m, Plan{MAC: MAC("old-web")}, true); !reflect.DeepEqual(current, legacy) {
		t.Fatalf("explicit NoCloud changed legacy launch: old=%+v new=%+v", legacy, current)
	}
	m.Spec.SeedMode = v1.SeedModeNoCloudNet
	if got := systemSerial(&m); got != "ds=nocloud-net;s=http://10.123.0.1:8091/old-web/" {
		t.Fatalf("network serial=%q", got)
	}
	m.Spec.UserData = ""
	if got := systemSerial(&m); got != "" {
		t.Fatalf("no bootstrap data but serial=%q", got)
	}
	m.Spec.UserDataSecret = &v1.SecretRef{Name: "bootstrap"}
	if got := systemSerial(&m); got != "ds=nocloud-net;s=http://10.123.0.1:8091/old-web/" {
		t.Fatalf("Secret bootstrap serial=%q", got)
	}
}

func TestNetworkSeedIsOptInAndBoundToGuest(t *testing.T) {
	for _, test := range []struct {
		name, mac, path, host string
		mode                  v1.SeedMode
		duplicate             bool
		status                int
	}{
		{name: "legacy absent", mac: "52:54:00:AA:BB:CC", path: "vm-uid-a", host: "10.123.0.3", status: 404},
		{name: "explicit NoCloud absent", mode: v1.SeedModeNoCloud, mac: "52:54:00:AA:BB:CC", path: "vm-uid-a", host: "10.123.0.3", status: 404},
		{name: "network config", mode: v1.SeedModeNoCloudNet, mac: "52:54:00:AA:BB:CC", path: "vm-uid-a", host: "10.123.0.3", status: 200},
		{name: "numeric MAC stays a YAML string", mode: v1.SeedModeNoCloudNet, mac: "52:54:00:12:34:56", path: "vm-uid-a", host: "10.123.0.3", status: 200},
		{name: "wrong source", mode: v1.SeedModeNoCloudNet, mac: "52:54:00:aa:bb:cc", path: "vm-uid-a", host: "10.123.0.4", status: 403},
		{name: "tenant name is not backend", mode: v1.SeedModeNoCloudNet, mac: "52:54:00:aa:bb:cc", path: "web", host: "10.123.0.3", status: 404},
		{name: "invalid MAC fails closed", mode: v1.SeedModeNoCloudNet, mac: "invalid", path: "vm-uid-a", host: "10.123.0.3", status: 503},
		{name: "non Ethernet MAC fails closed", mode: v1.SeedModeNoCloudNet, mac: "52:54:00:aa:bb:cc:00:01", path: "vm-uid-a", host: "10.123.0.3", status: 503},
		{name: "ambiguous binding fails closed", mode: v1.SeedModeNoCloudNet, mac: "52:54:00:aa:bb:cc", path: "vm-uid-a", host: "10.123.0.3", duplicate: true, status: 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := tenantVM("alice", "web", "uid-a")
			m.Spec.SeedMode = test.mode
			m.Status = v1.VirtualMachineStatus{BackendID: "vm-uid-a", InstanceID: "uid-a", MAC: test.mac, Address: "10.123.0.3"}
			machines := []v1.VirtualMachine{m}
			if test.duplicate {
				other := m.DeepCopy()
				other.Namespace = "bob"
				machines = append(machines, *other)
			}
			c := &Controller{Dynamic: identityClient(machines...), StableAddresses: true}
			req := httptest.NewRequest("GET", "http://gateway/"+test.path+"/network-config", nil)
			req.RemoteAddr = test.host + ":1234"
			w := httptest.NewRecorder()
			c.seedHandler().ServeHTTP(w, req)
			if w.Code != test.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if test.status != 200 {
				return
			}
			// Some YAML 1.1 consumers interpret an unquoted numeric MAC as
			// a sexagesimal integer even when this Go YAML decoder does not.
			if !strings.Contains(w.Body.String(), "macaddress: \""+strings.ToLower(test.mac)+"\"\n") {
				t.Fatalf("MAC is not quoted for YAML 1.1 consumers: %s", w.Body.String())
			}
			var config map[string]any
			if err := yaml.Unmarshal(w.Body.Bytes(), &config); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"version": float64(2), "ethernets": map[string]any{"vm": map[string]any{"match": map[string]any{"macaddress": strings.ToLower(test.mac)}, "dhcp4": true}}}
			if !reflect.DeepEqual(config, want) {
				t.Fatalf("network-config=%#v; want %#v", config, want)
			}
		})
	}
}
