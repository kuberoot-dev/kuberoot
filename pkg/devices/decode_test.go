package devices

import (
	"math"
	"testing"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
)

func TestDecode(t *testing.T) {
	f := math.Float32bits(21.5)
	for _, c := range []struct {
		p    v1.Point
		regs []uint16
		bit  bool
		want float64
		text string
	}{
		{v1.Point{Name: "a", Type: "uint16"}, []uint16{65535}, false, 65535, "65535"},
		{v1.Point{Name: "a", Type: "int16"}, []uint16{65535}, false, -1, "-1"},
		{v1.Point{Name: "a", Type: "int16", Scale: "0.1"}, []uint16{853}, false, 85.3, "85.3"},
		{v1.Point{Name: "a", Type: "int32"}, []uint16{0xffff, 0xfffe}, false, -2, "-2"},
		{v1.Point{Name: "a", Type: "uint32"}, []uint16{1, 0}, false, 65536, "65536"},
		{v1.Point{Name: "a", Type: "float32"}, []uint16{uint16(f >> 16), uint16(f)}, false, 21.5, "21.5"},
		{v1.Point{Name: "a", Kind: "coil"}, nil, true, 1, "true"},
		{v1.Point{Name: "a", Kind: "discrete"}, nil, false, 0, "false"},
	} {
		got, err := Decode(c.p, c.regs, c.bit)
		if err != nil || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%+v: %v %v, want %v", c.p, got, err, c.want)
			continue
		}
		if s := Format(c.p, got); s != c.text {
			t.Errorf("%+v formats as %q, want %q", c.p, s, c.text)
		}
	}
	if _, err := Decode(v1.Point{Name: "a", Type: "int32"}, []uint16{1}, false); err == nil {
		t.Error("a 32-bit value read from one register")
	}
	if Registers(v1.Point{Type: "float32"}) != 2 || Registers(v1.Point{Type: "int16"}) != 1 {
		t.Error("register counts")
	}
}
