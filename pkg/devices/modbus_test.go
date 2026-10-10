package devices

import (
	"fmt"
	"io"
	"log"
	"net"
	"testing"
	"time"

	"github.com/simonvetter/modbus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
)

// plc answers like a device: fixed holding registers and coils.
type plc struct {
	holding map[uint16]uint16
	coils   map[uint16]bool
}

func (p *plc) HandleCoils(req *modbus.CoilsRequest) ([]bool, error) {
	out := make([]bool, req.Quantity)
	for i := range out {
		out[i] = p.coils[req.Addr+uint16(i)]
	}
	return out, nil
}

func (p *plc) HandleDiscreteInputs(req *modbus.DiscreteInputsRequest) ([]bool, error) {
	return make([]bool, req.Quantity), nil
}

func (p *plc) HandleHoldingRegisters(req *modbus.HoldingRegistersRequest) ([]uint16, error) {
	out := make([]uint16, req.Quantity)
	for i := range out {
		out[i] = p.holding[req.Addr+uint16(i)]
	}
	return out, nil
}

func (p *plc) HandleInputRegisters(req *modbus.InputRegistersRequest) ([]uint16, error) {
	return nil, modbus.ErrIllegalDataAddress
}

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestModbusRoundTrip(t *testing.T) {
	addr := freePort(t)
	srv, err := modbus.NewServer(&modbus.ServerConfiguration{URL: "tcp://" + addr, Timeout: 10 * time.Second, MaxClients: 2,
		Logger: log.New(io.Discard, "", 0)},
		&plc{holding: map[uint16]uint16{0: 853, 10: 0x41ac, 11: 0}, coils: map[uint16]bool{3: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	spec := v1.DeviceSpec{Protocol: "modbus-tcp", Address: addr, UnitID: 1, Timeout: metav1.Duration{Duration: time.Second},
		Points: []v1.Point{
			{Name: "temperature", Kind: "holding", Register: 0, Type: "int16", Scale: "0.1"},
			{Name: "flow", Kind: "holding", Register: 10, Type: "float32"},
			{Name: "running", Kind: "coil", Register: 3},
		}}
	d := newModbusDevice(spec)
	defer d.Close()
	r, err := d.Read()
	if err != nil {
		t.Fatal(err)
	}
	if r["temperature"] != 85.3 || r["flow"] != 21.5 || r["running"] != 1 {
		t.Fatalf("read %v", r)
	}
	// An illegal read fails the reading and the next one connects anew.
	bad := newModbusDevice(v1.DeviceSpec{Address: addr, Timeout: metav1.Duration{Duration: time.Second},
		Points: []v1.Point{{Name: "x", Kind: "input", Register: 0, Type: "uint16"}}})
	defer bad.Close()
	if _, err := bad.Read(); err == nil {
		t.Fatal("an illegal address was read")
	}
	if r, err := d.Read(); err != nil || r["temperature"] != 85.3 {
		t.Fatal(fmt.Sprint(r, err))
	}
}
