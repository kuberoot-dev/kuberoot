package devices

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
)

var press = []v1.Point{
	{Name: "temperature", Type: "int16", Scale: "0.1"},
	{Name: "running", Kind: "coil"},
}

func TestConditionSendsOnRise(t *testing.T) {
	c, err := Compile("running && temperature > 80", press)
	if err != nil {
		t.Fatal(err)
	}
	var e Edge
	for i, step := range []struct {
		r    Reading
		send bool
	}{
		{Reading{"temperature": 70, "running": 1}, false},
		{Reading{"temperature": 85.3, "running": 1}, true}, // becomes true: sent
		{Reading{"temperature": 90, "running": 1}, false},  // still true: not again
		{Reading{"temperature": 90, "running": 0}, false},
		{Reading{"temperature": 91, "running": 1}, true}, // true again
	} {
		got, err := e.Send(c, press, step.r)
		if err != nil || got != step.send {
			t.Fatalf("step %d: send %v %v, want %v", i, got, err, step.send)
		}
	}
	var always Edge
	if got, _ := always.Send(nil, press, Reading{"temperature": 1, "running": 0}); !got {
		t.Error("a route with no condition held a reading back")
	}
}

func TestCompileRejects(t *testing.T) {
	for _, when := range []string{"temperature + 1", "pressure > 3", "temperature >"} {
		if _, err := Compile(when, press); err == nil {
			t.Errorf("%q compiled", when)
		}
	}
}

func TestMessage(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	raw := Message("press-7", v1.RouteSpec{Points: []string{"temperature"}, When: "temperature > 80"}, press,
		Reading{"temperature": 85.3, "running": 1}, at)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	values := m["values"].(map[string]any)
	if m["device"] != "press-7" || values["temperature"] != 85.3 || values["running"] != nil || m["when"] != "temperature > 80" {
		t.Fatalf("message %s", raw)
	}
}

func TestMessageKeepsConditionReadable(t *testing.T) {
	raw := Message("p", v1.RouteSpec{When: "running && temperature > 80"}, press, Reading{"temperature": 1, "running": 1}, time.Now())
	if !strings.Contains(string(raw), `"when":"running && temperature > 80"`) {
		t.Fatalf("condition escaped: %s", raw)
	}
}
