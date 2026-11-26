// Package blanketcontrol is the "blanket_control" module: it keeps dahlia
// tubers in an unheated shed between a floor and a hard maximum with an
// electric blanket that can only be toggled by a Bluetooth button-pusher.
// plan.go is the pure planner: a 15-minute-slot dynamic programme over the
// storage temperature (hp_control's 1R1C Model and grid DP, with on-blocks,
// a minimum on-block, the 2 h 50 refresh and a per-day start budget).
package blanketcontrol

import (
	"math"
	"time"

	"virtual_sensors/internal/modules/hpcontrol"
	"virtual_sensors/internal/modules/hwcontrol/planner"
)

const (
	quarter  = 15 * time.Minute
	gridStep = 0.1
	bigM     = 1000.0 // kr per deg C below the planning floor
	// WindowSlots is the on-window in slots: 11 x 15 min = 2 h 45, inside the
	// blanket's own 3 h cut-off with the executor's 2 h 50 window.
	WindowSlots = 11
	// MinHorizonSlots / MaxHorizonSlots: 24 h .. 36 h, as hp_control.
	MinHorizonSlots = 96
	MaxHorizonSlots = 144
)

// Params are the planner's tunables.
type Params struct {
	Model          hpcontrol.Model // U, Boost5 = H (K/h at full draw), Gain = G
	PowerW         float64         // draw while heating
	FloorC         float64         // the temperature the plan must keep (plus margin)
	TargetC        float64         // aim (lower in the band is cheaper and kinder)
	MaxC           float64         // hard cap: never heat at or above it
	MarginC        float64         // added to the floor for planning
	MinOnSlots     int
	MaxStartsDay   int
	StartPenaltyKr float64 // per start, so blocks are long
	TargetLambda   float64 // kr per K^2 per slot above the target
	Price          planner.Config
	Loc            *time.Location
}

// DefaultParams: conservative model (leaky, weak heater) and wide margins.
func DefaultParams() Params {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		loc = time.UTC
	}
	return Params{Model: DefaultModel(), PowerW: 70, FloorC: 5, TargetC: 6, MaxC: 8, MarginC: 0.5,
		MinOnSlots: 4, MaxStartsDay: 12, StartPenaltyKr: 0.3, TargetLambda: 0.002,
		Price: planner.DefaultConfig(), Loc: loc}
}

// DefaultModel is the starting model: U 0.30 /h (tau 3.3 h), H 1.5 K/h at
// full draw, G 0. Conservative: a leakier shed and a weaker blanket than
// likely, so the first plans heat more, not less.
func DefaultModel() hpcontrol.Model { return hpcontrol.Model{U: 0.30, Boost5: 1.5, Gain: 0} }

// Input is everything the pure planner needs.
type Input struct {
	Now      time.Time
	T0       float64
	Tout     float64
	Forecast map[int64]float64
	Prices   map[int64]float64 // quarter start (unix) -> all-in kr/kWh
	// InitAge: slots already on in the current window (0: off). InitMature:
	// the minimum on-block is already served.
	InitAge     int
	InitMature  bool
	StartsToday int
}

// Slot is one planned 15-minute slot.
type Slot struct {
	Start    time.Time
	On       bool
	NewStart bool // a start (cycle+press) at the beginning of this slot
	T, TEnd  float64
	PriceKr  float64
	KWh      float64
	CostKr   float64
	Priced   bool
	Tout     float64
}

// Result is a plan.
type Result struct {
	Slots        []Slot
	CostKr, KWh  float64
	MinT, MaxT   float64
	FloorAtRisk  bool
	Extrapolated bool
	NoPrices     bool
	CapBound     bool   // the start budget could not be met without breaking the floor
	Starts       [3]int // planned starts on today, tomorrow, the day after (local)
	PlanFloor    float64
}

type sidx struct{ m, w int }

func (s sidx) n() int           { return 1 + (s.m - 1) + s.w }
func (s sidx) young(a int) int  { return a }
func (s sidx) mature(a int) int { return s.m - 1 + a }

// trans returns the next state, whether it is a start, and whether the move is allowed.
func (s sidx) trans(cur int, on bool) (next int, start, ok bool) {
	switch {
	case cur == 0:
		if !on {
			return 0, false, true
		}
		if s.m > 1 {
			return s.young(1), true, true
		}
		return s.mature(1), true, true
	case cur < s.m: // young age cur
		if !on {
			return 0, false, false
		}
		if cur+1 < s.m {
			return s.young(cur + 1), false, true
		}
		return s.mature(cur + 1), false, true
	default:
		a := cur - (s.m - 1)
		if !on {
			return 0, false, true
		}
		if a < s.w {
			return s.mature(a + 1), false, true
		}
		return s.mature(1), true, true
	}
}

func (p Params) day(start, t time.Time) int {
	a, b := start.In(p.Loc), t.In(p.Loc)
	d := int(time.Date(b.Year(), b.Month(), b.Day(), 12, 0, 0, 0, p.Loc).Sub(time.Date(a.Year(), a.Month(), a.Day(), 12, 0, 0, 0, p.Loc)).Hours()/24 + 0.5)
	if d < 0 {
		d = 0
	}
	if d > 2 {
		d = 2
	}
	return d
}

// Plan runs the DP. Pure: no clock, no I/O.
func Plan(in Input, p Params) Result {
	start := in.Now.Truncate(quarter)
	last := -1
	for k := 0; k < MaxHorizonSlots; k++ {
		if _, ok := in.Prices[start.Add(time.Duration(k)*quarter).Unix()]; ok {
			last = k
		}
	}
	if last < 0 {
		return Result{NoPrices: true}
	}
	K := last + 1
	if K < MinHorizonSlots {
		K = MinHorizonSlots
	}
	var sum float64
	for _, v := range in.Prices {
		sum += v
	}
	mean := sum / float64(len(in.Prices))
	price := make([]float64, K)
	priced := make([]bool, K)
	ext := false
	for k := 0; k < K; k++ {
		if v, ok := in.Prices[start.Add(time.Duration(k)*quarter).Unix()]; ok {
			price[k], priced[k] = v, true
		} else {
			price[k] = mean
			ext = true
		}
	}
	M := p.MinOnSlots
	if M < 1 {
		M = 1
	}
	if M > WindowSlots {
		M = WindowSlots
	}
	S := sidx{m: M, w: WindowSlots}
	ns := S.n()
	floor := p.FloorC + p.MarginC
	lo := floor - 2
	hi := p.MaxC + 1
	N := int(math.Round((hi-lo)/gridStep)) + 1
	mod := p.Model
	tout := make([]float64, K)
	dayOf := make([]int, K)
	for k := 0; k < K; k++ {
		ts := start.Add(time.Duration(k) * quarter)
		tout[k] = hpcontrol.OutdoorAt(in.Forecast, in.Tout, ts.Add(quarter/2))
		dayOf[k] = p.day(start, ts)
	}
	step := func(k int, t float64, on bool) float64 {
		off := mod.Step(t, hpcontrol.Off, tout[k])
		if !on {
			return off
		}
		// Saturation: the blanket's thermostat (and the hard cap) stop the
		// benefit at MaxC; heating never lowers the off-result.
		return math.Min(mod.Step(t, hpcontrol.Boost, tout[k]), math.Max(p.MaxC, off))
	}
	pen := [3]float64{p.StartPenaltyKr, p.StartPenaltyKr, p.StartPenaltyKr}
	slotKWh := p.PowerW / 1000 * 0.25
	allowed := func(cur int, on bool, t float64) (int, bool, bool) {
		if on && t >= p.MaxC {
			return 0, false, false // hard cap: never heat at or above MaxC
		}
		if !on && t >= p.MaxC && cur != 0 {
			return 0, false, true // the cap beats the minimum on-block
		}
		return S.trans(cur, on)
	}
	cost := func(k int, cur int, on bool, t float64) (tn, c float64, next int, st, ok bool) {
		next, st, ok = allowed(cur, on, t)
		if !ok {
			return
		}
		tn = step(k, t, on)
		if on {
			c += price[k] * slotKWh
		}
		if st {
			c += pen[dayOf[k]]
		}
		if tn < floor {
			c += bigM * (floor - tn)
		}
		if tn > p.TargetC {
			d := tn - p.TargetC
			c += p.TargetLambda * d * d
		}
		return
	}
	interp := func(v []float64, t float64) float64 {
		x := (t - lo) / gridStep
		i := int(x)
		if i >= N-1 {
			return v[N-1]
		}
		if i < 0 {
			return v[0]
		}
		f := x - float64(i)
		return v[i]*(1-f) + v[i+1]*f
	}
	clamp := func(t float64) float64 { return math.Min(math.Max(t, lo), hi) }

	budget := [3]int{p.MaxStartsDay - in.StartsToday, p.MaxStartsDay, p.MaxStartsDay}
	for i := range budget {
		if budget[i] < 0 {
			budget[i] = 0
		}
	}
	var res Result
	for iter := 0; iter < 12; iter++ {
		V := make([][][]float64, K+1)
		for k := range V {
			V[k] = make([][]float64, ns)
			for s := range V[k] {
				V[k][s] = make([]float64, N)
			}
		}
		for i := 0; i < N; i++ {
			t := lo + float64(i)*gridStep
			v := 0.0
			if t < floor {
				v = bigM * (floor - t)
			}
			for s := 0; s < ns; s++ {
				V[K][s][i] = v
			}
		}
		for k := K - 1; k >= 0; k-- {
			for s := 0; s < ns; s++ {
				for i := 0; i < N; i++ {
					t := lo + float64(i)*gridStep
					best := math.Inf(1)
					for _, on := range []bool{false, true} {
						tn, c, nx, _, ok := cost(k, s, on, t)
						if !ok {
							continue
						}
						if v := c + interp(V[k+1][nx], clamp(tn)); v < best {
							best = v
						}
					}
					if math.IsInf(best, 1) {
						best = 1e9
					}
					V[k][s][i] = best
				}
			}
		}
		// Forward pass on the continuous temperature.
		res = Result{Extrapolated: ext, MinT: math.Inf(1), MaxT: math.Inf(-1), PlanFloor: floor}
		s := 0
		if in.InitAge > 0 {
			a := in.InitAge
			if a > WindowSlots {
				a = WindowSlots
			}
			if in.InitMature || a >= M {
				s = S.mature(a)
			} else {
				s = S.young(a)
			}
		}
		raw := in.T0
		for k := 0; k < K; k++ {
			bestOn, bestV := false, math.Inf(1)
			var bnext int
			var bst bool
			for _, on := range []bool{false, true} {
				tn, c, nx, st, ok := cost(k, s, on, raw)
				if !ok {
					continue
				}
				if v := c + interp(V[k+1][nx], clamp(tn)); v < bestV-1e-12 {
					bestV, bestOn, bnext, bst = v, on, nx, st
				}
			}
			if math.IsInf(bestV, 1) { // cannot happen: off is always allowed from off/mature; a young state under the cap is handled by allowed()
				bestOn, bnext, bst = false, 0, false
			}
			te := step(k, raw, bestOn)
			kwh := 0.0
			if bestOn {
				kwh = slotKWh
			}
			sl := Slot{Start: start.Add(time.Duration(k) * quarter), On: bestOn, NewStart: bst, T: raw, TEnd: te,
				PriceKr: price[k], KWh: kwh, CostKr: kwh * price[k], Priced: priced[k], Tout: tout[k]}
			res.Slots = append(res.Slots, sl)
			res.CostKr += sl.CostKr
			res.KWh += kwh
			if bst {
				res.Starts[dayOf[k]]++
			}
			res.MinT = math.Min(res.MinT, te)
			res.MaxT = math.Max(res.MaxT, te)
			raw, s = te, bnext
		}
		over := false
		for d := 0; d < 3; d++ {
			if res.Starts[d] > budget[d] {
				pen[d] = math.Max(pen[d], 0.3) * 3
				over = true
			}
		}
		if !over {
			break
		}
		res.CapBound = true
	}
	for d := 0; d < 3; d++ {
		if res.Starts[d] > budget[d] {
			res.CapBound = true
		}
	}
	res.FloorAtRisk = res.MinT < floor-0.05
	return res
}
