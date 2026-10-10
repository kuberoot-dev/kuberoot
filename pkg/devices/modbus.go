package devices

import (
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/simonvetter/modbus"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
)

// Reading is a device read once: each point's value, by name.
type Reading map[string]float64

// modbusDevice reads a Modbus TCP device over one connection, opened again
// after a failure.
type modbusDevice struct {
	spec   v1.DeviceSpec
	mu     sync.Mutex
	client *modbus.ModbusClient
}

func newModbusDevice(spec v1.DeviceSpec) *modbusDevice { return &modbusDevice{spec: spec} }

func (d *modbusDevice) open() error {
	if d.client != nil {
		return nil
	}
	c, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL: "tcp://" + d.spec.Address, Timeout: d.spec.Timeout.Duration,
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		return err
	}
	if err := c.Open(); err != nil {
		return err
	}
	unit := d.spec.UnitID
	if unit == 0 {
		unit = 1
	}
	if err := c.SetUnitId(uint8(unit)); err != nil {
		_ = c.Close()
		return err
	}
	d.client = c
	return nil
}

// Read reads every point of the device; a failure closes the connection
// for the next read to open anew.
func (d *modbusDevice) Read() (Reading, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.open(); err != nil {
		return nil, err
	}
	out := Reading{}
	for _, p := range d.spec.Points {
		v, err := d.point(p)
		if err != nil {
			_ = d.client.Close()
			d.client = nil
			return nil, err
		}
		out[p.Name] = v
	}
	return out, nil
}

func (d *modbusDevice) point(p v1.Point) (float64, error) {
	addr := uint16(p.Register)
	switch p.Kind {
	case "coil", "discrete":
		read := d.client.ReadCoils
		if p.Kind == "discrete" {
			read = d.client.ReadDiscreteInputs
		}
		bits, err := read(addr, 1)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", p.Name, err)
		}
		return Decode(p, nil, len(bits) > 0 && bits[0])
	default:
		table := modbus.HOLDING_REGISTER
		if p.Kind == "input" {
			table = modbus.INPUT_REGISTER
		}
		regs, err := d.client.ReadRegisters(addr, Registers(p), table)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", p.Name, err)
		}
		return Decode(p, regs, false)
	}
}

// Close drops the connection.
func (d *modbusDevice) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		_ = d.client.Close()
		d.client = nil
	}
}
