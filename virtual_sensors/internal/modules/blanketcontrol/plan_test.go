package blanketcontrol

import (
	"math"
	"testing"
	"time"
)

var sto, _ = time.LoadLocation("Europe/Stockholm")

// at builds a Stockholm local time on 2026-11-day.
func at(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, sto) }

func prices(from time.Time, hours int, f func(h int) float64) map[int64]float64 {
	out := map[int64]float64{}
	for t := from.Truncate(quarter); t.Before(from.Add(time.Duration(hours) * time.Hour)); t = t.Add(quarter) {
		out[t.Unix()] = f(t.In(sto).Hour())
	}
	return out
}

func coldInput(now time.Time, t0, tout float64, pr map[int64]float64) Input {
	return Input{Now: now, T0: t0, Tout: tout, Prices: pr}
}

// A fitted-like model: equilibrium with the blanket is Tout + H/U = +4 K.
func testParams() Params {
	p := DefaultParams()
	p.Model.U, p.Model.Boost5 = 0.10, 0.40
	return p
}

func TestColdNightShiftsHeatToCheapHours(t *testing.T) {
	p := testParams()
	p.StartPenaltyKr = 0.02 // 70 W is a few ore per hour: a 0.30 kr start would swamp any shift
	now := at(26, 12, 0)
	// expensive 17-21, cheap 01-05, else medium
	pr := prices(now, 36, func(h int) float64 {
		switch {
		case h >= 17 && h < 21:
			return 4
		case h >= 1 && h < 5:
			return 0.2
		}
		return 1.5
	})
	in := coldInput(now, 6.5, 3, pr)
	r := Plan(in, p)
	if r.NoPrices || len(r.Slots) < 96 {
		t.Fatalf("plan %+v", r.Slots)
	}
	var exp, cheap float64
	for _, s := range r.Slots[:96] {
		h := s.Start.In(sto).Hour()
		if s.On && h >= 17 && h < 21 {
			exp += s.KWh
		}
		if s.On && h >= 1 && h < 5 {
			cheap += s.KWh
		}
	}
	if cheap <= 0 || exp > cheap/2 {
		t.Fatalf("heat not shifted: cheap %.2f kWh, expensive %.2f kWh", cheap, exp)
	}
	if r.MinT < p.FloorC-0.05 {
		t.Fatalf("floor broken: min %.2f", r.MinT)
	}
	if r.FloorAtRisk {
		t.Fatal("floor at risk flagged on a feasible night")
	}
}

func TestPlanNeverHeatsAboveMaxAndBackstopDoesNotOverrideCap(t *testing.T) {
	p := testParams()
	now := at(26, 12, 0)
	pr := prices(now, 36, func(h int) float64 { return 0.01 }) // free heat
	for _, t0 := range []float64{8.0, 8.4, 9.5} {
		r := Plan(coldInput(now, t0, 7, pr), p)
		for _, s := range r.Slots {
			if s.On && s.T >= p.MaxC {
				t.Fatalf("t0 %.1f: planned heat at %.2f >= max", t0, s.T)
			}
		}
	}
	// even with a cold, expensive forecast the planned temperature never goes above the cap because of heating
	r := Plan(coldInput(now, 7.9, -5, pr), p)
	for _, s := range r.Slots {
		if s.TEnd > p.MaxC+1e-9 && s.On {
			t.Fatalf("heating pushed %.2f above max", s.TEnd)
		}
	}
}

func TestStartCapAndMinOnRespected(t *testing.T) {
	p := testParams()
	p.MaxStartsDay = 3
	now := at(26, 12, 0)
	// alternating price every hour tempts many short blocks
	pr := prices(now, 36, func(h int) float64 {
		if h%2 == 0 {
			return 0.1
		}
		return 3
	})
	r := Plan(coldInput(now, 6.0, 0, pr), p)
	for d, n := range r.Starts {
		if n > p.MaxStartsDay {
			t.Fatalf("day %d: %d starts > cap", d, n)
		}
	}
	// min-on: every block (between starts and offs) lasts >= MinOnSlots, except a block cut by the horizon
	run := 0
	for i, s := range r.Slots {
		if s.On {
			run++
			continue
		}
		if run > 0 && run < p.MinOnSlots {
			t.Fatalf("on-block of %d slots ending at %d", run, i)
		}
		run = 0
	}
	// starts already used today shrink today's budget
	in := coldInput(now, 6.0, 0, pr)
	in.StartsToday = 2
	r2 := Plan(in, p)
	if r2.Starts[0] > 1 {
		t.Fatalf("starts today %d with 2 already used (cap 3)", r2.Starts[0])
	}
}

func TestRefreshCountedInLongBlock(t *testing.T) {
	p := testParams()
	p.Model.Boost5 = 0.1 // too weak to hold the floor: plan heats continuously
	now := at(26, 12, 0)
	pr := prices(now, 36, func(int) float64 { return 1 })
	r := Plan(coldInput(now, 5.5, -10, pr), p)
	tot := r.Starts[0] + r.Starts[1] + r.Starts[2]
	if tot < 36*60/170-1 { // a start plus a refresh every 2 h 45
		t.Fatalf("refreshes not counted: %d starts over %d slots", tot, len(r.Slots))
	}
}

func TestNoPrices(t *testing.T) {
	if r := Plan(Input{Now: at(26, 12, 0), T0: 6, Tout: 0}, testParams()); !r.NoPrices {
		t.Fatal("want NoPrices")
	}
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestFitRecoversModelAndPriorHoldsWithLittleData(t *testing.T) {
	def := DefaultModel()
	if f := FitModel(nil, def); f.Fitted || f.U != def.U || f.H != def.Boost5 {
		t.Fatalf("empty fit %+v", f)
	}
	// synthetic truth U .12, H .5, G .02 with a square-wave draw and a swinging outdoor temp
	U, H, G := 0.12, 0.5, 0.02
	var recs []Rec
	T := 6.0
	t0 := at(20, 0, 0)
	for i := 0; i < 300; i++ {
		to := -2 + 4*math.Sin(float64(i)/15)
		u := 0.0
		if (i/12)%3 == 0 {
			u = 1
		}
		T += 0.25 * (-U*(T-to) + H*u + G)
		recs = append(recs, Rec{T: t0.Add(time.Duration(i) * quarter).Unix(), Tm: T, To: to, U: u})
	}
	f := FitModel(recs, def)
	if !f.Fitted || !near(f.U, U, 0.03) || !near(f.H, H, 0.15) || f.MAE > 0.1 {
		t.Fatalf("fit %+v", f)
	}
	if g := FitModel(recs[:20], def); g.Fitted {
		t.Fatalf("20 rows should not fit: %+v", g)
	}
}
