package evcontrol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"virtual_sensors/internal/haws"
	"virtual_sensors/internal/host"
	"virtual_sensors/internal/house"
	"virtual_sensors/internal/modules"
)

// night1123Hub is the house planner's forecast for the night of 2026-11-23:
// the heat pump boosting on L2 all night (1.7 kW on one phase), the immersion
// heater on L1+L2 (3 kW, 7.5 A each) from 02:00 to 05:15, base 3 A per phase.
func night1123Hub(now time.Time) *house.Hub {
	pm, _ := house.ParsePhaseMap("heater=L1+L2;hp=L2;ev=L1+L2+L3")
	hub := house.NewHub()
	hub.SetForecast(house.ComputeForecast(house.ForecastIn{Now: now, Horizon: 24 * time.Hour, BudgetA: 19,
		BaseA: [3]float64{3, 3, 3}, Map: pm,
		Heater: house.HeaterPlan{Valid: true, NominalW: 3000, Windows: []house.HeaterWindow{
			{Start: now.Add(-time.Hour), End: at(24, 2, 0), On: false},
			{Start: at(24, 2, 0), End: at(24, 5, 15), On: true},
			{Start: at(24, 5, 15), End: at(25, 12, 0), On: false}}},
		HP: house.HPPlan{Valid: true, W: map[int64]float64{}, MaxW: 1700}}))
	return hub
}

func nightPrices(h int) float64 {
	if h >= 2 && h < 6 {
		return 0.3
	}
	return 1.0
}

func newNightEnv(t *testing.T, opts string, armed bool) *env {
	m := New()
	now := at(23, 23, 30)
	m.hub = night1123Hub(now)
	if err := m.Configure([]byte(opts)); err != nil {
		t.Fatal(err)
	}
	m.appendLine = func(string, []byte) error { return nil }
	if err := m.Init(nil); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, m: m, now: now}
	m.OnLink(true, now)
	e.feed(m.priceEnt, "", 0, rawPrices(at(23, 0, 0), at(25, 0, 0), nightPrices))
	st := "off"
	if armed {
		st = "on"
	}
	e.feed(ArmedEntity, st, 0, nil)
	e.feed("hot_water_controller_plan", "", 0, nil)
	e.feed("loaver_status", "awaiting_start", 0, map[string]json.RawMessage{"id": json.RawMessage(`"EH5YL3HF"`)})
	e.feed("loaver_power", "0", 0, nil)
	e.grid(2000)
	return e
}

// Tonight's scenario of 11-23: the old per-slot ceiling collapsed (L2 busy
// with the heat pump, then the heater too); single-phase on L3 still delivers.
func TestNight1123PlanFeasibleViaSinglePhaseL3(t *testing.T) {
	e := newNightEnv(t, `{"ev_need_kwh":20,"ev_mode":"live"}`, true)
	e.advance(30*time.Second, 2000)
	p := e.m.plan
	if !e.m.havePlan || !p.Feasible || p.DeadlineMode || p.PlannedKWh < 20 {
		t.Fatalf("plan not feasible: %+v", p)
	}
	heater := 0
	for _, s := range p.Slots {
		if s.Start.Before(at(23, 23, 30)) || !s.Start.Before(at(24, 6, 30)) {
			t.Fatalf("slot outside 23:30-06:30: %v", s.Start)
		}
		in := !s.Start.Before(at(24, 2, 0)) && s.Start.Before(at(24, 5, 15))
		if in {
			heater++
			// L2 has 2 A left: three phases is out; single phase L3 at 16 A, 3.68 kW x 0.9.
			if s.Amps != 16 || !near(s.KW, 3.68*0.9) {
				t.Fatalf("heater-window slot %v: %v A %v kW, want single-phase L3 16 A %.2f kW", s.Start, s.Amps, s.KW, 3.68*0.9)
			}
		} else if s.Amps != 9 || !near(s.KW, 6.21*0.9) { // L2 10.4 A: three phases at 9 A
			t.Fatalf("slot %v: %v A %v kW", s.Start, s.Amps, s.KW)
		}
	}
	if heater != 13 {
		t.Fatalf("the cheap heater-window slots must all be used, got %d", heater)
	}
	// Drive it: pause until the first planned slot, resume there, no limit
	// command at all while charging (only the one release at arming).
	e.setStatus("charging")
	e.advance(3*time.Hour, 2000)
	rel := len(e.svc("set_charger_dynamic_limit"))
	if rel != 1 || len(e.svc("set_circuit_dynamic_limit")) != 1 {
		t.Fatalf("want exactly the one release, got %+v", e.calls)
	}
	for _, a := range e.actions() {
		if a != "start" && a != "resume" && a != "pause" {
			t.Fatalf("unexpected charger action %q in %v", a, e.actions())
		}
	}
	if len(e.actions()) == 0 {
		t.Fatal("no charger command at all")
	}
	for _, c := range e.calls {
		if c.Service == "set_phase_mode" || strings.Contains(c.Service, "phase") {
			t.Fatalf("phase command: %+v", c)
		}
	}
	for _, n := range e.notes {
		if n.Data["title"] == "EV guard breach" {
			t.Fatal("breach alert sent")
		}
	}
	// And the published plan carries the deliverable kW, positionally.
	var attrs struct {
		Slots  [][]any  `json:"slots"`
		Fields []string `json:"slot_fields"`
	}
	if err := json.Unmarshal([]byte(e.m.planOutput(e.now).AttributesJSON), &attrs); err != nil {
		t.Fatal(err)
	}
	if strings.Join(attrs.Fields, ",") != "start,amps,kw,price_kr_per_kwh" || len(attrs.Slots) == 0 || len(attrs.Slots[0]) != 4 {
		t.Fatalf("plan slot fields: %+v", attrs)
	}
}

// The same night routed through the host: inputs arrive as full entity ids
// and the host strips "sensor."; limits and the dead-man call go out as HA
// service calls.
func TestHostRoutedNight1123(t *testing.T) {
	m := New()
	now := at(23, 23, 30)
	m.hub = night1123Hub(now)
	if err := m.Configure([]byte(`{"ev_need_kwh":20,"ev_mode":"live"}`)); err != nil {
		t.Fatal(err)
	}
	m.appendLine = func(string, []byte) error { return nil }
	m.Init(nil)
	clk := &hClock{t: now}
	pub := &hPub{}
	h := host.New([]modules.Module{m}, pub, clk, "")
	ha := &hHA{}
	h.LinkUp(ha)
	send := func(full, state string, attrs map[string]json.RawMessage) {
		if !h.Match(full) {
			t.Fatalf("host does not route %s", full)
		}
		h.HandleState(haws.StateEvent{EntityID: full, Time: clk.t, State: state, Attrs: attrs}, false)
	}
	send("sensor."+m.priceEnt, "", rawPrices(at(23, 0, 0), at(25, 0, 0), nightPrices))
	send("input_boolean.ev_controller_armed", "off", nil)
	send("sensor.loaver_status", "ready_to_charge", map[string]json.RawMessage{"id": json.RawMessage(`"EH5YL3HF"`)})
	send("sensor.loaver_power", "0", nil)
	answered := 0
	tick := func(n int) {
		for i := 0; i < n; i++ {
			clk.t = clk.t.Add(30 * time.Second)
			send("sensor.grid_meter_power", "2000", nil)
			h.Tick()
			for ; answered < ha.n; answered++ {
				h.HandleResult(haws.Result{ID: answered + 1, Success: true})
				switch ha.calls[answered] {
				case "easee.action_command:resume":
					send("sensor.loaver_status", "charging", nil)
				case "easee.action_command:pause":
					send("sensor.loaver_status", "ready_to_charge", nil)
				}
			}
		}
	}
	count := func(prefix string) int {
		n := 0
		for _, c := range ha.calls {
			if strings.HasPrefix(c, prefix) {
				n++
			}
		}
		return n
	}
	// Disarmed: honest state, and not one service call about charging.
	tick(10)
	if st, _ := lastOut(pub.outs, ObjState); st.StateString != "disarmed" || !strings.Contains(planAttrs(t, st)["reason"].(string), "disarmed: will not start or pause the charger") {
		t.Fatalf("disarmed state %+v", st)
	}
	if count("easee.") != 0 || count("timer.") != 0 {
		t.Fatalf("calls while disarmed: %v", ha.calls)
	}
	// Armed: one release, one postponement, then only start/resume/pause.
	send("input_boolean.ev_controller_armed", "on", nil)
	tick(2 * 60 * 3) // 3 h of 30 s ticks
	if count("easee.set_charger_dynamic_limit") != 1 || count("easee.set_circuit_dynamic_limit") != 1 {
		t.Fatalf("want one release: %v", ha.calls)
	}
	if n := count("timer.start"); n != 6 { // at arm and every 30 min over 3 h (0, 30, ... 150 min)
		t.Fatalf("dead-man postponements: %d", n)
	}
	if !m.havePlan || !m.plan.Feasible || m.plan.PlannedKWh < 20 {
		t.Fatalf("plan %+v", m.plan)
	}
	if st, _ := lastOut(pub.outs, ObjState); st.StateString == "disarmed" {
		t.Fatalf("armed but still %q", st.StateString)
	}
	// Disarm again: dead-man postponement stops.
	before := count("timer.start")
	send("input_boolean.ev_controller_armed", "off", nil)
	tick(2 * 60)
	if count("timer.start") != before {
		t.Fatalf("postponed while disarmed: %v", ha.calls)
	}
}

// Stage B: while disarmed the state says so, whatever the plan would do.
func TestDisarmedStateIsHonest(t *testing.T) {
	for _, opts := range []string{`{"ev_need_kwh":10,"ev_mode":"live"}`, `{"ev_need_kwh":10}`} { // key off; shadow mode
		e := newEnv(t, opts, opts == `{"ev_need_kwh":10}`)
		e.setStatus("charging")
		e.advance(time.Minute, 1000)
		outs := e.m.publishAll(e.now, true)
		u, ok := lastOut(outs, ObjState)
		if !ok || u.StateString != "disarmed" {
			t.Fatalf("%s: state %+v", opts, u)
		}
		a := planAttrs(t, u)
		if a["reason"] != "disarmed: will not start or pause the charger" || a["would_state"] != "would_pause" {
			t.Fatalf("%s: attrs %v", opts, a)
		}
		if w, _ := lastOut(outs, ObjWould); w.StateString != "pause" {
			t.Fatalf("would command %+v", w)
		}
	}
	// Armed and live: the real state, no would_state.
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.advance(time.Minute, 1000)
	u, _ := lastOut(e.m.publishAll(e.now, true), ObjState)
	if u.StateString == "disarmed" {
		t.Fatalf("armed state reads disarmed: %+v", u)
	}
	if _, has := planAttrs(t, u)["would_state"]; has {
		t.Fatal("would_state present while armed")
	}
}

// Charge now while disarmed does not pretend.
func TestChargeNowWhileDisarmedDoesNotPretend(t *testing.T) {
	e := newEnv(t, cnOpts, false)
	e.advance(time.Minute, 1000)
	e.reset()
	e.press(ButtonChargeNow)
	if e.m.cn.On {
		t.Fatal("override activated while disarmed")
	}
	if got := logbookEntries(e); len(got) != 1 || got[0] != "EV charge now not started" {
		t.Fatalf("logbook %v", got)
	}
	ns := notesTitled(e, "EV charge now not started")
	if len(ns) != 1 || !strings.Contains(ns[0].Data["message"].(string), "disarmed") || !strings.Contains(ns[0].Data["message"].(string), "no charging was started") {
		t.Fatalf("push %+v", e.notes)
	}
	e.advance(5*time.Minute, 1000)
	if len(e.calls) != 0 {
		t.Fatalf("calls while disarmed: %+v", e.calls)
	}
	if _, a := cnState(t, e.m); a["reason"] != "not started: controller disarmed" {
		t.Fatalf("reason %v", a["reason"])
	}
	// Shadow mode too, and through the host.
	e2 := newEnv(t, `{"ev_need_kwh":10}`, true)
	e2.advance(time.Minute, 1000)
	e2.reset()
	e2.press(ButtonChargeNow)
	if e2.m.cn.On || len(notesTitled(e2, "EV charge now not started")) != 1 {
		t.Fatalf("shadow: on=%v notes=%+v", e2.m.cn.On, e2.notes)
	}
}

// Removed options still present in saved options are tolerated.
func TestRemovedOptionsTolerated(t *testing.T) {
	m := New()
	if err := m.Configure([]byte(`{"ev_limit_ttl_min":15,"ev_stale_after_min":10,"ev_fuse_a":25,"ev_derate":0.8}`)); err != nil {
		t.Fatal(err)
	}
	if m.p.FuseA != 25 || m.p.Derate != 0.8 {
		t.Fatalf("%+v", m.p)
	}
	m2 := New()
	m2.Configure([]byte(`{"ev_fuse_a":2,"ev_derate":3}`))
	if m2.p.FuseA != 20 || m2.p.Derate != 0.9 {
		t.Fatalf("defaults not applied: %+v", m2.p)
	}
}
