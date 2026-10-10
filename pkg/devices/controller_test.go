package devices

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
)

func dev(name string) v1.Device {
	return v1.Device{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1.DeviceSpec{Protocol: "modbus-tcp", Address: "plc:502", Points: press}}
}

func rt(name, device, when string) v1.Route {
	return v1.Route{ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.RouteSpec{Device: device, When: when, To: v1.Destination{MQTT: &v1.MQTT{Broker: "tcp://broker:1883", Topic: "t"}}}}
}

func TestCheck(t *testing.T) {
	cfg := Config{Devices: []v1.Device{dev("press")}, Routes: []v1.Route{
		rt("alarm", "press", "temperature > 80"),
		rt("ghost", "nope", ""),
		rt("typo", "press", "temprature > 80"),
	}}
	ok, problems := Check(cfg)
	if len(ok.Routes) != 1 || ok.Routes[0].Name != "alarm" {
		t.Fatalf("kept %v", ok.Routes)
	}
	if !strings.Contains(problems["Route/ghost"], "no device") || !strings.Contains(problems["Route/typo"], "when") {
		t.Fatalf("problems %v", problems)
	}
}

func TestBroken(t *testing.T) {
	cand := Config{Devices: []v1.Device{dev("press")}, Routes: []v1.Route{rt("alarm", "press", "")}}
	before := map[string]bool{"device press": true, "route alarm": true, "device gone": true}
	if b := Broken(before, map[string]bool{"device press": true, "route alarm": true}, cand); b != "" {
		t.Fatalf("a removed device counts as broken: %s", b)
	}
	b := Broken(before, map[string]bool{"device press": true}, cand)
	if !strings.Contains(b, "route alarm") || strings.Contains(b, "device press") {
		t.Fatalf("broken: %s", b)
	}
}

func TestRevisionIgnoresOrderAndStatus(t *testing.T) {
	a := Config{Devices: []v1.Device{dev("a"), dev("b")}}
	b := Config{Devices: []v1.Device{dev("b"), dev("a")}}
	b.Devices[0].Status.Connected = true
	if Revision(a) != Revision(b) {
		t.Fatal("order or status changed the revision")
	}
	c := Config{Devices: []v1.Device{dev("a"), dev("b")}}
	c.Devices[1].Spec.Address = "other:502"
	if Revision(a) == Revision(c) {
		t.Fatal("a spec change kept the revision")
	}
}
