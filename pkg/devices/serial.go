package devices

import (
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/simonvetter/modbus"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
)

// SerialSettings is how a modbus-rtu device's line runs, its defaults
// filled in: 9600 baud, 8 data bits, no parity, one stop bit.
func SerialSettings(spec v1.DeviceSpec) v1.Serial {
	s := v1.Serial{BaudRate: 9600, DataBits: 8, Parity: "none", StopBits: 1}
	if spec.Serial != nil {
		if spec.Serial.BaudRate != 0 {
			s.BaudRate = spec.Serial.BaudRate
		}
		if spec.Serial.DataBits != 0 {
			s.DataBits = spec.Serial.DataBits
		}
		if spec.Serial.Parity != "" {
			s.Parity = spec.Serial.Parity
		}
		if spec.Serial.StopBits != 0 {
			s.StopBits = spec.Serial.StopBits
		}
	}
	return s
}

// serialBus is a serial line and the devices on it. A line carries one
// request at a time, so the devices on a port take turns over one open
// port, each addressing its own unit; two opens of one port would mix
// their bytes.
type serialBus struct {
	port     string
	settings v1.Serial
	refs     int

	mu     sync.Mutex
	client *modbus.ModbusClient
}

type serialBuses struct {
	mu    sync.Mutex
	ports map[string]*serialBus
}

var buses = &serialBuses{ports: map[string]*serialBus{}}

// acquire takes a share of a port's line. A device that wants other
// settings than the line runs with gets a line of its own and fails to
// open it while the other holds the port: CheckSerial refuses that
// configuration before it gets here.
func (b *serialBuses) acquire(port string, settings v1.Serial) *serialBus {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bus, ok := b.ports[port]; ok && bus.settings == settings {
		bus.refs++
		return bus
	}
	bus := &serialBus{port: port, settings: settings, refs: 1}
	if _, taken := b.ports[port]; !taken {
		b.ports[port] = bus
	}
	return bus
}

// release gives up a share; the last one closes the port.
func (b *serialBuses) release(bus *serialBus) {
	b.mu.Lock()
	bus.refs--
	last := bus.refs <= 0
	if last && b.ports[bus.port] == bus {
		delete(b.ports, bus.port)
	}
	b.mu.Unlock()
	if last {
		bus.mu.Lock()
		bus.close()
		bus.mu.Unlock()
	}
}

func (bus *serialBus) open(timeout time.Duration) error {
	if bus.client != nil {
		return nil
	}
	parity := map[string]uint{"none": modbus.PARITY_NONE, "even": modbus.PARITY_EVEN, "odd": modbus.PARITY_ODD}[bus.settings.Parity]
	c, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL: "rtu://" + bus.port, Timeout: timeout,
		Speed: uint(bus.settings.BaudRate), DataBits: uint(bus.settings.DataBits),
		Parity: parity, StopBits: uint(bus.settings.StopBits),
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		return err
	}
	if err := c.Open(); err != nil {
		return fmt.Errorf("serial port %s: %w", bus.port, err)
	}
	bus.client = c
	return nil
}

func (bus *serialBus) close() {
	if bus.client != nil {
		_ = bus.client.Close()
		bus.client = nil
	}
}

// read reads a device's points on the line. A device that does not
// answer leaves the port open for the others; a port that fails is opened
// again on the next read.
func (bus *serialBus) read(spec v1.DeviceSpec) (Reading, error) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if err := bus.open(timeout(spec)); err != nil {
		return nil, err
	}
	if err := bus.client.SetUnitId(unitID(spec)); err != nil {
		return nil, err
	}
	out, err := readPoints(bus.client, spec.Points)
	// A Modbus error (no answer, a bad frame, an exception) is the device's;
	// any other is the port's.
	var me modbus.Error
	if err != nil && !errors.As(err, &me) {
		bus.close()
	}
	return out, err
}

// CheckSerial says which modbus-rtu devices share a port but not its
// settings: a line runs at one speed and framing.
func CheckSerial(devices []v1.Device) map[string]string {
	problems := map[string]string{}
	first := map[string]v1.Device{}
	for _, d := range devices {
		if d.Spec.Protocol != "modbus-rtu" {
			continue
		}
		f, ok := first[d.Spec.Address]
		if !ok {
			first[d.Spec.Address] = d
			continue
		}
		if SerialSettings(f.Spec) != SerialSettings(d.Spec) {
			problems["Device/"+d.Name] = fmt.Sprintf("serial port %s runs with device %s's settings; devices on a port share them", d.Spec.Address, f.Name)
		}
	}
	return problems
}
