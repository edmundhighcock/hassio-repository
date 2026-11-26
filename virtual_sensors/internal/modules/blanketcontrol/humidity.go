package blanketcontrol

import (
	"math"
	"time"
)

// Magnus formula (WMO/Sonntag constants), valid about -45..60 deg C.
const (
	magA = 17.62
	magB = 243.12
)

// satPressure is the saturation vapour pressure in hPa at t deg C.
func satPressure(t float64) float64 { return 6.112 * math.Exp(magA*t/(magB+t)) }

// DewPoint is the dew point (deg C) of air at t deg C and rh percent.
func DewPoint(t, rh float64) float64 {
	g := math.Log(rh/100) + magA*t/(magB+t)
	return magB * g / (magA - g)
}

// AbsHumidity is the water content of the air in g/m3.
func AbsHumidity(t, rh float64) float64 {
	return 216.7 * (rh / 100 * satPressure(t)) / (273.15 + t)
}

// RHAt is the relative humidity (percent) the same air would show at ref deg
// C: the moisture is unchanged, only the temperature differs. Raw RH falls
// about 5-6 points per degree of heating with no moisture lost, so alarms
// use this, never raw RH.
func RHAt(t, rh, ref float64) float64 {
	return 100 * (rh / 100 * satPressure(t)) / satPressure(ref)
}

// alarm is one alarm episode tracker: the condition must hold for `after`
// before it fires (once), and recovers when it stops holding.
type alarm struct {
	Since  time.Time `json:"since,omitempty"`
	Active bool      `json:"active,omitempty"`
}

func (a *alarm) step(cond bool, now time.Time, after time.Duration) (fired, cleared bool) {
	if cond {
		if a.Since.IsZero() {
			a.Since = now
		}
		if !a.Active && now.Sub(a.Since) >= after {
			a.Active = true
			return true, false
		}
		return false, false
	}
	a.Since = time.Time{}
	if a.Active {
		a.Active = false
		return false, true
	}
	return false, false
}
