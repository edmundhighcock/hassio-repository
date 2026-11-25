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

// The EV planner's per-slot deliverable energy follows the house forecast of
// the non-EV load per phase, not its static assumptions; the deadline
// backstop still applies.
func TestPlanDeliverableFollowsHouseForecast(t *testing.T) {
	p := DefaultParams()
	p.Derate = 1
	now := at(10, 22, 0)
	dep := at(11, 6, 30)
	// Forecast: quiet before 02:00 (3 A: 16 A three-phase), heat pump on L2
	// 02:00-04:00 (three phases 9 A), L2 and L3 full after 04:00 (nothing).
	load := func(t time.Time) ([3]float64, bool) {
		h := t.In(p.Loc).Hour()
		switch {
		case h >= 2 && h < 4:
			return [3]float64{3, 10.4, 3}, true
		case h >= 4:
			return [3]float64{3, 19, 16}, true
		}
		return [3]float64{3, 3, 3}, true
	}
	pr := prices(now, dep, func(h int) float64 { return 1 })
	r := Plan(Input{Now: now, Departure: dep, Prices: pr, LoadA: load}, p)
	if len(r.Slots) == 0 {
		t.Fatal("no slots")
	}
	for _, s := range r.Slots {
		h := s.Start.In(p.Loc).Hour()
		wantA, wantKW := 16.0, 11.04
		if h >= 2 && h < 4 {
			wantA, wantKW = 9, 6.21
		}
		if h >= 4 || s.Amps != wantA || !near(s.KW, wantKW) {
			t.Fatalf("slot %s amps %v kw %v want %v A %v kW", s.Start, s.Amps, s.KW, wantA, wantKW)
		}
	}
	// Static fallback (no forecast): heat pump assumed on L2, 9 A three-phase.
	r2 := Plan(Input{Now: at(10, 23, 30), Departure: dep, Prices: pr}, p)
	if r2.Slots[0].Amps != 9 {
		t.Fatalf("static: %v", r2.Slots[0].Amps)
	}
	// Deadline backstop: a forecast too small for the need uses every usable slot.
	small := func(time.Time) ([3]float64, bool) { return [3]float64{3, 14, 16}, true } // 6 A three-phase
	r3 := Plan(Input{Now: now, Departure: dep, Prices: pr, LoadA: small}, p)
	if !r3.DeadlineMode || r3.Feasible {
		t.Fatalf("want deadline mode, infeasible: %+v", r3.DeadlineMode)
	}
	// Forecast says not covered: static fallback.
	none := func(time.Time) ([3]float64, bool) { return [3]float64{}, false }
	if r4 := Plan(Input{Now: at(10, 23, 30), Departure: dep, Prices: pr, LoadA: none}, p); r4.Slots[0].Amps != 9 {
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
	if l, ok := m.houseLoad(m.now.Add(time.Hour)); !ok || l != [3]float64{3, 3, 3} {
		t.Fatalf("forecast load %v %v", l, ok)
	}
	m.now = m.now.Add(2 * time.Hour)
	if _, ok := m.houseLoad(m.now); ok {
		t.Fatal("a stale forecast must not be used")
	}
}

// The house planner's overload cap no longer lowers or pauses the EV: the
// charger and the Equalizer protect the phases the car uses. Live and armed
// it sends nothing about a cap.
func TestHouseCapIsNotActedOn(t *testing.T) {
	e := newEnv(t, `{"ev_need_kwh":44,"ev_mode":"live"}`, true)
	e.advance(time.Minute, 1000)
	e.setStatus("charging")
	e.reset()
	for i := 0; i < 6; i++ { // the planner refreshes the cap every tick
		e.m.hub.SetCaps(e.now, 0, -1)
		e.advance(30*time.Second, 1000)
	}
	if len(e.calls) != 0 {
		t.Fatalf("cap acted on: %+v", e.calls)
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
