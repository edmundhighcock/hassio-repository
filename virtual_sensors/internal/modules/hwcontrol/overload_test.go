package hwcontrol

import (
	"strings"
	"testing"
	"time"
)

// 0.6.9 shipped a module that never received data because it kept the
// "sensor." prefix the host strips. The Equalizer inputs must be declared
// without it (binary_sensor.* keeps its domain).
func TestEqualizerInputsUseHostConvention(t *testing.T) {
	m := New()
	if err := m.Configure([]byte(`{"ev_equalizer_entity":"sensor.my_eq"}`)); err != nil {
		t.Fatal(err)
	}
	ids := map[string][]string{}
	for _, in := range m.Inputs() {
		if strings.HasPrefix(in.EntityID, "sensor.") {
			t.Fatalf("input %q keeps the sensor. prefix; the host never delivers it", in.EntityID)
		}
		ids[in.EntityID] = in.Attributes
	}
	if len(ids["my_eq"]) != 3 {
		t.Fatalf("Equalizer attributes not requested: %v", ids)
	}
	if _, ok := ids["binary_sensor.equalizer_online"]; !ok {
		t.Fatalf("online flag not an input: %v", ids)
	}
	if !New().hasInput("equalizer_current") {
		t.Fatal("default Equalizer input missing")
	}
}

func (m *Module) hasInput(id string) bool {
	for _, in := range m.Inputs() {
		if in.EntityID == id {
			return true
		}
	}
	return false
}

func TestOverloadOptionsAndOutput(t *testing.T) {
	m := New()
	if !m.cfg.OverloadEnabled || m.cfg.OverloadAmps != 19.8 {
		t.Fatalf("defaults %+v", m.cfg)
	}
	if err := m.Configure([]byte(`{"hwc_overload_enabled":false,"hwc_overload_amps":18.5}`)); err != nil {
		t.Fatal(err)
	}
	if m.cfg.OverloadEnabled || m.cfg.OverloadAmps != 18.5 {
		t.Fatalf("set %+v", m.cfg)
	}
	_ = m.Configure([]byte(`{"hwc_overload_amps":40}`))
	if m.cfg.OverloadAmps != 19.8 || !m.cfg.OverloadEnabled {
		t.Fatalf("clamp %+v", m.cfg)
	}
	found := false
	for _, o := range New().Outputs() {
		if o.ObjectID == "hot_water_overload" && o.HasAttributes && o.UniqueID != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("hot_water_overload output not registered")
	}
}

// End to end through the host: the Equalizer state event is routed by the
// host's no-prefix id, reaches the core, and with the heater drawing a kettle
// overload makes hwcontrol command the cover OFF (resume is covered in core).
func TestOverloadEndToEndThroughHost(t *testing.T) {
	w := newWorld(t, `{"hwc_mode":"live"}`)
	w.bootInputs("on", 99)
	w.run(4*time.Minute, 500) // plan wait passes: IDLE_ON
	if st := w.pub.last["hot_water_controller_state"].StateString; st != "IDLE_ON" {
		t.Fatalf("state %q", st)
	}
	// The host must pass the three attributes through for the Equalizer.
	if wl := w.h.AttrWhitelist("sensor.equalizer_current"); len(wl) < 3 {
		t.Fatalf("attribute whitelist %v", wl)
	}
	if !w.h.Match("sensor.equalizer_current") || !w.h.Match("binary_sensor.equalizer_online") {
		t.Fatal("host does not route the Equalizer entities to any module")
	}
	w.state("binary_sensor.equalizer_online", "on", nil)
	w.run(3*time.Minute, 3500) // the heater: a ~3 kW step on the residual
	eq := func(l1, l2 float64) {
		w.state("sensor.equalizer_current", "1", map[string]any{"state_currentL1": l1, "state_currentL2": l2, "state_currentL3": 0.0})
	}
	eq(20.3, 14.1)
	w.run(30*time.Second, 3500)
	eq(20.4, 14.2)
	w.run(30*time.Second, 3500)
	cc := w.coverCalls()
	if len(cc) == 0 || cc[0].data["position"] != 0 {
		t.Fatalf("overload did not reach the cover through the host: %+v (state %q)", cc, w.pub.last["hot_water_controller_state"].StateString)
	}
	if got := w.pub.last["hot_water_overload"].StateString; got != "paused" {
		t.Fatalf("hot_water_overload %q", got)
	}
	if got := w.pub.last["hot_water_controller_off_gate"].StateString; got != "overload_pause" {
		t.Fatalf("off_gate %q", got)
	}
}
