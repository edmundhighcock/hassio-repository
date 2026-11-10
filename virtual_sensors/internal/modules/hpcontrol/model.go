// Package hpcontrol is the "hp_control" module: a planner for the heat pump
// and the Dyson fan (ham services/hp-planner/PLAN.md). It reads Home
// Assistant entities, plans 15-minute slots by dynamic programming over the
// comfort temperature (the mean of two room sensors out of the pump's
// airstream), and publishes what it WOULD do and how well its model
// predicts. SHADOW by default; with hp_mode live AND
// input_boolean.hp_controller_armed on, exec.go also drives the devices.
package hpcontrol

import (
	"math"
	"time"

	"virtual_sensors/internal/modules/hwcontrol/planner"
)

const (
	quarter = 15 * time.Minute
	dtH     = 0.25 // slot length in hours
)

// Level is the heat pump's command level for one slot.
type Level int

// The three v1 levels (PLAN "What it controls").
const (
	Off Level = iota
	Hold
	Boost
)

func (l Level) String() string { return [...]string{"off", "hold", "boost"}[l] }

// Model is the 0D house model (services/house-model). One state: the house
// mean temperature T (deg C). Per hour:
//
//	dT/dt = -U (T - Tout) + h(level, Tout) + Gain
//
// h(boost, Tout) = max(0, Boost5 + Slope (Tout - 5)); h(hold) = HoldFrac * h(boost).
// The bedroom is the comfort temperature + BedroomOffset.
type Model struct {
	U             float64 // loss coefficient, 1/h
	Boost5        float64 // h*COP at boost, 5 deg C outdoor, deg C/h
	Slope         float64 // change of boost rate per deg C of outdoor temperature, deg C/h per deg C
	HoldFrac      float64 // hold heating as a fraction of boost heating
	Gain          float64 // internal gain, deg C/h
	BedroomOffset float64 // bedroom minus house mean, deg C
}

// DefaultModel returns the fitted parameters: services/house-model
// RESULTS.md (2026-11-10), the "kitchen+bedroom" column, which is the
// default comfort sensor (the official house mean carries the heat-pump
// intake and Dyson sensors, ~2.4 K/kW high while the pump runs, so the
// "official mean" column is NOT used). 1R1C, free-run fit over 3.7 days:
// u 0.027 /h, a0 0.55 K/h per kW electrical (x 1.7 kW boost = 0.935 K/h),
// internal gain g 0.17 K/h per kW x the 0.30 kW median = 0.05 K/h; the
// bedroom is -0.2 K (pump on) to -0.46 K (off 2 h) below the kitchen+bedroom
// mean, so -0.3. The COP slope is not identifiable from the data
// (a1 = -0.00 +/- 0.01): it stays 0 until a cold spell pins it.
func DefaultModel() Model {
	return Model{
		U:             0.027, // RESULTS.md kitchen+bedroom u = 0.027 +/- 0.002 /h (tau 38 h)
		Boost5:        0.935, // RESULTS.md a0 0.55 K/h/kW x 1.7 kW (p_boost); extrapolated, the pump seldom ran >1.2 kW
		Slope:         0,     // RESULTS.md: a1 not identifiable (-0.00 +/- 0.01)
		HoldFrac:      0.41,  // 0.7 kW / 1.7 kW (heating is linear in electrical power in the fit)
		Gain:          0.05,  // RESULTS.md g 0.17 K/h/kW x 0.30 kW median internal power
		BedroomOffset: -0.3,  // RESULTS.md (4): -0.2 on, -0.46 off, vs kitchen+bedroom
	}
}

// Rate is the heating rate (deg C/h) of a level at outdoor temperature tout.
func (m Model) Rate(l Level, tout float64) float64 {
	b := math.Max(0, m.Boost5+m.Slope*(tout-5))
	switch l {
	case Boost:
		return b
	case Hold:
		return b * m.HoldFrac
	}
	return 0
}

// Step advances the house mean by one slot.
func (m Model) Step(t float64, l Level, tout float64) float64 {
	return t + dtH*(-m.U*(t-tout)+m.Rate(l, tout)+m.Gain)
}

// Params are the planner's tunables (PLAN "Options").
type Params struct {
	Model           Model
	PHoldW          float64 // electrical power at hold, W
	PBoostW         float64 // electrical power at boost, W
	MinDefault      float64 // used when the helper is missing
	DesiredDef      float64 // used when the helper is missing
	BedroomCap      float64
	NightStart      int // minutes after local midnight
	NightEnd        int
	EveStart        int
	EveEnd          int
	Lambda          float64 // kr per deg C^2 per slot around desired (0.05: replay-tuned, see TestReplayMorningPeak)
	EveningPenKr    float64 // kr per slot of running in the evening window
	SwitchPenaltyKr float64 // kr per change of level (every IR command beeps, D38)
	Dyson           DysonCaps
	Price           planner.Config
	Loc             *time.Location
}

// DefaultParams are PLAN's defaults. The model inside is PROVISIONAL.
func DefaultParams() Params {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		loc = time.UTC
	}
	return Params{Model: DefaultModel(), PHoldW: 700, PBoostW: 1700, MinDefault: 19.0, DesiredDef: 20.5,
		BedroomCap: 23, NightStart: 22 * 60, NightEnd: 7 * 60, EveStart: 17 * 60, EveEnd: 21 * 60,
		Lambda: 0.05, EveningPenKr: 3.0, SwitchPenaltyKr: 0.3, Dyson: DefaultDysonCaps(), Price: planner.DefaultConfig(), Loc: loc}
}

// PowerW is the electrical power of a level.
func (p Params) PowerW(l Level) float64 {
	switch l {
	case Boost:
		return p.PBoostW
	case Hold:
		return p.PHoldW
	}
	return 0
}

func minutesOfDay(t time.Time, loc *time.Location) int {
	l := t.In(loc)
	return l.Hour()*60 + l.Minute()
}

func inWindow(m, from, to int) bool {
	if from <= to {
		return m >= from && m < to
	}
	return m >= from || m < to
}

// IsNight reports whether t is in the night window (default 22:00-07:00).
func (p Params) IsNight(t time.Time) bool {
	return inWindow(minutesOfDay(t, p.Loc), p.NightStart, p.NightEnd)
}

// IsEvening reports whether t is in the evening window (default 17:00-21:00).
func (p Params) IsEvening(t time.Time) bool {
	return inWindow(minutesOfDay(t, p.Loc), p.EveStart, p.EveEnd)
}

// Presence is the resolved household presence.
type Presence int

// Presence values; Out and Away both mean nobody home.
const (
	Home Presence = iota
	Out
	Away
)

// Anyone reports whether someone is home.
func (p Presence) Anyone() bool { return p == Home }

// DysonCaps are the Dyson speed caps (PLAN "Dyson", D28), percent.
type DysonCaps struct {
	Night   int // 22:00-07:00 (cheap slots): 100
	DayHome int // 09:00-17:00 someone home: 60
	Evening int // 17:00-21:00: 50
	Away    int // nobody home: 100
	Other   int // remaining hours (07:00-09:00, 21:00-22:00): 100
}

// DefaultDysonCaps are PLAN's defaults.
func DefaultDysonCaps() DysonCaps {
	return DysonCaps{Night: 100, DayHome: 60, Evening: 50, Away: 100, Other: 100}
}

// DysonPct is the Dyson speed for a slot: 0 when the heat pump is off, else
// the cap for the time and presence, rounded down to a 10 % step.
func (p Params) DysonPct(t time.Time, l Level, pr Presence) int {
	if l == Off {
		return 0
	}
	c := p.Dyson
	v := c.Other
	switch {
	case !pr.Anyone():
		v = c.Away
	case p.IsNight(t):
		v = c.Night
	case p.IsEvening(t):
		v = c.Evening
	default:
		if m := minutesOfDay(t, p.Loc); m >= 9*60 && m < 17*60 {
			v = c.DayHome
		}
	}
	if v > 100 {
		v = 100
	}
	if v < 0 {
		v = 0
	}
	return v / 10 * 10
}
