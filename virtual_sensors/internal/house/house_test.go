package house

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 10, 22, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 0.05 }

func TestParsePhaseMap(t *testing.T) {
	m, err := ParsePhaseMap(DefaultPhaseMapString)
	if err != nil || m[LoadHeater] != (PhaseSet{true, true, false}) || m[LoadHP] != (PhaseSet{false, false, true}) || m[LoadEV] != (PhaseSet{true, true, true}) {
		t.Fatalf("%v %v", m, err)
	}
	m, err = ParsePhaseMap("hp=L2")
	if err != nil || m[LoadHP] != (PhaseSet{false, true, false}) || m[LoadEV].Count() != 3 {
		t.Fatalf("partial map keeps defaults: %v %v", m, err)
	}
	for _, bad := range []string{"fridge=L1", "hp=L4", "hp", "hp="} {
		if _, err := ParsePhaseMap(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func fcIn() ForecastIn {
	// Heater ON 22:00-23:00 only; heat pump 1700 W 22:00-22:30 (two slots), 700 W after.
	hp := map[int64]float64{}
	for k := 0; k < 96; k++ {
		w := 700.0
		if k < 2 {
			w = 1700
		}
		hp[t0.Add(time.Duration(k)*Quarter).Unix()] = w
	}
	return ForecastIn{Now: t0, Horizon: 24 * time.Hour, BudgetA: 19, BaseA: [3]float64{3, 3, 3}, Map: DefaultPhaseMap(),
		Heater: HeaterPlan{Valid: true, NominalW: 3000, Windows: []HeaterWindow{
			{Start: t0, End: t0.Add(time.Hour), On: true}, {Start: t0.Add(time.Hour), End: t0.Add(24 * time.Hour), On: false}}},
		HP: HPPlan{Valid: true, W: hp, MaxW: 1700}}
}

func TestForecastPerPhaseHeadroom(t *testing.T) {
	f := ComputeForecast(fcIn())
	if len(f.Slots) != 96 {
		t.Fatalf("slots %d", len(f.Slots))
	}
	s0 := f.Slots[0] // heater on (7.5 A on L1,L2), hp 1700 W (7.39 A on L3)
	if !near(s0.HeadA[0], 19-3-7.5) || !near(s0.HeadA[1], 19-3-7.5) || !near(s0.HeadA[2], 19-3-7.39) {
		t.Fatalf("slot0 %v", s0.HeadA)
	}
	if h, _ := f.EVHeadroom(t0); !near(h, 8.5) { // min over L1..L3
		t.Fatalf("ev headroom %v", h)
	}
	s2 := f.Slots[2] // heater still on, hp 700 W (3.04 A)
	if !near(s2.HeadA[2], 19-3-3.04) || !near(s2.HeadA[0], 8.5) {
		t.Fatalf("slot2 %v", s2.HeadA)
	}
	s5 := f.Slots[5] // heater off after 23:00
	if s5.HeaterOn || !near(s5.HeadA[0], 16) || !near(s5.HeadA[2], 19-3-3.04) {
		t.Fatalf("slot5 %+v", s5)
	}
	if h, _ := f.EVHeadroom(t0.Add(5 * Quarter)); !near(h, 12.96) {
		t.Fatalf("ev headroom after heater: %v", h)
	}
}

func TestForecastWorstCaseWithoutProviders(t *testing.T) {
	in := fcIn()
	in.Heater = HeaterPlan{NominalW: 3000}
	in.HP = HPPlan{MaxW: 1700}
	f := ComputeForecast(in)
	for _, s := range f.Slots {
		if !s.HeaterOn || !near(s.HeadA[0], 8.5) || !near(s.HeadA[2], 19-3-7.39) {
			t.Fatalf("worst case %+v", s)
		}
	}
	// Remapped wiring: the heater moves to L2+L3, the pump to L1.
	in.Map, _ = ParsePhaseMap("heater=L2+L3;hp=L1")
	f = ComputeForecast(in)
	if !near(f.Slots[0].HeadA[0], 19-3-7.39) || !near(f.Slots[0].HeadA[1], 8.5) || !near(f.Slots[0].HeadA[2], 8.5) {
		t.Fatalf("remapped %v", f.Slots[0].HeadA)
	}
}

func TestEVCeilingFromHub(t *testing.T) {
	h := NewHub()
	if _, ok := h.EVCeilingA(t0, t0, time.Hour); ok {
		t.Fatal("no forecast yet")
	}
	h.SetForecast(ComputeForecast(fcIn()))
	if v, ok := h.EVCeilingA(t0, t0.Add(5*Quarter), time.Hour); !ok || v != 12 {
		t.Fatalf("%v %v", v, ok)
	}
	if _, ok := h.EVCeilingA(t0.Add(2*time.Hour), t0, time.Hour); ok {
		t.Fatal("stale forecast must not be used")
	}
	if _, ok := h.EVCeilingA(t0, t0.Add(48*time.Hour), time.Hour); ok {
		t.Fatal("beyond the horizon")
	}
}

func meas(at time.Time, a, b, c, ev float64) Measure {
	return Measure{At: at, PhaseA: [3]float64{a, b, c}, EVA: ev, Measured: true}
}

// run steps the machine every 5 s for d with a fixed reading, returns the last decision.
func run(o *Overload, now *time.Time, d time.Duration, f func(time.Time) Measure) Decision {
	var dec Decision
	for end := now.Add(d); now.Before(end); {
		*now = now.Add(5 * time.Second)
		dec = o.Step(*now, f(*now), true)
	}
	return dec
}

func newO() *Overload {
	return NewOverload(OverloadCfg{BudgetA: 19, Delay: 30 * time.Second, Cooldown: 15 * time.Minute, Map: DefaultPhaseMap()})
}

func TestOverloadCurtailsEVBeforeHPAndNeverHeater(t *testing.T) {
	o, now := newO(), t0
	evA := 12.0
	cap := -1
	// L3 at 22 A with the EV on 12 A: the cause is shared by the EV (all phases).
	read := func(at time.Time) Measure {
		if cap >= 0 && float64(cap) < evA {
			evA = float64(cap) // the charger obeys
		}
		return meas(at, 15, 15, 10+evA, evA)
	}
	// Under 30 s: nothing.
	d := run(o, &now, 25*time.Second, read)
	if d.State != StateOK || d.EVCapA != -1 {
		t.Fatalf("acted before the delay: %+v", d)
	}
	d = run(o, &now, 10*time.Second, read)
	if d.State != StateCurtailEV || d.EVCapA != 9 || d.HPCap != -1 { // 12 A - 3 A over = 9
		t.Fatalf("first: want EV cap 9, got %+v", d)
	}
	cap = d.EVCapA
	// Still over once the EV is at 9 (house worsened): EV steps down, still no HP.
	read2 := func(at time.Time) Measure {
		e := 9.0
		if cap >= 0 && float64(cap) < e {
			e = float64(cap)
		}
		return meas(at, 17, 17, 14+e, e) // L3 = 23 A at 9 A
	}
	for i := 0; i < 40 && d.EVCapA != 0; i++ {
		d = run(o, &now, 30*time.Second, read2)
		cap = d.EVCapA
		if d.HPCap != -1 {
			t.Fatalf("heat pump touched while the EV still ran: %+v", d)
		}
	}
	if d.EVCapA != 0 {
		t.Fatalf("EV never reached zero: %+v", d)
	}
	// EV at zero and L3 (the heat pump's phase) still over: hold, then off.
	zero := func(at time.Time) Measure { return meas(at, 10, 10, 20, 0) }
	var seen []int
	for i := 0; i < 8; i++ {
		d = run(o, &now, 35*time.Second, zero)
		if len(seen) == 0 || seen[len(seen)-1] != d.HPCap {
			seen = append(seen, d.HPCap)
		}
	}
	if len(seen) < 2 || seen[len(seen)-2] != HPHold || seen[len(seen)-1] != HPOff || d.State != StateCurtailHP {
		t.Fatalf("heat pump ladder %v %+v", seen, d)
	}
}

func TestOverloadOnHeaterPhasesOnlyDoesNotTouchHP(t *testing.T) {
	// Over on L1 only (heater phase; the pump is on L3), EV at zero: nothing to curtail.
	o, now := newO(), t0
	d := run(o, &now, 5*time.Minute, func(at time.Time) Measure { return meas(at, 21, 12, 8, 0) })
	if d.HPCap != -1 || d.EVCapA != -1 || d.State != StateOK {
		t.Fatalf("must not curtail anything: %+v", d)
	}
	// The Decision has no heater field at all: the heater cannot be curtailed by construction.
}

func TestOverloadCooldownRelease(t *testing.T) {
	o, now := newO(), t0
	zero := func(at time.Time) Measure { return meas(at, 10, 10, 20, 0) }
	d := run(o, &now, 2*time.Minute, zero)
	if d.HPCap != HPOff {
		t.Fatalf("want hp off after sustained overload: %+v", d)
	}
	ok := func(at time.Time) Measure { return meas(at, 10, 10, 10, 0) }
	d = run(o, &now, 10*time.Minute, ok)
	if d.HPCap != HPOff || d.State != StateCurtailHP {
		t.Fatalf("must hold through the cool-down: %+v", d)
	}
	d = run(o, &now, 6*time.Minute, ok)
	if d.HPCap != -1 || d.State != StateOK {
		t.Fatalf("must release after the cool-down: %+v", d)
	}
}

func TestOverloadNoData(t *testing.T) {
	o := newO()
	if d := o.Step(t0, Measure{}, false); d.State != StateNoData {
		t.Fatalf("%+v", d)
	}
	// An old reading is no data too.
	if d := o.Step(t0, meas(t0.Add(-5*time.Minute), 25, 25, 25, 10), true); d.State != StateNoData || d.EVCapA != -1 {
		t.Fatalf("%+v", d)
	}
}

func TestCapsLapseWhenPlannerStops(t *testing.T) {
	h := NewHub()
	h.SetCaps(t0, 0, HPHold)
	if c, ok := h.EVCap(t0.Add(time.Minute)); !ok || c != 0 {
		t.Fatal("cap lost")
	}
	if _, ok := h.EVCap(t0.Add(3 * time.Minute)); ok {
		t.Fatal("a cap not refreshed must lapse")
	}
	if _, ok := h.HPCap(t0.Add(3 * time.Minute)); ok {
		t.Fatal("hp cap must lapse")
	}
}
