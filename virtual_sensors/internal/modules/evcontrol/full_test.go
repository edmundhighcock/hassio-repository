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

// lastOut returns the newest published update for an object id.
func lastOut(outs []modules.OutputUpdate, id string) (modules.OutputUpdate, bool) {
	for i := len(outs) - 1; i >= 0; i-- {
		if outs[i].ObjectID == id {
			return outs[i], true
		}
	}
	return modules.OutputUpdate{}, false
}

// makeFull drives the car to a CONFIRMED full state (completed, no draw,
// held past ev_full_confirm_min).
func (e *env) makeFull() {
	e.setStatus("completed")
	e.advance(DefaultFullConfirm+time.Minute, 1000)
	if !e.m.full {
		e.t.Fatal("not full after the confirm window")
	}
}

func planAttrs(t *testing.T, u modules.OutputUpdate) map[string]any {
	var a map[string]any
	if err := json.Unmarshal([]byte(u.AttributesJSON), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// assertFull checks the published plan and controller state of a full car.
func assertFull(t *testing.T, m *Module, now time.Time) {
	t.Helper()
	p := planAttrs(t, m.planOutput(now))
	if sl, _ := p["slots"].([]any); len(sl) != 0 || p["feasible"] != true || p["need_kwh"].(float64) != 0 || p["deadline_mode"] != false {
		t.Fatalf("plan not full: %v", p)
	}
	st, cmd, reason := m.decide(now)
	if st != "car_full" || cmd != "none" || reason != "car reports charging complete" {
		t.Fatalf("decide: %q %q %q", st, cmd, reason)
	}
}

// Before the fix: need 39.9 kWh, infeasible, deadline backstop.
func TestCompletedMeansFull(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":40,"ev_mode":"live"}`, true)
	e.setStatus("charging")
	e.advance(10*time.Minute, 1000)
	e.setStatus("completed")
	e.reset()
	e.advance(2*time.Hour, 1000)
	assertFull(t, e.m, e.now)
	if len(e.svc("action_command")) != 0 {
		t.Fatalf("commands while full: %v", e.actions())
	}
	if e.m.x.lastDesired.Mode != "full" {
		t.Fatalf("desired %+v", e.m.x.lastDesired)
	}
	// Published state and plan.
	outs := e.m.publishAll(e.now, true)
	if u, ok := lastOut(outs, ObjState); !ok || u.StateString != "car_full" {
		t.Fatalf("state output %+v", u)
	}
	if u, ok := lastOut(outs, ObjWould); !ok || u.StateString != "none" {
		t.Fatalf("would output %+v", u)
	}
	if u, _ := lastOut(outs, ObjPlan); u.Value != 0 {
		t.Fatalf("plan value %v", u.Value)
	}
}

func TestFullStaysUntilDisconnectedThenPlansAfresh(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.makeFull()
	assertFull(t, e.m, e.now)
	e.setStatus("disconnected")
	if e.m.full {
		t.Fatal("full flag survived unplug")
	}
	e.advance(time.Minute, 1000)
	e.setStatus("awaiting_start")
	e.advance(time.Minute, 1000)
	if e.m.full || !e.m.havePlan || e.m.plan.NeedKWh <= 0 || len(e.m.plan.Slots) == 0 {
		t.Fatalf("no fresh plan: full=%v have=%v %+v", e.m.full, e.m.havePlan, e.m.plan)
	}
}

func TestCarChargesAgainWhileFullIsNotFought(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live"}`, true)
	e.makeFull()
	e.reset()
	e.setStatus("charging") // e.g. preconditioning
	e.advance(30*time.Minute, 1000)
	if len(e.svc("action_command")) != 0 {
		t.Fatalf("must neither pause nor resume: %v", e.actions())
	}
	if n := len(e.svc("set_charger_dynamic_limit")); n != 0 {
		t.Fatalf("charger limit written while full (preheat must not be throttled): %d", n)
	}
	assertFull(t, e.m, e.now) // stays full until disconnected
	// The circuit guard is still maintained.
	if len(e.svc("set_circuit_dynamic_limit")) == 0 && e.m.x.circuit.known == false {
		t.Fatal("circuit limit not guarded while full")
	}
}

func TestRestartWhileCompletedConfirmsFromTheRestart(t *testing.T) {
	// No persisted state at all: the replayed status alone does not latch
	// full; the confirmation clock starts at the first tick after restart.
	m := New()
	m.hub = house.NewHub()
	m.appendLine = func(string, []byte) error { return nil }
	m.Init(nil)
	now := at(10, 22, 0)
	m.OnLink(true, now)
	m.OnStateUpdate(modules.StateUpdate{EntityID: m.priceEnt, Time: now, Attrs: rawPrices(at(10, 0, 0), at(12, 0, 0), spikePrice)})
	m.OnStateUpdate(modules.StateUpdate{EntityID: "loaver_status", Time: now.Add(-3 * time.Hour), State: "completed", Available: true, Replay: true})
	if m.full {
		t.Fatal("a replayed completed latched full at once")
	}
	m.OnTick(now)
	m.OnTick(now.Add(5 * time.Minute))
	if m.full {
		t.Fatal("full before the window")
	}
	if _, _, r := m.decide(now.Add(5 * time.Minute)); !strings.Contains(r, "confirming full (5/10 min)") {
		t.Fatalf("reason %q", r)
	}
	m.OnTick(now.Add(11 * time.Minute))
	assertFull(t, m, now.Add(11*time.Minute))
	if u, _ := lastOut(m.InitialStates(), ObjState); u.StateString != "car_full" {
		t.Fatalf("initial state %+v", u)
	}

	// A persisted full flag is kept across restarts even before any status.
	b, _ := m.Persist()
	m2 := New()
	m2.hub = house.NewHub()
	if err := m2.Init(b); err != nil || !m2.full {
		t.Fatalf("persisted full lost: %v %v", err, m2.full)
	}
}

func TestRestartDuringPendingConfirmationRestartsTheClock(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.press(ButtonChargeNow)
	e.setStatus("completed")
	e.advance(8*time.Minute, 1000)
	if e.m.full || !e.m.cn.On {
		t.Fatal("pending expected")
	}
	b, _ := e.m.Persist()
	m2 := New()
	m2.hub = house.NewHub()
	m2.appendLine = func(string, []byte) error { return nil }
	if err := m2.Configure([]byte(cnOpts)); err != nil {
		t.Fatal(err)
	}
	if err := m2.Init(b); err != nil {
		t.Fatal(err)
	}
	m2.OnLink(true, e.now)
	m2.OnStateUpdate(modules.StateUpdate{EntityID: "loaver_status", Time: e.now, State: "completed", Available: true, Replay: true})
	m2.OnTick(e.now)
	m2.OnTick(e.now.Add(5 * time.Minute)) // 13 min of completed overall, 5 observed
	if m2.full || !m2.cn.On {
		t.Fatalf("restart must not count unobserved time: full=%v on=%v", m2.full, m2.cn.On)
	}
	m2.OnTick(e.now.Add(11 * time.Minute))
	if !m2.full || m2.cn.On {
		t.Fatalf("full=%v on=%v", m2.full, m2.cn.On)
	}
}

func TestReplanButtonWhileFullKeepsNoPlan(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":40,"ev_mode":"live"}`, true)
	e.makeFull()
	e.reset()
	outs := e.m.OnButton(ButtonReplan, e.now.Add(time.Minute))
	e.drain()
	assertFull(t, e.m, e.now)
	if u, ok := lastOut(outs, ObjPlan); !ok || planAttrs(t, u)["feasible"] != true || u.Value != 0 {
		t.Fatalf("replan output %+v", u)
	}
	e.advance(time.Minute, 1000)
	if len(e.svc("action_command")) != 0 {
		t.Fatalf("commands after replan: %v", e.actions())
	}
}

// --- host-level routing -------------------------------------------------
// The host strips "sensor." before delivering to modules. These tests feed
// FULL entity ids through host.HandleState, as the Home Assistant websocket
// does, so a module input declared with the wrong id fails here.

type hClock struct{ t time.Time }

func (c *hClock) Now() time.Time { return c.t }

type hPub struct{ outs []modules.OutputUpdate }

func (p *hPub) PublishState(u modules.OutputUpdate) error { p.outs = append(p.outs, u); return nil }

type hHA struct {
	calls []string // service names with the action
	n     int
}

func (f *hHA) CallService(domain, service string, data, target map[string]any) (int, error) {
	s := domain + "." + service
	if a, ok := data["action_command"].(string); ok {
		s += ":" + a
	}
	f.calls = append(f.calls, s)
	f.n++
	return f.n, nil
}

func TestHostRoutesLoaverStatusCompletedToFull(t *testing.T) {
	m := New()
	m.hub = house.NewHub()
	if err := m.Configure([]byte(`{"ev_need_kwh":40,"ev_mode":"live"}`)); err != nil {
		t.Fatal(err)
	}
	m.appendLine = func(string, []byte) error { return nil }
	m.Init(nil)
	clk := &hClock{t: at(10, 22, 0)}
	pub := &hPub{}
	h := host.New([]modules.Module{m}, pub, clk, "")
	ha := &hHA{}
	h.LinkUp(ha)

	send := func(full, state string, attrs map[string]json.RawMessage) {
		if !h.Match(full) {
			t.Fatalf("host does not route %s to the module", full)
		}
		h.HandleState(haws.StateEvent{EntityID: full, Time: clk.t, State: state, Attrs: attrs}, false)
	}
	send("sensor."+m.priceEnt, "", rawPrices(at(10, 0, 0), at(12, 0, 0), spikePrice))
	send("input_boolean.ev_controller_armed", "on", nil)
	if h.Match("sensor.loaver_status") == false {
		t.Fatal("sensor.loaver_status is not an input")
	}
	send("sensor.loaver_status", "charging", map[string]json.RawMessage{"id": json.RawMessage(`"EH5YL3HF"`)})
	send("sensor.loaver_power", "5.5", nil)
	send("sensor.grid_meter_power", "1000", nil)
	send("sensor.loaver_status", "completed", nil)
	send("sensor.loaver_power", "0", nil)
	for i := 0; i < 24; i++ {
		clk.t = clk.t.Add(30 * time.Second)
		send("sensor.grid_meter_power", "1000", nil)
		h.Tick()
	}
	if !m.full {
		t.Fatal("host never delivered loaver_status=completed to the module")
	}
	plan, ok := lastOut(pub.outs, ObjPlan)
	if !ok {
		t.Fatal("no ev_plan published")
	}
	pa := planAttrs(t, plan)
	if pa["feasible"] != true || pa["need_kwh"].(float64) != 0 || len(pa["slots"].([]any)) != 0 {
		t.Fatalf("published plan %v", pa)
	}
	if st, _ := lastOut(pub.outs, ObjState); st.StateString != "car_full" {
		t.Fatalf("published state %q", st.StateString)
	}
	for _, c := range ha.calls {
		if c == "easee.action_command:start" || c == "easee.action_command:resume" || c == "easee.action_command:pause" {
			t.Fatalf("charger commanded while full: %v", ha.calls)
		}
	}
	// Button via the host.
	h.Press(ButtonReplan)
	if plan, _ = lastOut(pub.outs, ObjPlan); planAttrs(t, plan)["feasible"] != true {
		t.Fatal("button resurrected a plan")
	}
	send("sensor.loaver_status", "disconnected", nil)
	if m.full {
		t.Fatal("full survived disconnect via host")
	}
}
