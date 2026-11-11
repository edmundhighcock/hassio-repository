package core

import "time"

// Window-satisfied stamp (planner v2, 0.6.0; Config.WindowStamp).
//
// The tank clock (§4.1 rule 6) counts from the last FINISHED genuine
// reheat. Under planner v2 the switch is OFF by default and ON only in a
// few heating windows, so a window in which the thermostat never called
// (the tank was already hot) would leave the clock running although the
// tank is as full as after a reheat; on a day with no draw before the
// evening the clock would trip and the evening reheat would run at the
// evening price. A window therefore resets the clock, but only on strong
// evidence that the tank really was satisfied:
//
//   - the plan is a DefaultOff plan and the controller is LIVE;
//   - the window lasted >= MinSatisfiedWindow, and the controller observed
//     all of it (no gap > 2 min between its steps);
//   - the cover was ON and the target ON throughout, no input stale, the
//     detector present, no discontinuity, no detector cycle opened, and no
//     OFF command of ours fell inside it;
//   - proof of life: a real finished reheat ended within SatisfiedMaxAge
//     before the window's end. So a dead heater cannot keep the clock alive
//     for more than ~SatisfiedMaxAge + OffLimit.
//
// It sets P.SatisfiedEnd; the clock then counts from the later of that and
// LastFinishedReheatEnd. Nothing else reads it: the evening rule, condition
// G and the backstops still use real reheats.
const (
	MinSatisfiedWindow = 30 * time.Minute
	SatisfiedMaxAge    = 28 * time.Hour
)

type winObs struct {
	first, last time.Time
	bad         bool
	final       bool
}

// tankEnd is where the tank clock counts from.
func (c *Controller) tankEnd() time.Time {
	e := c.P.LastFinishedReheatEnd
	if c.cfg.WindowStamp && c.P.SatisfiedEnd.After(e) && !c.P.SatisfiedEnd.After(c.now) {
		e = c.P.SatisfiedEnd
	}
	return e
}

// trackWindows observes the active heating window and, once it has ended,
// decides whether it counts as a satisfied-tank stamp.
func (c *Controller) trackWindows() {
	if !c.cfg.WindowStamp || c.plan == nil || !c.plan.DefaultOff || !c.actuating() || !c.linkUp {
		return
	}
	if c.winObs == nil {
		c.winObs = map[int64]*winObs{}
	}
	for _, w := range c.P.Windows {
		key := w.Start.Unix()
		o := c.winObs[key]
		switch {
		case !c.now.Before(w.Start) && c.now.Before(w.End):
			if o == nil {
				o = &winObs{first: c.now, last: c.now}
				c.winObs[key] = o
			}
			if c.now.Sub(o.last) > 2*time.Minute {
				o.bad = true
			}
			o.last = c.now
			if c.now.Sub(w.Start) >= 2*time.Minute && c.windowBroken(w) {
				o.bad = true
			}
		case !c.now.Before(w.End) && o != nil && !o.final:
			o.final = true
			c.finishWindow(w, o)
		}
	}
	for k, o := range c.winObs {
		if c.now.Sub(o.last) > 48*time.Hour {
			delete(c.winObs, k)
		}
	}
}

// windowBroken: something in the current step says the no-call observation
// cannot be trusted.
func (c *Controller) windowBroken(w PlanWindow) bool {
	if !c.coverOn() || !c.targetIsOn() || !c.detPresent || c.offInputsStale() != "" {
		return true
	}
	if c.P.Cycle != nil || c.lastCycleOpen.After(w.Start.Add(-2*time.Minute)) {
		return true
	}
	for _, d := range c.discs {
		if !d.T.Before(w.Start) {
			return true
		}
	}
	return false
}

func (c *Controller) finishWindow(w PlanWindow, o *winObs) {
	if o.bad || w.End.Sub(w.Start) < MinSatisfiedWindow ||
		o.first.After(w.Start.Add(5*time.Minute)) || o.last.Before(w.End.Add(-2*time.Minute)) ||
		c.P.LastOffCmdAt.After(w.Start.Add(-time.Minute)) {
		return
	}
	end := c.P.LastFinishedReheatEnd
	if end.IsZero() || w.End.Sub(end) > SatisfiedMaxAge || w.End.Before(end) {
		return
	}
	if w.End.After(c.P.SatisfiedEnd) {
		c.P.SatisfiedEnd = w.End
		c.logf("window %s %s-%s ended with no call for heat: tank counted as full (clock restarts)",
			w.Kind, w.Start.Format(time.RFC3339), w.End.Format(time.RFC3339))
		c.persistNow()
	}
}
