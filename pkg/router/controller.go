package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/router/v1alpha1"
)

// kinds are the router's resources, by kind and plural.
var kinds = []struct{ Kind, Plural string }{
	{"Interface", "interfaces"}, {"Route", "routes"}, {"NATRule", "natrules"},
	{"FirewallZone", "firewallzones"}, {"FirewallRule", "firewallrules"},
	{"DHCPServer", "dhcpservers"}, {"BGPRouter", "bgprouters"}, {"BGPPeer", "bgppeers"},
	{"Safeguard", "safeguards"},
}

func resource(plural string) schema.GroupVersionResource {
	return v1.GroupVersion.WithResource(plural)
}

// Controller makes the node match the router's resources and reports back
// in their status whether it does.
type Controller struct {
	Client dynamic.Interface
	// RunDir holds the daemons' configuration and sockets; StateDir what
	// survives a reboot (DHCP leases).
	RunDir, StateDir string
	// RouterID for BGP when the BGPRouter names none.
	RouterID string

	trial                 trial
	dnsmasq, bird         *Daemon
	ruleset               string
	dnsmasqConf, birdConf string
	listers               map[string]cache.GenericLister
}

// Run watches the resources and reconciles on every change, and every half
// minute to refresh what the status reports (leases, BGP sessions).
func (c *Controller) Run(ctx context.Context) error {
	for _, d := range []string{c.RunDir, c.StateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	c.dnsmasq = &Daemon{Name: "dnsmasq", Args: []string{"/usr/sbin/dnsmasq", "--conf-file=" + filepath.Join(c.RunDir, "dnsmasq.conf")}}
	c.bird = &Daemon{Name: "bird", Args: []string{"/usr/sbin/bird", "-f", "-c", filepath.Join(c.RunDir, "bird.conf"), "-s", c.birdSocket()}}
	defer c.dnsmasq.Stop()
	defer c.bird.Stop()

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
		inf := factory.ForResource(resource(k.Plural))
		_, _ = inf.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: notify, DeleteFunc: notify, UpdateFunc: func(_, o any) { notify(o) },
		})
		c.listers[k.Kind] = inf.Lister()
	}
	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())

	for {
		c.reconcile(ctx)
		// Every half minute, and right at the end of a trial.
		wait := 30 * time.Second
		if !c.trial.Deadline.IsZero() {
			wait = min(wait, time.Until(c.trial.Deadline)+time.Second)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-poke:
			// Let a burst of changes (kubectl apply -f dir) settle.
			time.Sleep(500 * time.Millisecond)
			select {
			case <-poke:
			default:
			}
		case <-time.After(wait):
		}
	}
}

// listed is a resource as the cluster has it, and as its typed self.
type listed struct {
	ref Ref
	obj *unstructured.Unstructured
}

func (c *Controller) list() (Config, []listed, error) {
	var cfg Config
	var all []listed
	for _, k := range kinds {
		objs, err := c.listers[k.Kind].List(labels.Everything())
		if err != nil {
			return cfg, nil, err
		}
		for _, o := range objs {
			u := o.(*unstructured.Unstructured)
			all = append(all, listed{Ref{k.Kind, u.GetName()}, u})
			if err := appendTyped(&cfg, k.Kind, u); err != nil {
				return cfg, nil, fmt.Errorf("%s %s: %w", k.Kind, u.GetName(), err)
			}
		}
	}
	return cfg, all, nil
}

func appendTyped(cfg *Config, kind string, u *unstructured.Unstructured) error {
	conv := runtime.DefaultUnstructuredConverter
	var err error
	switch kind {
	case "Interface":
		var x v1.Interface
		err = conv.FromUnstructured(u.Object, &x)
		cfg.Interfaces = append(cfg.Interfaces, x)
	case "Route":
		var x v1.Route
		err = conv.FromUnstructured(u.Object, &x)
		cfg.Routes = append(cfg.Routes, x)
	case "NATRule":
		var x v1.NATRule
		err = conv.FromUnstructured(u.Object, &x)
		cfg.NAT = append(cfg.NAT, x)
	case "FirewallZone":
		var x v1.FirewallZone
		err = conv.FromUnstructured(u.Object, &x)
		cfg.Zones = append(cfg.Zones, x)
	case "FirewallRule":
		var x v1.FirewallRule
		err = conv.FromUnstructured(u.Object, &x)
		cfg.Rules = append(cfg.Rules, x)
	case "DHCPServer":
		var x v1.DHCPServer
		err = conv.FromUnstructured(u.Object, &x)
		cfg.DHCP = append(cfg.DHCP, x)
	case "BGPRouter":
		var x v1.BGPRouter
		err = conv.FromUnstructured(u.Object, &x)
		cfg.BGP = append(cfg.BGP, x)
	case "BGPPeer":
		var x v1.BGPPeer
		err = conv.FromUnstructured(u.Object, &x)
		cfg.Peers = append(cfg.Peers, x)
	case "Safeguard":
		var x v1.Safeguard
		err = conv.FromUnstructured(u.Object, &x)
		cfg.Safeguards = append(cfg.Safeguards, x)
	}
	return err
}

func (c *Controller) reconcile(ctx context.Context) {
	cfg, all, err := c.list()
	if err != nil {
		klog.Errorf("list: %v", err)
		return
	}
	candidate, problems := Check(cfg)
	var guard *v1.Safeguard
	for i := range cfg.Safeguards {
		if cfg.Safeguards[i].Name == "default" {
			guard = &cfg.Safeguards[i]
		} else {
			problems[Ref{"Safeguard", cfg.Safeguards[i].Name}] = "the safeguard is named default"
		}
	}
	rev := Revision(candidate)
	ok, running, changed := c.trial.decide(candidate, rev, guard, time.Now())
	if changed {
		if err := c.trial.Save(c.confirmedFile()); err != nil {
			klog.Errorf("keep the confirmed configuration: %v", err)
		}
	}
	failed := map[Ref]error{}
	if running != rev {
		undone := rolledBack{fmt.Sprintf("revision %s was not confirmed in time; the node runs the last confirmed one, %s", rev, running)}
		for _, k := range kinds {
			if k.Kind != "Safeguard" {
				failed[Ref{k.Kind, "*"}] = undone
			}
		}
	}
	for name, err := range ApplyInterfaces(ok.Interfaces) {
		if failed[Ref{"Interface", "*"}] == nil {
			failed[Ref{"Interface", name}] = err
		}
	}
	for name, err := range ApplyRoutes(ok.Routes) {
		if failed[Ref{"Route", "*"}] == nil {
			failed[Ref{"Route", name}] = err
		}
	}
	if err := c.applyRuleset(Ruleset(ok)); err != nil {
		for _, kind := range []string{"NATRule", "FirewallZone", "FirewallRule"} {
			failed[Ref{kind, "*"}] = err
		}
	}
	if err := c.applyDnsmasq(DnsmasqConfig(ok, c.leaseFile())); err != nil {
		failed[Ref{"DHCPServer", "*"}] = err
	} else if f := c.dnsmasq.Failure(); f != "" {
		failed[Ref{"DHCPServer", "*"}] = fmt.Errorf("%s", f)
	}
	routerID := c.RouterID
	if routerID == "" {
		routerID = FirstAddress4()
	}
	birdErr := c.applyBird(BirdConfig(ok, routerID))
	if birdErr == nil && c.bird.Failure() != "" {
		birdErr = fmt.Errorf("%s", c.bird.Failure())
	}
	if birdErr != nil {
		failed[Ref{"BGPRouter", "*"}] = birdErr
		failed[Ref{"BGPPeer", "*"}] = birdErr
	}
	sessions := c.sessions(ctx)
	leases := c.leases()
	for _, l := range all {
		err := failed[Ref{l.ref.Kind, "*"}]
		if err == nil {
			err = failed[l.ref]
		}
		c.report(ctx, l, problems[l.ref], err, ok, sessions, leases)
	}
}

func (c *Controller) confirmedFile() string { return filepath.Join(c.StateDir, "confirmed.json") }

func (c *Controller) applyRuleset(rs string) error {
	// Applied again when the table went missing, say flushed by hand.
	if rs == c.ruleset && exec.Command("nft", "list", "table", "inet", Table).Run() == nil {
		return nil
	}
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(rs)
	if out, err := cmd.CombinedOutput(); err != nil {
		c.ruleset = ""
		return fmt.Errorf("nft: %v: %s", err, bytes.TrimSpace(out))
	}
	c.ruleset = rs
	return nil
}

func (c *Controller) applyDnsmasq(conf string) error {
	path := filepath.Join(c.RunDir, "dnsmasq.conf")
	if conf == "" {
		c.dnsmasq.Stop()
		c.dnsmasqConf = ""
		return nil
	}
	conf += "user=root\npid-file=\n"
	if conf == c.dnsmasqConf && c.dnsmasq.Running() {
		return nil
	}
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		return err
	}
	// dnsmasq reads its DHCP ranges only when it starts.
	if out, err := exec.Command("/usr/sbin/dnsmasq", "--test", "--conf-file="+path).CombinedOutput(); err != nil {
		return fmt.Errorf("dnsmasq: %s", bytes.TrimSpace(out))
	}
	c.dnsmasq.Restart()
	c.dnsmasqConf = conf
	return nil
}

func (c *Controller) applyBird(conf string) error {
	path := filepath.Join(c.RunDir, "bird.conf")
	if conf == "" {
		c.bird.Stop()
		c.birdConf = ""
		return nil
	}
	if conf == c.birdConf && c.bird.Running() {
		return nil
	}
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("/usr/sbin/bird", "-p", "-c", path).CombinedOutput(); err != nil {
		return fmt.Errorf("bird: %s", bytes.TrimSpace(out))
	}
	if c.bird.Running() {
		// bird takes a new configuration without dropping sessions it keeps.
		if out, err := exec.Command("/usr/sbin/birdc", "-s", c.birdSocket(), "configure").CombinedOutput(); err != nil {
			klog.Errorf("birdc configure: %v: %s; restarting bird", err, out)
			c.bird.Restart()
		}
	} else {
		c.bird.Start()
	}
	c.birdConf = conf
	return nil
}

func (c *Controller) birdSocket() string { return filepath.Join(c.RunDir, "bird.ctl") }
func (c *Controller) leaseFile() string  { return filepath.Join(c.StateDir, "dnsmasq.leases") }

func (c *Controller) sessions(ctx context.Context) map[string]PeerState {
	if !c.bird.Running() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/birdc", "-s", c.birdSocket(), "show", "protocols", "all").Output()
	if err != nil {
		return nil
	}
	return ParseProtocols(bytes.NewReader(out))
}

func (c *Controller) leases() []v1.Lease {
	f, err := os.Open(c.leaseFile())
	if err != nil {
		return nil
	}
	defer f.Close()
	return ParseLeases(f)
}

// report writes a resource's status: Applied, or why not, and what the node
// says about it.
func (c *Controller) report(ctx context.Context, l listed, problem string, applyErr error, ok Config,
	sessions map[string]PeerState, leases []v1.Lease) {
	cond := metav1.Condition{Type: "Applied", Status: metav1.ConditionTrue, Reason: "Applied", Message: "the node runs this configuration"}
	switch {
	case problem != "":
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "Invalid", problem
	case applyErr != nil:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "Failed", applyErr.Error()
		if _, undone := applyErr.(rolledBack); undone {
			cond.Reason = "RolledBack"
		}
	}
	status := map[string]any{}
	if s, found, _ := unstructured.NestedMap(l.obj.Object, "status"); found {
		status = runtime.DeepCopyJSON(s)
	}
	var conds []metav1.Condition
	if raw, found, _ := unstructured.NestedSlice(status, "conditions"); found {
		if b, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(b, &conds)
		}
	}
	cond.ObservedGeneration = l.obj.GetGeneration()
	meta.SetStatusCondition(&conds, cond)
	extra := map[string]any{"observedGeneration": l.obj.GetGeneration()}
	switch l.ref.Kind {
	case "Interface":
		link, _, _ := unstructured.NestedString(l.obj.Object, "spec", "link")
		if st, err := ReadLink(link); err == nil {
			extra["mac"], extra["operState"] = st.MAC, st.OperState
			extra["addresses"] = toAny(st.Addresses)
		}
	case "DHCPServer":
		mine := leasesOn(ok, l.ref.Name, leases)
		extra["activeLeases"], extra["leases"] = nil, nil
		if len(mine) > 0 {
			items := make([]any, 0, len(mine))
			for _, le := range mine {
				u, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&le)
				items = append(items, u)
			}
			extra["activeLeases"], extra["leases"] = int64(len(mine)), items
		}
	case "Safeguard":
		extra["running"], extra["confirmed"], extra["rolledBack"] = c.trial.ConfirmedRev, c.trial.ConfirmedRev, c.trial.RolledBack
		if c.trial.Pending != "" {
			extra["running"], extra["pending"] = c.trial.Pending, c.trial.Pending
			extra["deadline"] = c.trial.Deadline.UTC().Format(time.RFC3339)
		} else {
			extra["pending"], extra["deadline"] = nil, nil
		}
		for k, v := range extra {
			if v == "" {
				extra[k] = nil
			}
		}
	case "BGPPeer":
		st, found := sessions[PeerProtocol(l.ref.Name)]
		if !found {
			st = PeerState{State: "Down"}
		}
		extra["state"], extra["since"], extra["routesReceived"] = st.State, st.Since, int64(st.RoutesReceived)
	}
	for k, v := range extra {
		if v == nil {
			delete(status, k)
		} else {
			status[k] = v
		}
	}
	condsU, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&struct {
		Items []metav1.Condition `json:"items"`
	}{conds})
	status["conditions"] = condsU["items"]
	old, _, _ := unstructured.NestedMap(l.obj.Object, "status")
	if equality.Semantic.DeepEqual(old, status) {
		return
	}
	updated := l.obj.DeepCopy()
	updated.Object["status"] = status
	plural := ""
	for _, k := range kinds {
		if k.Kind == l.ref.Kind {
			plural = k.Plural
		}
	}
	if _, err := c.Client.Resource(resource(plural)).UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		klog.V(2).Infof("status of %s: %v", l.ref, err)
	}
}

// leasesOn picks the leases inside a DHCP server's network.
func leasesOn(c Config, server string, leases []v1.Lease) []v1.Lease {
	var out []v1.Lease
	for _, d := range c.DHCP {
		if d.Name != server {
			continue
		}
		subnet := linkNet4(c.Interfaces, d.Spec.Link)
		for _, l := range leases {
			if a, err := netip.ParseAddr(l.Address); err == nil && subnet.Contains(a) {
				out = append(out, l)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

func toAny(ss []string) any {
	if len(ss) == 0 {
		return nil
	}
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// rolledBack is why a resource does not run: its revision ran out of time
// on trial.
type rolledBack struct{ msg string }

func (r rolledBack) Error() string { return r.msg }
