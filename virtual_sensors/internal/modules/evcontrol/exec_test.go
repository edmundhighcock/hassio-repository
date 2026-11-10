package evcontrol

import (
	"encoding/json"
	"testing"
	"time"

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
			if f.Domain == "notify" || f.Domain == "persistent_notification" {
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
	if e.calls[0].Data["charger_id"] != "EH5YL3HF" {
		t.Fatalf("target: %v", e.calls[0].Data)
	}
	e.setStatus("ready_to_charge")
	e.reset()
	e.advance(4*time.Hour+10*time.Minute, 1000) // to 02:10
	if got := e.actions(); !eqStr(got, []string{"resume"}) {
		t.Fatalf("want one resume in the slot, got %v", got)
	}
	cl := e.svc("set_charger_dynamic_limit")
	if len(cl) == 0 || cl[0].Data["current"] != 8 || cl[0].Data["time_to_live"] != 15 {
		t.Fatalf("charger limit: %+v", cl)
	}
	// Resume only after the limits were written.
	firstLimit, resumeAt := -1, -1
	for i, c := range e.calls {
		if c.Service == "set_charger_dynamic_limit" && firstLimit < 0 {
			firstLimit = i
		}
		if c.Service == "action_command" && resumeAt < 0 {
			resumeAt = i
		}
	}
	if firstLimit < 0 || resumeAt < firstLimit {
		t.Fatalf("resume must come after the limits: %d %d", firstLimit, resumeAt)
	}
	e.setStatus("charging")
	e.reset()
	e.advance(10*time.Minute, 1000)
	if len(e.svc("action_command")) != 0 {
		t.Fatalf("must not re-send resume: %v", e.actions())
	}
}

func TestResendOnChangeAndRefreshInsideTTL(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.advance(4*time.Hour+10*time.Minute, 1000)
	e.setStatus("charging")
	e.reset()
	e.reset()
	e.advance(14*time.Minute, 1000) // 28 ticks; unchanged limits go out only on the ~5 min refresh
	nc, nk := len(e.svc("set_charger_dynamic_limit")), len(e.svc("set_circuit_dynamic_limit"))
	if nc < 2 || nc > 3 || nk < 2 || nk > 3 {
		t.Fatalf("want a refresh about every 5 min (TTL 15), got charger %d circuit %d", nc, nk)
	}
	if e.svc("set_circuit_dynamic_limit")[0].Data["time_to_live"] != 15 {
		t.Fatal("circuit TTL missing")
	}
}

func TestLimitClampedByGuard(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.advance(4*time.Hour+10*time.Minute-time.Minute, 6210) // house 27 A, 9 A/phase: guard limit 10 > plan 8
	e.reset()
	e.advance(time.Minute, 6210)
	e.setStatus("charging")
	// Heavier house: 12 A/phase -> headroom 7 A: below the plan's 8.
	e.reset()
	e.advance(2*time.Minute, 230*3*12)
	cl := e.svc("set_charger_dynamic_limit")
	if len(cl) == 0 || cl[len(cl)-1].Data["current"] != 7 {
		t.Fatalf("limit not clamped to the guard: %+v", cl)
	}
	cc := e.svc("set_circuit_dynamic_limit")
	last := cc[len(cc)-1].Data
	if last["current_p1"] != 7 || last["current_p2"] != 7 || last["current_p3"] != 7 {
		t.Fatalf("circuit limits: %+v", last)
	}
	// Overloaded: no headroom for 6 A, so pause.
	e.reset()
	e.advance(2*time.Minute, 230*3*15)
	if !eqStr(e.actions(), []string{"pause"}) {
		t.Fatalf("want pause with no headroom, got %v", e.actions())
	}
}

func TestStaleDataFallsBackToCharging(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.advance(time.Minute, 1000) // 22:00, outside the cheap slots
	e.reset()
	e.advance(12*time.Minute, -1) // grid meter goes quiet beyond ev_stale_after_min (10)
	if got := e.actions(); !eqStr(got, []string{"start", "resume"}) {
		t.Fatalf("stale grid must charge now, got %v", got)
	}
	cl := e.svc("set_charger_dynamic_limit")
	if len(cl) == 0 || cl[0].Data["current"] != 8 { // forecast ceiling 19-3-7.4
		t.Fatalf("stale limit: %+v", cl)
	}
	if len(e.notes) == 0 {
		t.Fatal("fallback not notified")
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
	cl := e.svc("set_charger_dynamic_limit")
	if len(cl) == 0 || cl[0].Data["current"] != 16 { // guard limit at a quiet house is the max, 16
		t.Fatalf("deadline limit: %+v", cl)
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

func TestDisarmRestoresDefaults(t *testing.T) {
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
	cl, cc := e.svc("set_charger_dynamic_limit"), e.svc("set_circuit_dynamic_limit")
	if len(cl) != 1 || cl[0].Data["time_to_live"] != 1 || cl[0].Data["current"] != 32 ||
		len(cc) != 1 || cc[0].Data["time_to_live"] != 1 || cc[0].Data["current_p1"] != 32 {
		t.Fatalf("limits not released: %+v %+v", cl, cc)
	}
	sawDisarm := false
	for _, n := range e.notes {
		if n.Data["title"] == "EV controller disarmed" {
			sawDisarm = true
		}
	}
	if !sawDisarm {
		t.Fatal("disarm not notified")
	}
	e.reset()
	e.advance(10*time.Minute, 1000)
	if len(e.calls) != 0 {
		t.Fatalf("calls after release: %+v", e.calls)
	}
	if e.m.x.st.Paused || e.m.x.st.Holding {
		t.Fatal("hold flags not cleared")
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
	// 3 minutes later (stale) a grid update falls back to the estimate.
	m.OnStateUpdate(modules.StateUpdate{EntityID: gridEntity, Time: t0.Add(3 * time.Minute), Value: 800, Available: true})
	if m.guard.Measured || m.guard.LimitA != 16 {
		t.Fatalf("stale equalizer must fall back: %+v", m.guard)
	}
	// Offline: fall back even when fresh.
	m.OnStateUpdate(modules.StateUpdate{EntityID: "equalizer_current", Time: t0.Add(4 * time.Minute), Value: 12, State: "12", Available: true, Attrs: attrs("2", "12", "3")})
	if !m.guard.Measured {
		t.Fatal("fresh and online again must be measured")
	}
	m.OnStateUpdate(modules.StateUpdate{EntityID: "binary_sensor.equalizer_online", Time: t0.Add(4 * time.Minute), State: "off", Available: true})
	if m.guard.Measured {
		t.Fatalf("offline equalizer must fall back: %+v", m.guard)
	}
}
