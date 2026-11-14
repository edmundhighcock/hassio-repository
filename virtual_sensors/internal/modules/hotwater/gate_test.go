package hotwater

import (
	"encoding/json"
	"testing"
	"time"

	"virtual_sensors/internal/modules"
)

func pushState(m *Module, ent, state string, tSec float64) []modules.OutputUpdate {
	return m.OnStateUpdate(modules.StateUpdate{EntityID: ent, Time: unixTime(tSec).UTC(), State: state})
}

func pushEq(m *Module, tSec, l1, l2, l3 float64) {
	u := modules.StateUpdate{EntityID: "equalizer_current", Time: unixTime(tSec).UTC(), Available: true, Value: 1, State: "1",
		Attrs: map[string]json.RawMessage{}}
	for k, v := range map[string]float64{"state_currentL1": l1, "state_currentL2": l2, "state_currentL3": l3} {
		b, _ := json.Marshal(v)
		u.Attrs[k] = b
	}
	m.OnStateUpdate(u)
}

// gateRig is a module with a quiet house: grid 500 W, Equalizer 0.2 A on
// each phase, online, cover as given ("" = never reported).
type gateRig struct {
	t *testing.T
	m *Module
	T float64 // time of the step
	g float64 // current grid value
}

func newRig(t *testing.T, opts, cover string) *gateRig {
	t.Helper()
	m := New()
	if err := m.Configure([]byte(opts)); err != nil {
		t.Fatal(err)
	}
	m.Init(nil)
	r := &gateRig{t: t, m: m, T: 10000, g: 500}
	if cover != "" {
		pushState(m, DefaultCoverEntity, cover, 9000)
	}
	pushState(m, defaultEqOnline, "on", 9001)
	pushEq(m, 9002, 0.2, 0.2, 0.2)
	pushGrid(t, m, 9003, 500)
	return r
}

// run: grid steps by +3000 at T, the Equalizer reports eq (A on each phase,
// absolute) at T+6, then samples every 5 s for 70 s.
func (r *gateRig) run(eq [3]float64) {
	for tt := r.T - 20; tt <= r.T+70; tt += 5 {
		if tt >= r.T {
			r.g = 3500
		}
		if tt == r.T+5 {
			pushEq(r.m, r.T+6, eq[0], eq[1], eq[2])
		}
		pushGrid(r.t, r.m, tt, r.g)
	}
}

func TestPhaseGateHeaterConfirmed(t *testing.T) {
	r := newRig(t, `{}`, "open")
	r.run([3]float64{7.6, 7.5, 0.2})
	if !r.m.open || r.m.onTime < r.T-1 || r.m.onTime > r.T+30 {
		t.Fatalf("heater step not confirmed: open=%v on=%v", r.m.open, r.m.onTime)
	}
}

func TestPhaseGateRejectsOtherLoads(t *testing.T) {
	for name, eq := range map[string][3]float64{
		"kettle L1 only":     {10.8, 0.2, 0.2},
		"dishwasher L3 only": {0.2, 0.2, 13},
		"heat pump L2 only":  {0.2, 10, 0.2},
		"three-phase":        {7.5, 7.5, 7.5},
		"too small on L2":    {7.5, 3, 0.2},
	} {
		r := newRig(t, `{}`, "open")
		r.run(eq)
		if r.m.open || r.m.pendingOn != nil {
			t.Errorf("%s credited as the heater", name)
		}
	}
}

func TestPhaseGateFallsBack(t *testing.T) {
	// Equalizer offline.
	r := newRig(t, `{}`, "open")
	pushState(r.m, defaultEqOnline, "off", 9500)
	r.run([3]float64{10.8, 0.2, 0.2}) // would be rejected if consulted
	if !r.m.open {
		t.Errorf("offline Equalizer: step logic should stand")
	}
	// Stale: nothing heard for more than 15 minutes.
	r = newRig(t, `{}`, "open")
	r.T = 9002 + 16*60
	r.run([3]float64{10.8, 0.2, 0.2})
	// the reading at T+6 is fresh again, but the step was judged against
	// a stale baseline only if no update arrived: here one did, so it is
	// judged. Cut the update: no Equalizer reading at all in the window.
	r2 := newRig(t, `{}`, "open")
	r2.T = 9002 + 16*60
	for tt := r2.T - 20; tt <= r2.T+70; tt += 5 {
		if tt >= r2.T {
			r2.g = 3500
		}
		pushGrid(t, r2.m, tt, r2.g)
	}
	if !r2.m.open {
		t.Errorf("stale Equalizer: step logic should stand")
	}
	// Gate switched off by option.
	r3 := newRig(t, `{"hw_phase_gate": false}`, "open")
	r3.run([3]float64{10.8, 0.2, 0.2})
	if !r3.m.open {
		t.Errorf("hw_phase_gate=false: step logic should stand")
	}
	// Equalizer quiet in the window (no phase change): unknown, accepted.
	r4 := newRig(t, `{}`, "open")
	for tt := r4.T - 20; tt <= r4.T+70; tt += 5 {
		if tt >= r4.T {
			r4.g = 3500
		}
		pushGrid(t, r4.m, tt, r4.g)
	}
	if !r4.m.open {
		t.Errorf("no Equalizer update in the window: step logic should stand")
	}
}

func TestPhaseGateUnderEV(t *testing.T) {
	// EV charging at 4.4 kW; the heater steps on top: L1/L2 rise relative
	// to L3 even though L1 only rose 4.9 A (2026-11-13 19:24Z).
	r := newRig(t, `{}`, "open")
	pushLoad(t, r.m, "loaver_power", 9500, 4.4) // kW
	pushEq(r.m, 9600, 7.9, 8.6, 8.0)            // EV already on all three phases
	r.run([3]float64{12.8, 16.3, 8.0})
	if !r.m.open {
		t.Errorf("heater under an EV not confirmed")
	}
	// An EV ramp alone (all three phases) is not the heater.
	r = newRig(t, `{}`, "open")
	pushLoad(t, r.m, "loaver_power", 9500, 0.2)
	pushEq(r.m, 9600, 0.2, 0.2, 0.2)
	pushLoad(t, r.m, "loaver_power", r.T-1, 3.0)
	r.run([3]float64{7.9, 8.6, 8.0})
	if r.m.open {
		t.Errorf("EV ramp credited as the heater")
	}
}

func TestSwitchGate(t *testing.T) {
	good := [3]float64{7.6, 7.5, 0.2}
	r := newRig(t, `{}`, "closed")
	r.run(good)
	if r.m.open {
		t.Errorf("credited with the switch closed")
	}
	r = newRig(t, `{}`, "open")
	r.run(good)
	if !r.m.open {
		t.Errorf("not credited with the switch open")
	}
	r = newRig(t, `{}`, "") // no cover reading ever: gate cannot apply
	r.run(good)
	if !r.m.open {
		t.Errorf("unknown cover must not block")
	}
	r = newRig(t, `{}`, "unavailable")
	r.run(good)
	if !r.m.open {
		t.Errorf("unavailable cover must not block")
	}
	r = newRig(t, `{"hw_switch_gate": false}`, "closed")
	r.run(good)
	if !r.m.open {
		t.Errorf("hw_switch_gate=false must not block")
	}
	// "closed" for under the slack (the cover reports closed for a second at
	// the command, then closing): not known OFF.
	r = newRig(t, `{}`, "")
	r.m.cover = append(r.m.cover, coverTransition{T: r.T + 28, State: "closed"})
	r.run(good)
	if !r.m.open {
		t.Errorf("a closed state younger than the slack must not block")
	}
}

// An open cycle with the switch known OFF for 3 min is closed, credited up
// to just after the switch closed.
func TestSwitchOffClosesOpenCycle(t *testing.T) {
	r := newRig(t, `{}`, "open")
	r.run([3]float64{7.6, 7.5, 0.2})
	if !r.m.open {
		t.Fatal("cycle not open")
	}
	closeAt := r.T + 600
	var closed []CycleClosed
	r.m.SetSignalSink(func(s any) {
		if c, ok := s.(CycleClosed); ok {
			closed = append(closed, c)
		}
	})
	for tt := r.T + 75; tt <= closeAt+400; tt += 5 {
		if tt == closeAt {
			pushState(r.m, DefaultCoverEntity, "closed", tt)
		}
		pushGrid(t, r.m, tt, 3500) // the off step is never seen
	}
	if r.m.open || len(closed) != 1 || closed[0].Reason != "switch_off" || !closed[0].Accepted {
		t.Fatalf("not closed by the switch: open=%v closed=%+v", r.m.open, closed)
	}
	want := 3000 * (closeAt + switchOffCreditS - r.m.onTime) / 3600 / 1000
	if d := closed[0].OffTime.Sub(time.Unix(int64(closeAt+switchOffCreditS), 0)); d > time.Second || d < -time.Second {
		t.Errorf("off time %v", closed[0].OffTime)
	}
	if closed[0].Kwh < 0.5*want || closed[0].Kwh > 1.5*want {
		t.Errorf("kwh %v want about %v", closed[0].Kwh, want)
	}
}

// The plateau fallback needs a heater-level residual held for two minutes;
// a lost EV (its draw still in the grid) must not fake one.
func TestPlateauFallback(t *testing.T) {
	feed := func(m *Module, from, to, v float64) {
		for tt := from; tt <= to; tt += 5 {
			pushGrid(t, m, tt, v)
		}
	}
	// ramp climbs to v in sub-seed steps, so no step event is seen.
	ramp := func(m *Module, from, v float64) float64 {
		tt := from
		for _, x := range []float64{1150, 1900, 2650, 3400} { // 750 W per 40 s: never a step event
			pushGrid(t, m, tt, x)
			tt += 40
		}
		pushGrid(t, m, tt, v)
		return tt + 5
	}
	m := newTestModule(t)
	for _, e := range KnownLoadEntities {
		pushLoad(t, m, e, 99, 0)
	}
	feed(m, 100, 200, 400)
	t0 := ramp(m, 205, 3500)
	feed(m, t0, t0+80, 3500) // under 120 s at heater level
	feed(m, t0+85, t0+300, 400)
	if m.open {
		t.Fatal("a short plateau opened a cycle")
	}
	t1 := ramp(m, t0+305, 3500)
	feed(m, t1, t1+140, 3500)
	if !m.open || !m.rescued {
		t.Fatal("130 s plateau did not open a cycle")
	}
	// the residual leaves the floor: closed and credited from the open.
	feed(m, t1+145, t1+400, 400)
	if m.open || m.energyKwh <= 0 {
		t.Fatalf("rescued cycle not closed/credited: open=%v kwh=%v", m.open, m.energyKwh)
	}
	// A lost 3.5 kW EV keeps its draw in the grid: not a heater.
	m2 := newTestModule(t)
	for _, e := range KnownLoadEntities {
		pushLoad(t, m2, e, 99, 0)
	}
	feed(m2, 100, 200, 400)
	pushLoad(t, m2, "loaver_power", 201, 3.5)
	feed(m2, 205, 210, 400)
	pushGrid(t, m2, 211, 1200)
	pushGrid(t, m2, 212, 1900)
	pushGrid(t, m2, 213, 2600)
	pushGrid(t, m2, 214, 3300)
	pushGrid(t, m2, 215, 3900) // the EV's draw, metered (residual stays low)
	feed(m2, 220, 230, 3900)
	pushLoadUnavailable(t, m2, "loaver_power", 232)
	feed(m2, 235, 235+200, 3900) // residual now 3900 for 200 s: the EV, not the heater
	if m2.open {
		t.Fatal("a lost EV was taken for the heater")
	}
	// Option off.
	m3 := New()
	m3.Configure([]byte(`{"hw_plateau_rescue": false}`))
	m3.Init(nil)
	feed(m3, 100, 200, 400)
	t3 := ramp(m3, 205, 3500)
	feed(m3, t3, t3+300, 3500)
	if m3.open {
		t.Fatal("hw_plateau_rescue=false opened a cycle")
	}
}

// 0.6.6: phaseEnd confirms a thermostat end from the per-phase drop.
func TestPhaseEnd(t *testing.T) {
	cases := []struct {
		name   string
		before [3]float64
		after  *[3]float64
		want   int
	}{
		{"both drop", [3]float64{8, 7.8, 0.2}, &[3]float64{0.7, 0.5, 0.2}, 1},
		{"l1 only", [3]float64{8, 7.8, 0.2}, &[3]float64{0.7, 7.6, 0.2}, 2},
		{"no reading after", [3]float64{8, 7.8, 0.2}, nil, 0},
	}
	for _, c := range cases {
		r := newRig(t, `{}`, "open")
		pushEq(r.m, 19990, c.before[0], c.before[1], c.before[2])
		if c.after != nil {
			pushEq(r.m, 20020, c.after[0], c.after[1], c.after[2])
		}
		if got := r.m.phaseEnd(20000); got != c.want {
			t.Errorf("%s: %d want %d", c.name, got, c.want)
		}
	}
	// a reading beyond the 60 s window does not count
	r := newRig(t, `{}`, "open")
	pushEq(r.m, 19990, 8, 7.8, 0.2)
	pushEq(r.m, 20100, 0.7, 0.5, 0.2)
	if got := r.m.phaseEnd(20000); got != 0 {
		t.Errorf("late reading: %d", got)
	}
	// gate disabled: unknown
	r = newRig(t, `{"hw_phase_gate":false}`, "open")
	pushEq(r.m, 19990, 8, 7.8, 0.2)
	if got := r.m.phaseEnd(20000); got != 0 {
		t.Errorf("gate off: %d", got)
	}
}
