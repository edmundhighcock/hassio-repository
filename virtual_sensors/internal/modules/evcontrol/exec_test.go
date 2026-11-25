package evcontrol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"virtual_sensors/internal/house"
	"virtual_sensors/internal/modules"
)

// env drives a Module like the host does: states in, ticks, effects drained
// into a fake HA service caller that answers OK (or fails on request).
type env struct {
	t     *testing.T
	m     *Module
	now   time.Time
	calls []modules.Effect // every non-notification service call
	notes []modules.Effect // notifications
	fail  func(e modules.Effect) bool
}

func spikePrice(h int) float64 {
	if h >= 2 && h < 4 {
		return 0.2
	}
	return 1.0
}

func newEnv(t *testing.T, opts string, armed bool) *env {
	m := New()
	m.hub = house.NewHub()
	if err := m.Configure([]byte(opts)); err != nil {
		t.Fatal(err)
	}
	m.appendLine = func(string, []byte) error { return nil }
	if err := m.Init(nil); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, m: m, now: at(10, 22, 0)}
	m.OnLink(true, e.now)
	e.feed(m.priceEnt, "", 0, rawPrices(at(10, 0, 0), at(12, 0, 0), spikePrice))
	st := "off"
	if armed {
		st = "on"
	}
	e.feed(ArmedEntity, st, 0, nil)
	e.feed("hot_water_controller_plan", "", 0, nil)
	e.feed("loaver_status", "awaiting_start", 0, map[string]json.RawMessage{"id": json.RawMessage(`"EH5YL3HF"`)})
	e.feed("loaver_power", "0", 0, nil)
	e.feed("heat_pump_power_power", "0", 0, nil)
	e.grid(1000)
	return e
}

func (e *env) feed(id, state string, v float64, attrs map[string]json.RawMessage) {
	u := modules.StateUpdate{EntityID: id, Time: e.now, State: state, Value: v, Available: state != "unavailable", Attrs: attrs}
	if state == "on" || state == "off" || state == "" {
		u.Available = state != ""
	}
	e.m.OnStateUpdate(u)
	e.drain()
}

func (e *env) grid(w float64) { e.feed(gridEntity, "x", w, nil) }

func (e *env) drain() {
	for guard := 0; guard < 50; guard++ {
		effs := e.m.Effects()
		if len(effs) == 0 {
			return
		}
		for _, f := range effs {
			if f.Domain == "notify" || f.Domain == "logbook" {
				e.notes = append(e.notes, f)
				e.m.OnCommandResult(modules.CommandResult{CorrID: f.CorrID, OK: true, At: e.now})
				continue
			}
			e.calls = append(e.calls, f)
			ok := e.fail == nil || !e.fail(f)
			r := modules.CommandResult{CorrID: f.CorrID, OK: ok, At: e.now}
			if !ok {
				r.Err = "boom"
			}
			e.m.OnCommandResult(r)
			if ok && f.Service == "action_command" { // the charger follows the command
				switch f.Data["action_command"] {
				case "pause":
					e.m.OnStateUpdate(modules.StateUpdate{EntityID: "loaver_status", Time: e.now, State: "ready_to_charge", Available: true})
				case "resume":
					e.m.OnStateUpdate(modules.StateUpdate{EntityID: "loaver_status", Time: e.now, State: "charging", Available: true})
				}
			}
		}
	}
}

// advance steps in 30 s ticks; the grid meter is re-fed each step when w >= 0.
func (e *env) advance(d time.Duration, w float64) {
	end := e.now.Add(d)
	for e.now.Before(end) {
		e.now = e.now.Add(30 * time.Second)
		if w >= 0 {
			e.grid(w)
		}
		e.m.OnTick(e.now)
		e.drain()
	}
}

func (e *env) setStatus(s string) { e.feed("loaver_status", s, 0, nil) }

func (e *env) reset() { e.calls, e.notes = nil, nil }

func (e *env) svc(service string) []modules.Effect {
	var out []modules.Effect
	for _, c := range e.calls {
		if c.Service == service {
			out = append(out, c)
		}
	}
	return out
}

func (e *env) actions() []string {
	var out []string
	for _, c := range e.svc("action_command") {
		out = append(out, c.Data["action_command"].(string))
	}
	return out
}

func eqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const needOpt = `{"ev_need_kwh":10,`

func TestShadowCallsNothing(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10}`, true) // armed key on, but ev_mode defaults to shadow
	e.setStatus("charging")
	e.advance(6*time.Hour, 1000)
	if len(e.calls) != 0 || len(e.notes) != 0 {
		t.Fatalf("shadow made calls: %v %v", e.calls, e.notes)
	}
	e2 := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"shadow"}`, true)
	e2.setStatus("charging")
	e2.advance(time.Hour, 1000)
	if len(e2.calls)+len(e2.notes) != 0 {
		t.Fatal("explicit shadow made calls")
	}
}

func TestLiveWithoutArmingKeyCallsNothing(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, false)
	e.setStatus("charging")
	e.advance(6*time.Hour, 1000)
	if len(e.calls)+len(e.notes) != 0 {
		t.Fatalf("disarmed live made calls: %v", e.calls)
	}
}

func TestPauseAndResumeFollowThePlan(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	if len(e.notes) == 0 {
		e.advance(30*time.Second, 1000)
	}
	if len(e.notes) == 0 || e.notes[0].Domain != "notify" || e.notes[0].Service != "mobile_app_edmund15max" {
		t.Fatalf("arming not notified: %+v", e.notes)
	}
	// 22:00, cheap slots are 02:00-04:00: charging now must be paused.
	e.setStatus("charging")
	e.advance(time.Minute, 1000)
	if !eqStr(e.actions(), []string{"pause"}) {
		t.Fatalf("want one pause, got %v", e.actions())
	}
	if c := e.svc("action_command")[0]; c.Data["charger_id"] != "EH5YL3HF" {
		t.Fatalf("target: %v", c.Data)
	}
	e.setStatus("ready_to_charge")
	e.reset()
	e.advance(4*time.Hour+10*time.Minute, 1000) // to 02:10
	if got := e.actions(); !eqStr(got, []string{"resume"}) {
		t.Fatalf("want one resume in the slot, got %v", got)
	}
	if n := len(e.svc("set_charger_dynamic_limit")) + len(e.svc("set_circuit_dynamic_limit")); n != 0 {
		t.Fatalf("limit commands in the slot: %+v", e.calls)
	}
	e.setStatus("charging")
	e.reset()
	e.advance(10*time.Minute, 1000)
	if len(e.svc("action_command")) != 0 {
		t.Fatalf("must not re-send resume: %v", e.actions())
	}
}

// While armed and live and charging, the controller writes no limit at all,
// whatever the house does; the only limit commands are the one-shot release.
func TestNoLimitCommandsWhileCharging(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.advance(4*time.Hour+10*time.Minute, 1000)
	e.setStatus("charging")
	e.reset()
	for _, w := range []float64{1000, 230 * 3 * 12, 230 * 3 * 15, 230 * 3 * 20} { // quiet, loaded, overloaded
		e.advance(20*time.Minute, w)
	}
	for _, c := range e.calls {
		if c.Domain != "timer" { // the dead-man postponement is the only traffic
			t.Fatalf("no commands expected while charging in the slot, got %+v", e.calls)
		}
	}
}

// The controller reacts to a stale grid meter no more: it uses no meter.
func TestStaleGridMeterDoesNothing(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.advance(time.Minute, 1000) // 22:00, outside the cheap slots
	e.reset()
	e.advance(12*time.Minute, -1) // grid meter goes quiet
	if got := e.actions(); len(got) != 0 {
		t.Fatalf("a stale grid meter must not start the charger, got %v", got)
	}
}

func TestMissingPriceFallsBackToCharging(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.advance(2*time.Hour+time.Minute, 1000) // 00:01 on the 11th: prices run to the end of the 11th, fine
	e.reset()
	e.m.prices = map[int64]float64{} // price data lost
	e.advance(time.Minute, 1000)
	if got := e.actions(); !eqStr(got, []string{"start", "resume"}) {
		t.Fatalf("no prices must charge now, got %v", got)
	}
}

func TestDeadlineBackstopChargesAtMax(t *testing.T) {
	// 100 kWh cannot finish by 06:30: every usable quarter is planned.
	e := newEnv(t, `{"ev_need_kwh":100,"ev_mode":"live"}`, true)
	e.advance(2*time.Minute, 1000)
	if got := e.actions(); !eqStr(got, []string{"start", "resume"}) {
		t.Fatalf("deadline mode must charge at once, got %v", got)
	}
	if n := len(e.svc("set_charger_dynamic_limit")) - 1; n != 0 { // the arming release only
		t.Fatalf("deadline mode wrote limits: %+v", e.calls)
	}
	found := false
	for _, n := range e.notes {
		if n.Data["title"] == "EV deadline fallback" {
			found = true
		}
	}
	if !found {
		t.Fatalf("deadline fallback not notified: %+v", e.notes)
	}
}

func notesTitled(e *env, title string) []modules.Effect {
	var out []modules.Effect
	for _, n := range e.notes {
		if n.Data["title"] == title && n.Domain == "notify" {
			out = append(out, n)
		}
	}
	return out
}

// Disarming resumes a charger the controller had paused, writes no limit,
// and the push says a resume was sent only because one was.
func TestDisarmResumesWhatItPaused(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.setStatus("charging")
	e.advance(time.Minute, 1000) // outside the slots: paused
	if !eqStr(e.actions(), []string{"pause"}) {
		t.Fatalf("setup: %v", e.actions())
	}
	e.setStatus("ready_to_charge")
	e.reset()
	e.feed(ArmedEntity, "off", 0, nil)
	e.advance(time.Minute, 1000)
	if got := e.actions(); !eqStr(got, []string{"resume"}) {
		t.Fatalf("disarm must resume, got %v", got)
	}
	if n := len(e.svc("set_charger_dynamic_limit")) + len(e.svc("set_circuit_dynamic_limit")); n != 0 {
		t.Fatalf("disarm wrote limits: %+v", e.calls)
	}
	ns := notesTitled(e, "EV controller disarmed")
	if len(ns) != 1 || !strings.Contains(ns[0].Data["message"].(string), "a resume command was sent") {
		t.Fatalf("disarm push: %+v", ns)
	}
	e.reset()
	e.advance(10*time.Minute, 1000)
	if len(e.calls) != 0 {
		t.Fatalf("calls after disarm: %+v", e.calls)
	}
	if e.m.x.st.Paused {
		t.Fatal("hold flag not cleared")
	}
}

// Nothing was paused: the disarm push must not claim a resume.
func TestDisarmPushDoesNotClaimAResumeNotSent(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.advance(time.Minute, 1000) // armed, charger never paused by us
	e.reset()
	e.feed(ArmedEntity, "off", 0, nil)
	e.advance(time.Minute, 1000)
	if len(e.actions()) != 0 {
		t.Fatalf("resume sent without a pause: %v", e.actions())
	}
	ns := notesTitled(e, "EV controller disarmed")
	if len(ns) != 1 {
		t.Fatalf("disarm push: %+v", e.notes)
	}
	msg := ns[0].Data["message"].(string)
	if strings.Contains(msg, "is resumed") || strings.Contains(msg, "resume command was sent") || !strings.Contains(msg, "No resume was needed") {
		t.Fatalf("push claims a resume: %q", msg)
	}
}

// Stage A: the one limit release (charger and circuit, 16 A) on start/arm
// and never otherwise.
func TestOneShotLimitRelease(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.advance(4*time.Hour+30*time.Minute, 1000)
	cl, cc := e.svc("set_charger_dynamic_limit"), e.svc("set_circuit_dynamic_limit")
	if len(cl) != 1 || cl[0].Data["current"] != 16 || cl[0].Data["charger_id"] != "EH5YL3HF" ||
		len(cc) != 1 || cc[0].Data["current_p1"] != 16 || cc[0].Data["current_p2"] != 16 || cc[0].Data["current_p3"] != 16 {
		t.Fatalf("want exactly one 16 A release of each, got %+v / %+v", cl, cc)
	}
	// Disarm then arm again: one more release, once.
	e.feed(ArmedEntity, "off", 0, nil)
	e.advance(5*time.Minute, 1000)
	e.reset()
	e.feed(ArmedEntity, "on", 0, nil)
	e.advance(30*time.Minute, 1000)
	if len(e.svc("set_charger_dynamic_limit")) != 1 || len(e.svc("set_circuit_dynamic_limit")) != 1 {
		t.Fatalf("re-arm: want one release, got %+v", e.calls)
	}
}

func TestLimitReleaseRetriesAFailureThenStops(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	fails := 0
	e.fail = func(f modules.Effect) bool {
		if f.Service == "set_circuit_dynamic_limit" && fails < 1 {
			fails++
			return true
		}
		return false
	}
	e.advance(10*time.Minute, 1000)
	if n := len(e.svc("set_circuit_dynamic_limit")); n != 2 {
		t.Fatalf("one failure, one retry, then done: %d", n)
	}
}

// Stage C: the dead-man timer is postponed every 30 min, at once on arm, only
// while armed and live.
func TestDeadmanPostponement(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.advance(30*time.Second, 1000)
	dm := func() []modules.Effect { return e.svc("start") }
	if len(dm()) != 1 {
		t.Fatalf("not postponed at once on arm: %+v", e.calls)
	}
	c := dm()[0]
	if c.Domain != "timer" || c.Data["entity_id"] != "timer.ev_deadman" || c.Data["duration"] != "00:31:00" {
		t.Fatalf("call: %+v", c)
	}
	e.advance(29*time.Minute, 1000)
	if len(dm()) != 1 {
		t.Fatalf("postponed again before 30 min: %d", len(dm()))
	}
	e.advance(2*time.Minute, 1000)
	if len(dm()) != 2 {
		t.Fatalf("not postponed at 30 min: %d", len(dm()))
	}
	e.advance(2*time.Hour, 1000)
	if n := len(dm()); n != 6 {
		t.Fatalf("want one per 30 min (6 in 2.55 h), got %d", n)
	}
	// Disarmed: never.
	e.feed(ArmedEntity, "off", 0, nil)
	e.reset()
	e.advance(3*time.Hour, 1000)
	if len(dm()) != 0 {
		t.Fatalf("postponed while disarmed: %+v", e.calls)
	}
	// Armed again: immediately.
	e.feed(ArmedEntity, "on", 0, nil)
	e.advance(time.Minute, 1000)
	if len(dm()) != 1 {
		t.Fatalf("not postponed at once on re-arm: %d", len(dm()))
	}
}

func TestDeadmanNeverInShadowOrLiveWithoutArmingKey(t *testing.T) {
	for _, opts := range []string{`{"ev_need_kwh":10}`, `{"ev_need_kwh":10,"ev_mode":"live"}`} {
		e := newEnv(t, opts, opts == `{"ev_need_kwh":10}`) // shadow with the key on; live with it off
		e.advance(3*time.Hour, 1000)
		if len(e.calls) != 0 {
			t.Fatalf("%s: calls %+v", opts, e.calls)
		}
	}
}

// Not healthy (no charger id known): the timer is left to run out.
func TestDeadmanNotPostponedWhenUnhealthy(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.m.x.chargerID = ""
	e.advance(time.Hour, 1000)
	if len(e.svc("start")) != 0 {
		t.Fatalf("postponed with no charger id: %+v", e.calls)
	}
	e2 := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e2.advance(time.Minute, 1000)
	e2.m.OnLink(false, e2.now)
	e2.reset()
	e2.advance(time.Hour, 1000)
	if len(e2.svc("start")) != 0 {
		t.Fatalf("postponed with the link down: %+v", e2.calls)
	}
}

func TestDeadmanFailureRetriesSoon(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	n := 0
	e.fail = func(f modules.Effect) bool { n++; return f.Service == "start" && f.Domain == "timer" && n < 3 }
	e.advance(10*time.Minute, 1000)
	if len(e.svc("start")) < 2 {
		t.Fatalf("a failed postponement must retry long before 30 min: %d", len(e.svc("start")))
	}
}

func TestCommandFailureNotifiesAndRetries(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.setStatus("charging")
	e.fail = func(f modules.Effect) bool { return f.Service == "action_command" }
	e.reset()
	e.advance(3*time.Minute, 1000)
	if len(e.svc("action_command")) < 2 {
		t.Fatalf("failed pause not retried: %v", e.actions())
	}
	n := 0
	for _, x := range e.notes {
		if x.Data["title"] == "EV controller command failed" && x.Domain == "notify" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one failure notification (rate limited), got %d", n)
	}
	if e.m.x.st.Paused {
		t.Fatal("a failed pause must not be recorded as in force")
	}
}

func TestNoChargerIDNoCommand(t *testing.T) {
	m := New()
	m.Configure([]byte(`{"ev_mode":"live"}`))
	if m.x.target() != nil {
		t.Fatal("target without id")
	}
	m.Configure([]byte(`{"ev_mode":"live","ev_charger_id":"ABC"}`))
	if m.x.target()["charger_id"] != "ABC" {
		t.Fatal("option ignored")
	}
}

func TestOutputsPublishModeArmedLastCommandAndLimit(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.setStatus("charging")
	var got = map[string]string{}
	e.now = e.now.Add(time.Minute)
	e.grid(1000)
	for _, o := range e.m.OnTick(e.now) {
		got[o.ObjectID] = o.StateString
	}
	e.drain()
	for _, o := range e.m.publishAll(e.now, true) {
		got[o.ObjectID] = o.StateString
	}
	if got[ObjMode] != "live" || got[ObjArmed] != "on" || got[ObjLastCmd] == "" {
		t.Fatalf("outputs: %v", got)
	}
	if _, ok := got[ObjLimitForce]; !ok {
		t.Fatalf("no limit output: %v", got)
	}
	if _, ok := got[ObjHeartbeat]; !ok && e.m.lastHB.IsZero() {
		t.Fatal("no heartbeat")
	}
}

func TestGuardUsesEqualizerWhenFresh(t *testing.T) {
	p := DefaultParams()
	// Meter 2/12/3 A with the charger drawing 4 A: other load 0/8/0 (clamped).
	g := Guard(GuardIn{GridW: 1000, EqA: [3]float64{2, 12, 3}, EqOK: true, EVkW: 4 * 3 * 230 / 1000.0, ChargerA: 4, ChargerOK: true}, p)
	if !g.Measured || g.HouseA[0] != 0 || g.HouseA[1] != 8 || g.HouseA[2] != 0 {
		t.Fatalf("%+v", g)
	}
	if g.LimitA != 11 || g.Breach || g.PhaseA[1] != 12 { // floor(19-8)
		t.Fatalf("limit %d %+v", g.LimitA, g)
	}
	// Charger current under-reports the power-derived value: the smaller is subtracted.
	g = Guard(GuardIn{EqA: [3]float64{10, 10, 10}, EqOK: true, EVkW: 6.9, ChargerA: 5, ChargerOK: true}, p)
	if g.HouseA[0] != 5 {
		t.Fatalf("conservative subtraction: %+v", g.HouseA)
	}
	// Breach on the real meter.
	g = Guard(GuardIn{EqA: [3]float64{10, 20, 10}, EqOK: true}, p)
	if !g.Breach || g.LimitA != 0 {
		t.Fatalf("%+v", g)
	}
	// Offline/stale: the grid-power estimate is used and the Equalizer numbers ignored.
	g = Guard(GuardIn{GridW: 800, EqA: [3]float64{99, 99, 99}, EqOK: false}, p)
	if g.Measured || g.LimitA != 16 {
		t.Fatalf("%+v", g)
	}
}

func TestModuleEqualizerFreshOnlineThenFallback(t *testing.T) {
	m := New()
	m.hub = house.NewHub()
	m.Configure([]byte(`{}`))
	t0 := at(10, 22, 0)
	attrs := func(a, b, c string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"state_currentL1": json.RawMessage(a), "state_currentL2": json.RawMessage(b), "state_currentL3": json.RawMessage(c)}
	}
	m.OnStateUpdate(modules.StateUpdate{EntityID: gridEntity, Time: t0, Value: 800, Available: true})
	m.OnStateUpdate(modules.StateUpdate{EntityID: "binary_sensor.equalizer_online", Time: t0, State: "on", Available: true})
	m.OnStateUpdate(modules.StateUpdate{EntityID: "equalizer_current", Time: t0, Value: 12, State: "12", Available: true, Attrs: attrs("2", "12", "3")})
	if !m.guard.Measured || m.guard.LimitA != 7 { // floor(19-12) with no EV
		t.Fatalf("equalizer path not used: %+v", m.guard)
	}
	// A steady house is silent (the Equalizer updates on change only): 10
	// minutes on it is still fresh under the 15 min default window.
	m.OnStateUpdate(modules.StateUpdate{EntityID: gridEntity, Time: t0.Add(10 * time.Minute), Value: 800, Available: true})
	if !m.guard.Measured {
		t.Fatalf("10 min-old online reading must still be fresh: %+v", m.guard)
	}
	// 16 minutes later (stale) a grid update falls back to the estimate.
	m.OnStateUpdate(modules.StateUpdate{EntityID: gridEntity, Time: t0.Add(16 * time.Minute), Value: 800, Available: true})
	if m.guard.Measured || m.guard.LimitA != 16 {
		t.Fatalf("stale equalizer must fall back: %+v", m.guard)
	}
	// Offline: fall back even when fresh.
	m.OnStateUpdate(modules.StateUpdate{EntityID: "equalizer_current", Time: t0.Add(17 * time.Minute), Value: 12, State: "12", Available: true, Attrs: attrs("2", "12", "3")})
	if !m.guard.Measured {
		t.Fatal("fresh and online again must be measured")
	}
	m.OnStateUpdate(modules.StateUpdate{EntityID: "binary_sensor.equalizer_online", Time: t0.Add(17 * time.Minute), State: "off", Available: true})
	if m.guard.Measured {
		t.Fatalf("offline equalizer must fall back: %+v", m.guard)
	}
}
