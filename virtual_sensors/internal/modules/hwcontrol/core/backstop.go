package core

import (
	"fmt"
	"sort"
	"time"
)

// ---- genuine start and finished reheat (§4.4) ----

func (c *Controller) onCycleOpened(e CycleOpened) {
	c.P.Cycle = &CycleRec{OnTime: e.OnTime, Magnitude: e.Magnitude}
	c.lastCycleOpen = e.OnTime
	if c.P.Boost.Active && e.Magnitude >= c.cfg.GenuineMinW {
		c.P.Boost.ReheatSeenStart = true
	}
}

// discNear reports a real discontinuity within ±GenuineDiscGuard of t.
func (c *Controller) discNear(t time.Time) bool {
	for _, d := range c.discs {
		if dt := d.T.Sub(t); dt <= c.cfg.GenuineDiscGuard && dt >= -c.cfg.GenuineDiscGuard {
			return true
		}
	}
	return false
}

// qualifies applies the genuine-start qualifier to the open cycle given
// that it has stayed open until `until`: magnitude >= GenuineMinW, open
// >= GenuineMinDur, not rejected (the detector only opens unrejected
// steps), no discontinuity within ±60 s of the on-step.
func (c *Controller) qualifies(cy *CycleRec, until time.Time) bool {
	return cy.Magnitude >= c.cfg.GenuineMinW && until.Sub(cy.OnTime) >= c.cfg.GenuineMinDur && !c.discNear(cy.OnTime)
}

// qualifyGenuine stamps the clock once the open cycle has been seen open
// for GenuineMinDur. "Seen" is measured on the detector's own time (the
// latest residual sample), not wall time, so replayed history and a
// pending post-off dwell cannot qualify a short cycle early. It is never
// called while a residual sample is being handled, so a close resolved by
// that same sample has already arrived.
func (c *Controller) qualifyGenuine() {
	cy := c.P.Cycle
	if cy == nil || cy.Genuine {
		return
	}
	if c.qualifies(cy, c.detTime) {
		c.stampGenuine(cy)
	}
}

func (c *Controller) stampGenuine(cy *CycleRec) {
	cy.Genuine = true
	c.P.LastGenuineStart = cy.OnTime
	c.P.LastGenuineQualifier = fmt.Sprintf("detector_cycle>=%.1fkW,>=%dmin", c.cfg.GenuineMinW/1000, int(c.cfg.GenuineMinDur.Minutes()))
	// Retroactive: the heater ran from OnTime, so the switch was ON since,
	// up to where the ON-time clock has been accounted (the replay point
	// during warm-up, now when live).
	on := c.P.OnClockAt.Sub(cy.OnTime).Seconds()
	if on < 0 {
		on = 0
	}
	c.P.OnSSinceGenuineStart = on
	c.P.NextProbeOnS = c.cfg.ProbeAfter.Seconds()
	c.P.NextBackstopAlertOnS = c.cfg.AlertAfter.Seconds()
	delete(c.P.AlertsSent, AlertBackstop)
	c.logf("genuine start stamped at %s (%.0f W)", cy.OnTime.Format(time.RFC3339), cy.Magnitude)
	c.persistNow()
}

func (c *Controller) onCycleClosed(e CycleClosed) {
	cy := c.P.Cycle
	if cy == nil {
		return
	}
	c.P.Cycle = nil
	if !cy.Genuine && c.qualifies(cy, e.OffTime) {
		c.stampGenuine(cy)
	}
	if !cy.Genuine || !e.Accepted {
		return
	}
	// A finished reheat: the tank clock (§4.1 rule 6).
	c.P.LastFinishedReheatStart = cy.OnTime
	c.P.LastFinishedReheatEnd = e.OffTime
	c.logReheat(cy.OnTime, e.OffTime, e.Replay, e.PhaseEnd)
	c.logf("finished reheat %s - %s", cy.OnTime.Format(time.RFC3339), e.OffTime.Format(time.RFC3339))
	if c.P.Boost.Active && c.P.Boost.ReheatSeenStart {
		c.endBoost("reheat_finished")
	}
	c.persistNow()
}

// logReheat appends to the reheat log (planner v2's learning data). A
// reheat is "cut" if our own OFF command fell inside its last minutes: the
// controller, not the thermostat, ended it. Replayed (warm-up) cycles are
// logged too, without a kind, and duplicates are dropped.
func (c *Controller) logReheat(on, off time.Time, replay bool, phaseEnd int) {
	for _, r := range c.P.Reheats {
		if r.Start.Equal(on) {
			return
		}
	}
	rec := ReheatRec{Start: on, End: off}
	lc := c.P.LastCommand
	if lc.Target == c.cfg.CmdOff && !lc.At.IsZero() &&
		!off.Before(lc.At.Add(-time.Minute)) && off.Before(lc.At.Add(4*time.Minute)) {
		rec.Cut = true
	}
	rec.PhaseSeen = phaseEnd == 1 || phaseEnd == 2
	rec.Satisfied = !rec.Cut && phaseEnd == 1
	if !replay && c.plan != nil && c.plan.DefaultOff {
		for _, w := range c.P.Windows {
			if !on.Before(w.Start.Add(-2*time.Minute)) && on.Before(w.End) {
				rec.Kind = w.Kind
			}
		}
	}
	c.P.Reheats = append(c.P.Reheats, rec)
	c.logf("reheat logged %dm cut=%v satisfied=%v", int(rec.Duration().Minutes()), rec.Cut, rec.Satisfied)
	sort.SliceStable(c.P.Reheats, func(i, j int) bool { return c.P.Reheats[i].Start.Before(c.P.Reheats[j].Start) })
	if n := len(c.P.Reheats); n > MaxReheatLog {
		c.P.Reheats = append([]ReheatRec(nil), c.P.Reheats[n-MaxReheatLog:]...)
	}
}

// ---- tank clock and evening rule (§4.1 rules 6, 7) ----

func (c *Controller) tankOK() bool {
	end := c.tankEnd()
	return !end.IsZero() && !end.After(c.now) && c.now.Sub(end) <= c.cfg.OffLimitSinceReheatEnd
}

func (c *Controller) minuteOfDay(t time.Time) int {
	l := t.In(c.cfg.Loc)
	return l.Hour()*60 + l.Minute()
}

func (c *Controller) eveningOK() bool {
	if !c.cfg.EveningRule || c.minuteOfDay(c.now) < c.cfg.EveningOffFromMin {
		return true
	}
	end := c.P.LastFinishedReheatEnd
	if end.IsZero() || end.After(c.now) {
		return false
	}
	ln, le := c.now.In(c.cfg.Loc), end.In(c.cfg.Loc)
	return ln.Year() == le.Year() && ln.YearDay() == le.YearDay()
}

// ---- probe and alert clocks (§4.4): ON time only ----

func (c *Controller) probeDue() bool {
	return c.P.OnSSinceGenuineStart >= c.P.NextProbeOnS
}

func (c *Controller) backstopHold() bool {
	return c.P.OnSSinceGenuineStart >= c.cfg.AlertAfter.Seconds()
}

func (c *Controller) backstopAlerts() {
	if c.P.OnSSinceGenuineStart < c.P.NextBackstopAlertOnS {
		return
	}
	c.P.NextBackstopAlertOnS = c.P.OnSSinceGenuineStart + c.cfg.AlertRepeat.Seconds()
	// Bypass the wall-clock dedupe: the repeat is in ON time.
	delete(c.P.AlertsSent, AlertBackstop)
	c.alert(AlertBackstop, fmt.Sprintf("Hot water: no genuine heater start for %.1f h of switch-ON time (last %s). Holding ON; check the heater.",
		c.P.OnSSinceGenuineStart/3600, fmtTime(c.P.LastGenuineStart)), 0)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format(time.RFC3339)
}

// ---- boost (§4.7) ----

func (c *Controller) boostExits() {
	b := &c.P.Boost
	if !b.Active {
		return
	}
	switch {
	case c.now.Sub(b.PressedAt) >= c.cfg.BoostMax:
		c.endBoost("cap")
	case !b.ReheatSeenStart && c.now.Sub(b.PressedAt) >= c.cfg.BoostNoStartTimeout:
		c.endBoost("no_start")
	}
}

// endBoost: only a finished reheat stamps the tank clock (done by the
// cycle-close path); a no-start or cap exit leaves it untouched.
func (c *Controller) endBoost(why string) {
	c.P.Boost = BoostState{}
	c.logf("boost ended: %s", why)
	c.persistNow()
}

// ---- seeding from detector history (§4.4) ----

// SeedCycle is one detector cycle reconstructed from history.
type SeedCycle struct {
	On, Off   time.Time // Off zero if still open at the end of the history
	Magnitude float64
	Accepted  bool // the energy total rose at the close
}

// SeedFromHistory reconstructs cycles from sensor.hot_water_estimated_power
// (W: the open cycle's magnitude, 0 when closed) and
// sensor.hot_water_estimated_energy (kWh, rises only on an accepted close).
func SeedFromHistory(power, energy []SeedPoint) []SeedCycle {
	sort.SliceStable(power, func(i, j int) bool { return power[i].T.Before(power[j].T) })
	sort.SliceStable(energy, func(i, j int) bool { return energy[i].T.Before(energy[j].T) })
	var out []SeedCycle
	var cur *SeedCycle
	for _, p := range power {
		if !p.OK {
			continue
		}
		if p.V > 0 && cur == nil {
			cur = &SeedCycle{On: p.T, Magnitude: p.V}
		} else if p.V > 0 && cur != nil {
			if p.V > cur.Magnitude {
				cur.Magnitude = p.V
			}
		} else if p.V <= 0 && cur != nil {
			cur.Off = p.T
			cur.Accepted = energyRoseNear(energy, p.T, 10*time.Second)
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// SeedPoint is one history point.
type SeedPoint struct {
	T  time.Time
	V  float64
	OK bool
}

func energyRoseNear(energy []SeedPoint, t time.Time, tol time.Duration) bool {
	prev := -1.0
	for _, e := range energy {
		if !e.OK {
			continue
		}
		if prev >= 0 && e.V > prev {
			if d := e.T.Sub(t); d <= tol && d >= -tol {
				return true
			}
		}
		prev = e.V
	}
	return false
}

// SeedWanted reports whether the clocks are unknown, so a seed is needed.
func (c *Controller) SeedWanted() bool {
	return c.P.LastGenuineStart.IsZero() || c.P.LastFinishedReheatEnd.IsZero()
}

func (c *Controller) onSeed(s Seed) {
	var lastGenuine, lastFinished *SeedCycle
	for i := range s.Cycles {
		cy := &s.Cycles[i]
		end := cy.Off
		if end.IsZero() {
			end = s.At
		}
		if cy.Magnitude < c.cfg.GenuineMinW || end.Sub(cy.On) < c.cfg.GenuineMinDur {
			continue
		}
		lastGenuine = cy
		if !cy.Off.IsZero() && cy.Accepted {
			lastFinished = cy
			c.logReheat(cy.On, cy.Off, true, 0)
		}
	}
	if lastGenuine != nil && c.P.LastGenuineStart.IsZero() {
		c.P.LastGenuineStart = lastGenuine.On
		c.P.LastGenuineQualifier = "seeded_from_history"
		on := s.At.Sub(lastGenuine.On).Seconds() // assume ON throughout: the safe direction
		if on > c.P.OnSSinceGenuineStart {
			c.P.OnSSinceGenuineStart = on
		}
		c.logf("seeded genuine start %s", lastGenuine.On.Format(time.RFC3339))
	}
	if lastFinished != nil && c.P.LastFinishedReheatEnd.IsZero() {
		c.P.LastFinishedReheatStart = lastFinished.On
		c.P.LastFinishedReheatEnd = lastFinished.Off
		c.logf("seeded finished reheat ending %s", lastFinished.Off.Format(time.RFC3339))
	}
	c.persistNow()
}
