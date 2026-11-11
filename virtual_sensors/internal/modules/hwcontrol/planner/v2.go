package planner

// planner_v2: a 24 h hot-water optimiser (0.6.0).
//
// The heater can only be ALLOWED or BLOCKED; the tank's own thermostat
// decides when it draws. v2 therefore plans a few heating WINDOWS (switch
// ON) and permits OFF everywhere else (core.Plan.DefaultOff). It never
// touches a safety rule: the core still decides every OFF through
// MayGoOff, and the plan only ever PERMITS. A missing price, a missing
// plan or any doubt means ON.
//
//   - FULL: the cheapest contiguous window of the learned full-tank length
//     inside the night band (default 22:00-07:00);
//   - TOPUP: the cheapest contiguous window of the learned top-up length
//     inside the midday band (default 10:00-16:00);
//   - CATCHUP: when the full heat produced no finished reheat or was cut
//     short, the cheapest window before the tank clock expires;
//   - EVENING (option, off): a short top-up before the night window after
//     an evening draw-down.
//
// A window ends early when the tank is full (thermostat stepped off and
// stayed off), and is extended while the heater is still drawing at its
// planned end, so a reheat is not cut.

import (
	"fmt"
	"math"
	"sort"
	"time"

	"virtual_sensors/internal/modules/hwcontrol/core"
)

// V2ID names planner v2 in published plans.
const V2ID = "v2"

// Window kinds.
const (
	KindFull    = "full"
	KindTopup   = "topup"
	KindCatchup = "catchup"
	KindEvening = "evening"
)

// Bounds of the learned durations.
const (
	FullFloor   = 90 * time.Minute
	FullCap     = 4 * time.Hour
	TopupFloor  = 30 * time.Minute
	TopupCap    = 2 * time.Hour
	minSamples  = 5
	learnWindow = 45 * 24 * time.Hour
	// A reheat that starts at least this long after the previous one
	// ended, and has no window tag, is a full-type reheat (the tank had
	// time to cool); a shorter gap makes it a top-up type.
	fullGap = 4 * time.Hour
)

// V2Config is planner v2's configuration. None of it is a safety limit.
type V2Config struct {
	Price Config // AdderOre and VAT: the price definition shared with greedy_v1
	Loc   *time.Location

	NightFrom, NightTo   int // minutes after local midnight (default 22:00, 07:00)
	MiddayFrom, MiddayTo int // default 10:00, 16:00
	Topup                bool

	FullDefault, TopupDefault time.Duration // before there is history: 150 min, 60 min
	EarlyEnd                  time.Duration // heater off this long: end the window (10 min)
	EveningTopup              bool          // hwc_v2_evening_topup, default off
	EveningFrom               int           // minutes after midnight: demand counts from here (17:00)
	EveningGap                time.Duration // night window must start more than this later (3 h)

	HeaterKW    float64       // 3.0
	MotorMargin time.Duration // 30 s: the ON command leads the window, the window is this much longer
	MaxExtend   time.Duration // a window may run this long past its end while the heater draws
	HystOre     float64       // a planned future window moves only for a saving of this many öre/kWh

	MaxMovesDay, MoveReserve, MaxMovesHour int
	Horizon                                time.Duration
}

// DefaultV2Config is the decided default.
func DefaultV2Config() V2Config {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		loc = time.UTC
	}
	return V2Config{
		Price: DefaultConfig(), Loc: loc,
		NightFrom: 22 * 60, NightTo: 7 * 60, MiddayFrom: 10 * 60, MiddayTo: 16 * 60, Topup: true,
		FullDefault: 150 * time.Minute, TopupDefault: 60 * time.Minute,
		EarlyEnd: 10 * time.Minute, EveningFrom: 17 * 60, EveningGap: 3 * time.Hour,
		HeaterKW: 3.0, MotorMargin: 30 * time.Second, MaxExtend: 60 * time.Minute, HystOre: 3,
		MaxMovesDay: 8, MoveReserve: 3, MaxMovesHour: 6, Horizon: 36 * time.Hour,
	}
}

// Learned is the heating-duration model learned from the reheat log.
type Learned struct {
	Full, Topup           time.Duration // window lengths (full: p85 clamped 90 min..4 h)
	FullP50, TopupP50     time.Duration // expected on-time, for cost estimates
	NFull, NTopup         int
	FullDefault, TopDef   bool
	FullP85Raw, TopupP50R time.Duration
}

// percentile is the linear-interpolated percentile (0..1) of d, which it sorts.
func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	if len(s) == 1 {
		return s[0]
	}
	x := p * float64(len(s)-1)
	lo := int(math.Floor(x))
	hi := int(math.Ceil(x))
	f := x - float64(lo)
	return s[lo] + time.Duration(math.Round(f*float64(s[hi]-s[lo])))
}

func clampD(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}

// isFullType classifies a reheat. A window tag (set by the core when v2
// was in control) wins; untagged history (greedy_v1 era, seeds) uses the
// gap since the previous reheat ended.
func isFullType(rs []core.ReheatRec, i int) bool {
	switch rs[i].Kind {
	case KindFull, KindCatchup:
		return true
	case KindTopup, KindEvening:
		return false
	}
	if i == 0 {
		return true
	}
	return rs[i].Start.Sub(rs[i-1].End) >= fullGap
}

// Learn builds the duration model from the reheat log: the on-time from the
// heater's step on to its step off (the thermostat satisfied). Reheats cut
// by our own OFF are excluded, they say nothing about the full time.
func Learn(now time.Time, rs []core.ReheatRec, cfg V2Config) Learned {
	l := Learned{Full: cfg.FullDefault, Topup: cfg.TopupDefault, FullDefault: true, TopDef: true,
		FullP50: 120 * time.Minute, TopupP50: 40 * time.Minute}
	l.Full = clampD(l.Full, FullFloor, FullCap)
	l.Topup = clampD(l.Topup, TopupFloor, TopupCap)
	var full, top []time.Duration
	for i, r := range rs {
		if r.Cut || now.Sub(r.End) > learnWindow || r.End.Before(r.Start) {
			continue
		}
		if isFullType(rs, i) {
			full = append(full, r.Duration())
		} else {
			top = append(top, r.Duration())
		}
	}
	l.NFull, l.NTopup = len(full), len(top)
	if len(full) >= minSamples {
		l.FullP85Raw = percentile(full, 0.85)
		l.Full = clampD(l.FullP85Raw, FullFloor, FullCap)
		l.FullP50 = percentile(full, 0.5)
		l.FullDefault = false
	}
	if len(top) >= minSamples {
		l.TopupP50R = percentile(top, 0.5)
		l.Topup = clampD(l.TopupP50R, TopupFloor, TopupCap)
		l.TopupP50 = l.TopupP50R
		l.TopDef = false
	}
	return l
}

// V2Input is one planning call. Facts.Windows is the memory of earlier
// windows (the core persists it for the active planner; the shadow
// planner is handed its own).
type V2Input struct {
	Now           time.Time
	Prices        []Price
	Facts         core.Facts
	EveningDemand bool
	GreedyOff     []core.OffPeriod // greedy_v1's permitted OFF periods, for the comparison
	TankHeadroom  time.Duration    // 0: unknown or exceeded (then every OFF is refused anyway)
}

// V2Result is planner v2's output.
type V2Result struct {
	Plan    *core.Plan
	Alerts  []core.PlanAlert
	Learned Learned
	Cost    Costs
}

// Costs is the expected cost of the windows in the next 24 h against what
// greedy_v1 would have cost for the same energy (an estimate: see Plan).
type Costs struct {
	KWh       float64
	V2Kr      float64
	GreedyKr  float64
	SavingKr  float64
	V2Ore     float64 // mean öre/kWh paid
	GreedyOre float64
}

const memoKeep = 36 * time.Hour

func ceilQ(t time.Time) time.Time {
	u := t.Truncate(quarter)
	if u.Before(t) {
		u = u.Add(quarter)
	}
	return u
}

type band struct{ start, end time.Time }

// bandsAround lists the instances of a daily band [from, to) (to <= from
// crosses midnight) that end after lo and start before hi.
func bandsAround(loc *time.Location, from, to int, lo, hi time.Time) []band {
	var out []band
	l := lo.In(loc)
	for d := -2; d <= 4; d++ {
		day := time.Date(l.Year(), l.Month(), l.Day()+d, 0, 0, 0, 0, loc)
		s := time.Date(day.Year(), day.Month(), day.Day(), from/60, from%60, 0, 0, loc)
		ed := day.Day()
		if to <= from {
			ed++
		}
		e := time.Date(day.Year(), day.Month(), ed, to/60, to%60, 0, 0, loc)
		if e.After(lo) && s.Before(hi) {
			out = append(out, band{s, e})
		}
	}
	return out
}

// lastBandStart is the latest instance with start <= now (it may have ended).
func lastBand(loc *time.Location, from, to int, now time.Time) band {
	bs := bandsAround(loc, from, to, now.Add(-72*time.Hour), now.Add(time.Nanosecond))
	var best band
	for _, b := range bs {
		if !b.start.After(now) && (best.start.IsZero() || b.start.After(best.start)) {
			best = b
		}
	}
	return best
}

type pricing struct {
	q   map[int64]float64
	cfg Config
}

func (p pricing) at(t time.Time) (float64, bool) {
	v, ok := p.q[t.Unix()]
	if !ok {
		return 0, false
	}
	return p.cfg.AllInOre(v), true
}

// mean of n quarters from s, false if any is unknown.
func (p pricing) mean(s time.Time, n int) (float64, bool) {
	sum := 0.0
	for k := 0; k < n; k++ {
		v, ok := p.at(s.Add(time.Duration(k) * quarter))
		if !ok {
			return 0, false
		}
		sum += v
	}
	return sum / float64(n), true
}

// cheapest finds the cheapest contiguous run of n known quarters inside
// [from, to), starting at or after from (quarter-aligned up). The earliest
// wins a tie. prefer, if non-zero and still a valid candidate, is kept
// unless the best is cheaper by more than hyst.
func (p pricing) cheapest(from, to time.Time, n int, prefer time.Time, hyst float64) (time.Time, float64, bool) {
	var best time.Time
	bestV, found := math.Inf(1), false
	prefV, prefOK := 0.0, false
	for s := ceilQ(from); !s.Add(time.Duration(n) * quarter).After(to); s = s.Add(quarter) {
		v, ok := p.mean(s, n)
		if !ok {
			continue
		}
		if v < bestV-1e-9 {
			best, bestV, found = s, v, true
		}
		if !prefer.IsZero() && s.Equal(prefer) {
			prefV, prefOK = v, true
		}
	}
	if found && prefOK && prefV <= bestV+hyst {
		return prefer, prefV, true
	}
	return best, bestV, found
}

func nQuarters(d, margin time.Duration) int {
	n := int((d + margin + quarter - 1) / quarter)
	if n < 1 {
		n = 1
	}
	return n
}

func prio(kind string) int {
	switch kind {
	case KindCatchup:
		return 0
	case KindFull:
		return 1
	case KindTopup:
		return 2
	}
	return 3
}

func overlaps(a, b core.PlanWindow, gap time.Duration) bool {
	return a.Start.Before(b.End.Add(gap)) && b.Start.Before(a.End.Add(gap))
}

// PlanV2 builds the plan at in.Now. It is deterministic given its input
// and keeps windows stable across the rolling re-plans: a window that has
// started is pinned, and a future window moves only for a real saving.
func PlanV2(in V2Input, cfg V2Config) V2Result {
	now := in.Now
	res := V2Result{Learned: Learn(now, in.Facts.Reheats, cfg)}
	plan := &core.Plan{PlannerID: V2ID, GeneratedAt: now, DefaultOff: true}
	res.Plan = plan
	q := quarters(in.Prices)
	if len(q) == 0 {
		return res // no ValidUntil: treated as no plan: the switch stays ON
	}
	pr := pricing{q: q, cfg: cfg.Price}
	var last int64
	for k := range q {
		if k > last {
			last = k
		}
	}
	plan.ValidUntil = time.Unix(last, 0).Add(quarter)
	horizon := now.Add(cfg.Horizon)
	nFull := nQuarters(res.Learned.Full, cfg.MotorMargin)
	nTop := nQuarters(res.Learned.Topup, cfg.MotorMargin)
	reheats := in.Facts.Reheats

	// doneSince: a finished, not cut-short reheat ended in [since, now].
	doneSince := func(since time.Time) bool {
		for _, r := range reheats {
			if !r.Cut && !r.End.Before(since) && !r.End.After(now) {
				return true
			}
		}
		return false
	}
	lastFullReheat := time.Time{}
	for _, r := range reheats {
		if !r.Cut && r.End.After(lastFullReheat) {
			lastFullReheat = r.End
		}
	}

	// ---- carry memory: past windows (for the failure check and tagging),
	// current windows (pinned), future windows (kept unless a real saving).
	// A window is current (pinned) from one minute before its start: the
	// controller's ON command leads the window by MotorMargin, and a window
	// that is already switching ON must not slide to the next quarter on the
	// following re-plan.
	pin := 2 * cfg.MotorMargin
	var past, cur, fut []core.PlanWindow
	for _, w := range in.Facts.Windows {
		switch {
		case !w.End.After(now):
			if now.Sub(w.End) <= memoKeep {
				past = append(past, w)
			}
		case !w.Start.After(now.Add(pin)):
			cur = append(cur, w)
		default:
			fut = append(fut, w)
		}
	}

	// ---- current windows: extend while the heater draws, end early when full.
	var notes []string
	for i := range cur {
		w := &cur[i]
		maxEnd := w.Start.Add(5 * time.Hour)
		if w.Kind == KindTopup || w.Kind == KindEvening {
			maxEnd = w.Start.Add(3 * time.Hour)
		}
		if in.Facts.CycleOpen {
			// Never cut a reheat: keep the window alive until it ends.
			if want := now.Add(20 * time.Minute); w.End.Before(want) {
				if want.After(maxEnd) {
					want = maxEnd
				}
				if want.After(w.End) {
					w.End = want
					notes = append(notes, "extended while the heater draws")
				}
			}
			continue
		}
		// The tank is full: a finished reheat that started in this window,
		// with the heater off for EarlyEnd. Needs an OFF to be possible.
		for _, r := range reheats {
			if r.Cut || r.Start.Before(w.Start.Add(-2*time.Minute)) || r.End.After(now) {
				continue
			}
			if now.Sub(r.End) >= cfg.EarlyEnd && w.End.After(now) && offAffordable(in.Facts, cfg) {
				w.End = now
				w.Early = true
				notes = append(notes, "ended early: tank full")
			}
		}
	}
	// A window that just ended early is past now.
	var cur2 []core.PlanWindow
	for _, w := range cur {
		if w.End.After(now) {
			cur2 = append(cur2, w)
		} else {
			past = append(past, w)
		}
	}
	cur = cur2

	// ---- bands.
	nights := bandsAround(cfg.Loc, cfg.NightFrom, cfg.NightTo, now, horizon)
	var middays []band
	if cfg.Topup {
		middays = bandsAround(cfg.Loc, cfg.MiddayFrom, cfg.MiddayTo, now, horizon)
	}
	have := func(ws []core.PlanWindow, kinds string, b band) (core.PlanWindow, bool) {
		for _, w := range ws {
			if w.Kind == kinds && !w.Start.Before(b.start) && w.Start.Before(b.end) {
				return w, true
			}
		}
		return core.PlanWindow{}, false
	}
	var chosen []core.PlanWindow
	chosen = append(chosen, cur...)
	cutAt := time.Time{} // OFF is not permitted from here on: a needed window could not be placed
	setCut := func(t time.Time) {
		if cutAt.IsZero() || t.Before(cutAt) {
			cutAt = t
		}
	}
	place := func(kind string, b band, n int, lo time.Time) {
		if b.end.Before(lo) {
			return
		}
		if _, ok := have(cur, kind, b); ok {
			return // pinned
		}
		var prefer time.Time
		if pw, ok := have(fut, kind, b); ok && pw.End.Sub(pw.Start) == time.Duration(n)*quarter {
			prefer = pw.Start
		}
		s, v, ok := pr.cheapest(maxT(b.start, lo), b.end, n, prefer, cfg.HystOre)
		if !ok {
			setCut(maxT(b.start, now))
			return
		}
		chosen = append(chosen, core.PlanWindow{Start: s, End: s.Add(time.Duration(n) * quarter), Kind: kind, Ore: round1(v)})
	}
	lo := now.Add(cfg.MotorMargin)

	for _, b := range nights {
		// An instance with a past full window is judged below.
		if _, ok := have(past, KindFull, b); ok {
			continue
		}
		if doneSince(b.start) {
			continue
		}
		place(KindFull, b, nFull, lo)
	}
	for _, b := range middays {
		if doneSince(b.start) {
			continue
		}
		if _, ok := have(past, KindTopup, b); ok {
			continue
		}
		place(KindTopup, b, nTop, lo)
	}

	// ---- failure of the full heat: no finished reheat, or one cut short.
	b0 := lastBand(cfg.Loc, cfg.NightFrom, cfg.NightTo, now)
	if !b0.start.IsZero() {
		if fw, ok := have(past, KindFull, b0); ok && !doneSince(b0.start) {
			catchPast, catchLive := 0, false
			for _, w := range past {
				if w.Kind == KindCatchup && !w.Start.Before(b0.start) {
					catchPast++
				}
			}
			for _, w := range append(append([]core.PlanWindow{}, cur...), fut...) {
				if w.Kind == KindCatchup {
					catchLive = true
				}
			}
			if catchLive {
				// An already planned catch-up is carried over (cur ones are in chosen).
				for _, w := range fut {
					if w.Kind == KindCatchup {
						chosen = append(chosen, w)
					}
				}
			}
			if catchPast < 2 && !catchLive {
				hi := now.Add(23 * time.Hour)
				if in.TankHeadroom > 0 {
					if e := now.Add(in.TankHeadroom - cfg.MotorMargin); e.Before(hi) {
						hi = e
					}
				}
				var cw core.PlanWindow
				found := false
				if in.TankHeadroom > 0 {
					if s, v, ok := pr.cheapest(lo, hi.Add(time.Duration(nFull)*quarter), nFull, time.Time{}, 0); ok && !s.After(hi) {
						cw = core.PlanWindow{Start: s, End: s.Add(time.Duration(nFull) * quarter), Kind: KindCatchup, Ore: round1(v)}
						found = true
					}
				}
				if !found { // the tank clock is out or no price: the switch is ON anyway
					s := ceilQ(now)
					if now.Equal(s) {
						s = now
					}
					v, _ := pr.mean(s, nFull)
					cw = core.PlanWindow{Start: now, End: s.Add(time.Duration(nFull) * quarter), Kind: KindCatchup, Ore: round1(v)}
				}
				chosen = append(chosen, cw)
				why := "no finished reheat"
				if lastFullReheatCut(reheats, fw.Start) {
					why = "the reheat was cut short"
				}
				res.Alerts = append(res.Alerts, core.PlanAlert{Code: "FULL_HEAT_INCOMPLETE", Repeat: 6 * time.Hour,
					Text: fmt.Sprintf("Hot water: the full-heat window %s-%s ended with %s (the tank may simply have been hot). Catch-up planned %s-%s.",
						hhmm(fw.Start, cfg.Loc), hhmm(fw.End, cfg.Loc), why, hhmm(cw.Start, cfg.Loc), hhmm(cw.End, cfg.Loc))})
			}
		}
	}

	// ---- evening top-up (option): a draw-down after 17:00, with the night
	// window still far away.
	if cfg.EveningTopup && in.EveningDemand && minuteOf(now, cfg.Loc) >= cfg.EveningFrom {
		var nextFull *core.PlanWindow
		for i := range chosen {
			w := &chosen[i]
			if w.Kind == KindFull && w.End.After(now) && (nextFull == nil || w.Start.Before(nextFull.Start)) {
				nextFull = w
			}
		}
		recent := false
		for _, w := range past {
			if w.Kind == KindEvening && now.Sub(w.End) < 12*time.Hour {
				recent = true
			}
		}
		_, curEv := have(cur, KindEvening, band{now.Add(-12 * time.Hour), now.Add(time.Nanosecond)})
		if nextFull != nil && !recent && !curEv && nextFull.Start.Sub(now) > cfg.EveningGap {
			var prefer time.Time
			if pw, ok := have(fut, KindEvening, band{now, horizon}); ok {
				prefer = pw.Start
			}
			if s, v, ok := pr.cheapest(lo, nextFull.Start, nTop, prefer, cfg.HystOre); ok {
				chosen = append(chosen, core.PlanWindow{Start: s, End: s.Add(time.Duration(nTop) * quarter), Kind: KindEvening, Ore: round1(v)})
			}
		}
	} else if cfg.EveningTopup {
		// Keep an evening window already planned (it was decided on demand).
		for _, w := range fut {
			if w.Kind == KindEvening {
				chosen = append(chosen, w)
			}
		}
	}

	// ---- conflicts and the move budget. A catch-up swallows a top-up that
	// overlaps it or lies within 2 h; at most `pairs` windows in 24 h, the
	// highest priority first (each window costs an ON and an OFF move).
	pairs := (cfg.MaxMovesDay - cfg.MoveReserve) / 2
	if pairs < 1 {
		pairs = 1
	}
	sort.SliceStable(chosen, func(i, j int) bool {
		if prio(chosen[i].Kind) != prio(chosen[j].Kind) {
			return prio(chosen[i].Kind) < prio(chosen[j].Kind)
		}
		return chosen[i].Start.Before(chosen[j].Start)
	})
	var kept []core.PlanWindow
	for _, w := range chosen {
		pinned := !w.Start.After(now.Add(pin))
		ok := true
		n24 := 0
		for _, k := range kept {
			if overlaps(w, k, 0) {
				ok = false
			}
			if w.Kind != KindFull && k.Kind == KindCatchup && overlaps(w, k, 2*time.Hour) {
				ok = false
			}
			if d := w.Start.Sub(k.Start); d > -24*time.Hour && d < 24*time.Hour {
				n24++
			}
		}
		if !pinned && (!ok || n24 >= pairs) {
			notes = append(notes, fmt.Sprintf("%s window dropped (conflict or move budget)", w.Kind))
			continue
		}
		kept = append(kept, w)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Start.Before(kept[j].Start) })
	// A dropped needed window means an unplaced need: no OFF past it.
	for _, w := range chosen {
		found := false
		for _, k := range kept {
			if k == w {
				found = true
			}
		}
		if !found && w.Kind == KindFull {
			setCut(maxT(w.Start, now))
		}
	}

	// ---- OFF periods: the complement of the windows.
	limit := plan.ValidUntil
	if !cutAt.IsZero() && cutAt.Before(limit) {
		limit = cutAt
	}
	// OFF starts where the last window ended (stable across re-plans), else now.
	cursor := now
	if pe := latestEnd(past, now); !pe.IsZero() && now.Sub(pe) < 12*time.Hour {
		cursor = pe
	}
	emit := func(a, b time.Time, why string) {
		if b.After(limit) {
			b = limit
		}
		for b.Sub(a) > 23*time.Hour {
			plan.OffPeriods = append(plan.OffPeriods, core.OffPeriod{Start: a, End: a.Add(23 * time.Hour), Why: why})
			a = a.Add(23 * time.Hour)
		}
		if b.Sub(a) >= 5*time.Minute {
			plan.OffPeriods = append(plan.OffPeriods, core.OffPeriod{Start: a, End: b, Why: why})
		}
	}
	for _, w := range kept {
		if !w.End.After(now) {
			continue
		}
		if w.Start.After(now) {
			emit(cursor, w.Start.Add(-cfg.MotorMargin), "default OFF until the "+w.Kind+" window")
		}
		if w.End.After(cursor) {
			cursor = w.End
		}
	}
	emit(cursor, limit, "default OFF")

	// ---- windows for memory/publishing: recent past + current + future.
	plan.Windows = append(plan.Windows, past...)
	plan.Windows = append(plan.Windows, kept...)
	sort.Slice(plan.Windows, func(i, j int) bool { return plan.Windows[i].Start.Before(plan.Windows[j].Start) })

	res.Cost = costs(now, pr, kept, in.GreedyOff, res.Learned, cfg)
	plan.Extra = extra(now, res, kept, cfg, notes, cutAt)
	return res
}

func lastFullReheatCut(rs []core.ReheatRec, since time.Time) bool {
	for _, r := range rs {
		if r.Cut && !r.End.Before(since) {
			return true
		}
	}
	return false
}

func latestEnd(ws []core.PlanWindow, now time.Time) time.Time {
	var e time.Time
	for _, w := range ws {
		if !w.End.After(now) && w.End.After(e) {
			e = w.End
		}
	}
	return e
}

func offAffordable(f core.Facts, cfg V2Config) bool {
	return f.MovesLast24h+1+cfg.MoveReserve <= cfg.MaxMovesDay && f.MovesLastHour+1+cfg.MoveReserve <= cfg.MaxMovesHour
}

func maxT(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func minuteOf(t time.Time, loc *time.Location) int {
	l := t.In(loc)
	return l.Hour()*60 + l.Minute()
}

func hhmm(t time.Time, loc *time.Location) string { return t.In(loc).Format("15:04") }

// expectedOn is how long the heater is expected to draw in a window of this
// kind (front-loaded: the tank has been calling while blocked).
func expectedOn(kind string, l Learned, win time.Duration) time.Duration {
	d := l.FullP50
	if kind == KindTopup || kind == KindEvening {
		d = l.TopupP50
	}
	if d > win {
		d = win
	}
	return d
}

// costs prices the windows in [now, now+24h). For the comparison, greedy_v1
// keeps the switch ON except in its OFF periods and the thermostat draws at
// a flat rate over the ON time, so the same energy costs the mean all-in
// price over the quarters greedy leaves ON. It is an estimate, not a
// replay.
func costs(now time.Time, pr pricing, ws []core.PlanWindow, greedyOff []core.OffPeriod, l Learned, cfg V2Config) Costs {
	end := now.Add(24 * time.Hour)
	var c Costs
	var oreKWh float64
	for _, w := range ws {
		if !w.End.After(now) || !w.Start.Before(end) {
			continue
		}
		on := expectedOn(w.Kind, l, w.End.Sub(w.Start))
		left := on
		for t := w.Start; t.Before(w.End) && left > 0; t = t.Add(quarter) {
			span := quarter
			if span > left {
				span = left
			}
			p, ok := pr.at(t)
			if !ok {
				break
			}
			kwh := cfg.HeaterKW * span.Hours()
			c.KWh += kwh
			oreKWh += kwh * p
			left -= span
		}
	}
	if c.KWh <= 0 {
		return c
	}
	c.V2Ore = oreKWh / c.KWh
	c.V2Kr = oreKWh / 100
	sum, n := 0.0, 0
	for t := ceilQ(now); t.Before(end); t = t.Add(quarter) {
		p, ok := pr.at(t)
		if !ok {
			continue
		}
		off := false
		for _, op := range greedyOff {
			if !t.Before(op.Start) && t.Before(op.End) {
				off = true
			}
		}
		if !off {
			sum += p
			n++
		}
	}
	if n == 0 {
		return c
	}
	c.GreedyOre = sum / float64(n)
	c.GreedyKr = c.GreedyOre * c.KWh / 100
	c.SavingKr = c.GreedyKr - c.V2Kr
	return c
}

func extra(now time.Time, res V2Result, ws []core.PlanWindow, cfg V2Config, notes []string, cutAt time.Time) map[string]any {
	var wins []map[string]any
	for _, w := range ws {
		if !w.End.After(now) {
			continue
		}
		on := expectedOn(w.Kind, res.Learned, w.End.Sub(w.Start))
		// Front-loaded expected cost of this window alone.
		wins = append(wins, map[string]any{
			"start": w.Start.In(cfg.Loc).Format(time.RFC3339), "end": w.End.In(cfg.Loc).Format(time.RFC3339),
			"kind": w.Kind, "ore_kwh": w.Ore, "exp_kr": round2(cfg.HeaterKW * on.Hours() * w.Ore / 100),
		})
		if len(wins) >= 6 {
			break
		}
	}
	l := res.Learned
	m := map[string]any{
		"windows": wins,
		"learned": map[string]any{
			"full_min": int(l.Full.Minutes()), "topup_min": int(l.Topup.Minutes()),
			"n_full": l.NFull, "n_topup": l.NTopup, "full_default": l.FullDefault, "topup_default": l.TopDef,
			"full_p50_min": int(l.FullP50.Minutes()), "topup_p50_min": int(l.TopupP50.Minutes()),
		},
		"expected_24h": map[string]any{
			"kwh": round2(res.Cost.KWh), "v2_kr": round2(res.Cost.V2Kr), "greedy_v1_est_kr": round2(res.Cost.GreedyKr),
			"saving_kr": round2(res.Cost.SavingKr), "v2_ore_kwh": round1(res.Cost.V2Ore), "greedy_v1_ore_kwh": round1(res.Cost.GreedyOre),
		},
		"bands": fmt.Sprintf("night %s-%s midday %s-%s", hm(cfg.NightFrom), hm(cfg.NightTo), hm(cfg.MiddayFrom), hm(cfg.MiddayTo)),
	}
	if len(notes) > 0 {
		if len(notes) > 4 {
			notes = notes[:4]
		}
		m["notes"] = notes
	}
	if !cutAt.IsZero() {
		m["off_until"] = cutAt.In(cfg.Loc).Format(time.RFC3339)
	}
	return m
}

func hm(min int) string { return fmt.Sprintf("%02d:%02d", min/60, min%60) }

func round2(v float64) float64 { return math.Round(v*100) / 100 }
