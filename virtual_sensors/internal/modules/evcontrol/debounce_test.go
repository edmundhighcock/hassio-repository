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

// step sets a status and lets 30 s pass (n times 30 s).
func (e *env) step(status string, n int) {
	e.setStatus(status)
	e.advance(time.Duration(n)*30*time.Second, 1000)
}

// The real 2026-11-20/21 sequence (synthetic clock): completed blips in a
// charging session, then a long completed, then the car asking to charge.
func TestRealNightReplayDoesNotLatchFullAndStillCharges(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.advance(time.Minute, 1000) // 22:01
	e.press(ButtonChargeNow)
	e.step("charging", 2)
	// 17:16 charging -> completed -> charging -> awaiting_start -> ready_to_charge -> charging
	e.step("completed", 1)
	if e.m.full || !e.m.cn.On {
		t.Fatalf("latched at the blip: full=%v on=%v", e.m.full, e.m.cn.On)
	}
	e.step("charging", 1)
	e.step("awaiting_start", 1)
	e.step("ready_to_charge", 1)
	e.step("charging", 2)
	// then cycling for ~90 min, with completed blips (up to 2 min)
	for i := 0; i < 30; i++ {
		e.step("charging", 1)
		e.step("awaiting_start", 1)
		e.step("ready_to_charge", 1)
		if i%5 == 4 {
			e.step("completed", 3)
			e.step("charging", 1)
		}
		if e.m.full || !e.m.cn.On {
			t.Fatalf("cycle %d: full=%v on=%v", i, e.m.full, e.m.cn.On)
		}
	}
	// alternating completed/charging several times
	for i := 0; i < 4; i++ {
		e.step("completed", 2)
		e.step("charging", 1)
	}
	if e.m.full || !e.m.cn.On {
		t.Fatalf("alternation latched: full=%v on=%v", e.m.full, e.m.cn.On)
	}
	// Finally completed for hours: a CONFIRMED full ends Charge now.
	e.step("completed", 1)
	e.advance(4*time.Minute, 1000)
	if e.m.full || !e.m.cn.On {
		t.Fatalf("confirmed too early: full=%v on=%v", e.m.full, e.m.cn.On)
	}
	if _, _, r := e.m.decide(e.now); !strings.Contains(r, "confirming full (4/10 min)") {
		t.Fatalf("pending not shown: %q", r)
	}
	for e.now.Before(at(11, 1, 50)) {
		e.advance(10*time.Minute, 1000)
	}
	if !e.m.full || e.m.cn.On {
		t.Fatalf("long completed: full=%v on=%v", e.m.full, e.m.cn.On)
	}
	if _, a := cnState(t, e.m); a["reason_ended"] != "completed" {
		t.Fatalf("%v", a)
	}
	// 01:50 awaiting_start, 01:55 ready_to_charge: the car asks to charge.
	e.reset()
	e.setStatus("awaiting_start")
	e.advance(3*time.Minute, 1000)
	e.setStatus("ready_to_charge")
	e.advance(4*time.Minute, 1000) // 01:57: 7 min asking, but the clock runs from awaiting_start
	if e.m.full {
		t.Fatalf("still full after the car asked for %v", 7*time.Minute)
	}
	if !e.m.havePlan || e.m.plan.NeedKWh <= 0 || len(e.m.plan.Slots) == 0 {
		t.Fatalf("no plan after clearing: %+v", e.m.plan)
	}
	e.advance(5*time.Minute, 1000) // into the 02:00 slot
	if !eqStr(e.actions(), []string{"resume"}) {
		t.Fatalf("want a resume in the slot, got %v", e.actions())
	}
	got := logbookEntries(e)
	if len(got) != 1 || got[0] != "EV car no longer full" {
		t.Fatalf("logbook %v", got)
	}
}

func TestLongCompletedConfirmsFullAndLogs(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":10,"ev_mode":"live","ev_full_confirm_min":20}`, true)
	e.setStatus("completed")
	e.advance(19*time.Minute, 1000)
	if e.m.full {
		t.Fatal("custom confirm window ignored")
	}
	e.advance(2*time.Minute, 1000)
	assertFull(t, e.m, e.now)
	if got := logbookEntries(e); len(got) == 0 || got[len(got)-1] != "EV car full" {
		t.Fatalf("logbook %v", got)
	}
	outs := e.m.publishAll(e.now, true)
	if u, _ := lastOut(outs, ObjState); planAttrs(t, u)["full"] != true {
		t.Fatalf("state attrs %v", u.AttributesJSON)
	}
}

func TestChargerDrawBlocksConfirmation(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.setStatus("completed")
	e.advance(6*time.Minute, 1000)
	e.feed("loaver_power", "2.0", 2.0, nil) // still drawing while "completed"
	e.advance(6*time.Minute, 1000)
	if e.m.full {
		t.Fatal("confirmed while the charger draws")
	}
	e.feed("loaver_power", "0", 0, nil)
	e.advance(9*time.Minute, 1000)
	if e.m.full {
		t.Fatal("window must restart after the draw")
	}
	e.advance(2*time.Minute, 1000)
	if !e.m.full {
		t.Fatal("not confirmed after 10+ quiet minutes")
	}
	// loaver_current works too.
	e2 := newEnv(t, cnOpts, true)
	e2.setStatus("completed")
	e2.feed("loaver_current", "8", 8, nil)
	e2.advance(12*time.Minute, 1000)
	if e2.m.full {
		t.Fatal("confirmed while current flows")
	}
}

func TestFullClearsWhenCarAsksToCharge(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.makeFull()
	e.reset()
	e.setStatus("ready_to_charge")
	e.advance(4*time.Minute, 1000)
	if !e.m.full {
		t.Fatal("cleared early")
	}
	if _, _, r := e.m.decide(e.now); !strings.Contains(r, "confirming it wants to charge (4/5 min)") {
		t.Fatalf("reason %q", r)
	}
	e.setStatus("charging") // any change away restarts the clock
	e.setStatus("ready_to_charge")
	e.advance(4*time.Minute, 1000)
	if !e.m.full {
		t.Fatal("clock did not restart")
	}
	e.advance(90*time.Second, 1000)
	if e.m.full || !e.m.havePlan || e.m.plan.NeedKWh <= 0 || len(e.m.plan.Slots) == 0 {
		t.Fatalf("full=%v plan=%+v", e.m.full, e.m.plan)
	}
	if got := logbookEntries(e); len(got) != 1 || got[0] != "EV car no longer full" {
		t.Fatalf("logbook %v", got)
	}
	// And a fresh completed must be confirmed again.
	e.setStatus("completed")
	e.advance(time.Minute, 1000)
	if e.m.full {
		t.Fatal("re-latched at once")
	}
}

func TestStopAlwaysReplansAndResumesPlannedBehaviour(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.advance(time.Minute, 1000) // outside the 02:00-04:00 slots
	e.press(ButtonChargeNow)
	e.advance(2*time.Minute, 1000) // charging
	e.reset()
	e.press(ButtonChargeNowStop)
	if !eqStr(e.actions(), []string{"pause"}) {
		t.Fatalf("Stop must pause at once outside a slot, got %v", e.actions())
	}
	// Override already off; the car is charging again outside the slot.
	e.setStatus("charging")
	e.reset()
	e.m.lastPlanAt = time.Time{}
	e.press(ButtonChargeNowStop)
	if e.m.lastPlanAt.IsZero() {
		t.Fatal("Stop with the override off did not replan")
	}
	if !eqStr(e.actions(), []string{"pause"}) {
		t.Fatalf("planned behaviour not resumed: %v", e.actions())
	}
	// In a slot: Stop starts a waiting charger.
	e.advance(3*time.Hour, 1000) // 01:03? keep going into the slot
	for e.now.Before(at(11, 2, 5)) {
		e.advance(10*time.Minute, 1000)
	}
	e.setStatus("ready_to_charge")
	e.reset()
	e.press(ButtonChargeNowStop)
	if !eqStr(e.actions(), []string{"resume"}) {
		t.Fatalf("in a slot Stop must resume, got %v", e.actions())
	}
}

func TestStopWhileFullDoesNotClearFull(t *testing.T) {
	e := newEnv(t, cnOpts, true)
	e.makeFull()
	e.reset()
	e.m.lastPlanAt = time.Time{}
	e.press(ButtonChargeNowStop)
	if !e.m.full || e.m.lastPlanAt.IsZero() {
		t.Fatalf("full=%v replanned=%v", e.m.full, !e.m.lastPlanAt.IsZero())
	}
	if _, a := cnState(t, e.m); !strings.Contains(a["reason"].(string), "still marked full") {
		t.Fatalf("%v", a)
	}
	assertFull(t, e.m, e.now)
}

// --- host routing ----------------------------------------------------------

func TestHostDebounceChargeNowAndStop(t *testing.T) {
	m := New()
	m.hub = house.NewHub()
	if err := m.Configure([]byte(cnOpts)); err != nil {
		t.Fatal(err)
	}
	m.appendLine = func(string, []byte) error { return nil }
	m.Init(nil)
	clk := &hClock{t: at(10, 22, 0)}
	pub := &hPub{}
	h := host.New([]modules.Module{m}, pub, clk, "")
	ha := &hHA{}
	h.LinkUp(ha)
	send := func(full, state string) {
		if !h.Match(full) {
			t.Fatalf("host does not route %s", full)
		}
		var attrs map[string]json.RawMessage
		if full == "sensor.loaver_status" {
			attrs = map[string]json.RawMessage{"id": json.RawMessage(`"EH5YL3HF"`)}
		}
		if full == "sensor.nordpool" {
			attrs = rawPrices(at(10, 0, 0), at(12, 0, 0), spikePrice)
		}
		h.HandleState(haws.StateEvent{EntityID: full, Time: clk.t, State: state, Attrs: attrs}, false)
	}
	send("sensor."+m.priceEnt, "")
	m.OnStateUpdate(modules.StateUpdate{EntityID: m.priceEnt, Time: clk.t, Attrs: rawPrices(at(10, 0, 0), at(12, 0, 0), spikePrice)})
	send("input_boolean.ev_controller_armed", "on")
	send("sensor.loaver_status", "ready_to_charge")
	send("sensor.loaver_power", "0")
	answered := 0
	tick := func(n int) {
		for i := 0; i < n; i++ {
			clk.t = clk.t.Add(30 * time.Second)
			send("sensor.grid_meter_power", "1000")
			h.Tick()
			for ; answered < ha.n; answered++ {
				h.HandleResult(haws.Result{ID: answered + 1, Success: true})
				switch ha.calls[answered] {
				case "easee.action_command:resume":
					send("sensor.loaver_status", "charging")
				case "easee.action_command:pause":
					send("sensor.loaver_status", "ready_to_charge")
				}
			}
		}
	}
	tick(4)
	h.Press("ev_charge_now")
	tick(6)
	if !m.cn.On {
		t.Fatal("override not on")
	}
	// A completed blip routed through the host does not end it.
	send("sensor.loaver_status", "completed")
	tick(2)
	send("sensor.loaver_status", "charging")
	tick(2)
	if m.full || !m.cn.On {
		t.Fatalf("blip via host: full=%v on=%v", m.full, m.cn.On)
	}
	// Pending is visible in the published reason.
	send("sensor.loaver_status", "completed")
	tick(6)
	if st, _ := lastOut(pub.outs, ObjState); !strings.Contains(planAttrs(t, st)["reason"].(string), "confirming full (3/10 min)") {
		t.Fatalf("published reason %v", st.AttributesJSON)
	}
	tick(16)
	if !m.full || m.cn.On {
		t.Fatalf("confirmed via host: full=%v on=%v", m.full, m.cn.On)
	}
	if st, _ := lastOut(pub.outs, ObjState); st.StateString != "car_full" {
		t.Fatalf("state %q", st.StateString)
	}
	// The car asks to charge: full clears and the plan appears.
	send("sensor.loaver_status", "awaiting_start")
	tick(12)
	if m.full || !m.havePlan || m.plan.NeedKWh <= 0 {
		t.Fatalf("not cleared via host: full=%v plan=%+v", m.full, m.plan)
	}
	// Stop with the override off, car charging outside a slot: pause at once.
	send("sensor.loaver_status", "charging")
	n := len(ha.calls)
	h.Press("ev_charge_now_stop")
	if len(ha.calls) <= n || ha.calls[n] != "easee.action_command:pause" {
		t.Fatalf("Stop via host did not pause: %v", ha.calls[n:])
	}
}
