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

// modbusDevice reads a Modbus device: over its own TCP connection, or over
// the serial line it shares with the other devices on that port. Either is
// opened again after a failure.
type modbusDevice struct {
	spec   v1.DeviceSpec
	mu     sync.Mutex
	client *modbus.ModbusClient
	// bus is the serial line of a modbus-rtu device.
	bus *serialBus
}

func newModbusDevice(spec v1.DeviceSpec) *modbusDevice {
	d := &modbusDevice{spec: spec}
	if spec.Protocol == "modbus-rtu" {
		d.bus = buses.acquire(spec.Address, SerialSettings(spec))
	}
	return d
}

func unitID(spec v1.DeviceSpec) uint8 {
	if spec.UnitID == 0 {
		return 1
	}
	return uint8(spec.UnitID)
}

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
	if err := c.SetUnitId(unitID(d.spec)); err != nil {
		_ = c.Close()
		return err
	}
	d.client = c
	return nil
}

// Read reads every point of the device; a failure closes the connection
// for the next read to open anew.
func (d *modbusDevice) Read() (Reading, error) {
	if d.bus != nil {
		return d.bus.read(d.spec)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.open(); err != nil {
		return nil, err
	}
	out, err := readPoints(d.client, d.spec.Points)
	if err != nil {
		_ = d.client.Close()
		d.client = nil
	}
	return out, err
}

// readPoints reads each point over a client addressed to the device.
func readPoints(c *modbus.ModbusClient, points []v1.Point) (Reading, error) {
	out := Reading{}
	for _, p := range points {
		v, err := readPoint(c, p)
		if err != nil {
			return nil, err
		}
		out[p.Name] = v
	}
	return out, nil
}

func readPoint(c *modbus.ModbusClient, p v1.Point) (float64, error) {
	addr := uint16(p.Register)
	switch p.Kind {
	case "coil", "discrete":
		read := c.ReadCoils
		if p.Kind == "discrete" {
			read = c.ReadDiscreteInputs
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
		regs, err := c.ReadRegisters(addr, Registers(p), table)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", p.Name, err)
		}
		return Decode(p, regs, false)
	}
}

// Close drops the connection, or the device's share of its serial line.
func (d *modbusDevice) Close() {
	if d.bus != nil {
		buses.release(d.bus)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		_ = d.client.Close()
		d.client = nil
	}
}
