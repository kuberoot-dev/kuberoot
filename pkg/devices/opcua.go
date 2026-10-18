package devices

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
)

// opcuaDevice reads an OPC UA server over one session, opened again after
// a failure. It reads every point each period, or, subscribed, keeps what
// the server last reported of each.
type opcuaDevice struct {
	spec  v1.DeviceSpec
	creds Credentials

	mu     sync.Mutex
	client *opcua.Client
	nodes  []*ua.NodeID
	// Subscribed: the subscription, and what it last reported.
	sub     *opcua.Subscription
	stop    context.CancelFunc
	values  Reading
	pending map[string]error
	subErr  error
}

func (d *opcuaDevice) settings() v1.OPCUA {
	s := v1.OPCUA{SecurityPolicy: "None", SecurityMode: "SignAndEncrypt"}
	if d.spec.OPCUA != nil {
		s.Subscribe = d.spec.OPCUA.Subscribe
		if d.spec.OPCUA.SecurityPolicy != "" {
			s.SecurityPolicy = d.spec.OPCUA.SecurityPolicy
		}
		if d.spec.OPCUA.SecurityMode != "" {
			s.SecurityMode = d.spec.OPCUA.SecurityMode
		}
	}
	return s
}

func (d *opcuaDevice) open() error {
	if d.client != nil {
		return nil
	}
	nodes := make([]*ua.NodeID, len(d.spec.Points))
	for i, p := range d.spec.Points {
		id, err := ua.ParseNodeID(p.NodeID)
		if err != nil {
			return fmt.Errorf("point %s: node %q: %w", p.Name, p.NodeID, err)
		}
		nodes[i] = id
	}
	creds, err := d.creds()
	if err != nil {
		return err
	}
	set := d.settings()
	ctx, cancel := context.WithTimeout(context.Background(), timeout(d.spec))
	defer cancel()
	endpoints, err := opcua.GetEndpoints(ctx, d.spec.Address)
	if err != nil {
		return fmt.Errorf("endpoints: %w", err)
	}
	mode := ua.MessageSecurityModeNone
	if set.SecurityPolicy != "None" {
		mode = ua.MessageSecurityModeFromString(set.SecurityMode)
	}
	ep, err := opcua.SelectEndpoint(endpoints, set.SecurityPolicy, mode)
	if err != nil {
		return fmt.Errorf("the server offers no endpoint with security policy %s, mode %s", set.SecurityPolicy, mode)
	}
	opts := []opcua.Option{
		opcua.SecurityPolicy(set.SecurityPolicy), opcua.SecurityMode(mode),
		opcua.AutoReconnect(false), opcua.RequestTimeout(timeout(d.spec)),
		opcua.DialTimeout(timeout(d.spec)), opcua.SessionTimeout(time.Minute),
		opcua.ApplicationName("kuberoot-devices"),
	}
	if set.SecurityPolicy != "None" {
		if err := trustServer(d.spec, creds, ep.ServerCertificate); err != nil {
			return err
		}
		own, err := clientCertificate(creds)
		if err != nil {
			return err
		}
		opts = append(opts, own...)
	}
	auth := ua.UserTokenTypeAnonymous
	if creds["username"] != "" {
		auth = ua.UserTokenTypeUserName
		opts = append(opts, opcua.AuthUsername(creds["username"], creds["password"]))
	} else {
		opts = append(opts, opcua.AuthAnonymous())
	}
	opts = append(opts, opcua.SecurityFromEndpoint(ep, auth))
	c, err := opcua.NewClient(d.spec.Address, opts...)
	if err != nil {
		return err
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}
	d.client, d.nodes = c, nodes
	if set.Subscribe {
		if err := d.subscribe(); err != nil {
			d.close()
			return err
		}
	}
	return nil
}

// clientCertificate is the node's own certificate and key, from tls.crt
// and tls.key; its application URI is the one the certificate names, as
// servers check.
func clientCertificate(creds map[string]string) ([]opcua.Option, error) {
	if creds["tls.crt"] == "" || creds["tls.key"] == "" {
		return nil, fmt.Errorf("a security policy needs the client's certificate and key: tls.crt and tls.key in the credentials")
	}
	pair, err := tls.X509KeyPair([]byte(creds["tls.crt"]), []byte(creds["tls.key"]))
	if err != nil {
		return nil, fmt.Errorf("client certificate: %w", err)
	}
	key, ok := pair.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("client certificate: Basic256Sha256 takes an RSA key")
	}
	opts := []opcua.Option{opcua.Certificate(pair.Certificate[0]), opcua.PrivateKey(key)}
	if cert, err := x509.ParseCertificate(pair.Certificate[0]); err == nil && len(cert.URIs) > 0 {
		opts = append(opts, opcua.ApplicationURI(cert.URIs[0].String()))
	}
	return opts, nil
}

// trustServer checks the certificate the server offers: the one given as
// server.crt, or one issued by ca.crt, or any with insecureSkipVerify.
// The OPC UA library takes whatever certificate the server sends.
func trustServer(spec v1.DeviceSpec, creds map[string]string, der []byte) error {
	if spec.TLS != nil && spec.TLS.InsecureSkipVerify {
		return nil
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("server certificate: %w", err)
	}
	if pin := creds["server.crt"]; pin != "" {
		want, err := pemCertificates(pin)
		if err != nil {
			return fmt.Errorf("server.crt: %w", err)
		}
		for _, w := range want {
			if bytes.Equal(w.Raw, der) {
				return nil
			}
		}
		return fmt.Errorf("the server's certificate (%s) is not server.crt", cert.Subject)
	}
	if ca := creds["ca.crt"]; ca != "" {
		roots, err := pemCertificates(ca)
		if err != nil {
			return fmt.Errorf("ca.crt: %w", err)
		}
		pool := x509.NewCertPool()
		for _, r := range roots {
			pool.AddCert(r)
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			return fmt.Errorf("the server's certificate (%s): %w", cert.Subject, err)
		}
		return nil
	}
	return fmt.Errorf("nothing to trust the server's certificate (%s) by: give server.crt or ca.crt in the credentials, or tls.insecureSkipVerify", cert.Subject)
}

func pemCertificates(s string) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := []byte(s)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no certificate")
	}
	return out, nil
}

// subscribe has the server report the points' changes, sampled every
// period.
func (d *opcuaDevice) subscribe() error {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan *opcua.PublishNotificationData, 16)
	every := d.spec.Every.Duration
	if every <= 0 {
		every = time.Second
	}
	sub, err := d.client.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: every}, ch)
	if err != nil {
		cancel()
		return fmt.Errorf("subscribe: %w", err)
	}
	items := make([]*ua.MonitoredItemCreateRequest, len(d.nodes))
	for i, id := range d.nodes {
		items[i] = opcua.NewMonitoredItemCreateRequestWithDefaults(id, ua.AttributeIDValue, uint32(i))
		items[i].RequestedParameters.SamplingInterval = float64(every / time.Millisecond)
	}
	mctx, mcancel := context.WithTimeout(ctx, timeout(d.spec))
	res, err := sub.Monitor(mctx, ua.TimestampsToReturnNeither, items...)
	mcancel()
	if err != nil {
		cancel()
		return fmt.Errorf("monitor: %w", err)
	}
	d.values, d.pending, d.subErr = Reading{}, map[string]error{}, nil
	for i, r := range res.Results {
		if r.StatusCode != ua.StatusOK {
			cancel()
			return fmt.Errorf("point %s: %s", d.spec.Points[i].Name, r.StatusCode)
		}
	}
	// A server reports a change, and need not report what was before it:
	// one read gives every point a value to start from.
	first, err := d.readAll()
	if err != nil {
		cancel()
		return err
	}
	d.values = first
	d.sub, d.stop = sub, cancel
	go d.receive(ctx, ch)
	return nil
}

// receive keeps what the subscription reports.
func (d *opcuaDevice) receive(ctx context.Context, ch <-chan *opcua.PublishNotificationData) {
	for {
		select {
		case <-ctx.Done():
			return
		case n := <-ch:
			d.mu.Lock()
			if ctx.Err() != nil { // a reading of a session since closed
				d.mu.Unlock()
				return
			}
			if n.Error != nil {
				d.subErr = n.Error
			} else if dc, ok := n.Value.(*ua.DataChangeNotification); ok {
				for _, item := range dc.MonitoredItems {
					if int(item.ClientHandle) >= len(d.spec.Points) {
						continue
					}
					p := d.spec.Points[item.ClientHandle]
					v, err := opcuaValue(p, item.Value)
					if err != nil {
						d.pending[p.Name] = err
						continue
					}
					delete(d.pending, p.Name)
					d.values[p.Name] = v
				}
			}
			d.mu.Unlock()
		}
	}
}

// Read gets every point; a failure drops the session for the next read to
// open anew.
func (d *opcuaDevice) Read() (Reading, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.open(); err != nil {
		return nil, err
	}
	out, err := d.read()
	if err != nil {
		d.close()
	}
	return out, err
}

func (d *opcuaDevice) read() (Reading, error) {
	if d.client.State() != opcua.Connected {
		return nil, fmt.Errorf("the session is %s", d.client.State())
	}
	if d.sub != nil {
		if d.subErr != nil {
			return nil, d.subErr
		}
		out := Reading{}
		for _, p := range d.spec.Points {
			if err := d.pending[p.Name]; err != nil {
				return nil, err
			}
			v, ok := d.values[p.Name]
			if !ok {
				return nil, fmt.Errorf("point %s: no value reported yet", p.Name)
			}
			out[p.Name] = v
		}
		return out, nil
	}
	return d.readAll()
}

// readAll reads every point at once.
func (d *opcuaDevice) readAll() (Reading, error) {
	req := &ua.ReadRequest{TimestampsToReturn: ua.TimestampsToReturnNeither}
	for _, id := range d.nodes {
		req.NodesToRead = append(req.NodesToRead, &ua.ReadValueID{NodeID: id, AttributeID: ua.AttributeIDValue})
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout(d.spec))
	defer cancel()
	res, err := d.client.Read(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(res.Results) != len(d.spec.Points) {
		return nil, fmt.Errorf("asked for %d values, got %d", len(d.spec.Points), len(res.Results))
	}
	out := Reading{}
	for i, p := range d.spec.Points {
		if out[p.Name], err = opcuaValue(p, res.Results[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// opcuaValue is a variable's value as a number, scaled: booleans as 0 or 1,
// numbers as they are, a string when it holds a number.
func opcuaValue(p v1.Point, dv *ua.DataValue) (float64, error) {
	if dv == nil || dv.Value == nil {
		return 0, fmt.Errorf("point %s: no value", p.Name)
	}
	// The top two bits of a status code: 00 good, 01 uncertain, 10 bad.
	if uint32(dv.Status)>>30 != 0 {
		return 0, fmt.Errorf("point %s: %s", p.Name, dv.Status)
	}
	var v float64
	switch x := dv.Value.Value().(type) {
	case bool:
		if x {
			v = 1
		}
	case int8:
		v = float64(x)
	case uint8:
		v = float64(x)
	case int16:
		v = float64(x)
	case uint16:
		v = float64(x)
	case int32:
		v = float64(x)
	case uint32:
		v = float64(x)
	case int64:
		v = float64(x)
	case uint64:
		v = float64(x)
	case float32:
		// As the server meant it: 21.7, not 21.700000762939453.
		v, _ = strconv.ParseFloat(strconv.FormatFloat(float64(x), 'g', -1, 32), 64)
	case float64:
		v = x
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, fmt.Errorf("point %s is %q, not a number", p.Name, x)
		}
		v = f
	default:
		return 0, fmt.Errorf("point %s is a %T, not a number", p.Name, x)
	}
	return scaled(p, v)
}

func (d *opcuaDevice) close() {
	if d.stop != nil {
		d.stop()
		d.stop, d.sub = nil, nil
	}
	if d.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = d.client.Close(ctx)
		cancel()
		d.client = nil
	}
}

func (d *opcuaDevice) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.close()
}
