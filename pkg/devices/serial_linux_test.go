package devices

import (
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
	"github.com/kuberoot-dev/kuberoot/pkg/modbussim"
)

// serialLine is a pseudo-terminal standing in for a serial port: devices
// on the line answer on its master side, the node opens its slave side.
func serialLine(t *testing.T, line modbussim.Line) string {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	// The master side is raw too: no echo, no line editing.
	tio, err := unix.IoctlGetTermios(int(m.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	tio.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	tio.Oflag &^= unix.OPOST
	tio.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	if err := unix.IoctlSetTermios(int(m.Fd()), unix.TCSETS, tio); err != nil {
		t.Fatal(err)
	}
	go func() { _ = line.ServeRTU(m) }()
	t.Cleanup(func() { _ = m.Close() })
	return fmt.Sprintf("/dev/pts/%d", n)
}

func rtuSpec(port string, unit int32, points ...v1.Point) v1.DeviceSpec {
	return v1.DeviceSpec{Protocol: "modbus-rtu", Address: port, UnitID: unit,
		Serial:  &v1.Serial{BaudRate: 19200, Parity: "even"},
		Timeout: metav1.Duration{Duration: 300 * time.Millisecond}, Points: points}
}

func TestModbusRTUSharedLine(t *testing.T) {
	press, pump := modbussim.NewBank(), modbussim.NewBank()
	press.SetHolding(0, 853)
	press.SetCoil(0, true)
	pump.SetHolding(4, 1200)
	port := serialLine(t, modbussim.Line{1: press, 7: pump})

	a := newModbusDevice(rtuSpec(port, 1,
		v1.Point{Name: "temperature", Kind: "holding", Register: 0, Type: "int16", Scale: "0.1"},
		v1.Point{Name: "running", Kind: "coil", Register: 0}))
	b := newModbusDevice(rtuSpec(port, 7, v1.Point{Name: "rpm", Kind: "holding", Register: 4}))
	gone := newModbusDevice(rtuSpec(port, 9, v1.Point{Name: "x", Kind: "holding"}))
	defer a.Close()
	defer b.Close()
	if a.bus != b.bus || a.bus != gone.bus {
		t.Fatal("devices on one port do not share its line")
	}
	// A unit that does not answer leaves the line working for the others.
	if _, err := gone.Read(); err == nil {
		t.Fatal("a unit not on the line answered")
	}
	gone.Close()
	for range 3 {
		ra, err := a.Read()
		if err != nil || ra["temperature"] != 85.3 || ra["running"] != 1 {
			t.Fatalf("press: %v, %v", ra, err)
		}
		rb, err := b.Read()
		if err != nil || rb["rpm"] != 1200 {
			t.Fatalf("pump: %v, %v", rb, err)
		}
	}
	press.SetHolding(0, 901)
	if ra, err := a.Read(); err != nil || ra["temperature"] != 90.1 {
		t.Fatalf("after a change: %v, %v", ra, err)
	}
}

func TestModbusRTUNoPort(t *testing.T) {
	d := newModbusDevice(rtuSpec("/dev/ttyNOPE0", 1, v1.Point{Name: "x", Kind: "holding"}))
	defer d.Close()
	if _, err := d.Read(); err == nil {
		t.Fatal("read a port that is not there")
	}
}
