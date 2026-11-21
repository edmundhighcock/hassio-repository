package evcontrol

import (
	"encoding/json"
	"testing"
	"time"

	"virtual_sensors/internal/haws"
	"virtual_sensors/internal/host"
	"virtual_sensors/internal/house"
	"virtual_sensors/internal/modules"
)

func (e *env) press(id string) []modules.OutputUpdate {
	outs := e.m.OnButton(id, e.now)
	e.drain()
	return outs
}

func cnState(t *testing.T, m *Module) (string, map[string]any) {
	t.Helper()
	u, _ := m.chargeNowOutput(m.now)
	var a map[string]any
	if err := json.Unmarshal([]byte(u.AttributesJSON), &a); err != nil {
		t.Fatal(err)
	}
	return u.StateString, a
}

func logbookEntries(e *env) []string {
	var out []string
	for _, n := range e.notes {
		if n.Domain == "logbook" {
			out = append(out, n.Data["name"].(string))
		}
	}
	return out
}

func noPhone(t *testing.T, e *env) {
	t.Helper()
	for _, n := range e.notes {
		if n.Domain == "notify" {
			t.Fatalf("phone push for charge now: %+v", n)
		}
	}
}

const cnOpts = `{"ev_need_kwh":10,"ev_mode":"live"}`

func TestButtonsDeclared(t *testing.T) {
	m := New()
	got := map[string]string{}
	for _, b := range m.Buttons() {
		got[b.ObjectID] = b.Name
	}
	if got["ev_charge_now"] != "Charge car now" || got["ev_charge_now_stop"] != "Stop charge now" || got[ButtonReplan] == "" {
		t.Fatalf("buttons %v", got)
	}
	found := false
	for _, o := range m.Outputs() {
		if o.ObjectID == "ev_charge_now" && o.HasAttributes {
			found = true
		}
	}
	if !found {
		t.Fatal("status sensor not declared")
	}
}

func TestChargeNowStartsOutsideSlotAndGuardStaysInForce(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.advance(time.Minute, 1000) // 22:01, outside the 02:00-04:00 slots
	e.reset()
	outs := e.press(ButtonChargeNow)
	if st, _ := cnState(t, e.m); st != "on" {
		t.Fatalf("not on: %s", st)
	}
	if u, ok := lastOut(outs, ObjChargeNow); !ok || u.StateString != "on" {
		t.Fatalf("status sensor not published on press: %+v", u)
	}
	if u, _ := lastOut(outs, ObjState); u.StateString != "charge_now" || planAttrs(t, u)["reason"] == "" {
		t.Fatalf("state %+v", u)
	}
	e.advance(2*time.Minute, 1000)
	if got := e.actions(); !eqStr(got, []string{"start", "resume"}) {
		t.Fatalf("want start+resume, got %v", got)
	}
	cl := e.svc("set_charger_dynamic_limit")
	if len(cl) == 0 || cl[0].Data["current"] != 16 {
		t.Fatalf("quiet house: want 16 A, got %+v", cl)
	}
	if got := logbookEntries(e); len(got) != 1 || got[0] != "EV charge now started" {
		t.Fatalf("logbook %v", got)
	}
	noPhone(t, e)
	// Plan slots are ignored and it keeps charging: no pause.
	e.reset()
	e.advance(time.Hour, 1000)
	if len(e.svc("action_command")) != 0 {
		t.Fatalf("commands while charging: %v", e.actions())
	}
	// Heavy house: guard caps the current (12 A/phase house -> 7 A).
	e.reset()
	e.advance(3*time.Minute, 230*3*12)
	cl = e.svc("set_charger_dynamic_limit")
	if len(cl) == 0 || cl[len(cl)-1].Data["current"] != 7 {
		t.Fatalf("guard not capping: %+v", cl)
	}
	cc := e.svc("set_circuit_dynamic_limit")
	if last := cc[len(cc)-1].Data; last["current_p1"] != 7 || last["current_p3"] != 7 {
		t.Fatalf("circuit %+v", last)
	}
	if st, _, _ := e.m.decide(e.now); st != "charge_now" {
		t.Fatalf("state %s", st)
	}
	// No headroom: pause even in override.
	e.reset()
	e.advance(2*time.Minute, 230*3*15)
	if !eqStr(e.actions(), []string{"pause"}) {
		t.Fatalf("want pause with no headroom, got %v", e.actions())
	}
}

func TestChargeNowResumesPausedCharger(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.setStatus("charging")
	e.advance(time.Minute, 1000) // outside slots: paused by us
	e.setStatus("ready_to_charge")
	e.reset()
	e.press(ButtonChargeNow)
	e.advance(2*time.Minute, 1000)
	if got := e.actions(); !eqStr(got, []string{"resume"}) {
		t.Fatalf("want resume, got %v", got)
	}
}

func TestChargeNowEndsOnCompleted(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.advance(time.Minute, 1000)
	e.press(ButtonChargeNow)
	e.advance(2*time.Minute, 1000)
	e.reset()
	e.setStatus("completed")
	e.advance(5*time.Minute, 1000)
	if !e.m.cn.On || e.m.full {
		t.Fatalf("blip ended it: full=%v on=%v", e.m.full, e.m.cn.On)
	}
	e.advance(6*time.Minute, 1000)
	if !e.m.full || e.m.cn.On {
		t.Fatalf("full=%v on=%v", e.m.full, e.m.cn.On)
	}
	st, a := cnState(t, e.m)
	if st != "off" || a["reason_ended"] != "completed" {
		t.Fatalf("%s %v", st, a)
	}
	if got := logbookEntries(e); len(got) != 2 || got[0] != "EV car full" || got[1] != "EV charge now ended" {
		t.Fatalf("logbook %v", got)
	}
	noPhone(t, e)
	e.advance(10*time.Minute, 1000)
	if len(e.svc("action_command")) != 0 {
		t.Fatalf("commands after completed: %v", e.actions())
	}
	assertFull(t, e.m, e.now)
}

func TestChargeNowEndsOnDisconnected(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.press(ButtonChargeNow)
	e.advance(time.Minute, 1000)
	e.setStatus("disconnected")
	if st, a := cnState(t, e.m); st != "off" || a["reason_ended"] != "disconnected" {
		t.Fatalf("%s %v", st, a)
	}
}

func TestChargeNowTimeoutThenPlanningResumes(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live","ev_charge_now_hours":1}`, true)
	e.advance(time.Minute, 1000) // 22:01
	e.press(ButtonChargeNow)
	e.advance(55*time.Minute, 1000)
	if !e.m.cn.On {
		t.Fatal("ended early")
	}
	e.reset()
	e.advance(10*time.Minute, 1000) // past 23:01
	if e.m.cn.On {
		t.Fatal("did not time out")
	}
	if _, a := cnState(t, e.m); a["reason_ended"] != "timeout" {
		t.Fatalf("%v", a)
	}
	// Outside a slot, charging: the controller pauses.
	if !eqStr(e.actions(), []string{"pause"}) {
		t.Fatalf("want pause after timeout, got %v", e.actions())
	}
	if len(logbookEntries(e)) != 1 {
		t.Fatalf("logbook %v", logbookEntries(e))
	}
}

func TestDefaultTimeoutIs12h(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.press(ButtonChargeNow)
	if d := e.m.cn.Until.Sub(e.now); d != 12*time.Hour {
		t.Fatalf("default timeout %v", d)
	}
	m := New()
	if err := m.Configure([]byte(`{"ev_charge_now_hours":99}`)); err != nil || m.cnHours != 12 {
		t.Fatalf("out of range not defaulted: %v %v", err, m.cnHours)
	}
}

func TestStopButton(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.advance(time.Minute, 1000)
	e.press(ButtonChargeNow)
	e.advance(2*time.Minute, 1000)
	e.reset()
	e.press(ButtonChargeNowStop)
	if st, a := cnState(t, e.m); st != "off" || a["reason_ended"] != "stopped" {
		t.Fatalf("%s %v", st, a)
	}
	e.advance(2*time.Minute, 1000)
	if !eqStr(e.actions(), []string{"pause"}) {
		t.Fatalf("planning must resume and pause outside slot: %v", e.actions())
	}
	// Stop with nothing on is a no-op.
	e.reset()
	e.press(ButtonChargeNowStop)
	if len(e.notes) != 0 {
		t.Fatalf("notes %v", e.notes)
	}
}

func TestChargeNowWhileFull(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.makeFull()
	e.reset()
	e.press(ButtonChargeNow)
	if e.m.full || !e.m.cn.On {
		t.Fatalf("full=%v on=%v", e.m.full, e.m.cn.On)
	}
	e.advance(3*time.Minute, 1000)
	if got := e.actions(); !eqStr(got, []string{"start", "resume"}) {
		t.Fatalf("want start+resume while full, got %v", got)
	}
	// A repeat "completed" update (attributes) is not a new completion... the
	// car moved to charging via resume, so a fresh completed now ends it.
	e.setStatus("completed")
	if e.m.full || !e.m.cn.On {
		t.Fatalf("a blip must not end it: full=%v on=%v", e.m.full, e.m.cn.On)
	}
	e.advance(11*time.Minute, 1000)
	if !e.m.full || e.m.cn.On {
		t.Fatalf("full=%v on=%v", e.m.full, e.m.cn.On)
	}
	assertFull(t, e.m, e.now)
}

func TestChargeNowWhileFullSurvivesRepeatCompleted(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.setStatus("completed")
	e.press(ButtonChargeNow)
	e.setStatus("completed") // attribute-only repeat
	if !e.m.cn.On {
		t.Fatal("repeat completed ended the override")
	}
	e.m.cn.Kicked = true // the one start/resume attempt is spent; the car stays completed
	e.setStatus("charging")
	e.setStatus("completed")
	e.advance(11*time.Minute, 1000)
	if e.m.cn.On || !e.m.full {
		t.Fatalf("confirmed completion must end it: on=%v full=%v", e.m.cn.On, e.m.full)
	}
}

func TestChargeNowWhileDisconnectedDoesNothing(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.setStatus("disconnected")
	e.reset()
	outs := e.press(ButtonChargeNow)
	if e.m.cn.On || len(e.calls) != 0 || len(e.notes) != 0 {
		t.Fatalf("on=%v calls=%v notes=%v", e.m.cn.On, e.calls, e.notes)
	}
	u, ok := lastOut(outs, ObjChargeNow)
	if !ok || u.StateString != "off" || planAttrs(t, u)["reason"] != "not started: car not connected" {
		t.Fatalf("%+v", u)
	}
	e.advance(5*time.Minute, 1000)
	if len(e.calls) != 0 {
		t.Fatalf("calls %v", e.calls)
	}
}

func TestChargeNowSurvivesRestartUntilExpiry(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live","ev_charge_now_hours":1}`, true)
	e.press(ButtonChargeNow)
	b, err := e.m.Persist()
	if err != nil {
		t.Fatal(err)
	}
	m2 := New()
	m2.hub = house.NewHub()
	m2.appendLine = func(string, []byte) error { return nil }
	if err := m2.Configure([]byte(`{"ev_need_kwh":10,"ev_mode":"live","ev_charge_now_hours":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := m2.Init(b); err != nil {
		t.Fatal(err)
	}
	if !m2.cn.On {
		t.Fatal("override lost on restart")
	}
	if u, _ := lastOut(m2.InitialStates(), ObjChargeNow); u.StateString != "on" {
		t.Fatalf("initial %+v", u)
	}
	m2.OnLink(true, e.now)
	m2.OnStateUpdate(modules.StateUpdate{EntityID: "loaver_status", Time: e.now, State: "charging", Available: true, Replay: true})
	if !m2.cn.On {
		t.Fatal("replayed status ended it")
	}
	m2.OnTick(e.now.Add(61 * time.Minute))
	if m2.cn.On {
		t.Fatal("did not expire after restart")
	}
}

// --- host-level routing -------------------------------------------------

func TestHostRoutesChargeNowButtons(t *testing.T) {
	m := New()
	m.hub = house.NewHub()
	if err := m.Configure([]byte(`{"ev_need_kwh":10,"ev_mode":"live"}`)); err != nil {
		t.Fatal(err)
	}
	m.appendLine = func(string, []byte) error { return nil }
	m.Init(nil)
	clk := &hClock{t: at(10, 22, 0)}
	pub := &hPub{}
	h := host.New([]modules.Module{m}, pub, clk, "")
	ha := &hHA{}
	h.LinkUp(ha)

	ids := map[string]bool{}
	for _, b := range h.Buttons() {
		ids[b.ObjectID] = true
	}
	if !ids["ev_charge_now"] || !ids["ev_charge_now_stop"] {
		t.Fatalf("host does not know the buttons: %v", ids)
	}
	send := func(full, state string, attrs map[string]json.RawMessage) {
		if !h.Match(full) {
			t.Fatalf("host does not route %s", full)
		}
		h.HandleState(haws.StateEvent{EntityID: full, Time: clk.t, State: state, Attrs: attrs}, false)
	}
	send("sensor."+m.priceEnt, "", rawPrices(at(10, 0, 0), at(12, 0, 0), spikePrice))
	send("input_boolean.ev_controller_armed", "on", nil)
	send("sensor.loaver_status", "ready_to_charge", map[string]json.RawMessage{"id": json.RawMessage(`"EH5YL3HF"`)})
	send("sensor.loaver_power", "0", nil)
	answered := 0
	tick := func(n int) {
		for i := 0; i < n; i++ {
			clk.t = clk.t.Add(30 * time.Second)
			send("sensor.grid_meter_power", "1000", nil)
			h.Tick()
			// Answer every service call as the real HA does.
			for ; answered < ha.n; answered++ {
				h.HandleResult(haws.Result{ID: answered + 1, Success: true})
				if ha.calls[answered] == "easee.action_command:resume" {
					send("sensor.loaver_status", "charging", nil)
				}
			}
		}
	}
	tick(4)
	h.Press("ev_charge_now")
	if !m.cn.On {
		t.Fatal("host did not deliver the Charge now press")
	}
	if u, ok := lastOut(pub.outs, ObjChargeNow); !ok || u.StateString != "on" {
		t.Fatalf("status sensor via host: %+v", u)
	}
	tick(6)
	sawResume, sawLog := false, false
	for _, c := range ha.calls {
		if c == "easee.action_command:resume" {
			sawResume = true
		}
		if c == "logbook.log" {
			sawLog = true
		}
	}
	if !sawResume || !sawLog {
		t.Fatalf("calls %v", ha.calls)
	}
	if st, _ := lastOut(pub.outs, ObjState); st.StateString != "charge_now" {
		t.Fatalf("published state %q", st.StateString)
	}
	h.Press("ev_charge_now_stop")
	if m.cn.On {
		t.Fatal("host did not deliver the Stop press")
	}
	if u, _ := lastOut(pub.outs, ObjChargeNow); u.StateString != "off" {
		t.Fatalf("status after stop %+v", u)
	}
}
