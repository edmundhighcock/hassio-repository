package evcontrol

import "math"

// GuardIn is one set of synthetic or live readings.
type GuardIn struct {
	GridW       float64 // total house import, W (includes the EV)
	EVkW        float64 // loaver_power, kW
	HPW         float64 // heat pump plug, W
	ImmersionOn bool    // hot water says the 3 kW heater is drawing
}

// GuardOut is the per-phase estimate and the would-be EV limit.
type GuardOut struct {
	HouseA   [3]float64 // non-EV amps on phases A, B, C
	EVA      float64    // EV amps per phase
	PhaseA   [3]float64 // house + EV
	LimitA   int        // floor(min headroom), clamped [0, MaxA]; 0 = would pause
	Breach   bool       // any estimated phase above the budget
	Headroom float64
}

// Guard estimates per-phase current (PLAN "Guard"): house amps =
// (grid W - EV W) / 230; immersion HeaterA on A and B if drawing; heat
// pump HPW/230 on C; the remainder split evenly over the three phases.
func Guard(in GuardIn, p Params) GuardOut {
	var o GuardOut
	evW := in.EVkW * 1000
	houseSum := math.Max(0, (in.GridW-evW)/Volts)
	var asg [3]float64
	if in.ImmersionOn {
		asg[0] += p.HeaterA
		asg[1] += p.HeaterA
	}
	asg[2] += math.Max(0, in.HPW) / Volts
	rest := houseSum - (asg[0] + asg[1] + asg[2])
	if rest < 0 {
		rest = 0
	}
	o.EVA = evW / (3 * Volts)
	min := math.Inf(1)
	for i := range asg {
		o.HouseA[i] = asg[i] + rest/3
		o.PhaseA[i] = o.HouseA[i] + o.EVA
		if o.PhaseA[i] > p.BudgetA {
			o.Breach = true
		}
		if h := p.BudgetA - o.HouseA[i]; h < min {
			min = h
		}
	}
	o.Headroom = min
	l := math.Floor(min)
	if l > p.MaxA {
		l = p.MaxA
	}
	if l < 0 {
		l = 0
	}
	o.LimitA = int(l)
	return o
}
