package devices

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
)

func TestCheckSerial(t *testing.T) {
	dev := func(name, port string, s *v1.Serial) v1.Device {
		return v1.Device{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1.DeviceSpec{Protocol: "modbus-rtu", Address: port, Serial: s, Points: []v1.Point{{Name: "x"}}}}
	}
	cfg := Config{Devices: []v1.Device{
		dev("a", "/dev/ttyUSB0", nil),
		dev("b", "/dev/ttyUSB0", &v1.Serial{BaudRate: 9600, Parity: "none"}), // the defaults, spelled out
		dev("c", "/dev/ttyUSB0", &v1.Serial{BaudRate: 19200}),
		dev("d", "/dev/ttyUSB1", &v1.Serial{BaudRate: 19200}),
	}, Routes: []v1.Route{{ObjectMeta: metav1.ObjectMeta{Name: "r"},
		Spec: v1.RouteSpec{Device: "c", To: v1.Destination{MQTT: &v1.MQTT{Broker: "tcp://b:1883", Topic: "t"}}}}}}
	ok, problems := Check(cfg)
	if len(ok.Devices) != 3 || problems["Device/c"] == "" || problems["Device/a"] != "" || problems["Device/b"] != "" {
		t.Fatalf("devices %v, problems %v", ok.Devices, problems)
	}
	if problems["Route/r"] == "" || len(ok.Routes) != 0 {
		t.Fatalf("a route to a device the node does not run: %v", problems)
	}
}
