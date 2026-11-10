// Package evcontrol is the "ev_control" module: a SHADOW-ONLY planner and
// guard for the Zoe's Easee charger (ham services/ev-planner/PLAN.md). It
// reads Home Assistant entities, plans the night's charging against the
// quarter-hour spot prices, publishes what it WOULD command, and scores
// the plan against what actually happened. It never calls a Home
// Assistant or Easee service: there is deliberately no live mode here.
package evcontrol

import (
	"math"
	"sort"
	"time"

	"virtual_sensors/internal/modules/hwcontrol/planner"
)

const quarter = 15 * time.Minute

// Volts is the nominal phase voltage used to turn amps into power.
const Volts = 230.0

// Params are the planner's and guard's tunables (PLAN "Options").
type Params struct {
	NeedKWh    float64       // 44: 40 kWh plus ~10 % losses
	MinA       float64       // 6: below this a slot is unusable
	MaxA       float64       // 16: the charger circuit
	BudgetA    float64       // 19 per phase
	HouseBaseA float64       // 3 on every phase
	HeaterA    float64       // 7.5 on A and B while the immersion draws
	HPA        float64       // 7.4 on C (heat pump max)
	MinBlock   time.Duration // 30 min: shorter gaps are merged
	// HeaterFrom/HeaterTo (local hours) bound where a hot-water ON slot is
	// assumed to be drawing (16:00 to 23:00).
	HeaterFromH, HeaterToH int
	Price                  planner.Config // adder and VAT (hwcontrol's)
	Loc                    *time.Location
}

// DefaultParams are PLAN's defaults.
func DefaultParams() Params {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		loc = time.UTC
	}
	return Params{NeedKWh: 44, MinA: 6, MaxA: 16, BudgetA: 19, HouseBaseA: 3, HeaterA: 7.5, HPA: 7.4,
		MinBlock: 30 * time.Minute, HeaterFromH: 16, HeaterToH: 23, Price: planner.DefaultConfig(), Loc: loc}
}

// Input is everything the pure planner needs.
type Input struct {
	Now          time.Time
	Departure    time.Time
	DeliveredKWh float64 // loaver_session_energy so far
	Prices       []planner.Price
	// HotWaterOn reports whether hot water's own plan has the switch ON at
	// the slot start. nil means "unknown": treated as ON (conservative).
	HotWaterOn func(time.Time) bool
	// CeilingA (optional) is the house planner's per-slot headroom for the
	// EV, the minimum over its phases, in amps. ok=false (no forecast, or it
	// does not cover the slot) falls back to Params.Ceiling's static
	// assumptions. The result is floored, capped at MaxA and zeroed below
	// MinA like the static ceiling. The deadline backstop is unchanged.
	CeilingA func(time.Time) (float64, bool)
}

// Slot is one planned 15-minute charging slot.
type Slot struct {
	Start    time.Time `json:"start"`
	Amps     float64   `json:"amps"`
	KW       float64   `json:"kw"`
	PriceOre float64   `json:"price_ore"` // all-in öre/kWh; NaN-free: 0 with Priced=false when unknown
	Priced   bool      `json:"priced"`
}

// KWh is the energy of a full slot.
func (s Slot) KWh() float64 { return s.KW * 0.25 }

// Result is a plan.
type Result struct {
	Slots          []Slot
	NeedKWh        float64 // remaining need
	PlannedKWh     float64
	CostKr         float64 // all-in, priced slots only
	DeadlineMode   bool
	AwaitingPrices bool    // partial prices, need not yet fully planned; re-plan when tomorrow arrives
	UnplannedKWh   float64 // remainder of the need left unplanned while awaiting prices
	PricesPartial  bool
	Feasible       bool // PlannedKWh >= NeedKWh
}

// AmpsKW converts per-phase amps to 3-phase kW.
func AmpsKW(a float64) float64 { return 3 * Volts * a / 1000 }

// Ceiling is the per-slot EV ceiling in amps (whole amps, floored), from
// the non-EV forecast per phase; 0 when below MinA.
func (p Params) Ceiling(t time.Time, hwOn bool) float64 {
	a, b, c := p.HouseBaseA, p.HouseBaseA, p.HouseBaseA+p.HPA
	if hwOn && p.heaterWindow(t) {
		a += p.HeaterA
		b += p.HeaterA
	}
	head := math.Min(p.BudgetA-a, math.Min(p.BudgetA-b, p.BudgetA-c))
	head = math.Floor(head)
	if head > p.MaxA {
		head = p.MaxA
	}
	if head < p.MinA {
		return 0
	}
	return head
}

// ClampCeiling turns a headroom in amps into a usable per-slot ceiling:
// floored, at most MaxA, and 0 when below MinA.
func (p Params) ClampCeiling(a float64) float64 {
	a = math.Floor(a)
	if a > p.MaxA {
		a = p.MaxA
	}
	if a < p.MinA {
		return 0
	}
	return a
}

func (p Params) heaterWindow(t time.Time) bool {
	h := t.In(p.Loc).Hour()
	return h >= p.HeaterFromH && h < p.HeaterToH
}

// Plan builds the charging plan. Pure: no clock, no I/O.
func Plan(in Input, p Params) Result {
	need := math.Max(0, p.NeedKWh-in.DeliveredKWh)
	res := Result{NeedKWh: need}
	known := map[int64]float64{}
	for _, pr := range in.Prices {
		if pr.Dur <= 0 {
			continue
		}
		for k := 0; k < int(pr.Dur/quarter); k++ {
			known[pr.Start.Add(time.Duration(k)*quarter).Unix()] = pr.SEK
		}
	}
	type cand struct {
		i      int
		start  time.Time
		amps   float64
		price  float64
		priced bool
	}
	var all []cand // every quarter now..departure, in order
	for t := in.Now.Truncate(quarter); t.Before(in.Departure); t = t.Add(quarter) {
		on := true
		if in.HotWaterOn != nil {
			on = in.HotWaterOn(t)
		}
		c := cand{i: len(all), start: t, amps: p.Ceiling(t, on)}
		if in.CeilingA != nil {
			if v, ok := in.CeilingA(t); ok {
				c.amps = p.ClampCeiling(v)
			}
		}
		if sek, ok := known[t.Unix()]; ok {
			c.priced, c.price = true, p.Price.AllInOre(sek)
		} else {
			res.PricesPartial = true
		}
		all = append(all, c)
	}
	if need <= 0 {
		res.Feasible = true
		return res
	}
	slotOf := func(c cand) Slot {
		return Slot{Start: c.start, Amps: c.amps, KW: AmpsKW(c.amps), PriceOre: c.price, Priced: c.priced}
	}
	var usable []cand
	pricedKWh, usableKWh := 0.0, 0.0
	for _, c := range all {
		if c.amps > 0 {
			usable = append(usable, c)
			usableKWh += AmpsKW(c.amps) * 0.25
			if c.priced {
				pricedKWh += AmpsKW(c.amps) * 0.25
			}
		}
	}
	chosen := map[int]bool{}
	// Tomorrow's prices are not in yet and the time left still covers the
	// need at the ceiling: choose from the priced slots only, leave the
	// remainder unplanned and wait for the prices (not deadline mode).
	res.AwaitingPrices = res.PricesPartial && usableKWh > need
	if pricedKWh < need && !res.AwaitingPrices {
		// Backstop: every remaining usable slot at its ceiling.
		res.DeadlineMode = true
		for _, c := range usable {
			chosen[c.i] = true
		}
	} else {
		var priced []cand
		for _, c := range usable {
			if c.priced {
				priced = append(priced, c)
			}
		}
		sort.SliceStable(priced, func(i, j int) bool {
			if priced[i].price != priced[j].price {
				return priced[i].price < priced[j].price
			}
			return priced[i].start.Before(priced[j].start)
		})
		got := 0.0
		for _, c := range priced {
			if got >= need {
				break
			}
			chosen[c.i] = true
			got += AmpsKW(c.amps) * 0.25
		}
		// Merge gaps shorter than MinBlock between chosen runs.
		minQ := int(p.MinBlock / quarter)
		for i := 0; i < len(all); {
			if chosen[all[i].i] {
				i++
				continue
			}
			j := i
			for j < len(all) && !chosen[all[j].i] {
				j++
			}
			if i > 0 && j < len(all) && j-i < minQ {
				ok := true
				for k := i; k < j; k++ {
					if all[k].amps <= 0 {
						ok = false
					}
				}
				if ok {
					for k := i; k < j; k++ {
						chosen[all[k].i] = true
					}
				}
			}
			i = j
		}
	}
	for _, c := range all {
		if !chosen[c.i] {
			continue
		}
		s := slotOf(c)
		res.Slots = append(res.Slots, s)
		res.PlannedKWh += s.KWh()
		if s.Priced {
			res.CostKr += s.KWh() * s.PriceOre / 100
		}
	}
	res.Feasible = res.PlannedKWh >= need-1e-9
	if res.AwaitingPrices {
		res.UnplannedKWh = math.Max(0, need-res.PlannedKWh)
	}
	return res
}

// CostOfKWh prices kWh of charging as the plan would have bought it: the
// cheapest priced usable slots between from and departure, at their
// ceilings. Used to score a finished session (hindsight, full prices).
func CostOfKWh(kwh float64, from, departure time.Time, prices []planner.Price, hwOn func(time.Time) bool, p Params) (kr float64, finished bool) {
	q := p
	q.NeedKWh = kwh
	r := Plan(Input{Now: from, Departure: departure, Prices: prices, HotWaterOn: hwOn}, q)
	return r.CostKr, r.Feasible
}
