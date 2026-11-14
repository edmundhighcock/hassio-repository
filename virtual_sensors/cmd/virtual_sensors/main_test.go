package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"virtual_sensors/internal/host"
	"virtual_sensors/internal/modules"
)

type recPub struct{ outs []modules.OutputUpdate }

func (p *recPub) PublishState(u modules.OutputUpdate) error { p.outs = append(p.outs, u); return nil }

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// TestPriceShedRegisteredAndWired: main.go imports the module (so it is in
// the registry), its sensor and button are among what main hands to MQTT
// discovery, and a press on that button reaches the module through the host.
func TestPriceShedRegisteredAndWired(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `"virtual_sensors/internal/modules/priceshed"`) {
		t.Fatal("main.go does not import priceshed")
	}
	ctor, ok := modules.Get("price_shed")
	if !ok {
		t.Fatal("price_shed is not registered")
	}
	m := ctor()
	if c, ok := m.(modules.Configurable); ok {
		if err := c.Configure(nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Init(nil); err != nil {
		t.Fatal(err)
	}
	outputs, buttons := collectSpecs([]modules.Module{m})
	var sensor, button bool
	for _, o := range outputs {
		sensor = sensor || o.ObjectID == "price_shed_tool_shed_lights"
	}
	for _, b := range buttons {
		button = button || b.ObjectID == "replan_price_shed"
	}
	if !sensor || !button {
		t.Fatalf("sensor=%v button=%v outputs=%v buttons=%v", sensor, button, outputs, buttons)
	}
	// host routes the press to the module: it answers with its sensor state.
	pub := &recPub{}
	h := host.New([]modules.Module{m}, pub, fixedClock{time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}, "")
	h.Press("replan_price_shed")
	if len(pub.outs) != 1 || pub.outs[0].ObjectID != "price_shed_tool_shed_lights" {
		t.Fatalf("press did not reach the module: %+v", pub.outs)
	}
}
