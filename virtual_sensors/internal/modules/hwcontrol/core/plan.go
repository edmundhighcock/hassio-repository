package core

import (
	"sort"
	"time"
)

// Plan is the only thing the core knows about planning (PLAN §4.2): a
// list of PERMITTED (not mandatory) OFF periods with a validity time. Why
// is opaque to the core. The planner lives in a separate package that the
// core never imports.
type Plan struct {
	PlannerID   string
	GeneratedAt time.Time
	ValidUntil  time.Time
	OffPeriods  []OffPeriod
	// DefaultOff marks a plan whose OffPeriods are the COMPLEMENT of a few
	// planned heating windows (planner v2): the switch is permitted OFF
	// everywhere except in Windows. The core still enforces every safety
	// rule; this only changes how the heater schedule is reported
	// (Schedule) and lets the core tag reheats with the window they ran in.
	DefaultOff bool
	// Windows are the planned heating windows (informational, kept by the
	// core across restarts so the planner and the reheat log can use them).
	// Recently ended windows are included.
	Windows []PlanWindow
	// Extra is published on the plan sensor as extra attributes.
	Extra map[string]any
}

// PlanWindow is one planned heating (switch ON) window of a DefaultOff plan.
type PlanWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Kind  string    `json:"kind"`            // full | topup | catchup | evening
	Ore   float64   `json:"ore,omitempty"`   // mean all-in öre/kWh over the window
	Early bool      `json:"early,omitempty"` // ended early: the tank was full
}

// ReheatRec is one finished genuine reheat (§4.4), kept for duration
// learning (planner v2). Cut: it ended because the controller switched OFF.
type ReheatRec struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Cut   bool      `json:"cut,omitempty"`
	Kind  string    `json:"kind,omitempty"` // window kind it started in, "" if none
}

// Duration is the on-time.
func (r ReheatRec) Duration() time.Duration { return r.End.Sub(r.Start) }

// MaxReheatLog bounds the persisted reheat log.
const MaxReheatLog = 60

// OffPeriod is one permitted OFF window.
type OffPeriod struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Why   string    `json:"why,omitempty"`
}

// MaxPeriod is the longest period a valid plan may contain.
const MaxPeriod = 24 * time.Hour

// Valid reports whether p may be used at now: not expired, every period
// End > Start, no longer than 24 h, sorted and non-overlapping. A plan that
// fails is treated as no plan (no OFF).
func (p *Plan) Valid(now time.Time) bool {
	if p == nil || p.ValidUntil.IsZero() || !now.Before(p.ValidUntil) {
		return false
	}
	for i, op := range p.OffPeriods {
		if !op.End.After(op.Start) || op.End.Sub(op.Start) > MaxPeriod {
			return false
		}
		if i > 0 && op.Start.Before(p.OffPeriods[i-1].End) {
			return false
		}
	}
	return sort.SliceIsSorted(p.OffPeriods, func(i, j int) bool { return p.OffPeriods[i].Start.Before(p.OffPeriods[j].Start) })
}

// PeriodAt returns the period covering now (Start <= now < End) of a valid
// plan.
func (p *Plan) PeriodAt(now time.Time) (OffPeriod, bool) {
	if !p.Valid(now) {
		return OffPeriod{}, false
	}
	for _, op := range p.OffPeriods {
		if !now.Before(op.Start) && now.Before(op.End) {
			return op, true
		}
	}
	return OffPeriod{}, false
}

// Facts are read-only facts the core offers a planner (§4.2). The core
// never depends on how a planner uses them.
type Facts struct {
	LastFinishedReheatEnd   time.Time // zero if unknown
	LastFinishedReheatStart time.Time
	TankHeadroom            time.Duration // until the tank clock forbids OFF; 0 if unknown/exceeded
	OffLimitSinceReheatEnd  time.Duration
	// CurrentOff is the OFF period running now (nil if none). It is frozen
	// in the core (review fix 4); a planner should keep it so rolling
	// re-plans do not cut it short.
	CurrentOff *OffPeriod

	// For planner v2 (read-only; the core never depends on their use).
	Reheats        []ReheatRec  // finished genuine reheats, oldest first
	Windows        []PlanWindow // the active DefaultOff plan's windows, as last committed
	CycleOpen      bool         // a detector cycle (heater step) is open now
	CycleOpenSince time.Time
	MovesLast24h   int
	MovesLastHour  int
	Live           bool
}
