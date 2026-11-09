package hwcontrol

import (
	"encoding/json"
	"testing"
	"time"

	"virtual_sensors/internal/haws"
	"virtual_sensors/internal/host"
	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hotwater"
)

type clk struct{ t time.Time }

func (c *clk) Now() time.Time { return c.t }

type call struct {
	domain, service string
	data, target    map[string]any
}

type fakeHA struct {
	calls []call
	id    int
}

func (f *fakeHA) CallService(domain, service string, data, target map[string]any) (int, error) {
	f.calls = append(f.calls, call{domain, service, data, target})
	f.id++
	return f.id, nil
}

type pub struct {
	last map[string]modules.OutputUpdate
}

func (p *pub) PublishState(u modules.OutputUpdate) error { p.last[u.ObjectID] = u; return nil }

type world struct {
	t   *testing.T
	clk *clk
	h   *host.Host
	ha  *fakeHA
	pub *pub
	ctl *Module
}

func newWorld(t *testing.T, opts string) *world {
	hw := hotwater.New()
	if err := hw.Init(nil); err != nil {
		t.Fatal(err)
	}
	ctl := New()
	if err := ctl.Configure([]byte(opts)); err != nil {
		t.Fatal(err)
	}
	if err := ctl.Init(nil); err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, clk: &clk{t: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}, ha: &fakeHA{}, pub: &pub{last: map[string]modules.OutputUpdate{}}, ctl: ctl}
	w.h = host.New([]modules.Module{hw, ctl}, w.pub, w.clk, "")
	return w
}

func (w *world) state(id, state string, attrs map[string]any) {
	// hot_water drops an update not strictly after the previous one, so
	// successive test states get distinct times.
	w.clk.t = w.clk.t.Add(time.Millisecond)
	ev := haws.StateEvent{EntityID: id, Time: w.clk.t, State: state}
	if attrs != nil {
		ev.Attrs = map[string]json.RawMessage{}
		for k, v := range attrs {
			b, _ := json.Marshal(v)
			ev.Attrs[k] = b
		}
	}
	w.h.HandleState(ev, false)
}

func (w *world) cover(pos float64) {
	w.state("cover.hot_water_switch", "open", map[string]any{"current_position": pos})
	w.state("sensor.hot_water_switch_target_position", jsonNum(pos), nil)
}

func jsonNum(v float64) string { b, _ := json.Marshal(v); return string(b) }

func (w *world) run(d time.Duration, grid float64) {
	end := w.clk.t.Add(d)
	for w.clk.t.Before(end) {
		w.clk.t = w.clk.t.Add(5 * time.Second)
		if w.clk.t.Second()%60 == 0 {
			w.state("sensor.grid_meter_power", jsonNum(grid), nil)
		}
		w.h.Tick()
	}
}

func (w *world) bootInputs(armed string, pos float64) {
	w.cover(pos)
	w.state("sensor.hot_water_switch_motor_state", "stopped", nil)
	w.state("sensor.hot_water_switch_battery", "80", nil)
	w.state("input_select.hot_water", "Auto (Cost-saving)", nil)
	w.state("input_boolean.hot_water_controller_armed", armed, nil)
	w.state("input_boolean.hot_water_watchdog_tripped", "off", nil)
	w.state("input_button.hot_water_boost", "2026-01-01T00:00:00+00:00", nil)
	w.state("sensor.loaver_power", "0", nil)
	for _, l := range hotwater.KnownLoadEntities {
		w.state("sensor."+l, "10", nil)
	}
	w.state("sensor.grid_meter_power", "500", nil)
	w.h.LinkUp(w.ha)
}

func (w *world) coverCalls() []call {
	var out []call
	for _, c := range w.ha.calls {
		if c.domain == "cover" {
			out = append(out, c)
		}
	}
	return out
}

// Default options: SHADOW (D8: disarmed by default). The whole wiring runs:
// residual reaches the core through the hook (else the heartbeat would be
// withheld), telemetry is published, and the cover is never commanded even
// with the HA arming boolean on and the cover below 90.
func TestShadowByDefaultEndToEnd(t *testing.T) {
	w := newWorld(t, `{"modules":["hot_water","hot_water_control"]}`)
	w.bootInputs("on", 0)
	w.run(5*time.Minute, 500)
	if len(w.coverCalls()) != 0 {
		t.Fatalf("shadow commanded the cover: %+v", w.coverCalls())
	}
	hb, ok := w.pub.last["hot_water_controller_heartbeat"]
	if !ok || hb.StateString == "" {
		t.Fatal("no heartbeat: the residual did not reach the core through the hook")
	}
	if st := w.pub.last["hot_water_controller_state"].StateString; st != "IDLE_OFF" {
		t.Fatalf("state %q, want IDLE_OFF (observed)", st)
	}
	if w.ctl.Controller().Live() {
		t.Fatal("live by default")
	}
}

// hwc_mode: live + the HA boolean: a cover below 90 at boot is commanded
// to 99 through the HA client (row 4), and a conflict alert goes to both
// notification channels.
func TestLiveCommandsCoverAndNotifiesBothChannels(t *testing.T) {
	w := newWorld(t, `{"hwc_mode":"live"}`)
	w.bootInputs("on", 0)
	w.run(10*time.Second, 500)
	cc := w.coverCalls()
	if len(cc) != 1 || cc[0].service != "set_cover_position" || cc[0].data["position"] != 99 || cc[0].target["entity_id"] != "cover.hot_water_switch" {
		t.Fatalf("cover calls %+v", cc)
	}
	w.state("automation.hwc_old_thing", "on", nil) // prefix-matched conflict
	w.run(10*time.Second, 500)
	var phone, persistent bool
	for _, c := range w.ha.calls {
		if c.domain == "notify" && c.service == "mobile_app_edmund15max" {
			phone = true
		}
		if c.domain == "persistent_notification" && c.service == "create" {
			persistent = true
		}
	}
	if !phone || !persistent {
		t.Fatalf("notification channels phone=%v persistent=%v (%+v)", phone, persistent, w.ha.calls)
	}
}

// The armed boolean off: live option alone does not arm (two keys).
func TestLiveNeedsBothKeys(t *testing.T) {
	w := newWorld(t, `{"hwc_mode":"live"}`)
	w.bootInputs("off", 0)
	w.run(time.Minute, 500)
	if len(w.coverCalls()) != 0 {
		t.Fatal("moved with only one key")
	}
}

// Without hot_water the controller stays in SHADOW, degraded:no_detector.
func TestNoHotWaterModule(t *testing.T) {
	ctl := New()
	_ = ctl.Configure([]byte(`{"hwc_mode":"live"}`))
	_ = ctl.Init(nil)
	c := &clk{t: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	p := &pub{last: map[string]modules.OutputUpdate{}}
	h := host.New([]modules.Module{ctl}, p, c, "")
	ha := &fakeHA{}
	h.HandleState(haws.StateEvent{EntityID: "input_boolean.hot_water_controller_armed", Time: c.t, State: "on"}, false)
	h.LinkUp(ha)
	for i := 0; i < 20; i++ {
		c.t = c.t.Add(5 * time.Second)
		h.Tick()
	}
	if len(ha.calls) != 0 {
		t.Fatal("acted without the detector")
	}
	if st := p.last["hot_water_controller_state"].StateString; st != "degraded:no_detector" {
		t.Fatalf("state %q", st)
	}
}

func TestOptionsClampAndParse(t *testing.T) {
	m := New()
	if err := m.Configure([]byte(`{"hwc_backstop_alert_h": 40, "hwc_evening_off_from": "nope", "hwc_max_moves_day": 4, "hwc_planner_max_block_min": 120, "hwc_cover_entity": "cover.other"}`)); err != nil {
		t.Fatal(err)
	}
	if m.cfg.AlertAfter != 18*time.Hour || m.cfg.EveningOffFromMin != 18*60 || m.cfg.MaxMovesDay != 4 || m.pcfg.MaxBlock != 2*time.Hour {
		t.Fatalf("cfg %+v pcfg %+v", m.cfg, m.pcfg)
	}
	if m.ent.Target != "other_target_position" || m.ent.Cover != "cover.other" {
		t.Fatalf("entities %+v", m.ent)
	}
	if m.cfg.Live {
		t.Fatal("live without hwc_mode")
	}
}
