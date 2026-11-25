// Package evcontrol is the "ev_control" module: the planner and live
// executor for the Zoe's Easee charger (ham services/ev-planner/PLAN.md). It
// reads Home Assistant entities, plans the night's charging against the
// quarter-hour spot prices from the energy the charger can deliver, publishes
// what it would command, and scores the plan against what happened. Armed
// and live it decides only WHEN to charge (start/resume/pause); the charger
// and the Easee Equalizer decide how much. See exec.go.
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
	BudgetA    float64       // 19 per phase (the guard's budget; informational only since 0.6.17)
	FuseA      float64       // 20 per phase: the supply the deliverable estimate is made against
	Derate     float64       // 0.9: the charger and Equalizer rarely deliver all of the theoretical
	HouseBaseA float64       // 3 on every phase
	HeaterA    float64       // 7.5 on L1 and L2 while the immersion draws
	HPA        float64       // 7.4 on L2 (heat pump max)
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
	return Params{NeedKWh: 44, MinA: 6, MaxA: 16, BudgetA: 19, FuseA: 20, Derate: 0.9, HouseBaseA: 3, HeaterA: 7.5, HPA: 7.4,
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
	// LoadA (optional) is the house planner's forecast non-EV load per phase
	// (L1, L2, L3) in amps for a slot. ok=false (no forecast, or it does not
	// cover the slot) falls back to Params.StaticLoad.
	LoadA func(time.Time) ([3]float64, bool)
}

// Slot is one planned 15-minute charging slot. Amps is the current the
// charger is expected to carry (per phase on three phases, or the single
// phase), KW the deliverable power: the expected energy rate after the
// derate, the number the plan's energy is summed from.
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

// StaticLoad is the non-EV load per phase (L1, L2, L3) assumed when there is
// no house forecast: the base on each, the heat pump on L2, and the immersion
// heater on L1+L2 only while hot water's plan has it ON inside its usual
// window.
func (p Params) StaticLoad(t time.Time, hwOn bool) [3]float64 {
	l := [3]float64{p.HouseBaseA, p.HouseBaseA + p.HPA, p.HouseBaseA}
	if hwOn && p.heaterWindow(t) {
		l[0] += p.HeaterA
		l[1] += p.HeaterA
	}
	return l
}

// Deliverable is what the charger can deliver in a slot given the non-EV
// load per phase. The controller decides only WHEN to charge; the charger and
// the Equalizer decide how much, and the Equalizer backs the charger off, or
// switches it to a single phase, when a phase it uses is short. So the
// estimate is the better of the two ways the car can charge:
//
//	three phases: a3 = min over phases of (FuseA - load), capped MaxA, 0 below MinA
//	single phase on L3: a1 = FuseA - load(L3), capped MaxA, 0 below MinA
//
// amps is the expected current of the better one (whole amps), kw the power
// after the Derate. 0, 0 when neither is usable.
func (p Params) Deliverable(load [3]float64) (amps, kw float64) {
	fit := func(a float64) float64 {
		a = math.Floor(a)
		if a > p.MaxA {
			a = p.MaxA
		}
		if a < p.MinA {
			return 0
		}
		return a
	}
	a3 := fit(math.Min(p.FuseA-load[0], math.Min(p.FuseA-load[1], p.FuseA-load[2])))
	a1 := fit(p.FuseA - load[2])
	k3, k1 := AmpsKW(a3), Volts*a1/1000
	d := p.Derate
	if d <= 0 || d > 1 {
		d = 1
	}
	if k3 >= k1 {
		return a3, k3 * d
	}
	return a1, k1 * d
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
		kw     float64
		price  float64
		priced bool
	}
	var all []cand // every quarter now..departure, in order
	for t := in.Now.Truncate(quarter); t.Before(in.Departure); t = t.Add(quarter) {
		on := true
		if in.HotWaterOn != nil {
			on = in.HotWaterOn(t)
		}
		load := p.StaticLoad(t, on)
		if in.LoadA != nil {
			if l, ok := in.LoadA(t); ok {
				load = l
			}
		}
		c := cand{i: len(all), start: t}
		c.amps, c.kw = p.Deliverable(load)
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
		return Slot{Start: c.start, Amps: c.amps, KW: c.kw, PriceOre: c.price, Priced: c.priced}
	}
	var usable []cand
	pricedKWh, usableKWh := 0.0, 0.0
	for _, c := range all {
		if c.kw > 0 {
			usable = append(usable, c)
			usableKWh += c.kw * 0.25
			if c.priced {
				pricedKWh += c.kw * 0.25
			}
		}
	}
	chosen := map[int]bool{}
	// Tomorrow's prices are not in yet and the time left still covers the
	// need at the deliverable rate: choose from the priced slots only, leave the
	// remainder unplanned and wait for the prices (not deadline mode).
	res.AwaitingPrices = res.PricesPartial && usableKWh > need
	if pricedKWh < need && !res.AwaitingPrices {
		// Backstop: every remaining usable slot.
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
			got += c.kw * 0.25
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
					if all[k].kw <= 0 {
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
// deliverable power. Used to score a finished session (hindsight, full prices).
func CostOfKWh(kwh float64, from, departure time.Time, prices []planner.Price, hwOn func(time.Time) bool, p Params) (kr float64, finished bool) {
	q := p
	q.NeedKWh = kwh
	r := Plan(Input{Now: from, Departure: departure, Prices: prices, HotWaterOn: hwOn}, q)
	return r.CostKr, r.Feasible
}
