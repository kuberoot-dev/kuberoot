package devices

import (
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/gosnmp/gosnmp"

	v1 "github.com/tym83/kuberoot/pkg/apis/devices/v1alpha1"
)

// snmpDevice reads an SNMP agent: switches, PDUs, UPSes. Each poll gets all
// of the device's OIDs in as few requests as the agent takes.
type snmpDevice struct {
	spec  v1.DeviceSpec
	creds Credentials

	mu     sync.Mutex
	client *gosnmp.GoSNMP
}

// oidsPerRequest: agents answer up to this many OIDs in one get.
const oidsPerRequest = 32

func (d *snmpDevice) open() error {
	if d.client != nil {
		return nil
	}
	creds, err := d.creds()
	if err != nil {
		return err
	}
	host, port := d.spec.Address, uint16(161)
	if h, p, err := net.SplitHostPort(d.spec.Address); err == nil {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return fmt.Errorf("address %s: port %q", d.spec.Address, p)
		}
		host, port = h, uint16(n)
	}
	c := &gosnmp.GoSNMP{Target: host, Port: port, Timeout: timeout(d.spec), Retries: 1, MaxOids: oidsPerRequest}
	if d.spec.SNMP != nil && d.spec.SNMP.Version == "3" {
		params, flags, err := usm(creds)
		if err != nil {
			return err
		}
		c.Version, c.SecurityModel, c.MsgFlags, c.SecurityParameters = gosnmp.Version3, gosnmp.UserSecurityModel, flags, params
	} else {
		c.Version, c.Community = gosnmp.Version2c, creds["community"]
		if c.Community == "" {
			c.Community = "public"
		}
	}
	if err := c.Connect(); err != nil {
		return err
	}
	d.client = c
	return nil
}

// usm builds SNMPv3 user security from the credentials: no auth, auth, or
// auth and privacy, by what they hold.
func usm(c map[string]string) (*gosnmp.UsmSecurityParameters, gosnmp.SnmpV3MsgFlags, error) {
	p := &gosnmp.UsmSecurityParameters{UserName: c["username"]}
	if p.UserName == "" {
		return nil, 0, fmt.Errorf("snmp v3: the credentials have no username")
	}
	flags := gosnmp.NoAuthNoPriv
	if c["authProtocol"] != "" {
		protos := map[string]gosnmp.SnmpV3AuthProtocol{"MD5": gosnmp.MD5, "SHA": gosnmp.SHA, "SHA256": gosnmp.SHA256, "SHA512": gosnmp.SHA512}
		ap, ok := protos[strings.ToUpper(c["authProtocol"])]
		if !ok {
			return nil, 0, fmt.Errorf("snmp v3: auth protocol %q: MD5, SHA, SHA256 or SHA512", c["authProtocol"])
		}
		p.AuthenticationProtocol, p.AuthenticationPassphrase, flags = ap, c["authPassword"], gosnmp.AuthNoPriv
	}
	if c["privProtocol"] != "" {
		if flags == gosnmp.NoAuthNoPriv {
			return nil, 0, fmt.Errorf("snmp v3: privacy needs authentication")
		}
		protos := map[string]gosnmp.SnmpV3PrivProtocol{"DES": gosnmp.DES, "AES": gosnmp.AES, "AES256": gosnmp.AES256}
		pp, ok := protos[strings.ToUpper(c["privProtocol"])]
		if !ok {
			return nil, 0, fmt.Errorf("snmp v3: privacy protocol %q: DES, AES or AES256", c["privProtocol"])
		}
		p.PrivacyProtocol, p.PrivacyPassphrase, flags = pp, c["privPassword"], gosnmp.AuthPriv
	}
	return p, flags, nil
}

// Read gets every point; a failure drops the session for the next read to
// open anew.
func (d *snmpDevice) Read() (Reading, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.open(); err != nil {
		return nil, err
	}
	byOID := map[string]v1.Point{}
	var oids []string
	for _, p := range d.spec.Points {
		oid := "." + strings.TrimPrefix(p.OID, ".")
		byOID[oid] = p
		oids = append(oids, oid)
	}
	out := Reading{}
	for start := 0; start < len(oids); start += oidsPerRequest {
		end := min(start+oidsPerRequest, len(oids))
		res, err := d.client.Get(oids[start:end])
		if err != nil {
			d.close()
			return nil, err
		}
		for _, pdu := range res.Variables {
			p, ok := byOID["."+strings.TrimPrefix(pdu.Name, ".")]
			if !ok {
				continue
			}
			v, err := snmpValue(pdu)
			if err != nil {
				return nil, fmt.Errorf("point %s: %w", p.Name, err)
			}
			if out[p.Name], err = scaled(p, v); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// snmpValue is an SNMP value as a number: integers, counters, gauges and
// timeticks as they are, a string when it holds a number.
func snmpValue(pdu gosnmp.SnmpPDU) (float64, error) {
	switch pdu.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		return 0, fmt.Errorf("the agent has no %s", pdu.Name)
	case gosnmp.OctetString:
		b, _ := pdu.Value.([]byte)
		f, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err != nil {
			return 0, fmt.Errorf("%s is %q, not a number", pdu.Name, b)
		}
		return f, nil
	case gosnmp.Opaque:
		switch v := pdu.Value.(type) {
		case float32:
			return float64(v), nil
		case float64:
			return v, nil
		}
	}
	n := gosnmp.ToBigInt(pdu.Value)
	if n == nil {
		return 0, fmt.Errorf("%s is %v, not a number", pdu.Name, pdu.Type)
	}
	f, _ := new(big.Float).SetInt(n).Float64()
	return f, nil
}

func (d *snmpDevice) close() {
	if d.client != nil && d.client.Conn != nil {
		_ = d.client.Conn.Close()
	}
	d.client = nil
}

func (d *snmpDevice) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.close()
}
