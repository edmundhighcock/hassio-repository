package evcontrol

import (
	"testing"
	"time"

	"virtual_sensors/internal/house"
)

func TestEqualizerFreshRule(t *testing.T) {
	t0 := at(10, 22, 0)
	w := DefaultEqFresh
	if w != 15*time.Minute {
		t.Fatalf("default window %v", w)
	}
	cases := []struct {
		ok, online bool
		age        time.Duration
		want       bool
	}{
		{true, true, 10 * time.Minute, true},
		{true, true, 14 * time.Minute, true},
		{true, true, 16 * time.Minute, false},
		{true, false, time.Minute, false},
		{false, true, time.Minute, false},
	}
	for i, c := range cases {
		if got := EqualizerFresh(c.ok, c.online, t0, t0.Add(c.age), w); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
}

// The EV planner's per-slot ceiling follows the house forecast, not its
// static assumptions; the deadline backstop still applies.
func TestPlanCeilingFollowsHouseForecast(t *testing.T) {
	p := DefaultParams()
	now := at(10, 22, 0)
	dep := at(11, 6, 30)
	// Forecast: 12 A before 02:00, 7 A from 02:00 to 04:00, 0 (no room) after.
	ceil := func(t time.Time) (float64, bool) {
		h := t.In(p.Loc).Hour()
		switch {
		case h >= 2 && h < 4:
			return 7.9, true
		case h >= 4:
			return 3, true
		}
		return 12.4, true
	}
	prices := prices(now, dep, func(h int) float64 { return 1 })
	r := Plan(Input{Now: now, Departure: dep, Prices: prices, CeilingA: ceil}, p)
	for _, s := range r.Slots {
		h := s.Start.In(p.Loc).Hour()
		want := 12.0
		switch {
		case h >= 2 && h < 4:
			want = 7
		case h >= 4:
			want = 0
		}
		if s.Amps != want || (want == 0) {
			t.Fatalf("slot %s amps %v want %v", s.Start, s.Amps, want)
		}
	}
	if len(r.Slots) == 0 {
		t.Fatal("no slots")
	}
	// Static ceiling (no forecast) differs: 8 A with the heat pump assumed on.
	r2 := Plan(Input{Now: now, Departure: dep, Prices: prices}, p)
	if r2.Slots[0].Amps != 8 {
		t.Fatalf("static ceiling %v", r2.Slots[0].Amps)
	}
	// Deadline backstop: a forecast too small for the need uses every usable slot.
	small := func(time.Time) (float64, bool) { return 6, true }
	r3 := Plan(Input{Now: now, Departure: dep, Prices: prices, CeilingA: small}, p)
	if !r3.DeadlineMode || r3.Feasible {
		t.Fatalf("want deadline mode, infeasible: %+v", r3.DeadlineMode)
	}
	// Forecast says not covered: static fallback.
	none := func(time.Time) (float64, bool) { return 0, false }
	if r4 := Plan(Input{Now: now, Departure: dep, Prices: prices, CeilingA: none}, p); r4.Slots[0].Amps != 8 {
		t.Fatalf("fallback %v", r4.Slots[0].Amps)
	}
}

func TestModuleUsesHubForecastAndFallsBackWhenStale(t *testing.T) {
	m := New()
	m.hub = house.NewHub()
	m.now = at(10, 22, 0)
	f := house.ComputeForecast(house.ForecastIn{Now: m.now, Horizon: 24 * time.Hour, BudgetA: 19,
		BaseA: [3]float64{3, 3, 3}, Map: house.DefaultPhaseMap(),
		Heater: house.HeaterPlan{Valid: true, NominalW: 3000, Windows: []house.HeaterWindow{{Start: m.now, End: m.now.Add(24 * time.Hour), On: false}}},
		HP:     house.HPPlan{Valid: true, W: map[int64]float64{}, MaxW: 0}})
	m.hub.SetForecast(f)
	if v, ok := m.houseCeiling(m.now.Add(time.Hour)); !ok || v != 16 { // 19-3 = 16, capped at MaxA
		t.Fatalf("forecast ceiling %v %v", v, ok)
	}
	m.now = m.now.Add(2 * time.Hour)
	if _, ok := m.houseCeiling(m.now); ok {
		t.Fatal("a stale forecast must not be used")
	}
	if got := m.ceilingNow(m.now); got != m.p.Ceiling(m.now, m.hwOn(m.now)) {
		t.Fatalf("static fallback %v", got)
	}
}

// Overload: shadow publishes (would-command) but sends nothing; armed live
// it lowers the circuit and pauses.
func TestHouseCapShadowVersusLive(t *testing.T) {
	for _, live := range []bool{false, true} {
		opts := `{"ev_need_kwh":44}`
		if live {
			opts = `{"ev_need_kwh":44,"ev_mode":"live"}`
		}
		e := newEnv(t, opts, true)
		e.advance(time.Minute, 1000)
		e.setStatus("charging")
		e.reset()
		for i := 0; i < 6; i++ { // the planner refreshes the cap every tick
			e.m.hub.SetCaps(e.now, 0, -1)
			e.advance(30*time.Second, 1000)
		}
		if !live {
			if len(e.calls) != 0 {
				t.Fatalf("shadow acted: %v", e.calls)
			}
			_, cmd, why := e.m.decide(e.now)
			if cmd != "pause" {
				t.Fatalf("shadow would-command %q (%s), want pause", cmd, why)
			}
			continue
		}
		cs := e.svc("set_circuit_dynamic_limit")
		if len(cs) == 0 || cs[len(cs)-1].Data["current_p1"] != 0 {
			t.Fatalf("live: circuit limit not lowered: %+v", e.calls)
		}
		if !eqStr(e.actions(), []string{"pause"}) {
			t.Fatalf("live: want pause, got %v calls %+v last %+v desired %+v status %q fly %v pauseAt %v linkUp %v", e.actions(), e.calls, e.m.x.last, e.m.x.lastDesired, e.m.status, e.m.x.pauseFly, e.m.x.pauseAt, e.m.x.linkUp)
		}
	}
}

func TestGuardPublishesMeasureToHub(t *testing.T) {
	m := New()
	m.hub = house.NewHub()
	t0 := at(10, 22, 0)
	m.runGuard(t0) // no data: nothing published
	if _, ok := m.hub.Measure(); ok {
		t.Fatal("measure published without data")
	}
	m.gridOK, m.gridW = true, 1000
	m.runGuard(t0)
	ms, ok := m.hub.Measure()
	if !ok || ms.Measured || ms.At != t0 {
		t.Fatalf("%+v %v", ms, ok)
	}
}
