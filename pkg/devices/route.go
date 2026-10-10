package devices

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/cel-go/cel"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/devices/v1alpha1"
)

// Condition is a route's when, compiled against its device's points: bits
// are bools, the rest numbers, compared with numbers of any kind.
type Condition struct {
	program cel.Program
}

// Compile checks a route's condition against the device's points.
func Compile(when string, points []v1.Point) (*Condition, error) {
	if when == "" {
		return nil, nil
	}
	opts := []cel.EnvOption{cel.CrossTypeNumericComparisons(true)}
	for _, p := range points {
		t := cel.DoubleType
		if IsBit(p) || p.Type == "bool" {
			t = cel.BoolType
		}
		opts = append(opts, cel.Variable(p.Name, t))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(when)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("when is %s, not a bool", ast.OutputType())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return nil, err
	}
	return &Condition{prg}, nil
}

// True evaluates the condition on a reading.
func (c *Condition) True(points []v1.Point, r Reading) (bool, error) {
	vars := map[string]any{}
	for _, p := range points {
		v, ok := r[p.Name]
		if !ok {
			return false, fmt.Errorf("no value for %s", p.Name)
		}
		if IsBit(p) || p.Type == "bool" {
			vars[p.Name] = v != 0
		} else {
			vars[p.Name] = v
		}
	}
	out, _, err := c.program.Eval(vars)
	if err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	return ok && b, nil
}

// Edge sends a route once each time its condition becomes true, and every
// reading when it has none.
type Edge struct{ was bool }

// Send reports whether this reading goes out.
func (e *Edge) Send(c *Condition, points []v1.Point, r Reading) (bool, error) {
	if c == nil {
		return true, nil
	}
	now, err := c.True(points, r)
	if err != nil {
		return false, err
	}
	rising := now && !e.was
	e.was = now
	return rising, nil
}

// Message is what a route sends: the device, when it was read, the points
// asked for, and the condition that sent it if any.
func Message(device string, route v1.RouteSpec, points []v1.Point, r Reading, at time.Time) []byte {
	values := map[string]any{}
	for _, p := range points {
		if len(route.Points) > 0 && !slices.Contains(route.Points, p.Name) {
			continue
		}
		if v, ok := r[p.Name]; ok {
			if IsBit(p) || p.Type == "bool" {
				values[p.Name] = v != 0
			} else {
				values[p.Name] = v
			}
		}
	}
	msg := map[string]any{"device": device, "time": at.UTC().Format(time.RFC3339Nano), "values": values}
	if route.When != "" {
		msg["when"] = route.When
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // a condition reads as written: && and >, not \u0026
	_ = enc.Encode(msg)
	return bytes.TrimRight(b.Bytes(), "\n")
}
