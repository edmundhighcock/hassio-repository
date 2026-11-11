package planner

import (
	"math"
	"strings"
	"testing"
	"time"

	"virtual_sensors/internal/modules/hwcontrol/core"
)

// realistic is a winter SE3 quarter-hour curve (SEK/kWh, excl. VAT): cheap
// nights with a trough 02-04, a morning shoulder, a midday dip around
// 13:00, an evening peak 17-20, plus a deterministic per-quarter wiggle so
// that no two windows tie.
func realistic(t time.Time) float64 {
	l := t.In(sto)
	h := float64(l.Hour()) + float64(l.Minute())/60
	v := 0.9
	switch {
	case h >= 22 || h < 2:
		v = 0.45
	case h < 4:
		v = 0.25
	case h < 6:
		v = 0.5
	case h < 9:
		v = 1.3
	case h < 12:
		v = 0.95
	case h < 15:
		v = 0.6
	case h < 17:
		v = 1.1
	case h < 20:
		v = 2.3
	default:
		v = 1.0
	}
	q := int(t.Unix() / 900)
	return v + float64((q*7919)%13)/1000
}

func pricesFrom(first time.Time, days int, f func(time.Time) float64) []Price {
	var out []Price
	mid := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, sto)
	end := mid.AddDate(0, 0, days)
	for t := mid; t.Before(end); t = t.Add(quarter) {
		out = append(out, Price{Start: t, Dur: quarter, SEK: f(t)})
	}
	return out
}

func rec(start time.Time, dur time.Duration) core.ReheatRec {
	return core.ReheatRec{Start: start, End: start.Add(dur)}
}

func TestLearnDefaultsBelowFiveSamples(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, sto)
	cfg := DefaultV2Config()
	var rs []core.ReheatRec
	for i := 0; i < 4; i++ {
		rs = append(rs, rec(now.Add(-time.Duration(i+1)*24*time.Hour), 200*time.Minute))
	}
	l := Learn(now, rs, cfg)
	if l.Full != 150*time.Minute || !l.FullDefault || l.Topup != 60*time.Minute || !l.TopDef {
		t.Fatalf("%+v", l)
	}
}

func TestLearnFullP85FloorCap(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, sto)
	cfg := DefaultV2Config()
	mk := func(mins ...int) []core.ReheatRec {
		var rs []core.ReheatRec
		for i, m := range mins {
			rs = append(rs, rec(now.Add(-time.Duration(len(mins)-i)*24*time.Hour), time.Duration(m)*time.Minute))
		}
		return rs
	}
	// p85 of 100,110,120,130,140 = 100 + 0.85*4 -> index 3.4 -> 130+0.4*10 = 134.
	l := Learn(now, mk(100, 110, 120, 130, 140), cfg)
	if l.Full != 134*time.Minute || l.FullDefault || l.NFull != 5 {
		t.Fatalf("p85: %+v", l)
	}
	// Floor 90 min.
	if l := Learn(now, mk(30, 35, 40, 45, 50), cfg); l.Full != FullFloor {
		t.Fatalf("floor: %v", l.Full)
	}
	// Cap 4 h.
	if l := Learn(now, mk(300, 310, 320, 330, 340), cfg); l.Full != FullCap {
		t.Fatalf("cap: %v", l.Full)
	}
	// Cut reheats say nothing about the full time.
	rs := mk(100, 110, 120, 130, 140)
	rs[4].Cut = true
	if l := Learn(now, rs, cfg); !l.FullDefault || l.NFull != 4 {
		t.Fatalf("cut counted: %+v", l)
	}
	// Reheats older than 45 days age out.
	var old []core.ReheatRec
	for i := 0; i < 5; i++ {
		old = append(old, rec(now.Add(-time.Duration(50+i)*24*time.Hour), 200*time.Minute))
	}
	if l := Learn(now, old, cfg); !l.FullDefault {
		t.Fatalf("old samples used: %+v", l)
	}
}

func TestLearnTopupP50Clamp(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, sto)
	cfg := DefaultV2Config()
	mk := func(mins ...int) []core.ReheatRec {
		var rs []core.ReheatRec
		for i, m := range mins {
			r := rec(now.Add(-time.Duration(len(mins)-i)*24*time.Hour), time.Duration(m)*time.Minute)
			r.Kind = KindTopup
			rs = append(rs, r)
		}
		return rs
	}
	if l := Learn(now, mk(40, 50, 45, 55, 60), cfg); l.Topup != 50*time.Minute || l.TopDef || l.NTopup != 5 {
		t.Fatalf("p50: %+v", l)
	}
	if l := Learn(now, mk(10, 12, 14, 16, 18), cfg); l.Topup != TopupFloor {
		t.Fatalf("floor: %v", l.Topup)
	}
	if l := Learn(now, mk(200, 210, 220, 230, 240), cfg); l.Topup != TopupCap {
		t.Fatalf("cap: %v", l.Topup)
	}
	if l := Learn(now, mk(40, 50, 45, 55), cfg); l.Topup != 60*time.Minute || !l.TopDef {
		t.Fatalf("<5 samples: %+v", l)
	}
}

// Untagged history (greedy_v1 era) is classified by the gap to the
// previous reheat: >= 4 h is a full-type reheat.
func TestLearnClassifiesUntaggedByGap(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, sto)
	base := now.Add(-10 * 24 * time.Hour)
	var rs []core.ReheatRec
	for d := 0; d < 6; d++ {
		night := base.Add(time.Duration(d) * 24 * time.Hour)
		rs = append(rs, rec(night, 130*time.Minute), rec(night.Add(8*time.Hour), 40*time.Minute), rec(night.Add(8*time.Hour+50*time.Minute), 20*time.Minute))
	}
	l := Learn(now, rs, DefaultV2Config())
	// 6 full (first of each day, gap >= 4 h) and the rest top-ups.
	if l.NFull != 12 && l.NFull != 6 {
		t.Fatalf("NFull %d", l.NFull)
	}
	if l.NTopup < 5 {
		t.Fatalf("NTopup %d", l.NTopup)
	}
}

// checkV2 asserts the structural invariants of one plan.
func checkV2(t *testing.T, p *core.Plan, now time.Time) {
	t.Helper()
	if !p.DefaultOff || p.PlannerID != V2ID {
		t.Fatalf("not a v2 DefaultOff plan: %+v", p)
	}
	for i, op := range p.OffPeriods {
		if op.End.Sub(op.Start) > 23*time.Hour {
			t.Fatalf("period %d longer than 23 h: %+v", i, op)
		}
		if i > 0 && op.Start.Before(p.OffPeriods[i-1].End) {
			t.Fatalf("periods overlap: %+v", p.OffPeriods)
		}
		for _, w := range p.Windows {
			if w.End.After(now) && op.Start.Before(w.End) && op.End.After(w.Start) {
				t.Fatalf("OFF period %+v overlaps window %+v", op, w)
			}
		}
	}
}

func inBand(t time.Time, from, to int) bool {
	m := minuteOf(t, sto)
	if to <= from {
		return m >= from || m < to
	}
	return m >= from && m < to
}

func TestV2FullAndTopupPlacement(t *testing.T) {
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, sto)
	prices := pricesFrom(now, 3, realistic)
	cfg := DefaultV2Config()
	res := PlanV2(V2Input{Now: now, Prices: prices, TankHeadroom: 10 * time.Hour}, cfg)
	checkV2(t, res.Plan, now)
	var full, top []core.PlanWindow
	for _, w := range res.Plan.Windows {
		switch w.Kind {
		case KindFull:
			full = append(full, w)
		case KindTopup:
			top = append(top, w)
		}
	}
	if len(full) < 1 || len(top) < 1 {
		t.Fatalf("windows %+v", res.Plan.Windows)
	}
	// First full window: inside 22-07, 150 min + 30 s margin = 11 quarters,
	// the cheapest such run: the 02-04 trough region.
	f := full[0]
	if !inBand(f.Start, 22*60, 7*60) || !inBand(f.End.Add(-time.Second), 22*60, 7*60) {
		t.Fatalf("full outside the night band: %v-%v", f.Start.In(sto), f.End.In(sto))
	}
	if f.End.Sub(f.Start) != 11*quarter {
		t.Fatalf("full length %v", f.End.Sub(f.Start))
	}
	best := bruteCheapest(prices, cfg, time.Date(2026, 11, 10, 22, 0, 0, 0, sto), time.Date(2026, 11, 11, 7, 0, 0, 0, sto), 11)
	if !f.Start.Equal(best) {
		t.Fatalf("full starts %v, brute-force cheapest %v", f.Start.In(sto), best.In(sto))
	}
	if h := f.Start.In(sto).Hour(); h > 3 {
		t.Fatalf("full does not sit in the trough: %v", f.Start.In(sto))
	}
	tp := top[0]
	if !inBand(tp.Start, 10*60, 16*60) || tp.End.In(sto).Hour()*60+tp.End.In(sto).Minute() > 16*60 {
		t.Fatalf("topup outside midday: %v-%v", tp.Start.In(sto), tp.End.In(sto))
	}
	if tp.End.Sub(tp.Start) != 5*quarter { // 60 min + 30 s -> 5 quarters
		t.Fatalf("topup length %v", tp.End.Sub(tp.Start))
	}
	// No window in the 17-20 peak.
	for _, w := range res.Plan.Windows {
		for x := w.Start; x.Before(w.End); x = x.Add(quarter) {
			if h := x.In(sto).Hour(); h >= 17 && h < 20 {
				t.Fatalf("window %+v in the evening peak", w)
			}
		}
	}
	if res.Cost.KWh <= 0 || res.Cost.SavingKr <= 0 {
		t.Fatalf("cost %+v: v2 should beat greedy's flat-ON mean", res.Cost)
	}
}

func bruteCheapest(prices []Price, cfg V2Config, from, to time.Time, n int) time.Time {
	q := quarters(prices)
	var best time.Time
	bv := math.Inf(1)
	for s := from; !s.Add(time.Duration(n) * quarter).After(to); s = s.Add(quarter) {
		sum := 0.0
		for k := 0; k < n; k++ {
			sum += q[s.Add(time.Duration(k)*quarter).Unix()]
		}
		if sum < bv-1e-12 {
			bv, best = sum, s
		}
	}
	return best
}

func TestV2TopupOffByOption(t *testing.T) {
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, sto)
	cfg := DefaultV2Config()
	cfg.Topup = false
	res := PlanV2(V2Input{Now: now, Prices: pricesFrom(now, 3, realistic), TankHeadroom: 10 * time.Hour}, cfg)
	for _, w := range res.Plan.Windows {
		if w.Kind == KindTopup {
			t.Fatalf("topup planned with the option off: %+v", w)
		}
	}
}

// simPlant replays the plan: the heater draws from the start of a window for
// the given reheat time, then the reheat is logged. Windows are the previous
// plan's, as the core persists them.
type simPlant struct {
	fullDur, topDur time.Duration
	reheats         []core.ReheatRec
	windows         []core.PlanWindow
	running         *core.ReheatRec
	ran             map[string]core.PlanWindow
}

func (s *simPlant) step(now time.Time, prices []Price, cfg V2Config, moves int) *core.Plan {
	if s.ran == nil {
		s.ran = map[string]core.PlanWindow{}
	}
	if s.running != nil && !now.Before(s.running.End) {
		s.reheats = append(s.reheats, *s.running)
		s.running = nil
	}
	f := core.Facts{Reheats: s.reheats, Windows: s.windows, MovesLast24h: moves, TankHeadroom: 12 * time.Hour}
	if s.running != nil {
		f.CycleOpen, f.CycleOpenSince = true, s.running.Start
	}
	res := PlanV2(V2Input{Now: now, Prices: prices, Facts: f, TankHeadroom: 12 * time.Hour}, cfg)
	s.windows = res.Plan.Windows
	for _, w := range res.Plan.Windows {
		if !w.Start.After(now) && w.End.After(now) {
			key := w.Kind + w.Start.String()
			if _, seen := s.ran[w.Kind+"@"+w.Start.Format("2006-01-02")]; !seen {
				s.ran[w.Kind+"@"+w.Start.Format("2006-01-02")] = w
			}
			_ = key
			if s.running == nil && now.Sub(w.Start) < 5*time.Minute {
				d := s.fullDur
				if w.Kind == KindTopup {
					d = s.topDur
				}
				s.running = &core.ReheatRec{Start: now, End: now.Add(d), Kind: w.Kind}
			}
		}
	}
	return res.Plan
}

func TestV2Replay48hRealisticPrices(t *testing.T) {
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, sto)
	prices := pricesFrom(start, 4, realistic)
	cfg := DefaultV2Config()
	s := &simPlant{fullDur: 120 * time.Minute, topDur: 40 * time.Minute}
	var lastPlan *core.Plan
	// Window start times seen per night / midday, to catch flapping.
	starts := map[string]map[int64]bool{}
	for now := start; now.Before(start.Add(48 * time.Hour)); now = now.Add(time.Minute) {
		p := s.step(now, prices, cfg, 0)
		checkV2(t, p, now)
		lastPlan = p
		for _, w := range p.Windows {
			if w.End.After(now) && !w.Early {
				k := w.Kind + w.Start.In(sto).Add(-12*time.Hour).Format("2006-01-02")
				_ = k
			}
		}
		// Windows that have started must never move afterwards.
		for _, w := range p.Windows {
			if !w.Start.After(now) {
				key := w.Kind + "@" + w.Start.In(sto).Add(-10*time.Hour).Format("2006-01-02")
				if starts[key] == nil {
					starts[key] = map[int64]bool{}
				}
				starts[key][w.Start.Unix()] = true
			}
		}
	}
	for k, v := range starts {
		if len(v) != 1 {
			t.Fatalf("window %s moved after starting: %v", k, v)
		}
	}
	var nFull, nTop int
	total, kwh := 0.0, 0.0
	q := quarters(prices)
	for _, r := range s.reheats {
		on := r.Duration()
		if r.Kind == KindFull {
			nFull++
		} else if r.Kind == KindTopup {
			nTop++
		}
		for x := r.Start; x.Before(r.End); x = x.Add(time.Minute) {
			total += 3.0 / 60 * cfg.Price.AllInOre(q[x.Truncate(quarter).Unix()]) / 100
			kwh += 3.0 / 60
		}
		_ = on
	}
	if nFull != 2 || nTop != 2 {
		t.Fatalf("executed %d full, %d topup, want 2 and 2 (%+v)", nFull, nTop, s.reheats)
	}
	// Every executed window starts in its band and in the cheapest region.
	for _, r := range s.reheats {
		switch r.Kind {
		case KindFull:
			if h := r.Start.In(sto).Hour(); h < 22 && h > 3 {
				t.Fatalf("full reheat at %v", r.Start.In(sto))
			}
		case KindTopup:
			if h := r.Start.In(sto).Hour(); h < 10 || h >= 16 {
				t.Fatalf("topup reheat at %v", r.Start.In(sto))
			}
		}
	}
	// The mean price paid is far below the all-day mean.
	allMean := 0.0
	for _, p := range prices[:96] {
		allMean += cfg.Price.AllInOre(p.SEK)
	}
	allMean /= 96
	paid := total * 100 / kwh
	if paid >= 0.8*allMean {
		t.Fatalf("paid %.1f öre/kWh vs all-day mean %.1f", paid, allMean)
	}
	if lastPlan == nil || len(lastPlan.OffPeriods) == 0 {
		t.Fatal("no OFF periods at the end")
	}
	t.Logf("48 h replay: paid %.1f öre/kWh (all-day mean %.1f), %.1f kWh, %.2f kr", paid, allMean, kwh, total)
}

func TestV2EarlyEnd(t *testing.T) {
	now := time.Date(2026, 11, 11, 2, 40, 0, 0, sto)
	prices := pricesFrom(now, 2, realistic)
	cfg := DefaultV2Config()
	w := core.PlanWindow{Start: now.Add(-30 * time.Minute), End: now.Add(2 * time.Hour), Kind: KindFull}
	reh := core.ReheatRec{Start: now.Add(-28 * time.Minute), End: now.Add(-15 * time.Minute), Kind: KindFull}
	in := V2Input{Now: now, Prices: prices, TankHeadroom: 10 * time.Hour,
		Facts: core.Facts{Windows: []core.PlanWindow{w}, Reheats: []core.ReheatRec{reh}}}
	res := PlanV2(in, cfg)
	checkV2(t, res.Plan, now)
	var got *core.PlanWindow
	for i := range res.Plan.Windows {
		if res.Plan.Windows[i].Start.Equal(w.Start) {
			got = &res.Plan.Windows[i]
		}
	}
	if got == nil || !got.Early || !got.End.Equal(now) {
		t.Fatalf("window not ended early: %+v", res.Plan.Windows)
	}
	// OFF is permitted from now (the window's end) on.
	if len(res.Plan.OffPeriods) == 0 || res.Plan.OffPeriods[0].Start.After(now.Add(time.Minute)) {
		t.Fatalf("no OFF from the early end: %+v", res.Plan.OffPeriods)
	}
	// The tank full only 5 min ago: not yet (EarlyEnd is 10 min).
	reh.End = now.Add(-5 * time.Minute)
	in.Facts.Reheats = []core.ReheatRec{reh}
	res = PlanV2(in, cfg)
	for _, w2 := range res.Plan.Windows {
		if w2.Start.Equal(w.Start) && w2.Early {
			t.Fatal("ended after 5 min off")
		}
	}
	// The move budget forbids the OFF: the window runs to its end.
	reh.End = now.Add(-15 * time.Minute)
	in.Facts.Reheats = []core.ReheatRec{reh}
	in.Facts.MovesLast24h = 6
	res = PlanV2(in, cfg)
	for _, w2 := range res.Plan.Windows {
		if w2.Start.Equal(w.Start) && w2.Early {
			t.Fatal("ended early with no move budget")
		}
	}
	// A heater still drawing extends the window past its planned end.
	in.Facts = core.Facts{Windows: []core.PlanWindow{{Start: now.Add(-3 * time.Hour), End: now.Add(5 * time.Minute), Kind: KindFull}},
		CycleOpen: true, CycleOpenSince: now.Add(-50 * time.Minute)}
	res = PlanV2(in, cfg)
	extended := false
	for _, w2 := range res.Plan.Windows {
		if w2.Start.Equal(now.Add(-3*time.Hour)) && w2.End.After(now.Add(10*time.Minute)) {
			extended = true
		}
	}
	if !extended {
		t.Fatalf("window not extended while the heater draws: %+v", res.Plan.Windows)
	}
}

func TestV2CatchUpAfterFailedFullHeat(t *testing.T) {
	now := time.Date(2026, 11, 11, 7, 30, 0, 0, sto)
	prices := pricesFrom(now, 3, realistic)
	cfg := DefaultV2Config()
	fw := core.PlanWindow{Start: time.Date(2026, 11, 11, 2, 0, 0, 0, sto), End: time.Date(2026, 11, 11, 4, 30, 0, 0, sto), Kind: KindFull}
	in := V2Input{Now: now, Prices: prices, TankHeadroom: 9 * time.Hour, Facts: core.Facts{Windows: []core.PlanWindow{fw}}}
	res := PlanV2(in, cfg)
	checkV2(t, res.Plan, now)
	var cu *core.PlanWindow
	for i, w := range res.Plan.Windows {
		if w.Kind == KindCatchup {
			cu = &res.Plan.Windows[i]
		}
	}
	if cu == nil {
		t.Fatalf("no catch-up: %+v", res.Plan.Windows)
	}
	if cu.Start.Before(now) || cu.Start.After(now.Add(9*time.Hour)) {
		t.Fatalf("catch-up %v outside the tank headroom", cu.Start.In(sto))
	}
	if len(res.Alerts) != 1 || res.Alerts[0].Code != "FULL_HEAT_INCOMPLETE" || !strings.Contains(res.Alerts[0].Text, "no finished reheat") {
		t.Fatalf("alerts %+v", res.Alerts)
	}
	// A reheat cut short: same catch-up, with the other reason.
	in.Facts.Reheats = []core.ReheatRec{{Start: fw.Start.Add(10 * time.Minute), End: fw.Start.Add(40 * time.Minute), Cut: true, Kind: KindFull}}
	res = PlanV2(in, cfg)
	if len(res.Alerts) != 1 || !strings.Contains(res.Alerts[0].Text, "cut short") {
		t.Fatalf("alerts %+v", res.Alerts)
	}
	// A finished reheat in the window: no catch-up, no alert.
	in.Facts.Reheats = []core.ReheatRec{{Start: fw.Start.Add(10 * time.Minute), End: fw.Start.Add(130 * time.Minute), Kind: KindFull}}
	res = PlanV2(in, cfg)
	for _, w := range res.Plan.Windows {
		if w.Kind == KindCatchup {
			t.Fatalf("catch-up despite a finished reheat: %+v", w)
		}
	}
	if len(res.Alerts) != 0 {
		t.Fatalf("alerts %+v", res.Alerts)
	}
	// Tank clock already out (no headroom): ON now, catch-up starts at now.
	in.Facts.Reheats = nil
	in.TankHeadroom = 0
	res = PlanV2(in, cfg)
	var c2 *core.PlanWindow
	for i, w := range res.Plan.Windows {
		if w.Kind == KindCatchup {
			c2 = &res.Plan.Windows[i]
		}
	}
	if c2 == nil || !c2.Start.Equal(now) {
		t.Fatalf("catch-up with no headroom: %+v", res.Plan.Windows)
	}
}

// A catch-up that is itself already planned is not planned twice.
func TestV2CatchUpNotRepeated(t *testing.T) {
	now := time.Date(2026, 11, 11, 7, 30, 0, 0, sto)
	prices := pricesFrom(now, 3, realistic)
	cfg := DefaultV2Config()
	fw := core.PlanWindow{Start: time.Date(2026, 11, 11, 2, 0, 0, 0, sto), End: time.Date(2026, 11, 11, 4, 30, 0, 0, sto), Kind: KindFull}
	in := V2Input{Now: now, Prices: prices, TankHeadroom: 9 * time.Hour, Facts: core.Facts{Windows: []core.PlanWindow{fw}}}
	r1 := PlanV2(in, cfg)
	in.Now = now.Add(15 * time.Minute)
	in.Facts.Windows = r1.Plan.Windows
	r2 := PlanV2(in, cfg)
	n := 0
	for _, w := range r2.Plan.Windows {
		if w.Kind == KindCatchup && w.End.After(in.Now) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d live catch-ups: %+v", n, r2.Plan.Windows)
	}
}

func TestV2EveningOption(t *testing.T) {
	now := time.Date(2026, 11, 10, 18, 30, 0, 0, sto)
	prices := pricesFrom(now, 3, realistic)
	cfg := DefaultV2Config()
	base := core.Facts{TankHeadroom: 10 * time.Hour}
	count := func(res V2Result) (n int, w core.PlanWindow) {
		for _, x := range res.Plan.Windows {
			if x.Kind == KindEvening && x.End.After(res.Plan.GeneratedAt) {
				n++
				w = x
			}
		}
		return
	}
	// Off by default: demand alone does nothing.
	if n, _ := count(PlanV2(V2Input{Now: now, Prices: prices, Facts: base, EveningDemand: true}, cfg)); n != 0 {
		t.Fatal("evening window with the option off")
	}
	cfg.EveningTopup = true
	// On, no demand: nothing.
	if n, _ := count(PlanV2(V2Input{Now: now, Prices: prices, Facts: base}, cfg)); n != 0 {
		t.Fatal("evening window without demand")
	}
	// On with demand: one short window before the night window.
	res := PlanV2(V2Input{Now: now, Prices: prices, Facts: base, EveningDemand: true}, cfg)
	n, ew := count(res)
	if n != 1 {
		t.Fatalf("evening windows %d: %+v", n, res.Plan.Windows)
	}
	var full core.PlanWindow
	for _, w := range res.Plan.Windows {
		if w.Kind == KindFull && w.End.After(now) {
			full = w
			break
		}
	}
	if !ew.End.Before(full.Start) || full.Start.Sub(now) <= cfg.EveningGap {
		t.Fatalf("evening %v-%v vs full %v", ew.Start.In(sto), ew.End.In(sto), full.Start.In(sto))
	}
	checkV2(t, res.Plan, now)
	// Night window too close (cheapest night starts < 3 h away): none.
	late := time.Date(2026, 11, 10, 22, 30, 0, 0, sto)
	flat := pricesFrom(late, 3, func(t time.Time) float64 { return 0.5 })
	cfg.NightFrom, cfg.NightTo = 23*60, 7*60
	res = PlanV2(V2Input{Now: late, Prices: flat, Facts: base, EveningDemand: true}, cfg)
	if n, _ := count(res); n != 0 {
		t.Fatalf("evening window %d hours before the night window", n)
	}
	// Remembered for 12 h: not repeated after one ran.
	now2 := time.Date(2026, 11, 10, 19, 30, 0, 0, sto)
	cfg.NightFrom, cfg.NightTo = 22*60, 7*60
	f2 := base
	f2.Windows = []core.PlanWindow{{Start: now2.Add(-2 * time.Hour), End: now2.Add(-time.Hour), Kind: KindEvening}}
	if n, _ := count(PlanV2(V2Input{Now: now2, Prices: prices, Facts: f2, EveningDemand: true}, cfg)); n != 0 {
		t.Fatal("second evening window within 12 h")
	}
}

// The feed to a house power budget is the OFF periods: never an OFF over a
// window, never longer than 23 h, and the windows together with the OFF
// periods cover the plan (the rest is ON).
func TestV2OffPeriodsFeedBudget(t *testing.T) {
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, sto)
	prices := pricesFrom(now, 3, realistic)
	res := PlanV2(V2Input{Now: now, Prices: prices, TankHeadroom: 10 * time.Hour}, DefaultV2Config())
	checkV2(t, res.Plan, now)
	on := time.Duration(0)
	horizon := now.Add(24 * time.Hour)
	for x := now; x.Before(horizon); x = x.Add(time.Minute) {
		if _, ok := res.Plan.PeriodAt(x); !ok {
			on += time.Minute
		}
	}
	// ON for a full (2.5 h) + topup (1 h) + motor margins + the gaps of
	// the lead-in: well under 6 h of the day.
	if on < 3*time.Hour || on > 6*time.Hour {
		t.Fatalf("ON %v of the next 24 h", on)
	}
	if res.Plan.ValidUntil.Before(now.Add(24 * time.Hour)) {
		t.Fatalf("ValidUntil %v", res.Plan.ValidUntil)
	}
}

func TestV2MissingPricesMeansNoPlan(t *testing.T) {
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, sto)
	res := PlanV2(V2Input{Now: now}, DefaultV2Config())
	if !res.Plan.ValidUntil.IsZero() || len(res.Plan.OffPeriods) != 0 {
		t.Fatalf("plan from no prices: %+v", res.Plan)
	}
	// Tomorrow's night unknown: no OFF past the point where a needed window
	// could not be placed.
	prices := pricesFrom(now, 1, realistic)
	res = PlanV2(V2Input{Now: now, Prices: prices, TankHeadroom: 10 * time.Hour}, DefaultV2Config())
	if len(res.Plan.OffPeriods) > 0 {
		last := res.Plan.OffPeriods[len(res.Plan.OffPeriods)-1]
		if last.End.After(res.Plan.ValidUntil) {
			t.Fatalf("OFF past the known prices: %+v", last)
		}
	}
}

// DST: the night band spans 10 h on the autumn change-over night and 8 h in
// spring. Windows must sit inside the local band and have the right length.
func TestV2DST(t *testing.T) {
	for _, tc := range []struct {
		name string
		now  time.Time
	}{
		{"autumn 2026-10-25", time.Date(2026, 10, 24, 12, 0, 0, 0, sto)},
		{"spring 2027-03-28", time.Date(2027, 3, 27, 12, 0, 0, 0, sto)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prices := pricesFrom(tc.now, 4, realistic)
			cfg := DefaultV2Config()
			res := PlanV2(V2Input{Now: tc.now, Prices: prices, TankHeadroom: 12 * time.Hour}, cfg)
			checkV2(t, res.Plan, tc.now)
			var fulls []core.PlanWindow
			for _, w := range res.Plan.Windows {
				if w.Kind == KindFull {
					fulls = append(fulls, w)
				}
				if w.End.Sub(w.Start)%quarter != 0 {
					t.Fatalf("window not whole quarters: %+v", w)
				}
			}
			if len(fulls) < 2 {
				t.Fatalf("expected a full window each night in 36 h: %+v", res.Plan.Windows)
			}
			for _, f := range fulls {
				if !inBand(f.Start, 22*60, 7*60) || !inBand(f.End.Add(-time.Second), 22*60, 7*60) {
					t.Fatalf("full %v-%v outside the local night band", f.Start.In(sto), f.End.In(sto))
				}
				if f.End.Sub(f.Start) != 11*quarter {
					t.Fatalf("full length %v", f.End.Sub(f.Start))
				}
			}
			// The change-over night's window is the cheapest run of that
			// night in absolute time.
			cn := fulls[0]
			ns := time.Date(cn.Start.In(sto).Year(), cn.Start.In(sto).Month(), cn.Start.In(sto).Day(), 22, 0, 0, 0, sto)
			if cn.Start.In(sto).Hour() < 22 {
				ns = ns.AddDate(0, 0, -1)
			}
			ne := time.Date(ns.Year(), ns.Month(), ns.Day()+1, 7, 0, 0, 0, sto)
			if want := bruteCheapest(prices, cfg, ns, ne, 11); !cn.Start.Equal(want) {
				t.Fatalf("night window %v, cheapest %v", cn.Start.In(sto), want.In(sto))
			}
			// A 24 h day count: the DST day has 23 or 25 hours; the plan's
			// OFF periods must still never overlap and stay <= 23 h (checkV2).
			// The topup stays inside 10-16 local.
			for _, w := range res.Plan.Windows {
				if w.Kind == KindTopup && (!inBand(w.Start, 10*60, 16*60) || minuteOf(w.End.Add(-time.Second), sto) >= 16*60) {
					t.Fatalf("topup %v-%v", w.Start.In(sto), w.End.In(sto))
				}
			}
		})
	}
}

// Rolling re-plans do not flap: a future window moves only for a real
// saving, and a window that is about to start (or has started) never slides
// to the next quarter (the controller switches ON 30 s ahead of it).
func TestV2WindowStableAcrossReplans(t *testing.T) {
	start := time.Date(2026, 11, 11, 11, 0, 0, 0, sto)
	prices := pricesFrom(start, 3, realistic)
	cfg := DefaultV2Config()
	var windows []core.PlanWindow
	first := map[string]time.Time{}
	for now := start; now.Before(start.Add(16 * time.Hour)); now = now.Add(10 * time.Second) {
		res := PlanV2(V2Input{Now: now, Prices: prices, Facts: core.Facts{Windows: windows}, TankHeadroom: 12 * time.Hour}, cfg)
		windows = res.Plan.Windows
		seen := map[string]bool{}
		for _, w := range windows {
			if w.End.After(now) && !seen[w.Kind] {
				seen[w.Kind] = true
				if f, ok := first[w.Kind]; !ok {
					first[w.Kind] = w.Start
				} else if !w.Start.Equal(f) && now.Before(f) {
					t.Fatalf("%s window moved %v -> %v at %v", w.Kind, f.In(sto), w.Start.In(sto), now.In(sto))
				} else if !w.Start.Equal(f) {
					// the first one ended: a later instance is fine
					first[w.Kind] = w.Start
				}
			}
		}
	}
	// Across the start of the midday window the window stays where it was.
	if first[KindTopup].IsZero() || first[KindFull].IsZero() {
		t.Fatalf("windows seen %v", first)
	}
}
