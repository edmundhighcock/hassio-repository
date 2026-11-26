package blanketcontrol

import (
	"math"
	"time"

	"virtual_sensors/internal/modules/hpcontrol"
)

// Rec is one closed 15-minute slot of history for the online fit: the
// storage temperature at the slot's end (Missing if unknown), the mean
// outdoor temperature and the mean draw as a fraction of the nominal power.
type Rec struct {
	T  int64   `json:"t"`  // slot start, unix
	Tm float64 `json:"tm"` // storage temperature at slot end; Missing if none
	To float64 `json:"to"` // mean outdoor temperature
	U  float64 `json:"u"`  // mean draw / nominal power, 0..1
}

// Missing marks an unknown temperature in a Rec.
const Missing = -999.0

// MaxRecs is the fit window: 5 days of quarters.
const MaxRecs = 480

// fitRow is one regression row: dT/dt (K/h) against [-(T-Tout), u, 1].
type fitRow struct {
	x [3]float64
	y float64
}

// rows builds 1 h-difference rows from consecutive valid records.
func rows(recs []Rec) []fitRow {
	var out []fitRow
	for i := 4; i < len(recs); i++ {
		ok := recs[i].T-recs[i-4].T == int64(time.Hour.Seconds())
		var tsum, osum, usum float64
		for j := i - 4; j <= i && ok; j++ {
			if recs[j].Tm == Missing {
				ok = false
			}
			tsum += recs[j].Tm
			if j > i-4 {
				osum += recs[j].To
				usum += recs[j].U
			}
		}
		if !ok {
			continue
		}
		tm := tsum / 5
		out = append(out, fitRow{x: [3]float64{-(tm - osum/4), usum / 4, 1}, y: recs[i].Tm - recs[i-4].Tm})
	}
	return out
}

// Fit is the model fitted online plus its prediction error.
type Fit struct {
	U, H, G float64
	N       int     // rows used
	MAE     float64 // mean |error| of the 1 h-ahead temperature, last 24 h, deg C
	Bias    float64
	Fitted  bool
}

// Prior is the default model and how far each parameter may be trusted to
// differ from it (1 sigma), in the units of the parameter.
var priorSigma = [3]float64{0.15, 1.0, 0.1}

const noiseSigma = 0.5 // K/h, a 1 h difference of a 0.1-0.5 K sensor

// FitModel is a windowed ridge least-squares fit of dT/dt = -U (T - Tout) +
// H u + G toward the defaults: with little data it stays at them, with a
// few days of data the evidence dominates. Parameters are clamped to
// physically sane ranges. u is the measured draw (thermostat included).
func FitModel(recs []Rec, def hpcontrol.Model) Fit {
	th0 := [3]float64{def.U, def.Boost5, def.Gain}
	rs := rows(recs)
	f := Fit{U: def.U, H: def.Boost5, G: def.Gain, N: len(rs)}
	if len(rs) < 24 {
		return f
	}
	var A [3][4]float64
	for i := 0; i < 3; i++ {
		lam := noiseSigma * noiseSigma / (priorSigma[i] * priorSigma[i])
		A[i][i] += lam
		A[i][3] += lam * th0[i]
	}
	for _, r := range rs {
		for i := 0; i < 3; i++ {
			for j := 0; j < 3; j++ {
				A[i][j] += r.x[i] * r.x[j]
			}
			A[i][3] += r.x[i] * r.y
		}
	}
	th, ok := solve3(A)
	if !ok {
		return f
	}
	f.U = math.Min(2, math.Max(0.02, th[0]))
	f.H = math.Min(10, math.Max(0.1, th[1]))
	f.G = math.Min(0.3, math.Max(-0.3, th[2]))
	f.Fitted = true
	// error over the last 24 h of rows
	n := len(rs)
	if n > 96 {
		rs = rs[n-96:]
	}
	var abs, sum float64
	for _, r := range rs {
		e := r.y - (f.U*r.x[0] + f.H*r.x[1] + f.G)
		abs += math.Abs(e)
		sum += e
	}
	f.MAE, f.Bias = abs/float64(len(rs)), sum/float64(len(rs))
	return f
}

// solve3 solves a 3x3 augmented system by Gaussian elimination.
func solve3(a [3][4]float64) ([3]float64, bool) {
	for c := 0; c < 3; c++ {
		p := c
		for r := c + 1; r < 3; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		if math.Abs(a[p][c]) < 1e-12 {
			return [3]float64{}, false
		}
		a[c], a[p] = a[p], a[c]
		for r := c + 1; r < 3; r++ {
			f := a[r][c] / a[c][c]
			for k := c; k < 4; k++ {
				a[r][k] -= f * a[c][k]
			}
		}
	}
	var x [3]float64
	for i := 2; i >= 0; i-- {
		s := a[i][3]
		for j := i + 1; j < 3; j++ {
			s -= a[i][j] * x[j]
		}
		x[i] = s / a[i][i]
	}
	return x, true
}
