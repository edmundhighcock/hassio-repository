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
}

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
}
