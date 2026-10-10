// Package devices runs a gateway node's devices.kuberoot.dev resources: it
// reads the devices, evaluates the routes and publishes what they send,
// keeping the configuration under a Safeguard. No pod and no container
// runtime take part.
package devices

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	v1 "github.com/tym83/kuberoot/pkg/apis/devices/v1alpha1"
)

// Registers a point takes in its table: two for 32-bit types.
func Registers(p v1.Point) uint16 {
	switch p.Type {
	case "int32", "uint32", "float32":
		return 2
	}
	return 1
}

// IsBit: the point is read from coils or discrete inputs.
func IsBit(p v1.Point) bool { return p.Kind == "coil" || p.Kind == "discrete" }

// Decode turns what was read of a point into its value, scaled: registers
// for register tables, the high word first for 32-bit types, or a bit.
func Decode(p v1.Point, regs []uint16, bit bool) (float64, error) {
	if IsBit(p) {
		if bit {
			return 1, nil
		}
		return 0, nil
	}
	if len(regs) < int(Registers(p)) {
		return 0, fmt.Errorf("point %s: %d registers read, %d needed", p.Name, len(regs), Registers(p))
	}
	var v float64
	switch p.Type {
	case "int16":
		v = float64(int16(regs[0]))
	case "", "uint16":
		v = float64(regs[0])
	case "int32":
		v = float64(int32(uint32(regs[0])<<16 | uint32(regs[1])))
	case "uint32":
		v = float64(uint32(regs[0])<<16 | uint32(regs[1]))
	case "float32":
		v = float64(math.Float32frombits(uint32(regs[0])<<16 | uint32(regs[1])))
	case "bool":
		if regs[0] != 0 {
			v = 1
		}
	default:
		return 0, fmt.Errorf("point %s: no type %q", p.Name, p.Type)
	}
	scale, err := Scale(p)
	if err != nil {
		return 0, err
	}
	// As many decimals as the scale has: 853 * 0.1 is 85.3, not
	// 85.30000000000001.
	if d := decimals(p.Scale); d > 0 {
		pow := math.Pow10(d)
		return math.Round(v*scale*pow) / pow, nil
	}
	return v * scale, nil
}

// scaled multiplies a value that may have decimals of its own, as Redfish
// and SNMP send them, and drops the binary noise of the product: 344 *
// 0.001 is 0.344, not 0.34400000000000003.
func scaled(p v1.Point, v float64) (float64, error) {
	s, err := Scale(p)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strconv.FormatFloat(v*s, 'g', 12, 64), 64)
}

func decimals(scale string) int {
	if i := strings.IndexByte(scale, '.'); i >= 0 {
		return len(scale) - i - 1
	}
	return 0
}

// Scale is the point's multiplier, 1 when it has none.
func Scale(p v1.Point) (float64, error) {
	if p.Scale == "" {
		return 1, nil
	}
	s, err := strconv.ParseFloat(p.Scale, 64)
	if err != nil {
		return 0, fmt.Errorf("point %s: scale %q: %w", p.Name, p.Scale, err)
	}
	return s, nil
}

// Format writes a value for people: bits as true or false, the rest with
// no more digits than they have.
func Format(p v1.Point, v float64) string {
	if IsBit(p) || p.Type == "bool" {
		return strconv.FormatBool(v != 0)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
