// Package modbussim plays Modbus devices for tests and demos: register
// banks that take reads and writes over Modbus TCP, as a handler of
// github.com/simonvetter/modbus, and over Modbus RTU framing on any byte
// stream: a serial port, a pseudo-terminal, or a TCP socket standing in
// for a serial line.
package modbussim

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/simonvetter/modbus"
)

// Bank is one device's registers and coils.
type Bank struct {
	mu      sync.Mutex
	Holding map[uint16]uint16
	Coils   map[uint16]bool
	// Writes, when set, hears every write.
	Writes func(table string, addr, value uint16)
}

// NewBank is a bank with nothing set: every register 0, every coil off.
func NewBank() *Bank {
	return &Bank{Holding: map[uint16]uint16{}, Coils: map[uint16]bool{}}
}

func (b *Bank) SetHolding(addr, v uint16) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Holding[addr] = v
}

func (b *Bank) SetCoil(addr uint16, v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Coils[addr] = v
}

func (b *Bank) coils(addr, n uint16, write []bool) []bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]bool, n)
	for i := range out {
		a := addr + uint16(i)
		if write != nil {
			b.Coils[a] = write[i]
			if b.Writes != nil {
				b.Writes("coil", a, boolWord(write[i]))
			}
		}
		out[i] = b.Coils[a]
	}
	return out
}

func (b *Bank) registers(addr, n uint16, write []uint16) []uint16 {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]uint16, n)
	for i := range out {
		a := addr + uint16(i)
		if write != nil {
			b.Holding[a] = write[i]
			if b.Writes != nil {
				b.Writes("holding", a, write[i])
			}
		}
		out[i] = b.Holding[a]
	}
	return out
}

func boolWord(v bool) uint16 {
	if v {
		return 1
	}
	return 0
}

// The Modbus TCP handler: the bank answers whatever unit is asked.

func (b *Bank) HandleCoils(req *modbus.CoilsRequest) ([]bool, error) {
	var w []bool
	if req.IsWrite {
		w = req.Args
	}
	return b.coils(req.Addr, req.Quantity, w), nil
}

func (b *Bank) HandleDiscreteInputs(req *modbus.DiscreteInputsRequest) ([]bool, error) {
	return make([]bool, req.Quantity), nil
}

func (b *Bank) HandleHoldingRegisters(req *modbus.HoldingRegistersRequest) ([]uint16, error) {
	var w []uint16
	if req.IsWrite {
		w = req.Args
	}
	return b.registers(req.Addr, req.Quantity, w), nil
}

func (b *Bank) HandleInputRegisters(req *modbus.InputRegistersRequest) ([]uint16, error) {
	return make([]uint16, req.Quantity), nil
}

// Line is a serial line with devices on it, by unit address.
type Line map[uint8]*Bank

// Exception codes.
const (
	illegalFunction  = 0x01
	illegalDataValue = 0x03
)

// ServeRTU answers RTU requests read from rw until it fails: the devices
// on the line answer theirs, a request for no device on it, or with a bad
// CRC, goes unanswered, as on a real line.
func (l Line) ServeRTU(rw io.ReadWriter) error {
	for {
		frame, err := readRequest(rw)
		if err != nil {
			return err
		}
		if len(frame) < 4 || CRC(frame[:len(frame)-2]) != binary.LittleEndian.Uint16(frame[len(frame)-2:]) {
			continue
		}
		bank, ok := l[frame[0]]
		if !ok {
			continue
		}
		reply := bank.answer(frame[:len(frame)-2])
		reply = binary.LittleEndian.AppendUint16(reply, CRC(reply))
		if _, err := rw.Write(reply); err != nil {
			return err
		}
	}
}

// readRequest reads one request frame: its length follows from its
// function code.
func readRequest(r io.Reader) ([]byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, err
	}
	switch head[1] {
	case 1, 2, 3, 4, 5, 6:
		rest := make([]byte, 6)
		_, err := io.ReadFull(r, rest)
		return append(head, rest...), err
	case 15, 16:
		rest := make([]byte, 5)
		if _, err := io.ReadFull(r, rest); err != nil {
			return nil, err
		}
		tail := make([]byte, int(rest[4])+2)
		_, err := io.ReadFull(r, tail)
		return append(append(head, rest...), tail...), err
	}
	// A function this line does not know: its length is unknown, so
	// nothing after it can be framed either.
	return nil, fmt.Errorf("function %d: %w", head[1], errors.ErrUnsupported)
}

// answer is the reply to a request without its CRC, and the reply without
// one.
func (b *Bank) answer(req []byte) []byte {
	unit, fc := req[0], req[1]
	addr := binary.BigEndian.Uint16(req[2:4])
	arg := binary.BigEndian.Uint16(req[4:6])
	fail := func(code byte) []byte { return []byte{unit, fc | 0x80, code} }
	switch fc {
	case 1, 2:
		if arg == 0 || arg > 2000 {
			return fail(illegalDataValue)
		}
		bits := make([]bool, arg)
		if fc == 1 {
			bits = b.coils(addr, arg, nil)
		}
		out := []byte{unit, fc, byte((arg + 7) / 8)}
		packed := make([]byte, (arg+7)/8)
		for i, v := range bits {
			if v {
				packed[i/8] |= 1 << (i % 8)
			}
		}
		return append(out, packed...)
	case 3, 4:
		if arg == 0 || arg > 125 {
			return fail(illegalDataValue)
		}
		regs := make([]uint16, arg)
		if fc == 3 {
			regs = b.registers(addr, arg, nil)
		}
		out := []byte{unit, fc, byte(2 * arg)}
		for _, r := range regs {
			out = binary.BigEndian.AppendUint16(out, r)
		}
		return out
	case 5:
		if arg != 0xFF00 && arg != 0 {
			return fail(illegalDataValue)
		}
		b.coils(addr, 1, []bool{arg == 0xFF00})
		return append([]byte(nil), req...)
	case 6:
		b.registers(addr, 1, []uint16{arg})
		return append([]byte(nil), req...)
	case 15:
		if arg == 0 || int(req[6]) != int(arg+7)/8 || len(req) < 7+int(req[6]) {
			return fail(illegalDataValue)
		}
		bits := make([]bool, arg)
		for i := range bits {
			bits[i] = req[7+i/8]&(1<<(i%8)) != 0
		}
		b.coils(addr, arg, bits)
		return append([]byte(nil), req[:6]...)
	case 16:
		if arg == 0 || int(req[6]) != 2*int(arg) || len(req) < 7+int(req[6]) {
			return fail(illegalDataValue)
		}
		regs := make([]uint16, arg)
		for i := range regs {
			regs[i] = binary.BigEndian.Uint16(req[7+2*i:])
		}
		b.registers(addr, arg, regs)
		return append([]byte(nil), req[:6]...)
	}
	return fail(illegalFunction)
}

// CRC is the Modbus RTU checksum of a frame, sent low byte first.
func CRC(frame []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, c := range frame {
		crc ^= uint16(c)
		for range 8 {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
