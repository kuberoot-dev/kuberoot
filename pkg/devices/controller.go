package devices

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	v1 "github.com/tym83/kuberoot/pkg/apis/devices/v1alpha1"
	"github.com/tym83/kuberoot/pkg/safeguard"
)

var kinds = []struct{ Kind, Plural string }{{"Device", "devices"}, {"Route", "routes"}, {"Safeguard", "safeguards"}, {"Heartbeat", "heartbeats"}}

// Config is what the node runs: its devices and their routes.
type Config struct {
	Devices []v1.Device `json:"devices"`
	Routes  []v1.Route  `json:"routes"`
}

// Revision names a configuration by its specs.
func Revision(c Config) string {
	type entry struct {
		Kind, Name string
		Spec       any
	}
	var all []entry
	for _, d := range c.Devices {
		all = append(all, entry{"Device", d.Name, d.Spec})
	}
	for _, r := range c.Routes {
		all = append(all, entry{"Route", r.Name, r.Spec})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Kind+all[i].Name < all[j].Kind+all[j].Name })
	raw, _ := json.Marshal(all)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:12]
}

// Check keeps the resources the node can run, and says what is wrong with
// the others: a route whose device is missing or whose condition does not
// compile against the device's points.
func Check(c Config) (Config, map[string]string) {
	problems := map[string]string{}
	out := Config{Devices: c.Devices}
	points := map[string][]v1.Point{}
	for _, d := range c.Devices {
		points[d.Name] = d.Spec.Points
		for _, p := range d.Spec.Points {
			if _, err := Scale(p); err != nil {
				problems["Device/"+d.Name] = err.Error()
			}
		}
	}
	for _, r := range c.Routes {
		pts, ok := points[r.Spec.Device]
		switch {
		case !ok:
			problems["Route/"+r.Name] = "no device " + r.Spec.Device
		case r.Spec.To.MQTT == nil:
			problems["Route/"+r.Name] = "the route goes nowhere: set to.mqtt"
		default:
			if _, err := Compile(r.Spec.When, pts); err != nil {
				problems["Route/"+r.Name] = "when: " + err.Error()
			} else {
				out.Routes = append(out.Routes, r)
			}
		}
	}
	return out, problems
}

// Broken names what was healthy when a change went on trial and is not
// now: a device that stopped answering, a destination no longer taking
// messages. What the change removed does not count.
func Broken(before, now map[string]bool, candidate Config) string {
	keep := map[string]bool{}
	for _, d := range candidate.Devices {
		keep["device "+d.Name] = true
	}
	for _, r := range candidate.Routes {
		keep["route "+r.Name] = true
	}
	var broken []string
	for k := range before {
		if keep[k] && !now[k] {
			broken = append(broken, k)
		}
	}
	if len(broken) == 0 {
		return ""
	}
	sort.Strings(broken)
	return "the change broke what worked before it: " + strings.Join(broken, ", ") + " stopped working"
}

// Controller makes a gateway node run its devices and routes.
type Controller struct {
	Client   dynamic.Interface
	StateDir string

	brokers Brokers
	trial   safeguard.Trial[Config]
	// baseline is what was healthy when the pending change went on trial.
	baseline   map[string]bool
	trialSince time.Time

	mu         sync.Mutex
	devices    map[string]*device
	routes     map[string]*route
	beats      map[string]*heartbeat
	appliedRev string
	listers    map[string]cache.GenericLister
	lastStatus time.Time
}

// device is a device the node polls.
type device struct {
	spec   v1.DeviceSpec
	conn   reader
	cancel context.CancelFunc

	mu        sync.Mutex
	reading   Reading
	lastRead  time.Time
	lastOK    time.Time
	err       string
	failures  int
	connected bool
}

// route is a route the node runs.
type route struct {
	spec v1.RouteSpec
	cond *Condition

	mu       sync.Mutex
	edge     Edge
	sent     int64
	lastSent time.Time
	err      string
}

// Run watches the resources and reconciles on every change, every couple of
// seconds to follow trials and refresh what the status reports.
func (c *Controller) Run(ctx context.Context) error {
	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		return err
	}
	c.devices, c.routes, c.beats = map[string]*device{}, map[string]*route{}, map[string]*heartbeat{}
	c.trial.Load(c.confirmedFile())
	factory := dynamicinformer.NewDynamicSharedInformerFactory(c.Client, 10*time.Minute)
	poke := make(chan struct{}, 1)
	notify := func(any) {
		select {
		case poke <- struct{}{}:
		default:
		}
	}
	c.listers = map[string]cache.GenericLister{}
	for _, k := range kinds {
		inf := factory.ForResource(v1.GroupVersion.WithResource(k.Plural))
		_, _ = inf.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: notify, DeleteFunc: notify, UpdateFunc: func(_, o any) { notify(o) },
		})
		c.listers[k.Kind] = inf.Lister()
	}
	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())
	defer c.stopAll()
	for {
		c.reconcile(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-poke:
			time.Sleep(300 * time.Millisecond) // let a burst of changes settle
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *Controller) confirmedFile() string { return filepath.Join(c.StateDir, "confirmed.json") }

type listed struct {
	kind string
	obj  *unstructured.Unstructured
}

func (c *Controller) list() (Config, []listed, *v1.Safeguard, []v1.Heartbeat, error) {
	var cfg Config
	var all []listed
	var guard *v1.Safeguard
	var beats []v1.Heartbeat
	for _, k := range kinds {
		objs, err := c.listers[k.Kind].List(labels.Everything())
		if err != nil {
			return cfg, nil, nil, nil, err
		}
		for _, o := range objs {
			u := o.(*unstructured.Unstructured)
			all = append(all, listed{k.Kind, u})
			conv := runtime.DefaultUnstructuredConverter
			switch k.Kind {
			case "Device":
				var d v1.Device
				if err := conv.FromUnstructured(u.Object, &d); err == nil {
					cfg.Devices = append(cfg.Devices, d)
				}
			case "Route":
				var r v1.Route
				if err := conv.FromUnstructured(u.Object, &r); err == nil {
					cfg.Routes = append(cfg.Routes, r)
				}
			case "Safeguard":
				var s v1.Safeguard
				if err := conv.FromUnstructured(u.Object, &s); err == nil && s.Name == "default" {
					guard = &s
				}
			case "Heartbeat":
				var h v1.Heartbeat
				if err := conv.FromUnstructured(u.Object, &h); err == nil {
					beats = append(beats, h)
				}
			}
		}
	}
	return cfg, all, guard, beats, nil
}

func (c *Controller) reconcile(ctx context.Context) {
	cfg, all, sg, beats, err := c.list()
	if err != nil {
		klog.Errorf("list: %v", err)
		return
	}
	// Heartbeats take effect at once: one that stops beating is what they
	// are there to tell, not a change to undo.
	c.applyHeartbeats(beats)
	candidate, problems := Check(cfg)
	rev := Revision(candidate)
	var guard *safeguard.Guard
	if sg != nil {
		guard = &safeguard.Guard{ConfirmWithin: sg.Spec.ConfirmWithin.Duration, Confirm: sg.Spec.Confirm,
			AutoConfirm: sg.Spec.AutoConfirm == nil || *sg.Spec.AutoConfirm}
	}
	now := time.Now()
	// A new change on trial: remember what works before it runs.
	if guard != nil && c.trial.ConfirmedRev != "" && rev != c.trial.ConfirmedRev && rev != c.trial.Pending && rev != c.trial.RolledBack {
		c.baseline, c.trialSince = c.healthy(), now
	}
	unhealthy := ""
	if c.trial.Pending == rev && now.Sub(c.trialSince) > c.settle(candidate) {
		unhealthy = Broken(c.baseline, c.healthy(), candidate)
	}
	running, runningRev, changed := c.trial.Decide(candidate, rev, guard, unhealthy, now)
	if changed {
		if err := c.trial.Save(c.confirmedFile()); err != nil {
			klog.Errorf("keep the confirmed configuration: %v", err)
		}
	}
	if unhealthy != "" && runningRev != rev {
		klog.Warningf("revision %s rolled back: %s", rev, unhealthy)
	}
	if runningRev != c.appliedRev {
		c.apply(running)
		c.appliedRev = runningRev
	}
	// Status: at once after a change, every few seconds otherwise.
	if changed || now.Sub(c.lastStatus) > 4*time.Second {
		c.lastStatus = now
		for _, l := range all {
			c.report(ctx, l, problems[l.kind+"/"+l.obj.GetName()], runningRev != rev, rev, runningRev)
		}
	}
}

// settle is how long a change runs before its health counts: a few reads
// of the slowest device, and a reconnect to the brokers.
func (c *Controller) settle(cfg Config) time.Duration {
	d := 10 * time.Second
	for _, dev := range cfg.Devices {
		if every := 3*dev.Spec.Every.Duration + dev.Spec.Timeout.Duration; every > d {
			d = every
		}
	}
	return d
}

// healthy lists what works now: devices answering, routes whose broker is
// connected.
func (c *Controller) healthy() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]bool{}
	for name, d := range c.devices {
		d.mu.Lock()
		out["device "+name] = d.connected
		d.mu.Unlock()
	}
	for name, r := range c.routes {
		out["route "+name] = r.spec.To.MQTT != nil && c.brokers.Connected(r.spec.To.MQTT.Broker)
	}
	for k, ok := range out {
		if !ok {
			delete(out, k)
		}
	}
	return out
}

// apply makes the node poll the configuration's devices and run its
// routes; devices and routes whose specs did not change go on as they were.
func (c *Controller) apply(cfg Config) {
	c.mu.Lock()
	defer c.mu.Unlock()
	want := map[string]v1.DeviceSpec{}
	for _, d := range cfg.Devices {
		want[d.Name] = d.Spec
	}
	for name, d := range c.devices {
		if spec, ok := want[name]; !ok || !reflect.DeepEqual(spec, d.spec) {
			d.cancel()
			d.conn.Close()
			delete(c.devices, name)
		}
	}
	for name, spec := range want {
		if _, ok := c.devices[name]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		d := &device{spec: spec, conn: newReader(spec, SecretCredentials(c.Client, spec.Credentials)), cancel: cancel}
		c.devices[name] = d
		go c.poll(ctx, name, d)
	}
	routes := map[string]*route{}
	brokers := map[string]bool{}
	for _, r := range cfg.Routes {
		brokers[r.Spec.To.MQTT.Broker] = true
		if old, ok := c.routes[r.Name]; ok && reflect.DeepEqual(old.spec, r.Spec) {
			routes[r.Name] = old
			continue
		}
		cond, _ := Compile(r.Spec.When, want[r.Spec.Device].Points) // checked already
		nr := &route{spec: r.Spec, cond: cond}
		// Same device, same condition: it remembers whether the condition
		// held, so a change elsewhere (or undoing one) sends no alarm again.
		if old, ok := c.routes[r.Name]; ok && old.spec.Device == r.Spec.Device && old.spec.When == r.Spec.When {
			old.mu.Lock()
			nr.edge, nr.sent, nr.lastSent = old.edge, old.sent, old.lastSent
			old.mu.Unlock()
		}
		routes[r.Name] = nr
		c.brokers.client(r.Spec.To.MQTT.Broker)
	}
	c.routes = routes
	c.brokers.Keep(brokers)
}

// shown are the points a device's status lists: those it declares, or, for
// a BMC or a probe that declares none, every value it reported, by name.
func shown(points []v1.Point, r Reading) []v1.Point {
	if len(points) > 0 {
		return points
	}
	names := make([]string, 0, len(r))
	for k := range r {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]v1.Point, len(names))
	for i, n := range names {
		out[i] = v1.Point{Name: n}
	}
	return out
}

func (c *Controller) stopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, d := range c.devices {
		d.cancel()
		d.conn.Close()
	}
	for _, h := range c.beats {
		h.cancel()
	}
}

// applyHeartbeats runs the heartbeats as they are; one whose spec changed
// starts anew.
func (c *Controller) applyHeartbeats(beats []v1.Heartbeat) {
	c.mu.Lock()
	defer c.mu.Unlock()
	want := map[string]v1.HeartbeatSpec{}
	for _, h := range beats {
		want[h.Name] = h.Spec
	}
	for name, h := range c.beats {
		if spec, ok := want[name]; !ok || !reflect.DeepEqual(spec, h.spec) {
			h.cancel()
			delete(c.beats, name)
		}
	}
	for name, spec := range want {
		if _, ok := c.beats[name]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		h := newHeartbeat(spec)
		h.cancel = cancel
		c.beats[name] = h
		go h.run(ctx)
	}
}

// poll reads a device on its schedule and hands each reading to its routes.
func (c *Controller) poll(ctx context.Context, name string, d *device) {
	every := d.spec.Every.Duration
	if every <= 0 {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		r, err := d.conn.Read()
		now := time.Now()
		d.mu.Lock()
		d.lastRead = now
		if err != nil {
			d.err, d.failures = err.Error(), d.failures+1
			// A read missed now and then is not a device lost.
			if d.failures >= 3 {
				d.connected = false
			}
			// A probe of a service down measured that: up is 0.
			if r != nil {
				d.reading = r
			}
		} else {
			d.reading, d.lastOK, d.err, d.failures, d.connected = r, now, "", 0, true
		}
		d.mu.Unlock()
		if err == nil {
			c.route(name, d.spec.Points, r, now)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// route sends a reading through every route of its device.
func (c *Controller) route(name string, points []v1.Point, r Reading, at time.Time) {
	c.mu.Lock()
	var mine []*route
	for _, rt := range c.routes {
		if rt.spec.Device == name {
			mine = append(mine, rt)
		}
	}
	c.mu.Unlock()
	for _, rt := range mine {
		rt.mu.Lock()
		send, err := rt.edge.Send(rt.cond, points, r)
		rt.mu.Unlock()
		if err == nil && send {
			m := rt.spec.To.MQTT
			err = c.brokers.Publish(m.Broker, m.Topic, byte(m.QoS), m.Retain, Message(name, rt.spec, points, r, at))
		}
		rt.mu.Lock()
		if err != nil {
			rt.err = err.Error()
		} else {
			rt.err = ""
			if send {
				rt.sent, rt.lastSent = rt.sent+1, at
			}
		}
		rt.mu.Unlock()
	}
}

// report writes a resource's status: whether the node runs it, and what it
// says about it.
func (c *Controller) report(ctx context.Context, l listed, problem string, undone bool, rev, runningRev string) {
	cond := metav1.Condition{Type: "Applied", Status: metav1.ConditionTrue, Reason: "Applied", Message: "the node runs this configuration"}
	switch {
	case l.kind == "Safeguard":
	case problem != "":
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "Invalid", problem
	case undone && l.kind != "Heartbeat":
		cond.Status, cond.Reason = metav1.ConditionFalse, "RolledBack"
		cond.Message = fmt.Sprintf("revision %s was rolled back (%s); the node runs %s", rev, c.trial.Why, runningRev)
	}
	status := map[string]any{"observedGeneration": l.obj.GetGeneration()}
	name := l.obj.GetName()
	switch l.kind {
	case "Device":
		c.mu.Lock()
		d := c.devices[name]
		c.mu.Unlock()
		if d != nil {
			d.mu.Lock()
			status["connected"] = d.connected
			if d.err != "" {
				status["error"] = d.err
			}
			if !d.lastRead.IsZero() {
				status["lastRead"] = d.lastRead.UTC().Format(time.RFC3339)
			}
			var values []any
			var summary []string
			for _, p := range shown(d.spec.Points, d.reading) {
				if v, ok := d.reading[p.Name]; ok {
					s := Format(p, v)
					v := map[string]any{"name": p.Name, "value": s}
					if p.Unit != "" {
						v["unit"] = p.Unit
					}
					values = append(values, v)
					summary = append(summary, p.Name+"="+s+p.Unit)
				}
			}
			if len(values) > 0 {
				status["values"], status["summary"] = values, strings.Join(summary, " ")
			}
			d.mu.Unlock()
		}
	case "Route":
		c.mu.Lock()
		rt := c.routes[name]
		c.mu.Unlock()
		if rt != nil {
			rt.mu.Lock()
			status["sent"] = rt.sent
			if !rt.lastSent.IsZero() {
				status["lastSent"] = rt.lastSent.UTC().Format(time.RFC3339)
			}
			if rt.err != "" {
				status["error"] = rt.err
			}
			status["connected"] = rt.spec.To.MQTT != nil && c.brokers.Connected(rt.spec.To.MQTT.Broker)
			rt.mu.Unlock()
		}
	case "Heartbeat":
		c.mu.Lock()
		h := c.beats[name]
		c.mu.Unlock()
		if h != nil {
			h.mu.Lock()
			status["healthy"], status["sent"] = h.healthy, h.sent
			if !h.lastSent.IsZero() {
				status["lastSent"] = h.lastSent.UTC().Format(time.RFC3339)
			}
			if h.err != "" {
				status["error"] = h.err
			}
			h.mu.Unlock()
		}
	case "Safeguard":
		status["running"], status["confirmed"] = c.trial.ConfirmedRev, c.trial.ConfirmedRev
		if c.trial.Pending != "" {
			status["running"], status["pending"] = c.trial.Pending, c.trial.Pending
			status["deadline"] = c.trial.Deadline.UTC().Format(time.RFC3339)
		}
		if c.trial.RolledBack != "" {
			status["rolledBack"], status["why"] = c.trial.RolledBack, c.trial.Why
		}
	}
	var conds []metav1.Condition
	if raw, found, _ := unstructured.NestedSlice(l.obj.Object, "status", "conditions"); found {
		if b, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(b, &conds)
		}
	}
	if l.kind != "Safeguard" {
		cond.ObservedGeneration = l.obj.GetGeneration()
		meta.SetStatusCondition(&conds, cond)
		u, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&struct {
			Items []metav1.Condition `json:"items"`
		}{conds})
		status["conditions"] = u["items"]
	}
	status = runtime.DeepCopyJSON(status)
	old, _, _ := unstructured.NestedMap(l.obj.Object, "status")
	if equality.Semantic.DeepEqual(old, status) {
		return
	}
	updated := l.obj.DeepCopy()
	updated.Object["status"] = status
	plural := ""
	for _, k := range kinds {
		if k.Kind == l.kind {
			plural = k.Plural
		}
	}
	if _, err := c.Client.Resource(v1.GroupVersion.WithResource(plural)).UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		klog.V(2).Infof("status of %s %s: %v", l.kind, name, err)
	}
}
