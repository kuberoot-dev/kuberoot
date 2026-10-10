package devices

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	v1 "github.com/tym83/kuberoot/pkg/apis/devices/v1alpha1"
)

// redfishDevice reads a server's BMC over Redfish: its health, temperatures,
// fans, power supplies, power drawn and how many events its logs hold. The
// BMC says what it has; the values are named after what it calls them.
type redfishDevice struct {
	spec   v1.DeviceSpec
	creds  Credentials
	client *http.Client

	mu   sync.Mutex
	user string
	pass string
	auth bool
}

// Health of a Redfish resource as a number: 0 OK, 1 Warning, 2 Critical.
var healthValue = map[string]float64{"OK": 0, "Warning": 1, "Critical": 2}

type rfStatus struct {
	Health string `json:"Health"`
	State  string `json:"State"`
}

type rfLink struct {
	ID string `json:"@odata.id"`
}

type rfCollection struct {
	Members []rfLink `json:"Members"`
	Count   *int     `json:"Members@odata.count"`
}

func (d *redfishDevice) get(path string, into any) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(d.spec.Address, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if d.user != "" {
		req.SetBasicAuth(d.user, d.pass)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

func (d *redfishDevice) members(path string) ([]string, error) {
	var c rfCollection
	if err := d.get(path, &c); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range c.Members {
		out = append(out, m.ID)
	}
	return out, nil
}

// Read walks the BMC's systems, chassis and managers. A part a BMC does not
// have is skipped; the read fails only when the BMC does not answer at all.
func (d *redfishDevice) Read() (Reading, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.auth {
		creds, err := d.creds()
		if err != nil {
			return nil, err
		}
		d.user, d.pass, d.auth = creds["username"], creds["password"], true
	}
	var root struct {
		Systems  rfLink `json:"Systems"`
		Chassis  rfLink `json:"Chassis"`
		Managers rfLink `json:"Managers"`
	}
	if err := d.get("/redfish/v1/", &root); err != nil {
		d.auth = false // the credentials may have changed
		return nil, err
	}
	r := Reading{}
	if root.Systems.ID != "" {
		systems, err := d.members(root.Systems.ID)
		if err != nil {
			return nil, err
		}
		for _, s := range systems {
			d.system(s, r)
		}
	}
	if root.Chassis.ID != "" {
		chassis, err := d.members(root.Chassis.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range chassis {
			d.chassis(c, r)
		}
	}
	if root.Managers.ID != "" {
		if managers, err := d.members(root.Managers.ID); err == nil {
			for _, m := range managers {
				d.logs(m, "manager_"+leaf(m), r)
			}
		}
	}
	return pick(d.spec.Points, r), nil
}

func (d *redfishDevice) system(path string, r Reading) {
	var s struct {
		Status     rfStatus `json:"Status"`
		PowerState string   `json:"PowerState"`
	}
	if d.get(path, &s) != nil {
		return
	}
	id := "system_" + leaf(path)
	if h, ok := healthValue[s.Status.Health]; ok {
		r[id+"_health"] = h
	}
	if s.PowerState != "" {
		r[id+"_powered_on"] = boolValue(s.PowerState == "On")
	}
	d.logs(path, id, r)
}

func (d *redfishDevice) chassis(path string, r Reading) {
	var c struct {
		Thermal rfLink `json:"Thermal"`
		Power   rfLink `json:"Power"`
	}
	if d.get(path, &c) != nil {
		return
	}
	if c.Thermal.ID != "" {
		var t struct {
			Temperatures []struct {
				Name           string   `json:"Name"`
				MemberID       string   `json:"MemberId"`
				ReadingCelsius *float64 `json:"ReadingCelsius"`
			} `json:"Temperatures"`
			Fans []struct {
				Name         string   `json:"Name"`
				FanName      string   `json:"FanName"`
				MemberID     string   `json:"MemberId"`
				Reading      *float64 `json:"Reading"`
				ReadingUnits string   `json:"ReadingUnits"`
				Status       rfStatus `json:"Status"`
			} `json:"Fans"`
		}
		if d.get(c.Thermal.ID, &t) == nil {
			temps := sensorNames{}
			for i, s := range t.Temperatures {
				if s.ReadingCelsius != nil {
					r["temperature_"+temps.of(s.Name, s.MemberID, i)+"_celsius"] = *s.ReadingCelsius
				}
			}
			fans := sensorNames{}
			for i, f := range t.Fans {
				n := f.Name
				if n == "" {
					n = f.FanName
				}
				n = fans.of(n, f.MemberID, i)
				if f.Reading != nil {
					unit := "rpm"
					if strings.EqualFold(f.ReadingUnits, "Percent") {
						unit = "percent"
					}
					r["fan_"+n+"_"+unit] = *f.Reading
				}
				if h, ok := healthValue[f.Status.Health]; ok {
					r["fan_"+n+"_health"] = h
				}
			}
		}
	}
	if c.Power.ID != "" {
		var p struct {
			PowerControl []struct {
				Name               string   `json:"Name"`
				MemberID           string   `json:"MemberId"`
				PowerConsumedWatts *float64 `json:"PowerConsumedWatts"`
			} `json:"PowerControl"`
			PowerSupplies []struct {
				Name     string   `json:"Name"`
				MemberID string   `json:"MemberId"`
				Status   rfStatus `json:"Status"`
			} `json:"PowerSupplies"`
		}
		if d.get(c.Power.ID, &p) == nil {
			controls := sensorNames{}
			for i, pc := range p.PowerControl {
				if pc.PowerConsumedWatts != nil {
					r["power_"+controls.of(pc.Name, pc.MemberID, i)+"_watts"] = *pc.PowerConsumedWatts
				}
			}
			psus := sensorNames{}
			for i, ps := range p.PowerSupplies {
				if h, ok := healthValue[ps.Status.Health]; ok {
					r["psu_"+psus.of(ps.Name, ps.MemberID, i)+"_health"] = h
				}
			}
		}
	}
}

// sensorNames names the sensors of one list: by what the BMC calls them,
// and, for two of the same name, by their member id or place as well.
type sensorNames map[string]bool

func (s sensorNames) of(n, member string, i int) string {
	base := name(n)
	if !s[base] {
		s[base] = true
		return base
	}
	id := member
	if id == "" {
		id = fmt.Sprint(i)
	}
	return base + "_" + name(id)
}

// logs counts the entries of a system's or a manager's log services.
func (d *redfishDevice) logs(path, id string, r Reading) {
	var res struct {
		LogServices rfLink `json:"LogServices"`
	}
	if d.get(path, &res) != nil || res.LogServices.ID == "" {
		return
	}
	services, err := d.members(res.LogServices.ID)
	if err != nil {
		return
	}
	for _, s := range services {
		var ls struct {
			Entries rfLink `json:"Entries"`
		}
		if d.get(s, &ls) != nil || ls.Entries.ID == "" {
			continue
		}
		var e rfCollection
		if d.get(ls.Entries.ID, &e) != nil {
			continue
		}
		n := len(e.Members)
		if e.Count != nil {
			n = *e.Count
		}
		r[id+"_log_"+name(leaf(s))+"_entries"] = float64(n)
	}
}

func (d *redfishDevice) Close() {}

var notName = regexp.MustCompile(`[^a-z0-9]+`)

// name makes what a BMC calls something into part of a value's name.
func name(s string) string {
	n := strings.Trim(notName.ReplaceAllString(strings.ToLower(s), "_"), "_")
	if n == "" {
		return "unnamed"
	}
	return n
}

func leaf(path string) string {
	path = strings.TrimRight(path, "/")
	return name(path[strings.LastIndex(path, "/")+1:])
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// pick keeps the values the points name, scaled; all of them when there are
// no points.
func pick(points []v1.Point, r Reading) Reading {
	if len(points) == 0 {
		return r
	}
	out := Reading{}
	for _, p := range points {
		if v, ok := r[p.Name]; ok {
			if s, err := scaled(p, v); err == nil {
				out[p.Name] = s
			}
		}
	}
	return out
}
