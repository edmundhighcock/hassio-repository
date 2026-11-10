package hpcontrol

import (
	"math"
	"time"
)

const (
	gridStep = 0.1
	gridHi   = 26.0
	bigM     = 1000.0 // kr per deg C of floor or bedroom-cap breach: hard in all but infeasible cases
	// MinHorizonSlots is the shortest horizon planned (24 h); missing prices
	// beyond the known ones are filled with their mean and flagged.
	MinHorizonSlots = 96
	// MaxHorizonSlots is the longest (36 h).
	MaxHorizonSlots = 144
)

// Input is everything the pure planner needs.
type Input struct {
	Now     time.Time         // quarter-aligned slot start of the first slot
	T0      float64           // house mean now
	Tout    float64           // outdoor temperature, held over the horizon (v1)
	Min     float64           // floor
	Desired float64           // desired mean
	Prices  map[int64]float64 // quarter start (unix) -> all-in kr/kWh
	Prev    Level             // level in force now (Off if unknown); changing it costs SwitchPenaltyKr
}

// Slot is one planned 15-minute slot.
type Slot struct {
	Start    time.Time
	Level    Level
	T        float64 // house mean at slot start
	TEnd     float64 // predicted at slot end
	PriceKr  float64 // all-in kr/kWh
	KWh      float64
	CostKr   float64
	Priced   bool // false: price extrapolated
	BedroomT float64
}

// Result is a plan.
type Result struct {
	Slots         []Slot
	CostKr        float64
	KWh           float64
	MeanT         float64 // mean of the house temperature over the first 24 h of the plan
	MinT          float64
	FloorAtRisk   bool
	Extrapolated  bool
	NoPrices      bool
	RuntimeMicros int64
}

// Plan runs the DP. Pure: no clock, no I/O.
func Plan(in Input, p Params) Result {
	start := in.Now.Truncate(quarter)
	// Horizon: through the last known price, at least 24 h, at most 36 h.
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
	var n int
	for _, v := range in.Prices {
		sum += v
		n++
	}
	mean := sum / float64(n)
	slotPrice := make([]float64, K)
	priced := make([]bool, K)
	ext := false
	for k := 0; k < K; k++ {
		if v, ok := in.Prices[start.Add(time.Duration(k)*quarter).Unix()]; ok {
			slotPrice[k], priced[k] = v, true
		} else {
			slotPrice[k] = mean
			ext = true
		}
	}

	lo := in.Min - 1
	hi := gridHi
	if in.Desired+2 > hi {
		hi = in.Desired + 2
	}
	N := int(math.Round((hi-lo)/gridStep)) + 1
	mod := p.Model

	night := make([]bool, K)
	eve := make([]bool, K)
	for k := 0; k < K; k++ {
		t := start.Add(time.Duration(k) * quarter)
		night[k], eve[k] = p.IsNight(t), p.IsEvening(t)
	}

	// slot evaluates a transition: next temperature (clamped to the grid)
	// and the slot's cost, including penalties.
	slot := func(k int, prev, l Level, t float64) (float64, float64) {
		tn := mod.Step(t, l, in.Tout)
		c := slotPrice[k] * p.PowerW(l) / 1000 * dtH
		if tn < in.Min {
			c += bigM * (in.Min - tn)
		}
		if night[k] {
			if b := tn + mod.BedroomOffset; b > p.BedroomCap {
				c += bigM * (b - p.BedroomCap)
			}
		}
		if l != prev {
			c += p.SwitchPenaltyKr
		}
		if eve[k] && l != Off {
			c += p.EveningPenKr
		}
		d := tn - in.Desired
		c += p.Lambda * d * d
		if tn < lo {
			tn = lo
		} else if tn > hi {
			tn = hi
		}
		return tn, c
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

	// V[k][prev][i]: cost to go from slot k, given the level in force
	// before it (prev) and the grid temperature i.
	V := make([][3][]float64, K+1)
	for k := range V {
		for l := 0; l < 3; l++ {
			V[k][l] = make([]float64, N)
		}
	}
	for i := 0; i < N; i++ {
		t := lo + float64(i)*gridStep
		d := t - in.Desired
		v := 20 * p.Lambda * d * d
		if t < in.Min {
			v += bigM * (in.Min - t)
		}
		for l := 0; l < 3; l++ {
			V[K][l][i] = v
		}
	}
	for k := K - 1; k >= 0; k-- {
		for prev := Off; prev <= Boost; prev++ {
			for i := 0; i < N; i++ {
				t := lo + float64(i)*gridStep
				best := math.Inf(1)
				for l := Off; l <= Boost; l++ {
					tn, c := slot(k, prev, l, t)
					if v := c + interp(V[k+1][l], tn); v < best {
						best = v
					}
				}
				V[k][prev][i] = best
			}
		}
	}

	// Forward pass with the continuous temperature.
	res := Result{Extrapolated: ext, MinT: math.Inf(1)}
	t := math.Min(math.Max(in.T0, lo), hi)
	prev := in.Prev
	var tsum float64
	var tn24 int
	for k := 0; k < K; k++ {
		bestL, bestV := Off, math.Inf(1)
		for l := Off; l <= Boost; l++ {
			nt, c := slot(k, prev, l, t)
			if v := c + interp(V[k+1][l], nt); v < bestV-1e-12 {
				bestV, bestL = v, l
			}
		}
		raw := mod.Step(t, bestL, in.Tout)
		kwh := p.PowerW(bestL) / 1000 * dtH
		s := Slot{Start: start.Add(time.Duration(k) * quarter), Level: bestL, T: t, TEnd: raw,
			PriceKr: slotPrice[k], KWh: kwh, CostKr: kwh * slotPrice[k], Priced: priced[k], BedroomT: raw + mod.BedroomOffset}
		res.Slots = append(res.Slots, s)
		res.CostKr += s.CostKr
		res.KWh += kwh
		if raw < res.MinT {
			res.MinT = raw
		}
		if k < 96 {
			tsum += raw
			tn24++
		}
		t = math.Min(math.Max(raw, lo), hi)
		prev = bestL
	}
	if tn24 > 0 {
		res.MeanT = tsum / float64(tn24)
	}
	res.FloorAtRisk = res.MinT < in.Min-0.05
	return res
}
