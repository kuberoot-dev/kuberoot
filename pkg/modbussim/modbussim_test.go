package modbussim

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func TestCRC(t *testing.T) {
	// Read holding registers 0 and 1 of unit 1: the frame ends C4 0B.
	if got := CRC([]byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x02}); got != 0x0BC4 {
		t.Fatalf("crc %04x", got)
	}
}

func frame(b ...byte) []byte { return binary.LittleEndian.AppendUint16(b, CRC(b)) }

func TestServeRTU(t *testing.T) {
	bank := NewBank()
	bank.SetHolding(0, 853)
	bank.SetCoil(3, true)
	client, line := net.Pipe()
	go func() { _ = Line{7: bank}.ServeRTU(line) }()
	defer client.Close()

	ask := func(req []byte, want []byte) {
		t.Helper()
		if _, err := client.Write(req); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(want))
		if _, err := client.Read(got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("asked % x, got % x, want % x", req, got, want)
		}
	}
	// A request for another unit, and one with a bad CRC, go unanswered;
	// the next one is answered.
	_, _ = client.Write(frame(0x05, 0x03, 0, 0, 0, 1))
	_, _ = client.Write([]byte{0x07, 0x03, 0, 0, 0, 1, 0, 0})
	ask(frame(0x07, 0x03, 0, 0, 0, 1), frame(0x07, 0x03, 2, 0x03, 0x55))
	ask(frame(0x07, 0x01, 0, 0, 0, 4), frame(0x07, 0x01, 1, 0x08))
	ask(frame(0x07, 0x06, 0, 1, 0, 42), frame(0x07, 0x06, 0, 1, 0, 42))
	if bank.Holding[1] != 42 {
		t.Fatalf("write: %v", bank.Holding)
	}
	ask(frame(0x07, 0x10, 0, 2, 0, 2, 4, 0, 1, 0, 2), frame(0x07, 0x10, 0, 2, 0, 2))
	if bank.Holding[2] != 1 || bank.Holding[3] != 2 {
		t.Fatalf("write multiple: %v", bank.Holding)
	}
	ask(frame(0x07, 0x03, 0, 0, 0, 200), frame(0x07, 0x83, illegalDataValue))
}
