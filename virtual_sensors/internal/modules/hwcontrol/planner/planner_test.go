package planner

import (
	"encoding/json"
	"testing"
	"time"

	"virtual_sensors/internal/modules/hwcontrol/core"
)

var sto, _ = time.LoadLocation("Europe/Stockholm")

// day builds quarter prices for one local day from an hourly shape
// (SEK/kWh), replicated to quarters.
func day(date time.Time, hourly func(h int) float64) []Price {
	mid := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, sto)
	next := mid.AddDate(0, 0, 1)
	var out []Price
	for t := mid; t.Before(next); t = t.Add(quarter) {
		out = append(out, Price{Start: t, Dur: quarter, SEK: hourly(t.In(sto).Hour())})
	}
	return out
}

func checkPlan(t *testing.T, p *core.Plan, now time.Time) {
	t.Helper()
	if !p.Valid(now) && len(p.OffPeriods) > 0 {
		t.Fatalf("planner produced an invalid plan: %+v", p)
	}
	for i, op := range p.OffPeriods {
		if op.End.Sub(op.Start) > 3*time.Hour || op.End.Sub(op.Start) < 30*time.Minute {
			t.Fatalf("period %d length %v", i, op.End.Sub(op.Start))
		}
		if i > 0 && op.Start.Sub(p.OffPeriods[i-1].End) < 2*time.Hour {
			t.Fatalf("periods %d and %d closer than 2 h", i-1, i)
		}
	}
}

func TestFlatCurveNoPeriods(t *testing.T) {
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, sto)
	prices := append(day(now, func(int) float64 { return 0.8 }), day(now.AddDate(0, 0, 1), func(int) float64 { return 0.8 })...)
	p := Plan(now, prices, DefaultConfig(), core.Facts{})
	if len(p.OffPeriods) != 0 {
		t.Fatalf("flat curve gave %v", p.OffPeriods)
	}
}

func TestWinterEveningPeak(t *testing.T) {
	now := time.Date(2026, 12, 10, 12, 0, 0, 0, sto)
	shape := func(h int) float64 {
		if h >= 17 && h < 20 {
			return 3.0
		}
		if h >= 22 || h < 5 {
			return 0.4
		}
		return 1.0
	}
	prices := append(day(now, shape), day(now.AddDate(0, 0, 1), shape)...)
	p := Plan(now, prices, DefaultConfig(), core.Facts{})
	checkPlan(t, p, now)
	if len(p.OffPeriods) == 0 {
		t.Fatal("no period over a 3 SEK evening peak")
	}
	first := p.OffPeriods[0]
	if h := first.Start.In(sto).Hour(); h < 17 || h >= 20 {
		t.Fatalf("first period starts %v, want inside the 17-20 peak", first.Start.In(sto))
	}
}

func TestSummerMiddayTrough(t *testing.T) {
	now := time.Date(2026, 7, 10, 6, 0, 0, 0, sto)
	shape := func(h int) float64 {
		switch {
		case h >= 11 && h < 15:
			return -0.05 // negative midday
		case h >= 7 && h < 10:
			return 1.5
		}
		return 0.7
	}
	prices := append(day(now, shape), day(now.AddDate(0, 0, 1), shape)...)
	p := Plan(now, prices, DefaultConfig(), core.Facts{})
	checkPlan(t, p, now)
	if len(p.OffPeriods) == 0 {
		t.Fatal("no period before the midday trough")
	}
	for _, op := range p.OffPeriods {
		if h := op.Start.In(sto).Hour(); h >= 11 && h < 15 {
			t.Fatalf("permitted OFF inside the negative-price trough: %v", op.Start.In(sto))
		}
	}
}

func TestMissingTomorrowAndUnknownQuarters(t *testing.T) {
	now := time.Date(2026, 11, 10, 20, 0, 0, 0, sto)
	prices := day(now, func(h int) float64 {
		if h == 21 {
			return 3.0
		}
		return 0.5
	})
	p := Plan(now, prices, DefaultConfig(), core.Facts{})
	checkPlan(t, p, now)
	for _, op := range p.OffPeriods {
		if op.End.After(p.ValidUntil) {
			t.Fatalf("period beyond known prices: %+v (valid until %v)", op, p.ValidUntil)
		}
	}
	if p.ValidUntil.After(time.Date(2026, 11, 11, 0, 0, 0, 0, sto)) {
		t.Fatalf("valid until %v beyond the known prices", p.ValidUntil)
	}
	// No prices at all: no plan.
	if q := Plan(now, nil, DefaultConfig(), core.Facts{}); q.Valid(now) {
		t.Fatal("empty prices gave a valid plan")
	}
}

func TestAdderInvariance(t *testing.T) {
	now := time.Date(2026, 12, 10, 12, 0, 0, 0, sto)
	shape := func(h int) float64 {
		if h >= 17 && h < 20 {
			return 3.0
		}
		return 0.6
	}
	prices := append(day(now, shape), day(now.AddDate(0, 0, 1), shape)...)
	a := DefaultConfig()
	b := DefaultConfig()
	b.AdderOre = 80
	pa, pb := Plan(now, prices, a, core.Facts{}), Plan(now, prices, b, core.Facts{})
	if len(pa.OffPeriods) != len(pb.OffPeriods) {
		t.Fatal("adder changed the plan")
	}
	for i := range pa.OffPeriods {
		if !pa.OffPeriods[i].Start.Equal(pb.OffPeriods[i].Start) || !pa.OffPeriods[i].End.Equal(pb.OffPeriods[i].End) {
			t.Fatal("adder changed the plan")
		}
	}
}

func TestMaxBlocksPer24h(t *testing.T) {
	now := time.Date(2026, 12, 10, 0, 0, 0, 0, sto)
	shape := func(h int) float64 {
		if h == 7 || h == 12 || h == 18 {
			return 3.0
		}
		return 0.5
	}
	prices := day(now, shape)
	cfg := DefaultConfig()
	cfg.MaxBlocksPer24h = 1
	p := Plan(now, prices, cfg, core.Facts{})
	if len(p.OffPeriods) != 1 {
		t.Fatalf("periods %d with max 1 per 24 h", len(p.OffPeriods))
	}
	cfg.MaxBlocksPer24h = 2
	if p := Plan(now, prices, cfg, core.Facts{}); len(p.OffPeriods) != 2 {
		t.Fatalf("periods %d with max 2 per 24 h", len(p.OffPeriods))
	}
}

func TestParseNordpoolDSTAndHourly(t *testing.T) {
	// 2026-10-25 is the autumn DST day in Sweden: 25 hours, 100 quarters.
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, sto)
	vals := make([]float64, 100)
	for i := range vals {
		vals[i] = float64(i)
	}
	b, _ := json.Marshal(vals)
	ps := ParseNordpool(map[string]json.RawMessage{"today": b, "tomorrow_valid": json.RawMessage("false")}, now, sto)
	if len(ps) != 100 {
		t.Fatalf("100-quarter DST day parsed to %d", len(ps))
	}
	if d := ps[99].Start.Add(quarter).Sub(ps[0].Start); d != 25*time.Hour {
		t.Fatalf("DST day spans %v", d)
	}
	// Spring DST (2026-03-29): 92 quarters.
	spring := time.Date(2026, 3, 29, 1, 0, 0, 0, sto)
	b92, _ := json.Marshal(make([]float64, 92))
	if ps := ParseNordpool(map[string]json.RawMessage{"today": b92}, spring, sto); len(ps) != 92 {
		t.Fatalf("92-quarter day parsed to %d", len(ps))
	}
	// A wrong length is unknown, not guessed.
	if ps := ParseNordpool(map[string]json.RawMessage{"today": b92}, now, sto); len(ps) != 0 {
		t.Fatal("mismatched length accepted")
	}
	// Hourly (pre-Oct-2025) replicates to quarters in the planner.
	b24, _ := json.Marshal(make([]float64, 24))
	ps = ParseNordpool(map[string]json.RawMessage{"today": b24}, time.Date(2025, 6, 1, 1, 0, 0, 0, sto), sto)
	if len(ps) != 24 || len(quarters(ps)) != 96 {
		t.Fatalf("hourly: %d prices, %d quarters", len(ps), len(quarters(ps)))
	}
	// tomorrow ignored when tomorrow_valid is false.
	b96, _ := json.Marshal(make([]float64, 96))
	nov := time.Date(2026, 11, 10, 14, 0, 0, 0, sto)
	if ps := ParseNordpool(map[string]json.RawMessage{"today": b96, "tomorrow": b96, "tomorrow_valid": json.RawMessage("false")}, nov, sto); len(ps) != 96 {
		t.Fatalf("tomorrow used despite tomorrow_valid=false: %d", len(ps))
	}
	// raw_today preferred.
	raw := `[{"start":"2026-11-10T00:00:00+01:00","end":"2026-11-10T00:15:00+01:00","value":1.5}]`
	if ps := ParseNordpool(map[string]json.RawMessage{"raw_today": json.RawMessage(raw), "today": b96}, nov, sto); len(ps) != 1 || ps[0].SEK != 1.5 {
		t.Fatalf("raw_today not preferred: %+v", ps)
	}
}

// Review fix 4: a running period is kept exactly as started by rolling
// re-plans, even when a different block would now score higher.
func TestRunningPeriodPinned(t *testing.T) {
	now := time.Date(2026, 12, 10, 17, 20, 0, 0, sto)
	shape := func(h int) float64 {
		if h >= 17 && h < 20 {
			return 3.0
		}
		return 0.4
	}
	prices := append(day(now, shape), day(now.AddDate(0, 0, 1), shape)...)
	running := core.OffPeriod{Start: time.Date(2026, 12, 10, 17, 0, 0, 0, sto), End: time.Date(2026, 12, 10, 17, 45, 0, 0, sto)}
	p := Plan(now, prices, DefaultConfig(), core.Facts{CurrentOff: &running})
	checkPlan(t, p, now)
	got, ok := p.PeriodAt(now)
	if !ok || !got.Start.Equal(running.Start) || !got.End.Equal(running.End) {
		t.Fatalf("running period not kept: %+v (plan %+v)", got, p.OffPeriods)
	}
	for _, op := range p.OffPeriods {
		if op.Start.Before(running.End.Add(2*time.Hour)) && !op.Start.Equal(running.Start) {
			t.Fatalf("a new period within 2 h of the running one: %+v", op)
		}
	}
}
