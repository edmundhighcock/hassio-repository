// Package planner is the deliberately simple first planner of PLAN §4.2.
// It owns the price definition and every price-dependent parameter, and
// turns quarter-hour prices into a core.Plan of PERMITTED OFF periods.
// The core never imports this package; it only consumes core.Plan, and no
// safety rule depends on anything here. A wrong or missing price can only
// mean fewer (or no) permitted periods, i.e. "stay ON".
package planner

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"virtual_sensors/internal/modules/hwcontrol/core"
)

// ID names this planner in published plans.
const ID = "greedy_v1"

// Config holds the planner's options (none is a safety limit).
type Config struct {
	AdderOre        float64       // 55.1 öre/kWh (2026: 16.0 transfer + 36.0 tax + 3.10 Fortum)
	VAT             float64       // 1.25
	MinBlock        time.Duration // 30 min
	MaxBlock        time.Duration // 3 h (R4: 2 h)
	MaxBlocksPer24h int           // 2 (R4: 1)
	MinSavingOre    float64       // 20 öre/kWh all-in (D9)
	AfterSpan       time.Duration // look for the deferred reheat within 4 h after a block
	AfterWindow     time.Duration // cheapest contiguous 60 min
	Separation      time.Duration // chosen blocks at least 2 h apart
	Horizon         time.Duration // ~36 h of known prices
}

// DefaultConfig is the decided planner configuration.
func DefaultConfig() Config {
	return Config{AdderOre: 55.1, VAT: 1.25, MinBlock: 30 * time.Minute, MaxBlock: 3 * time.Hour,
		MaxBlocksPer24h: 2, MinSavingOre: 20, AfterSpan: 4 * time.Hour, AfterWindow: time.Hour,
		Separation: 2 * time.Hour, Horizon: 36 * time.Hour}
}

// Price is one spot price interval in SEK/kWh excluding VAT (the nordpool
// sensor's unit).
type Price struct {
	Start time.Time
	Dur   time.Duration
	SEK   float64
}

const quarter = 15 * time.Minute

// AllInOre is p = (spot*100 + adder) * vat, öre/kWh.
func (c Config) AllInOre(sek float64) float64 { return (sek*100 + c.AdderOre) * c.VAT }

// quarters normalises prices to quarter-hours (hourly prices are
// replicated to four quarters), keyed by start, dropping any not
// quarter-aligned.
func quarters(prices []Price) map[int64]float64 {
	q := map[int64]float64{}
	for _, p := range prices {
		if p.Dur <= 0 || math.IsNaN(p.SEK) {
			continue
		}
		n := int(p.Dur / quarter)
		for k := 0; k < n; k++ {
			t := p.Start.Add(time.Duration(k) * quarter)
			if t.Unix()%int64(quarter.Seconds()) != 0 {
				continue
			}
			q[t.Unix()] = p.SEK
		}
	}
	return q
}

type block struct {
	start, end time.Time
	price      float64 // mean all-in öre/kWh
	after      float64 // cheapest 60 min within 4 h after
	saving     float64
}

// Plan builds the plan at now from known prices. It never permits a
// quarter whose price is unknown. Of the facts, only CurrentOff is used:
// a running period is kept exactly as started (review fix 4).
func Plan(now time.Time, prices []Price, cfg Config, facts core.Facts) *core.Plan {
	q := quarters(prices)
	plan := &core.Plan{PlannerID: ID, GeneratedAt: now}
	if len(q) == 0 {
		return plan // no ValidUntil: treated as no plan
	}
	var keys []int64
	for k := range q {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	last := time.Unix(keys[len(keys)-1], 0).Add(quarter)
	plan.ValidUntil = last
	cur := now.Truncate(quarter)
	horizonEnd := now.Add(cfg.Horizon)
	known := func(t time.Time) (float64, bool) {
		v, ok := q[t.Unix()]
		return cfg.AllInOre(v), ok
	}
	minQ := int(cfg.MinBlock / quarter)
	maxQ := int(cfg.MaxBlock / quarter)
	afterQ := int(cfg.AfterSpan / quarter)
	winQ := int(cfg.AfterWindow / quarter)

	var cands []block
	for s := cur; s.Before(horizonEnd) && s.Before(last); s = s.Add(quarter) {
		sum := 0.0
		for L := 1; L <= maxQ; L++ {
			v, ok := known(s.Add(time.Duration(L-1) * quarter))
			if !ok {
				break
			}
			sum += v
			if L < minQ {
				continue
			}
			end := s.Add(time.Duration(L) * quarter)
			if end.After(horizonEnd) {
				break
			}
			after, ok := cheapestWindow(known, end, afterQ, winQ)
			if !ok {
				continue
			}
			pb := sum / float64(L)
			if sav := pb - after; sav >= cfg.MinSavingOre {
				cands = append(cands, block{start: s, end: end, price: pb, after: after, saving: sav})
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].saving != cands[j].saving {
			return cands[i].saving > cands[j].saving
		}
		if !cands[i].start.Equal(cands[j].start) {
			return cands[i].start.Before(cands[j].start)
		}
		return cands[i].end.Before(cands[j].end)
	})
	var chosen []block
	// A running OFF period is frozen (review fix 4): keep it exactly, and
	// plan any further periods around it. Unstarted periods may move.
	pinned := map[int64]bool{}
	if op := facts.CurrentOff; op != nil && now.Before(op.End) && !now.Before(op.Start) {
		chosen = append(chosen, block{start: op.Start, end: op.End})
		pinned[op.Start.Unix()] = true
	}
	for _, b := range cands {
		if !compatible(b, chosen, cfg) {
			continue
		}
		chosen = append(chosen, b)
	}
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].start.Before(chosen[j].start) })
	for _, b := range chosen {
		why := fmt.Sprintf("mean %.1f öre/kWh vs %.1f within 4 h after: saving %.1f öre/kWh", b.price, b.after, b.saving)
		if pinned[b.start.Unix()] {
			why = "running period, kept as started"
		}
		plan.OffPeriods = append(plan.OffPeriods, core.OffPeriod{Start: b.start, End: b.end, Why: why})
	}
	if len(pinned) > 0 && plan.ValidUntil.Before(chosen[0].end) {
		// Keep the plan valid for as long as the pinned period runs.
		for _, b := range chosen {
			if pinned[b.start.Unix()] && plan.ValidUntil.Before(b.end) {
				plan.ValidUntil = b.end
			}
		}
	}
	return plan
}

func compatible(b block, chosen []block, cfg Config) bool {
	in24 := 0
	for _, c := range chosen {
		if b.start.Before(c.end.Add(cfg.Separation)) && c.start.Before(b.end.Add(cfg.Separation)) {
			return false // overlapping or within 2 h
		}
		if d := b.start.Sub(c.start); d < 24*time.Hour && d > -24*time.Hour {
			in24++
		}
	}
	return in24 < cfg.MaxBlocksPer24h
}

// cheapestWindow is the lowest mean of winQ contiguous known quarters
// starting within [from, from + spanQ quarters).
func cheapestWindow(known func(time.Time) (float64, bool), from time.Time, spanQ, winQ int) (float64, bool) {
	best, found := math.Inf(1), false
	for i := 0; i+winQ <= spanQ; i++ {
		sum, ok := 0.0, true
		for k := 0; k < winQ; k++ {
			v, kn := known(from.Add(time.Duration(i+k) * quarter))
			if !kn {
				ok = false
				break
			}
			sum += v
		}
		if ok && sum/float64(winQ) < best {
			best, found = sum/float64(winQ), true
		}
	}
	return best, found
}

// ParseNordpool reads the nordpool sensor's attributes. It prefers the
// timestamped raw_today/raw_tomorrow lists; otherwise it maps the plain
// today/tomorrow arrays from local midnight in absolute time, so DST days
// (92/100 quarters, or 23/25 hours) land correctly. tomorrow is used only
// if tomorrow_valid is not false.
func ParseNordpool(attrs map[string]json.RawMessage, now time.Time, loc *time.Location) []Price {
	var out []Price
	tomorrowValid := true
	if b, ok := attrs["tomorrow_valid"]; ok {
		var v bool
		if json.Unmarshal(b, &v) == nil {
			tomorrowValid = v
		}
	}
	for _, day := range []struct {
		raw, plain string
		offset     int
	}{{"raw_today", "today", 0}, {"raw_tomorrow", "tomorrow", 1}} {
		if day.offset == 1 && !tomorrowValid {
			continue
		}
		if ps := parseRaw(attrs[day.raw]); len(ps) > 0 {
			out = append(out, ps...)
			continue
		}
		out = append(out, parsePlain(attrs[day.plain], now, loc, day.offset)...)
	}
	return out
}

func parseRaw(b json.RawMessage) []Price {
	if len(b) == 0 {
		return nil
	}
	var rows []struct {
		Start string   `json:"start"`
		End   string   `json:"end"`
		Value *float64 `json:"value"`
	}
	if json.Unmarshal(b, &rows) != nil {
		return nil
	}
	var out []Price
	for _, r := range rows {
		if r.Value == nil {
			continue
		}
		s, err1 := time.Parse(time.RFC3339, r.Start)
		e, err2 := time.Parse(time.RFC3339, r.End)
		if err1 != nil || err2 != nil || !e.After(s) {
			continue
		}
		out = append(out, Price{Start: s, Dur: e.Sub(s), SEK: *r.Value})
	}
	return out
}

func parsePlain(b json.RawMessage, now time.Time, loc *time.Location, dayOffset int) []Price {
	if len(b) == 0 {
		return nil
	}
	var vals []*float64
	if json.Unmarshal(b, &vals) != nil || len(vals) == 0 {
		return nil
	}
	l := now.In(loc)
	midnight := time.Date(l.Year(), l.Month(), l.Day()+dayOffset, 0, 0, 0, 0, loc)
	next := time.Date(l.Year(), l.Month(), l.Day()+dayOffset+1, 0, 0, 0, 0, loc)
	dayLen := next.Sub(midnight)
	var step time.Duration
	switch {
	case time.Duration(len(vals))*quarter == dayLen:
		step = quarter
	case time.Duration(len(vals))*time.Hour == dayLen:
		step = time.Hour
	default:
		return nil // length does not match the day: unknown, publish nothing
	}
	var out []Price
	for i, v := range vals {
		if v == nil {
			continue
		}
		out = append(out, Price{Start: midnight.Add(time.Duration(i) * step), Dur: step, SEK: *v})
	}
	return out
}
