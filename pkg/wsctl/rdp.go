package wsctl

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

// An RDP client opens with an X.224 Connection Request in a TPKT. Its
// variable part may start with a line ending in CRLF: the routing token
// (mstsc's loadbalanceinfo, FreeRDP's /load-balance-info) or the
// "Cookie: mstshash=" one. The gateway takes its ticket from that line and
// passes the request on without it, so the desktop's xrdp sees an ordinary
// connection.

const maxTPKT = 4096

// readConnectionRequest reads the client's first TPKT.
func readConnectionRequest(r io.Reader) ([]byte, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, err
	}
	if head[0] != 3 {
		return nil, errors.New("not a TPKT")
	}
	n := int(binary.BigEndian.Uint16(head[2:]))
	if n < 11 || n > maxTPKT {
		return nil, errors.New("bad TPKT length")
	}
	pkt := make([]byte, n)
	copy(pkt, head)
	if _, err := io.ReadFull(r, pkt[4:]); err != nil {
		return nil, err
	}
	if pkt[5]&0xf0 != 0xe0 || int(pkt[4]) != n-5 {
		return nil, errors.New("not a connection request")
	}
	return pkt, nil
}

// takeTicket returns the ticket the request carries and the request
// without its line.
func takeTicket(pkt []byte) (string, []byte, error) {
	data := pkt[11:]
	end := strings.Index(string(data), "\r\n")
	if end < 0 {
		return "", nil, errors.New("no ticket: open the workspace's .rdp file from the gateway")
	}
	line := strings.TrimPrefix(string(data[:end]), "Cookie: ")
	if i := strings.LastIndexByte(line, '='); i >= 0 {
		line = line[i+1:]
	}
	cut := end + 2
	out := make([]byte, 0, len(pkt)-cut)
	out = append(out, pkt[:11]...)
	out = append(out, data[cut:]...)
	binary.BigEndian.PutUint16(out[2:], uint16(len(out)))
	out[4] = byte(len(out) - 5)
	return line, out, nil
}
