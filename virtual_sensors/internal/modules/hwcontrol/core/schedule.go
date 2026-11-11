package core

import "time"

// ScheduleHorizon is how far ahead HeaterSchedule looks.
const ScheduleHorizon = 24 * time.Hour

// Heater draw for a shared per-phase budget: about 3 kW, split across
// phases A and B (nominal; the controller does not measure it per phase).
const (
	HeaterNominalW = 3000
	HeaterPhases   = "AB"
)

// HeaterState is the heater's present state as the controller sees it.
type HeaterState string

const (
	HeaterOn      HeaterState = "on"      // cover at or above OnPos
	HeaterOff     HeaterState = "off"     // cover at or below OffPos
	HeaterUnknown HeaterState = "unknown" // position unknown or mid-travel
)

// HeaterWindow is one span of the schedule. Off windows are PERMITTED
// OFF periods of the plan (the controller may still stay ON, §4.2); On
// windows are everything else, including any time the plan is absent.
type HeaterWindow struct {
	Start time.Time
	End   time.Time
	On    bool
}

// HeaterSchedule is a read-only snapshot for a consumer sharing a power
// budget. For a worst-case budget, treat the heater as drawing NominalW
// at any time except where it is certainly off.
type HeaterSchedule struct {
	At       time.Time
	Heater   HeaterState
	Planned  bool           // false: no valid plan, so one ON window
	Windows  []HeaterWindow // contiguous, cover [At, At+ScheduleHorizon)
	NominalW int
	Phases   string
}

// HeaterScheduleSource is implemented by the hwcontrol module so another
// module in the same process can read the schedule. Safe for concurrent
// use by the implementer.
type HeaterScheduleSource interface {
	HeaterSchedule() HeaterSchedule
}

// Schedule builds the snapshot at the controller's now. It changes nothing.
func (c *Controller) Schedule() HeaterSchedule {
	now := c.now
	end := now.Add(ScheduleHorizon)
	s := HeaterSchedule{At: now, Heater: HeaterUnknown, NominalW: HeaterNominalW, Phases: HeaterPhases}
	switch {
	case c.coverOn():
		s.Heater = HeaterOn
	case c.coverPositionAtMost(c.cfg.OffPos):
		s.Heater = HeaterOff
	}
	add := func(a, b time.Time, on bool) {
		if b.After(end) {
			b = end
		}
		if a.Before(now) {
			a = now
		}
		if !b.After(a) {
			return
		}
		if n := len(s.Windows); n > 0 && s.Windows[n-1].On == on && s.Windows[n-1].End.Equal(a) {
			s.Windows[n-1].End = b
			return
		}
		s.Windows = append(s.Windows, HeaterWindow{Start: a, End: b, On: on})
	}
	if !c.plan.Valid(now) {
		add(now, end, true)
		return s
	}
	periods := c.plan.OffPeriods
	if c.plan.DefaultOff {
		// Planner v2: the permitted-OFF periods are the whole day bar the
		// heating windows, so a house planner must not read them as
		// certain OFF when the controller would not actually hold OFF.
		if !c.Live() {
			add(now, end, true) // SHADOW: the controller never switches
			return s
		}
		periods = c.clipOffForTankClock(periods)
	}
	s.Planned = true
	cur := now
	for _, op := range periods {
		if !op.End.After(now) {
			continue
		}
		add(cur, op.Start, true)
		add(op.Start, op.End, false)
		cur = op.End
	}
	add(cur, end, true) // beyond the plan's last period (or ValidUntil) is ON
	return s
}

// clipOffForTankClock shortens permitted-OFF periods to what the tank
// clock (§4.1 rule 6) would let the controller hold: OFF is refused from
// LastFinishedReheatEnd + OffLimit on, unless a reheat in a planned heating
// window (the gap between two periods) resets it. A reheat is assumed in
// each such window, which is the optimistic case, so an OFF stretch that
// only holds if the window's reheat happens is still reported OFF, and an
// unknown clock reports no OFF at all (the core would refuse every OFF).
func (c *Controller) clipOffForTankClock(in []OffPeriod) []OffPeriod {
	end := c.tankEnd()
	if end.IsZero() || end.After(c.now) {
		return nil
	}
	expiry := end.Add(c.cfg.OffLimitSinceReheatEnd)
	var out []OffPeriod
	for i, op := range in {
		if op.Start.Before(expiry) {
			e := op.End
			if e.After(expiry) {
				e = expiry
			}
			if e.After(op.Start) {
				out = append(out, OffPeriod{Start: op.Start, End: e, Why: op.Why})
			}
		}
		if i+1 < len(in) && in[i+1].Start.After(op.End) {
			// The ON window between the periods ends at the next period's
			// start; its reheat resets the clock there.
			expiry = in[i+1].Start.Add(c.cfg.OffLimitSinceReheatEnd)
		}
	}
	return out
}
