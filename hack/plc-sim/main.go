// Command plc-sim plays a PLC for trying the iot distribution: a Modbus TCP
// server whose holding registers and coils start at the values given and
// take writes, and, with -rtu, the same PLC as a Modbus RTU device on a
// serial line carried over TCP, as a VM's serial port connected to a
// socket sees it. Usage:
//
//	plc-sim serve -listen tcp://0.0.0.0:5020 -holding 0=700 -coil 0=1 [-rtu 0.0.0.0:5021 -unit 7]
//	plc-sim set -addr tcp://plc:5020 -holding 0=853
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/simonvetter/modbus"

	"github.com/kuberoot-dev/kuberoot/pkg/modbussim"
)

// pairs reads "address=value" flags.
type pairs map[uint16]int64

func (p pairs) String() string { return fmt.Sprint(map[uint16]int64(p)) }
func (p pairs) Set(s string) error {
	a, v, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("%q: address=value", s)
	}
	addr, err := strconv.ParseUint(a, 10, 16)
	if err != nil {
		return err
	}
	val, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return err
	}
	p[uint16(addr)] = val
	return nil
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: plc-sim serve|set ...")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	holding, coils := pairs{}, pairs{}
	fs.Var(holding, "holding", "holding register address=value, repeated")
	fs.Var(coils, "coil", "coil address=0|1, repeated")
	listen := fs.String("listen", "tcp://0.0.0.0:5020", "where to serve Modbus TCP")
	rtu := fs.String("rtu", "", "where to serve Modbus RTU framing over TCP, as host:port; none when empty")
	unit := fs.Uint("unit", 1, "the RTU unit address of the PLC")
	addr := fs.String("addr", "tcp://127.0.0.1:5020", "the PLC to write to")
	_ = fs.Parse(os.Args[2:])
	switch os.Args[1] {
	case "serve":
		p := modbussim.NewBank()
		p.Writes = func(table string, a, v uint16) { log.Printf("%s %d = %d", table, a, v) }
		for a, v := range holding {
			p.SetHolding(a, uint16(v))
		}
		for a, v := range coils {
			p.SetCoil(a, v != 0)
		}
		srv, err := modbus.NewServer(&modbus.ServerConfiguration{URL: *listen, Timeout: 5 * time.Minute, MaxClients: 16,
			Logger: log.New(io.Discard, "", 0)}, p)
		if err != nil {
			log.Fatal(err)
		}
		if err := srv.Start(); err != nil {
			log.Fatal(err)
		}
		if *rtu != "" {
			go serveRTU(*rtu, modbussim.Line{uint8(*unit): p})
		}
		log.Printf("serving %s: holding %v, coils %v", *listen, holding, coils)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		_ = srv.Stop()
	case "set":
		c, err := modbus.NewClient(&modbus.ClientConfiguration{URL: *addr, Timeout: 3 * time.Second})
		if err != nil {
			log.Fatal(err)
		}
		if err := c.Open(); err != nil {
			log.Fatal(err)
		}
		defer c.Close()
		for a, v := range holding {
			if err := c.WriteRegister(a, uint16(v)); err != nil {
				log.Fatal(err)
			}
		}
		for a, v := range coils {
			if err := c.WriteCoil(a, v != 0); err != nil {
				log.Fatal(err)
			}
		}
	default:
		log.Fatal("usage: plc-sim serve|set ...")
	}
}

// serveRTU answers RTU requests on each connection in turn: a serial line
// has one master.
func serveRTU(addr string, line modbussim.Line) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving Modbus RTU on %s, units %v", addr, units(line))
	for {
		c, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("rtu: line connected from %s", c.RemoteAddr())
		err = line.ServeRTU(c)
		log.Printf("rtu: line from %s closed: %v", c.RemoteAddr(), err)
		_ = c.Close()
	}
}

func units(l modbussim.Line) []uint8 {
	var out []uint8
	for u := range l {
		out = append(out, u)
	}
	return out
}
