package devices

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/tym83/kuberoot/pkg/apis/devices/v1alpha1"
)

// bmc serves a small Redfish tree, as a BMC with one system, one chassis and
// one manager does.
func bmc(t *testing.T, user, pass string) *httptest.Server {
	tree := map[string]string{
		"/redfish/v1/":                                  `{"Systems":{"@odata.id":"/redfish/v1/Systems"},"Chassis":{"@odata.id":"/redfish/v1/Chassis"},"Managers":{"@odata.id":"/redfish/v1/Managers"}}`,
		"/redfish/v1/Systems":                           `{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}],"Members@odata.count":1}`,
		"/redfish/v1/Systems/1":                         `{"Status":{"Health":"Warning","State":"Enabled"},"PowerState":"On","LogServices":{"@odata.id":"/redfish/v1/Systems/1/LogServices"}}`,
		"/redfish/v1/Systems/1/LogServices":             `{"Members":[{"@odata.id":"/redfish/v1/Systems/1/LogServices/SEL"}]}`,
		"/redfish/v1/Systems/1/LogServices/SEL":         `{"Entries":{"@odata.id":"/redfish/v1/Systems/1/LogServices/SEL/Entries"}}`,
		"/redfish/v1/Systems/1/LogServices/SEL/Entries": `{"Members":[],"Members@odata.count":17}`,
		"/redfish/v1/Chassis":                           `{"Members":[{"@odata.id":"/redfish/v1/Chassis/1U"}]}`,
		"/redfish/v1/Chassis/1U":                        `{"Thermal":{"@odata.id":"/redfish/v1/Chassis/1U/Thermal"},"Power":{"@odata.id":"/redfish/v1/Chassis/1U/Power"}}`,
		"/redfish/v1/Chassis/1U/Thermal": `{"Temperatures":[{"Name":"CPU1 Temp","ReadingCelsius":41.5},{"Name":"Inlet","ReadingCelsius":null}],
			"Fans":[{"Name":"BaseBoard System Fan","Reading":2100,"ReadingUnits":"RPM","Status":{"Health":"OK"}}]}`,
		"/redfish/v1/Chassis/1U/Power": `{"PowerControl":[{"Name":"System Power Control","PowerConsumedWatts":344}],
			"PowerSupplies":[{"Name":"Power Supply Bay","MemberId":"0","Status":{"Health":"OK"}},
			                 {"Name":"Power Supply Bay","MemberId":"1","Status":{"Health":"Critical"}}]}`,
		"/redfish/v1/Managers":     `{"Members":[{"@odata.id":"/redfish/v1/Managers/BMC"}]}`,
		"/redfish/v1/Managers/BMC": `{"Status":{"Health":"OK"}}`,
	}
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != pass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := tree[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
}

func TestRedfish(t *testing.T) {
	srv := bmc(t, "root", "calvin")
	defer srv.Close()
	spec := v1.DeviceSpec{Protocol: "redfish", Address: srv.URL, TLS: &v1.TLS{InsecureSkipVerify: true}}
	creds := func() (map[string]string, error) {
		return map[string]string{"username": "root", "password": "calvin"}, nil
	}
	r, err := newReader(spec, creds).Read()
	if err != nil {
		t.Fatal(err)
	}
	want := Reading{
		"system_1_health": 1, "system_1_powered_on": 1, "system_1_log_sel_entries": 17,
		"temperature_cpu1_temp_celsius": 41.5, "fan_baseboard_system_fan_rpm": 2100, "fan_baseboard_system_fan_health": 0,
		"power_system_power_control_watts": 344,
		// Two power supplies of one name: the second by its member id.
		"psu_power_supply_bay_health": 0, "psu_power_supply_bay_1_health": 2,
	}
	for k, v := range want {
		if r[k] != v {
			t.Errorf("%s = %v, want %v", k, r[k], v)
		}
	}
	if _, ok := r["temperature_inlet_celsius"]; ok {
		t.Error("a sensor with no reading was reported")
	}
	// Points pick, and scale.
	spec.Points = []v1.Point{{Name: "power_system_power_control_watts", Scale: "0.001"}}
	r, _ = newReader(spec, creds).Read()
	if len(r) != 1 || r["power_system_power_control_watts"] != 0.344 {
		t.Errorf("picked %v", r)
	}
	// Wrong credentials: the BMC does not answer, the device is down.
	bad := func() (map[string]string, error) { return map[string]string{"username": "root", "password": "x"}, nil }
	if _, err := newReader(spec, bad).Read(); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong credentials read: %v", err)
	}
}

func TestProbes(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ok.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer broken.Close()
	r, err := newReader(v1.DeviceSpec{Protocol: "http", Address: ok.URL}, nil).Read()
	if err != nil || r["up"] != 1 || r["status"] != 200 || r["latency_seconds"] <= 0 {
		t.Errorf("http up: %v %v", r, err)
	}
	r, err = newReader(v1.DeviceSpec{Protocol: "http", Address: broken.URL}, nil).Read()
	if err == nil || r["up"] != 0 || r["status"] != 503 {
		t.Errorf("http 503: %v %v", r, err)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	r, err = newReader(v1.DeviceSpec{Protocol: "tcp", Address: addr}, nil).Read()
	if err != nil || r["up"] != 1 {
		t.Errorf("tcp up: %v %v", r, err)
	}
	_ = l.Close()
	r, err = newReader(v1.DeviceSpec{Protocol: "tcp", Address: addr, Timeout: metav1.Duration{Duration: time.Second}}, nil).Read()
	if err == nil || r["up"] != 0 {
		t.Errorf("tcp closed: %v %v", r, err)
	}
	// ICMP needs raw sockets: root only.
	r, err = newReader(v1.DeviceSpec{Protocol: "icmp", Address: "127.0.0.1"}, nil).Read()
	if err != nil && strings.Contains(err.Error(), "icmp:") {
		t.Skipf("no raw ICMP here: %v", err)
	}
	if err != nil || r["up"] != 1 {
		t.Errorf("icmp to localhost: %v %v", r, err)
	}
}

func TestSNMPValues(t *testing.T) {
	cases := []struct {
		pdu  gosnmp.SnmpPDU
		want float64
		bad  bool
	}{
		{gosnmp.SnmpPDU{Name: ".1", Type: gosnmp.Integer, Value: -5}, -5, false},
		{gosnmp.SnmpPDU{Name: ".1", Type: gosnmp.Counter64, Value: uint64(1 << 40)}, 1 << 40, false},
		{gosnmp.SnmpPDU{Name: ".1", Type: gosnmp.Gauge32, Value: uint(230)}, 230, false},
		{gosnmp.SnmpPDU{Name: ".1", Type: gosnmp.TimeTicks, Value: uint32(12345)}, 12345, false},
		{gosnmp.SnmpPDU{Name: ".1", Type: gosnmp.OctetString, Value: []byte(" 21.5 ")}, 21.5, false},
		{gosnmp.SnmpPDU{Name: ".1", Type: gosnmp.OctetString, Value: []byte("Cisco")}, 0, true},
		{gosnmp.SnmpPDU{Name: ".1", Type: gosnmp.NoSuchObject}, 0, true},
	}
	for i, c := range cases {
		v, err := snmpValue(c.pdu)
		if (err != nil) != c.bad || (!c.bad && v != c.want) {
			t.Errorf("case %d: %v %v", i, v, err)
		}
	}
}

func TestUSM(t *testing.T) {
	p, flags, err := usm(map[string]string{"username": "mon", "authProtocol": "sha256", "authPassword": "a", "privProtocol": "aes", "privPassword": "p"})
	if err != nil || flags != gosnmp.AuthPriv || p.AuthenticationProtocol != gosnmp.SHA256 || p.PrivacyProtocol != gosnmp.AES {
		t.Fatalf("%+v %v %v", p, flags, err)
	}
	if _, _, err := usm(map[string]string{"username": "mon", "privProtocol": "AES"}); err == nil {
		t.Error("privacy without authentication accepted")
	}
	if _, _, err := usm(map[string]string{}); err == nil {
		t.Error("no username accepted")
	}
}

func TestHeartbeat(t *testing.T) {
	var beats int
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { beats++ }))
	defer receiver.Close()
	result := `[{"metric":{},"value":[1,"1"]}]`
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":` + result + `}}`))
	}))
	defer prom.Close()
	h := newHeartbeat(v1.HeartbeatSpec{URL: receiver.URL, Query: &v1.HeartbeatQuery{URL: prom.URL, Expr: "up"}})
	if err := h.beat(context.Background()); err != nil || beats != 1 {
		t.Fatalf("beat: %v, %d beats", err, beats)
	}
	// The monitoring stopped collecting: no beat.
	result = `[]`
	if err := h.beat(context.Background()); err == nil || beats != 1 {
		t.Fatalf("beat with nothing collected: %v, %d beats", err, beats)
	}
}

func TestMetrics(t *testing.T) {
	c := &Controller{devices: map[string]*device{
		"bmc1": {spec: v1.DeviceSpec{Protocol: "redfish"}, connected: true, reading: Reading{"system_1_health": 0}, lastOK: time.Unix(100, 0)},
		"web":  {spec: v1.DeviceSpec{Protocol: "http"}, reading: Reading{"up": 0}},
	}, beats: map[string]*heartbeat{"dms": {healthy: true, sent: 3}}}
	want := `
# HELP kuberoot_device_up 1 when the last reads of the device succeeded.
# TYPE kuberoot_device_up gauge
kuberoot_device_up{device="bmc1",protocol="redfish"} 1
kuberoot_device_up{device="web",protocol="http"} 0
# HELP kuberoot_device_value A value of the device, as last read.
# TYPE kuberoot_device_value gauge
kuberoot_device_value{device="bmc1",point="system_1_health",protocol="redfish",unit=""} 0
kuberoot_device_value{device="web",point="up",protocol="http",unit=""} 0
# HELP kuberoot_heartbeat_healthy 1 when the heartbeat's last beat went.
# TYPE kuberoot_heartbeat_healthy gauge
kuberoot_heartbeat_healthy{heartbeat="dms"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "kuberoot_device_up", "kuberoot_device_value", "kuberoot_heartbeat_healthy"); err != nil {
		t.Fatal(err)
	}
}

func TestShown(t *testing.T) {
	got := shown(nil, Reading{"b": 1, "a": 2})
	if len(got) != 2 || got[0].Name != "a" {
		t.Fatalf("%v", got)
	}
	if got := shown([]v1.Point{{Name: "x"}}, Reading{"a": 1}); len(got) != 1 || got[0].Name != "x" {
		t.Fatalf("declared points ignored: %v", got)
	}
}
