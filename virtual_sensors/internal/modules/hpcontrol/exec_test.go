package hpcontrol

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"virtual_sensors/internal/modules"
)

// rig drives a Module the way the host does, with a fake service caller:
// every effect is recorded, and device calls are echoed back as the state
// change SmartIR and the Dyson integration would report.
type rig struct {
	t    *testing.T
	m    *Module
	now  time.Time
	all  []modules.Effect
	outs []modules.OutputUpdate
	// fake devices
	hpState, hpFan, fanOn string
	hpSet, fanPct         float64
	noEcho                bool
}

func rawStr(s string) json.RawMessage  { return json.RawMessage(`"` + s + `"`) }
func rawNum(v float64) json.RawMessage { return json.RawMessage(fmt.Sprintf("%g", v)) }

// newRig builds a module in the given mode with the helper armed or not, at
// 12:00 on 10 Nov, prices known (the dayPrices shape), HP off, Dyson off.
func newRig(t *testing.T, mode string, armed bool, extra string, start time.Time, comfort float64, setup ...func(*rig)) *rig {
	t.Helper()
	m := New()
	opts := fmt.Sprintf(`{"hp_mode":%q,"hp_forecast_entity":""%s}`, mode, extra)
	if err := m.Configure([]byte(opts)); err != nil {
		t.Fatal(err)
	}
	_ = m.Init(nil)
	r := &rig{t: t, m: m, now: start, hpState: "off", hpSet: 22, hpFan: "high", fanOn: "off", fanPct: 0}
	for _, f := range setup {
		f(r)
	}
	m.OnLink(true, r.now)
	m.OnStateUpdate(modules.StateUpdate{EntityID: m.priceEnt, Time: r.now, Available: true, Attrs: raw(t, at(10, 0, 0), 192, dayPrices)})
	r.feedNum(outdoorEnt, 3)
	r.comfort(comfort)
	r.feedNum(minimumEnt, 19)
	r.feedNum(desiredEnt, 20.5)
	r.feedState(presenceEnt, "Home")
	r.feedNum(hpPowerEnt, 8)
	st := "off"
	if armed {
		st = "on"
	}
	r.feedState(armedEnt, st)
	r.feedState(tvTimerEnt, "idle")
	r.feedState(tvActiveEnt, "off")
	r.pushHP()
	r.pushFan()
	r.tick(time.Minute)
	return r
}

func (r *rig) drain() {
	for i := 0; i < 20; i++ {
		es := r.m.Effects()
		if len(es) == 0 {
			return
		}
		for _, e := range es {
			r.all = append(r.all, e)
			r.apply(e)
		}
	}
	r.t.Fatal("effects did not settle")
}

func (r *rig) pushHP() {
	r.outs = append(r.outs, r.m.OnStateUpdate(modules.StateUpdate{EntityID: climateEnt, Time: r.now, State: r.hpState,
		Attrs: map[string]json.RawMessage{"temperature": rawNum(r.hpSet), "fan_mode": rawStr(r.hpFan)}})...)
}

func (r *rig) pushFan() {
	r.outs = append(r.outs, r.m.OnStateUpdate(modules.StateUpdate{EntityID: dysonEnt, Time: r.now, State: r.fanOn,
		Attrs: map[string]json.RawMessage{"percentage": rawNum(r.fanPct)}})...)
}

// apply is the fake device: what SmartIR / the Dyson do with a call.
func (r *rig) apply(e modules.Effect) {
	if r.noEcho {
		return
	}
	switch e.Domain + "." + e.Service {
	case "climate.turn_off":
		r.hpState = "off"
		r.pushHP()
	case "climate.set_temperature":
		r.hpSet = e.Data["temperature"].(float64)
		if hm, ok := e.Data["hvac_mode"]; ok {
			r.hpState = hm.(string)
		}
		r.pushHP()
	case "climate.set_fan_mode":
		r.hpFan = e.Data["fan_mode"].(string)
		r.pushHP()
	case "fan.turn_on":
		r.fanOn, r.fanPct = "on", float64(e.Data["percentage"].(int))
		r.pushFan()
	case "fan.turn_off":
		r.fanOn, r.fanPct = "off", 0
		r.pushFan()
	}
}

func (r *rig) feedNum(ent string, v float64) {
	r.outs = append(r.outs, feed(r.m, r.now, ent, v)...)
	r.drain()
}

func (r *rig) feedState(ent, st string) {
	r.outs = append(r.outs, r.m.OnStateUpdate(modules.StateUpdate{EntityID: ent, Time: r.now, State: st})...)
	r.drain()
}

func (r *rig) comfort(v float64) {
	r.outs = append(r.outs, comfortAt(r.m, r.now, v)...)
	r.drain()
}

func (r *rig) tick(d time.Duration) {
	r.now = r.now.Add(d)
	r.outs = append(r.outs, r.m.OnTick(r.now)...)
	r.drain()
}

// advance runs d in 1-minute ticks. With fresh, the sensors keep reporting
// every 30 minutes at comfort value v; without, only the clock moves.
func (r *rig) advance(d time.Duration, fresh bool, v float64) {
	for el := time.Duration(0); el < d; el += time.Minute {
		r.tick(time.Minute)
		if fresh && int((el+time.Minute)/time.Minute)%30 == 0 {
			r.comfort(v)
			r.feedNum(hpPowerEnt, 8)
		}
	}
}

func (r *rig) mark() int { return len(r.all) }

// devCalls are the climate and fan calls since mark.
func (r *rig) devCalls(mark int) []modules.Effect {
	var out []modules.Effect
	for _, e := range r.all[mark:] {
		if e.Domain == "climate" || e.Domain == "fan" {
			out = append(out, e)
		}
	}
	return out
}

func (r *rig) notes(mark int, code string) int {
	n := 0
	for _, e := range r.all[mark:] {
		if e.Domain == "persistent_notification" && e.Data["notification_id"] == "hp_controller_"+code {
			n++
		}
	}
	return n
}

func (r *rig) anyNotes(mark int) int {
	n := 0
	for _, e := range r.all[mark:] {
		if e.Domain == "persistent_notification" || e.Domain == "notify" {
			n++
		}
	}
	return n
}

func describe(es []modules.Effect) string {
	var s []string
	for _, e := range es {
		s = append(s, fmt.Sprintf("%s.%s%v", e.Domain, e.Service, e.Data))
	}
	return strings.Join(s, " ")
}

func (r *rig) out(obj string) (string, string) {
	for i := len(r.outs) - 1; i >= 0; i-- {
		if r.outs[i].ObjectID == obj {
			return r.outs[i].StateString, r.outs[i].AttributesJSON
		}
	}
	return "", ""
}

// ---- tests ----

func TestShadowAndUnarmedSendNothing(t *testing.T) {
	cases := []struct {
		name, mode string
		armed      bool
	}{{"shadow, helper on", "shadow", true}, {"live, helper off", "live", false}}
	for _, c := range cases {
		r := newRig(t, c.mode, c.armed, "", at(10, 12, 0), 18.6) // well below the minimum
		mk := r.mark()
		r.advance(2*time.Hour, true, 18.6)
		if es := r.devCalls(mk); len(es) != 0 {
			t.Fatalf("%s: sent %s", c.name, describe(es))
		}
		if n := r.anyNotes(0); n != 0 {
			t.Fatalf("%s: %d notifications", c.name, n)
		}
		if st, _ := r.out(ObjState); !strings.HasPrefix(st, "below_min") && !strings.HasPrefix(st, "would_") && st != "floor_at_risk" {
			t.Fatalf("%s: state %q", c.name, st)
		}
	}
	// Helper missing (not created yet on hass): also nothing.
	m := New()
	_ = m.Configure([]byte(`{"hp_mode":"live","hp_forecast_entity":""}`))
	if m.ex.active(m) {
		t.Fatal("live without the helper must not be active")
	}
}

func TestLiveSendsOnChangeOnly(t *testing.T) {
	r := newRig(t, "live", true, "", at(10, 12, 0), 18.6)
	if r.notes(0, "armed") != 1 {
		t.Fatalf("arm notification: %d", r.notes(0, "armed"))
	}
	// Below the minimum: heat at least at hold, fan high, Dyson at the day cap.
	calls := r.devCalls(0)
	if len(calls) == 0 {
		t.Fatal("expected commands")
	}
	if r.hpState != "heat" || r.hpFan != "high" || r.fanOn != "on" || r.fanPct != 60 {
		t.Fatalf("devices: %s %v %s %s %v after %s", r.hpState, r.hpSet, r.hpFan, r.fanOn, r.fanPct, describe(calls))
	}
	if r.hpSet != 22 && r.hpSet != 30 {
		t.Fatalf("set-point %v is neither hold nor boost", r.hpSet)
	}
	// Quiet period: nothing more is sent while nothing changes, however long.
	mk := r.mark()
	r.advance(90*time.Minute, true, 18.6)
	if es := r.devCalls(mk); len(es) != 0 {
		t.Fatalf("re-sent a state it was already in: %s", describe(es))
	}
	// A different target is sent once, and only the difference. Evening,
	// warm house: the plan is off, so heat pump off and Dyson off.
	r2 := newRig(t, "live", false, "", at(10, 18, 0), 22.5, func(r *rig) {
		r.hpState, r.hpSet, r.fanOn, r.fanPct = "heat", 22, "on", 50
	})
	mk = r2.mark()
	r2.feedState(armedEnt, "on")
	r2.tick(time.Minute)
	es := r2.devCalls(mk)
	if len(es) != 2 || es[0].Service != "turn_off" || es[1].Service != "turn_off" {
		t.Fatalf("evening off: %s", describe(es))
	}
	mk = r2.mark()
	r2.advance(time.Hour, true, 22.5)
	if es := r2.devCalls(mk); len(es) != 0 {
		t.Fatalf("repeated off: %s", describe(es))
	}
}

func TestHoldAndBoostMapping(t *testing.T) {
	m := New()
	_ = m.Configure([]byte(`{"hp_mode":"live","hp_hold_setpoint_c":21,"hp_boost_setpoint_c":29}`))
	m.hpState, m.hpSet, m.hpSetOK, m.hpFan = "off", 22, true, "auto"
	cs := m.hpCalls(Hold)
	if len(cs) != 2 || cs[0].Service != "set_temperature" || cs[0].Data["temperature"] != 21.0 || cs[0].Data["hvac_mode"] != "heat" ||
		cs[1].Service != "set_fan_mode" || cs[1].Data["fan_mode"] != "high" {
		t.Fatalf("hold from off: %+v", cs)
	}
	m.hpState, m.hpSet, m.hpFan = "heat", 21, "high"
	if cs := m.hpCalls(Hold); len(cs) != 0 {
		t.Fatalf("already in hold: %+v", cs)
	}
	if cs := m.hpCalls(Boost); len(cs) != 1 || cs[0].Data["temperature"] != 29.0 || cs[0].Data["hvac_mode"] != nil {
		t.Fatalf("hold -> boost is one set-point change: %+v", cs)
	}
	if cs := m.hpCalls(Off); len(cs) != 1 || cs[0].Service != "turn_off" {
		t.Fatalf("%+v", cs)
	}
	m.hpState = "unavailable"
	if cs := m.hpCalls(Hold); cs != nil {
		t.Fatal("no calls to an unavailable device")
	}
	if m.holdSet != 21 || m.boostSet != 29 {
		t.Fatal("options")
	}
}

func TestTVQuietSuppressesCommands(t *testing.T) {
	r := newRig(t, "live", true, "", at(10, 12, 0), 18.6)
	// The package pauses both devices (a script, not us).
	r.feedState(tvTimerEnt, "active")
	r.feedState(tvActiveEnt, "on")
	r.hpState = "off"
	r.pushHP()
	r.fanOn, r.fanPct = "off", 0
	r.pushFan()
	mk := r.mark()
	r.advance(2*time.Hour, true, 18.6)
	if es := r.devCalls(mk); len(es) != 0 {
		t.Fatalf("sent during TV quiet: %s", describe(es))
	}
	if r.notes(mk, "override") != 0 {
		t.Fatal("the package's own pause is not a manual override")
	}
	if st, _ := r.out(ObjState); st != "tv_quiet" {
		t.Fatalf("state %q", st)
	}
	// Timer ends; the package restores. The module waits out the restore,
	// then resumes the plan.
	r.feedState(tvTimerEnt, "idle")
	r.tick(30 * time.Second)
	if es := r.devCalls(mk); len(es) != 0 {
		t.Fatalf("raced the package's restore: %s", describe(es))
	}
	r.feedState(tvActiveEnt, "off")
	r.advance(3*time.Minute, true, 18.6)
	if es := r.devCalls(mk); len(es) == 0 {
		t.Fatal("did not resume after TV quiet")
	}
	if r.hpState != "heat" {
		t.Fatalf("hp %s", r.hpState)
	}
}

func TestManualOverrideHoldOff(t *testing.T) {
	r := newRig(t, "live", true, "", at(10, 12, 0), 18.6)
	if r.hpState != "heat" {
		t.Fatalf("setup: %s", r.hpState)
	}
	mk := r.mark()
	// Someone switches the heat pump off with the remote / the HA card.
	r.hpState = "off"
	r.pushHP()
	r.tick(10 * time.Second)
	if r.notes(mk, "override") != 1 {
		t.Fatal("expected one override notification")
	}
	if st, _ := r.out(ObjOverride); st != "active" {
		t.Fatalf("override sensor %q", st)
	}
	r.advance(119*time.Minute, true, 18.6)
	if es := r.devCalls(mk); len(es) != 0 {
		t.Fatalf("sent during the hold-off: %s", describe(es))
	}
	if r.notes(mk, "override") != 1 {
		t.Fatal("one notification per override")
	}
	if r.notes(mk, "floor_in_override") < 1 {
		t.Fatal("the below-minimum house should be reported during the override")
	}
	r.advance(5*time.Minute, true, 18.6)
	if es := r.devCalls(mk); len(es) == 0 || r.hpState != "heat" {
		t.Fatalf("did not resume after the hold-off: %s", describe(es))
	}
	if st, _ := r.out(ObjOverride); st != "none" {
		t.Fatalf("override sensor %q", st)
	}
}

func TestOverrideHoldIsAnOption(t *testing.T) {
	r := newRig(t, "live", true, `,"hp_override_hold_h":0.5`, at(10, 12, 0), 18.6)
	mk := r.mark()
	r.fanOn, r.fanPct = "on", 30 // the Dyson turned by hand
	r.pushFan()
	r.advance(25*time.Minute, true, 18.6)
	if es := r.devCalls(mk); len(es) != 0 {
		t.Fatalf("%s", describe(es))
	}
	r.advance(10*time.Minute, true, 18.6)
	if r.fanPct != 60 {
		t.Fatalf("Dyson not restored after 30 min: %v", r.fanPct)
	}
}

func TestStaleDataFallsToHold(t *testing.T) {
	// (a) Home Assistant goes quiet while the plan says off.
	r := newRig(t, "live", true, "", at(10, 18, 0), 22.5)
	if r.hpState != "off" {
		t.Fatalf("setup: evening, warm: %s", r.hpState)
	}
	mk := r.mark()
	r.advance(2*time.Hour, false, 0)
	if r.hpState != "heat" || r.hpSet != 22 || r.hpFan != "high" {
		t.Fatalf("stale: %s %v %s after %s", r.hpState, r.hpSet, r.hpFan, describe(r.devCalls(mk)))
	}
	if r.notes(mk, "degraded") != 1 {
		t.Fatal("expected one degraded notification")
	}
	if st, _ := r.out(ObjState); st != "safe_hold" {
		t.Fatalf("state %q", st)
	}
	// Data returns: back to the plan (off).
	r.comfort(22.5)
	r.feedNum(hpPowerEnt, 8)
	r.advance(2*time.Minute, true, 22.5)
	if r.hpState != "off" {
		t.Fatalf("did not return to the plan: %s", r.hpState)
	}

	// (b) Prices run out.
	m := New()
	_ = m.Configure([]byte(`{"hp_mode":"live","hp_forecast_entity":""}`))
	_ = m.Init(nil)
	rb := &rig{t: t, m: m, now: at(10, 18, 0), hpState: "off", hpSet: 22, hpFan: "high", fanOn: "off"}
	m.OnLink(true, rb.now)
	m.OnStateUpdate(modules.StateUpdate{EntityID: m.priceEnt, Time: rb.now, Available: true, Attrs: raw(t, at(10, 17, 0), 8, dayPrices)})
	rb.feedNum(outdoorEnt, 3)
	rb.comfort(22.5)
	rb.feedNum(minimumEnt, 19)
	rb.feedState(armedEnt, "on")
	rb.pushHP()
	rb.pushFan()
	rb.advance(30*time.Minute, true, 22.5) // 18:30: still priced
	if rb.hpState != "off" {
		t.Fatalf("planned off while prices known: %s", rb.hpState)
	}
	rb.advance(2*time.Hour, true, 22.5)
	if rb.hpState != "heat" || rb.hpSet != 22 {
		t.Fatalf("prices stale: %s %v", rb.hpState, rb.hpSet)
	}

	// (c) The comfort sensors go stale (but HA is otherwise alive).
	rc := newRig(t, "live", true, "", at(10, 18, 0), 22.5)
	for el := 0; el < 200; el += 30 {
		rc.advance(30*time.Minute, false, 0)
		rc.feedNum(hpPowerEnt, 8)
		rc.feedNum(outdoorEnt, 3)
	}
	if rc.hpState != "heat" {
		t.Fatalf("stale comfort sensors must not leave the house unheated: %s", rc.hpState)
	}
}

func TestBelowMinimumHeatsEvenWhenPlanIsOff(t *testing.T) {
	// A model that gains nothing from heating makes the plan stay off.
	r := newRig(t, "live", true, `,"hp_model_boost_rate":0,"hp_model_gain":0`, at(10, 18, 0), 18.5)
	if r.hpState != "heat" || r.hpSet != 22 {
		t.Fatalf("below the minimum: %s %v", r.hpState, r.hpSet)
	}
	if k := r.m.target(r.now).Kind; k != "below_min" {
		t.Fatalf("target kind %q", k)
	}
	if r.fanOn != "on" || r.fanPct != 50 { // evening cap
		t.Fatalf("Dyson %s %v", r.fanOn, r.fanPct)
	}
	// Above the minimum the same plan is off again.
	r.comfort(22)
	r.advance(2*time.Minute, true, 22)
	if r.hpState != "off" || r.fanOn != "off" {
		t.Fatalf("%s %s", r.hpState, r.fanOn)
	}
}

func TestDisarmLeavesDevicesAlone(t *testing.T) {
	r := newRig(t, "live", true, "", at(10, 12, 0), 18.6)
	hp, set := r.hpState, r.hpSet
	mk := r.mark()
	r.feedState(armedEnt, "off")
	r.advance(10*time.Minute, true, 18.6)
	if es := r.devCalls(mk); len(es) != 0 || r.hpState != hp || r.hpSet != set {
		t.Fatalf("disarm touched the devices: %s", describe(es))
	}
	if r.notes(mk, "disarmed") != 1 {
		t.Fatal("expected a disarm notification")
	}
	// A hand change while disarmed is not an override either.
	r.hpState = "off"
	r.pushHP()
	r.feedState(armedEnt, "on")
	r.advance(2*time.Minute, true, 18.6)
	if r.notes(mk, "override") != 0 {
		t.Fatal("no override while disarmed")
	}
}

func TestCommandFailureRetriesAndNotifies(t *testing.T) {
	r := newRig(t, "live", true, "", at(10, 12, 0), 18.6, func(r *rig) { r.noEcho = true })
	m := r.m
	var id uint64
	for _, e := range r.all {
		if e.Domain == "climate" {
			id = e.CorrID
			break
		}
	}
	if id == 0 {
		t.Fatalf("no climate call: %v", r.all)
	}
	mk := r.mark()
	m.OnCommandResult(modules.CommandResult{CorrID: id, OK: false, Err: "timeout", At: r.now})
	r.drain()
	if r.notes(mk, "command_failed") != 1 {
		t.Fatalf("failure notifications: %d", r.notes(mk, "command_failed"))
	}
	r.advance(2*time.Minute, true, 18.6)
	if es := r.devCalls(mk); len(es) != 0 {
		t.Fatalf("retried too soon: %s", describe(es))
	}
	r.advance(4*time.Minute, true, 18.6)
	if es := r.devCalls(mk); len(es) == 0 {
		t.Fatal("no retry after 5 minutes")
	}
	// A command that succeeds yet leaves the state different is not resent
	// in a loop (it would beep every time).
	mk = r.mark()
	r.advance(20*time.Minute, true, 18.6)
	n := 0
	for _, e := range r.devCalls(mk) {
		if e.Domain == "climate" {
			n++
		}
	}
	if n != 0 {
		t.Fatalf("looped %d climate calls", n)
	}
}

func TestForecastFeedsThePlan(t *testing.T) {
	m := New()
	_ = m.Configure([]byte(`{"hp_mode":"shadow"}`))
	_ = m.Init(nil)
	t0 := at(10, 12, 0)
	m.OnLink(true, t0)
	m.OnStateUpdate(modules.StateUpdate{EntityID: m.priceEnt, Time: t0, Available: true, Attrs: raw(t, at(10, 0, 0), 192, dayPrices)})
	feed(m, t0, outdoorEnt, 3)
	comfortAt(m, t0, 20.5)
	m.OnTick(t0.Add(time.Minute))
	var req *modules.Effect
	for _, e := range m.Effects() {
		e := e
		if e.Domain == "weather" {
			req = &e
		}
	}
	if req == nil || req.Service != "get_forecasts" || !req.ReturnResponse || req.Data["type"] != "hourly" || req.Data["entity_id"] != "weather.smhi_home" {
		t.Fatalf("forecast request: %+v", req)
	}
	// Before the forecast the plan uses the current reading.
	if m.plan.Slots[0].Tout != 3 || m.plan.Slots[40].Tout != 3 {
		t.Fatalf("constant outdoor: %v %v", m.plan.Slots[0].Tout, m.plan.Slots[40].Tout)
	}
	// The response, as Home Assistant returns it over the WebSocket: a
	// forecast colder at night.
	var pts []string
	for h := 0; h < 48; h++ {
		ts := at(10, 12, 0).Add(time.Duration(h) * time.Hour)
		pts = append(pts, fmt.Sprintf(`{"datetime":%q,"temperature":%g}`, ts.UTC().Format(time.RFC3339), 3.0-float64(h)/8))
	}
	resp := `{"weather.smhi_home":{"forecast":[` + strings.Join(pts, ",") + `]}}`
	m.OnCommandResult(modules.CommandResult{CorrID: req.CorrID, OK: true, At: t0.Add(time.Minute), Response: json.RawMessage(resp)})
	// Slot 40 is 10 h on, at 12:00+10 h = 22:00: 3 - 10/8 = 1.75 (+ interpolation within the hour).
	got := m.plan.Slots[40].Tout
	if math.Abs(got-(3-10.125/8)) > 0.02 {
		t.Fatalf("forecast not used: %.3f", got)
	}
	// The forecast request repeats every 30 minutes, not every tick.
	m.OnTick(t0.Add(10 * time.Minute))
	if len(m.Effects()) != 0 {
		t.Fatal("forecast requested again too soon")
	}
	m.OnTick(t0.Add(40 * time.Minute))
	if es := m.Effects(); len(es) != 1 || es[0].Domain != "weather" {
		t.Fatalf("%v", es)
	}
	// Stale forecast (3 h+) is ignored: the reading is used again.
	m.forecastBusy = false
	m.OnTick(t0.Add(4 * time.Hour))
	m.Effects()
	if m.forecastFresh(t0.Add(4 * time.Hour)) {
		t.Fatal("a 4 h old forecast must not be used")
	}
	// Both forms of the response parse.
	if f := parseForecast(json.RawMessage(`{"service_response":`+resp+`}`), "weather.smhi_home"); len(f) != 48 {
		t.Fatalf("%d", len(f))
	}
}

func TestOutdoorAtInterpolatesAndHolds(t *testing.T) {
	h0 := at(10, 12, 0)
	in := Input{Tout: 9, Forecast: map[int64]float64{h0.Unix(): 2, h0.Add(time.Hour).Unix(): 4, h0.Add(2 * time.Hour).Unix(): 4}}
	if v := in.outdoorAt(h0.Add(30 * time.Minute)); math.Abs(v-3) > 1e-9 {
		t.Fatalf("%v", v)
	}
	if v := in.outdoorAt(h0.Add(-time.Hour)); v != 2 {
		t.Fatalf("before: %v", v)
	}
	if v := in.outdoorAt(h0.Add(5 * time.Hour)); v != 4 {
		t.Fatalf("after: %v", v)
	}
	if v := (Input{Tout: 9}).outdoorAt(h0); v != 9 {
		t.Fatalf("no forecast: %v", v)
	}
}

func TestComfortSensorsDefaultAndOption(t *testing.T) {
	m := New()
	if got := strings.Join(m.comfortIDs, ","); got != "kitchen_temperature_sensor_temperature,bedroom_midheight_sensor_temperature" {
		t.Fatal(got)
	}
	t0 := at(10, 12, 0)
	feed(m, t0, kitchenEnt, 20)
	feed(m, t0, bedroomEnt, 19)
	feed(m, t0, "house_mean_temperature", 25) // not a comfort sensor: ignored
	if v, ok := m.comfortNow(t0); !ok || math.Abs(v-19.5) > 1e-9 {
		t.Fatalf("%v %v", v, ok)
	}
	// One sensor stale: the other alone.
	if v, _ := m.comfortNow(t0.Add(4 * time.Hour)); v != 0 {
		t.Fatalf("both stale should be unavailable, got %v", v)
	}
	feed(m, t0.Add(3*time.Hour+30*time.Minute), kitchenEnt, 21)
	if v, ok := m.comfortNow(t0.Add(4 * time.Hour)); !ok || v != 21 {
		t.Fatalf("%v %v", v, ok)
	}
	// Option: a different list.
	n := New()
	_ = n.Configure([]byte(`{"hp_comfort_sensors":"sensor.a_temp, sensor.b_temp"}`))
	ids := []string{}
	for _, in := range n.Inputs() {
		ids = append(ids, in.EntityID)
	}
	if !strings.Contains(strings.Join(ids, ","), "a_temp,b_temp") || strings.Contains(strings.Join(ids, ","), kitchenEnt) {
		t.Fatalf("%v", ids)
	}
	// The default inputs include both sensors and not the house mean.
	for _, in := range New().Inputs() {
		if in.EntityID == "house_mean_temperature" {
			t.Fatal("house mean must not be an input by default")
		}
	}
}

func TestNoSpuriousZeroDayKey(t *testing.T) {
	m := New()
	_ = m.Init(nil)
	_, a := stateOf(m.InitialStates(), ObjCost)
	if strings.Contains(a, "0001-01-01") {
		t.Fatalf("zero day key in %s", a)
	}
	if _, bad := m.days[zeroDayKey]; bad {
		t.Fatal("zero day created")
	}
	// A state file written by the old build carries the key: dropped on load.
	n := New()
	_ = n.Init([]byte(`{"days":{"0001-01-01":{"plan_kr":0},"2026-11-09":{"plan_kr":1.5}}}`))
	_, a = stateOf(n.publishAll(at(10, 12, 0), true), ObjCost)
	if strings.Contains(a, "0001-01-01") || !strings.Contains(a, "2026-11-09") {
		t.Fatal(a)
	}
}

func TestPublishedLiveEntitiesAndHeartbeat(t *testing.T) {
	r := newRig(t, "live", true, "", at(10, 12, 0), 18.6)
	if st, _ := r.out(ObjMode); st != "live" {
		t.Fatalf("mode %q", st)
	}
	if st, a := r.out(ObjArmed); st != "on" || !strings.Contains(a, `"active":true`) {
		t.Fatalf("armed %q %s", st, a)
	}
	if st, _ := r.out(ObjLastCmd); st == "none" || st == "" {
		t.Fatalf("last command %q", st)
	}
	if st, _ := r.out(ObjOverride); st != "none" {
		t.Fatalf("override %q", st)
	}
	specs := map[string]bool{}
	for _, o := range r.m.Outputs() {
		specs[o.ObjectID] = true
	}
	for _, id := range []string{ObjMode, ObjArmed, ObjLastCmd, ObjOverride, ObjHeartbeat} {
		if !specs[id] {
			t.Fatalf("no discovery spec for %s", id)
		}
	}
	// The heartbeat is refreshed at least every 60 s.
	var last time.Time
	for i := 0; i < 40; i++ {
		r.now = r.now.Add(5 * time.Second)
		for _, o := range r.m.OnTick(r.now) {
			if o.ObjectID == ObjHeartbeat {
				if !last.IsZero() && r.now.Sub(last) > 60*time.Second {
					t.Fatalf("heartbeat gap %v", r.now.Sub(last))
				}
				last = r.now
			}
		}
		r.drain()
	}
	if last.IsZero() {
		t.Fatal("no heartbeat")
	}
	// Shadow publishes too.
	s := newRig(t, "shadow", false, "", at(10, 12, 0), 20.5)
	if st, _ := s.out(ObjMode); st != "shadow" {
		t.Fatalf("%q", st)
	}
	if _, _, ok := firstOutput(s.m.OnTick(s.now.Add(time.Minute)), ObjHeartbeat); !ok {
		t.Fatal("shadow heartbeat")
	}
}

func firstOutput(outs []modules.OutputUpdate, obj string) (string, string, bool) {
	for _, o := range outs {
		if o.ObjectID == obj {
			return o.StateString, o.AttributesJSON, true
		}
	}
	return "", "", false
}

func TestNewOptionsRangeChecked(t *testing.T) {
	m := New()
	_ = m.Configure([]byte(`{"hp_hold_setpoint_c":99,"hp_override_hold_h":0,"hp_stale_after_min":1,"hp_notify_service":"bad"}`))
	if m.holdSet != 22 || m.overrideFor != 2*time.Hour || m.staleAfter != 90*time.Minute || m.notifySvc != "notify.mobile_app_edmund15max" {
		t.Fatalf("%+v", m)
	}
	_ = m.Configure([]byte(`{"hp_mode":"live","hp_boost_setpoint_c":28,"hp_floor_margin_c":0.5}`))
	if !m.live || m.boostSet != 28 || m.floorMargin != 0.5 {
		t.Fatal("options not applied")
	}
	_ = m.Configure([]byte(`{}`))
	if m.live || m.boostSet != 30 {
		t.Fatal("Configure must reset to defaults")
	}
}

// ---- replay: the tuned lambda on a realistic day ----

// morningPeakPrice is an all-in kr/kWh quarter-hour shape of a winter weekday
// (spot + 0.69 kr of tax and transfer + VAT): cheap night, a 06:00-08:00
// morning peak up to 4.8 kr, a midday dip, and the 17:00-21:00 evening peak.
func morningPeakPrice(t time.Time) float64 {
	l := t.In(sto)
	h := float64(l.Hour()) + float64(l.Minute())/60
	q := float64(l.Hour()*4 + l.Minute()/15)
	wob := 0.05 * math.Sin(q*1.7) // quarter-to-quarter noise
	var v float64
	switch {
	case h < 5:
		v = 1.0
	case h < 6:
		v = 1.6
	case h < 8:
		v = 3.2 + 1.6*(1-math.Abs(h-6.75)/1.25)
	case h < 11:
		v = 2.2 - 0.2*(h-8)/3
	case h < 14:
		v = 1.5
	case h < 17:
		v = 1.9 + 0.2*(h-14)
	case h < 21:
		v = 3.6 + 0.9*(1-math.Abs(h-18.5)/2.5)
	case h < 23:
		v = 1.8
	default:
		v = 1.2
	}
	return v + wob
}

type replayStats struct {
	levels        []Level
	temps         []float64
	cost, kwh     float64
	mean, minT    float64
	bedroomMax    float64
	morningPeakOn int
	eveningOn     int
}

// replayDay runs the planner closed-loop (re-planning every quarter hour from
// the true state) over 24 h from day0 00:00 with T0, using the fitted model as
// the plant, a diurnal outdoor swing the planner does not know about
// (it plans on a constant 3.0 C reading), and the real DefaultParams.
func replayDay(t *testing.T, p Params, t0 float64, startHour int) replayStats {
	t.Helper()
	day0 := at(11, startHour, 0)
	prices := map[int64]float64{}
	for k := 0; k < 96*3; k++ {
		ts := day0.Add(time.Duration(k) * quarter)
		prices[ts.Unix()] = morningPeakPrice(ts)
	}
	st := replayStats{minT: 99}
	T, prev := t0, Off
	var sum float64
	for k := 0; k < 96; k++ {
		now := day0.Add(time.Duration(k) * quarter)
		r := Plan(Input{Now: now, T0: T, Tout: 3, Min: 19.3, Desired: 20.5, Prices: prices, Prev: prev,
			BedOffset: -0.3, BedOffsetOK: true}, p)
		l := r.Slots[0].Level
		hm := now.In(sto).Hour()*60 + now.In(sto).Minute()
		if l != Off && hm >= 6*60 && hm < 8*60 {
			st.morningPeakOn++
		}
		if l != Off && hm >= 17*60 && hm < 21*60 && T > 19.8 {
			st.eveningOn++
		}
		kwh := p.PowerW(l) / 1000 * dtH
		st.kwh += kwh
		st.cost += kwh * prices[now.Unix()]
		st.levels = append(st.levels, l)
		st.temps = append(st.temps, T)
		to := 3.5 + 1.5*math.Sin(float64(k)/96*2*math.Pi)
		T = p.Model.Step(T, l, to)
		sum += T
		st.minT = math.Min(st.minT, T)
		if p.IsNight(now) {
			st.bedroomMax = math.Max(st.bedroomMax, T-0.3)
		}
		prev = l
	}
	st.mean = sum / 96
	return st
}

func (s replayStats) table() string {
	var b strings.Builder
	sym := map[Level]string{Off: ".", Hold: "h", Boost: "B"}
	for k := 0; k < len(s.levels); k += 4 {
		fmt.Fprintf(&b, "%02d:00 %.1fC ", (k/4)%24, s.temps[k])
		for j := k; j < k+4 && j < len(s.levels); j++ {
			b.WriteString(sym[s.levels[j]])
		}
		if (k/4)%4 == 3 {
			b.WriteString("\n")
		} else {
			b.WriteString(" | ")
		}
	}
	return b.String()
}

func TestReplayMorningPeakNoBoost(t *testing.T) {
	p := DefaultParams() // fitted model, lambda 0.05
	st := replayDay(t, p, 20.5, 0)
	t.Logf("lambda %.3f: cost %.1f kr, %.1f kWh, mean %.2f C, min %.2f C\n%s", p.Lambda, st.cost, st.kwh, st.mean, st.minT, st.table())
	if st.morningPeakOn != 0 {
		t.Fatalf("heated in %d slots of the 06:00-08:00 peak with the house above the minimum\n%s", st.morningPeakOn, st.table())
	}
	if st.eveningOn != 0 {
		t.Fatalf("ran %d slots in the evening peak with the house comfortable\n%s", st.eveningOn, st.table())
	}
	if st.minT < 19.0 {
		t.Fatalf("below the minimum: %.2f", st.minT)
	}
	if math.Abs(st.mean-20.5) > 0.3 {
		t.Fatalf("daily mean %.2f is not within 0.3 of the desired 20.5", st.mean)
	}
	if st.bedroomMax > 23 {
		t.Fatalf("bedroom %.2f at night", st.bedroomMax)
	}
	// Pre-heat: warm going into the evening peak, then coasting down.
	if st.temps[17*4] < 20.5+0.5 {
		t.Fatalf("no pre-heat: %.2f at 17:00", st.temps[17*4])
	}
	if st.temps[21*4] > st.temps[17*4]-1 {
		t.Fatalf("the house did not coast through the evening: %.2f -> %.2f", st.temps[17*4], st.temps[21*4])
	}
	// Above desired going into the dear period, below it at the end of it.
	if st.temps[17*4] < 20.5+0.3 || st.temps[20*4+2] > 20.5 {
		t.Fatalf("not above desired before the peak (%.2f) and below it after (%.2f)", st.temps[17*4], st.temps[20*4+2])
	}
	// Starting at 06:00 just above the minimum plus a margin of warmth: still
	// no heating through the peak (it coasts to 08:00).
	st2 := replayDay(t, p, 21.5, 6)
	if st2.morningPeakOn != 0 {
		t.Fatalf("from 06:00 at 21.5 C: heated %d peak slots\n%s", st2.morningPeakOn, st2.table())
	}
}

func TestReplayLambdaIsTheSweetSpot(t *testing.T) {
	// A much higher lambda pins the house to the desired temperature and
	// ignores prices (dearer); a much lower one rides the floor (mean low).
	// 0.05 keeps the mean on desired AND shifts load away from the peaks.
	base := DefaultParams()
	lo, hi := base, base
	lo.Lambda, hi.Lambda = 0.002, 1.0
	a, b, c := replayDay(t, lo, 20.5, 0), replayDay(t, base, 20.5, 0), replayDay(t, hi, 20.5, 0)
	t.Logf("cost/mean: lambda 0.002 %.1f kr %.2f C | 0.05 %.1f kr %.2f C | 1.0 %.1f kr %.2f C", a.cost, a.mean, b.cost, b.mean, c.cost, c.mean)
	if !(b.cost < c.cost) {
		t.Fatalf("lambda 0.05 should be cheaper than pinning: %.1f vs %.1f", b.cost, c.cost)
	}
	if math.Abs(b.mean-20.5) > math.Abs(a.mean-20.5)+1e-9 {
		t.Fatalf("lambda 0.05 should keep the mean nearer desired than 0.002: %.2f vs %.2f", b.mean, a.mean)
	}
}
