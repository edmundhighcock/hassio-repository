package core

import (
	"math"
	"time"
)

// Overload pause (0.6.11). The immersion heater draws ~7.3 A on BOTH L1 and
// L2; a kettle on L1 during a reheat can push a 20 A main fuse over. When a
// phase stays above OverloadAmps while the heater is drawing, the controller
// switches the heater OFF through its own state machine (HOLD_OFF with
// holdOffKind "overload": same move reserve, caps, strikes and electrical
// verification as every other OFF) and switches it back when the phases have
// room again. The detection lives in the core because it needs the
// controller's evidence (cover, detector cycle, step tracker) and its move
// safety; house_planner plans budgets from a schedule and reads no live
// currents, and it must never move the cover.

// holdOffKind values for StHoldOff.
const (
	holdOffOverride = "override_off"
	holdOffOverload = "overload"
)

// PhaseSample is one live Equalizer reading (A per phase).
type PhaseSample struct {
	At         time.Time
	L1, L2, L3 float64
	Replay     bool
}

// PhaseOnline is the Equalizer's online flag.
type PhaseOnline struct {
	At     time.Time
	Online bool
}

// When implementations.
func (e PhaseSample) When() time.Time { return e.At }
func (e PhaseOnline) When() time.Time { return e.At }

type phaseRec struct {
	at     time.Time
	l1, l2 float64
	max    float64 // max(L1, L2): the heater's phases
}

type overloadState struct {
	last         phaseRec
	have         bool
	online       bool
	onlineSeen   bool
	overSince    time.Time // first sample of the current consecutive over-threshold run
	overN        int
	calmSince    time.Time // first sample of the current consecutive "room for the heater" run
	start        time.Time // current pause start (zero when not paused)
	lastPauseAt  time.Time
	lastPhase    string
	lastAmps     float64
	lastL1       float64
	lastL2       float64
	noteAt       time.Time
	lastResumeAt time.Time
}

func (c *Controller) onPhase(p PhaseSample) {
	if p.Replay {
		return
	}
	o := &c.ov
	if o.have && !p.At.After(o.last.at) {
		return
	}
	r := phaseRec{at: p.At, l1: p.L1, l2: p.L2, max: math.Max(p.L1, p.L2)}
	// A gap longer than the freshness limit breaks both runs: the samples
	// either side of it are not "consecutive fresh" samples.
	gap := !o.have || r.at.Sub(o.last.at) > c.cfg.OverloadFresh
	if r.max > c.cfg.OverloadAmps {
		if gap || o.overSince.IsZero() {
			o.overSince, o.overN = r.at, 1
		} else {
			o.overN++
		}
	} else {
		o.overSince, o.overN = time.Time{}, 0
	}
	if r.max+c.cfg.OverloadResumeMargin <= c.cfg.OverloadAmps {
		if gap || o.calmSince.IsZero() {
			o.calmSince = r.at
		}
	} else {
		o.calmSince = time.Time{}
	}
	o.last, o.have = r, true
}

func (c *Controller) onPhaseOnline(p PhaseOnline) {
	c.ov.online, c.ov.onlineSeen = p.Online, true
}

// phaseFresh: a sample no older than OverloadFresh and the Equalizer not
// known to be offline.
func (c *Controller) phaseFresh() bool {
	o := &c.ov
	if !o.have || c.now.Sub(o.last.at) > c.cfg.OverloadFresh {
		return false
	}
	return !o.onlineSeen || o.online
}

// overloadPaused reports whether a HOLD_OFF(overload) is running.
func (c *Controller) overloadPaused() bool {
	return c.P.State == StHoldOff && c.holdOffKind == holdOffOverload
}

// heaterDrawing is the controller's existing evidence that a reheat is
// running: the cover is ON and the step tracker sees an up-step held, or the
// detector has an open cycle of genuine magnitude.
func (c *Controller) heaterDrawing() bool {
	if !c.coverOn() {
		return false
	}
	if c.tracker.drawing {
		return true
	}
	cy := c.P.Cycle
	return cy != nil && cy.Magnitude >= c.cfg.GenuineMinW
}

func (c *Controller) ovNote(format string, args ...any) {
	if !c.ov.noteAt.IsZero() && c.now.Sub(c.ov.noteAt) < 10*time.Minute {
		return
	}
	c.ov.noteAt = c.now
	c.logf(format, args...)
}

// maybeOverloadPause starts a pause when the trigger rules hold. It reports
// whether it changed state. Only IDLE_ON (Auto) and HOLD_ON(boost) may be
// interrupted: Override On is an explicit user choice, Override Off is
// already off, and every other hold (watchdog, conflict, strikes, battery,
// on_failed, backstop) keeps the heater ON.
func (c *Controller) maybeOverloadPause() bool {
	o := &c.ov
	if !c.cfg.OverloadEnabled || o.overN < 2 || !c.phaseFresh() || c.overloadPaused() {
		return false
	}
	if o.last.at.Sub(o.overSince) < c.cfg.OverloadSustain || o.last.max <= c.cfg.OverloadAmps {
		return false
	}
	fromBoost := c.P.State == StHoldOn && c.hold == HoldBoost
	switch {
	case c.mv != nil:
		return false
	case c.P.State == StIdleOn && c.hold == "" && c.desiredHold() == "":
	case fromBoost && c.desiredHoldX(true) == "":
	default:
		return false
	}
	if c.entityFresh(RoleSelect) && c.str(RoleSelect) == SelectOverrideOff {
		return false
	}
	if !c.heaterDrawing() {
		return false
	}
	if !o.lastPauseAt.IsZero() && c.now.Sub(o.lastPauseAt) < c.cfg.OverloadMinGap {
		c.ovNote("overload pause suppressed: L1=%.1fA L2=%.1fA, last pause %s ago (limit one per %s)",
			o.last.l1, o.last.l2, c.now.Sub(o.lastPauseAt).Round(time.Second), c.cfg.OverloadMinGap)
		return false
	}
	if !c.linkUp {
		return false
	}
	if s := c.offInputsStale(); s != "" {
		c.ovNote("overload pause not started: input stale (%s); heater stays ON (L1=%.1fA L2=%.1fA)", s, o.last.l1, o.last.l2)
		return false
	}
	if !c.reserveAfter(1) {
		c.ovNote("overload pause refused: move reserve/budget would be violated (%d/h, %d/day); heater stays ON (L1=%.1fA L2=%.1fA)",
			c.movesInLast(time.Hour), c.movesInLast(24*time.Hour), o.last.l1, o.last.l2)
		return false
	}
	c.logf("overload pause: L1=%.1fA L2=%.1fA -> OFF", o.last.l1, o.last.l2)
	o.start, o.lastPauseAt = c.now, c.now
	o.lastL1, o.lastL2 = o.last.l1, o.last.l2
	o.lastAmps = o.last.max
	o.lastPhase = "L1"
	if o.last.l2 > o.last.l1 {
		o.lastPhase = "L2"
	}
	c.hold = ""
	c.offPeriod = nil
	c.P.CurrentOffPeriod = nil
	c.subStep = 0
	c.holdOffKind = holdOffOverload
	c.setState(StHoldOff, "overload_pause")
	c.runHoldOff()
	return true
}

// runOverloadPause is the HOLD_OFF(overload) tail (after the OFF move and its
// verification): decide whether the pause ends.
func (c *Controller) runOverloadPause() {
	o := &c.ov
	if c.entityFresh(RoleSelect) && c.str(RoleSelect) == SelectOverrideOff {
		// The user now wants it off: the pause becomes an Override Off hold.
		c.logf("overload pause: Override Off selected, continuing as Override Off")
		c.holdOffKind = holdOffOverride
		c.overrideOffDone = true
		return
	}
	if !c.linkUp {
		c.enterDegraded("overload_pause_ha_lost")
		return
	}
	elapsed := c.now.Sub(o.start)
	switch {
	case elapsed >= c.cfg.OverloadMaxPause:
		c.endOverloadPause("max_pause")
	case c.offInputsStale() != "":
		c.endOverloadPause("stale_" + c.offInputsStale())
	case elapsed >= c.cfg.OverloadMinHold && c.phaseFresh() && !o.calmSince.IsZero() &&
		o.last.at.Sub(o.calmSince) >= c.cfg.OverloadResumeSustain:
		c.endOverloadPause("load_cleared")
	}
}

// endOverloadPause returns the heater to whatever the controller wants: an
// active Boost continues; a plan OFF period covering now keeps it OFF (no
// move: it is already off); otherwise ON is commanded through the normal ON
// path (retries, verification, strikes).
func (c *Controller) endOverloadPause(why string) {
	o := &c.ov
	c.logf("overload pause ended (%s) after %s: L1=%.1fA L2=%.1fA; resuming", why,
		c.now.Sub(o.start).Round(time.Second), o.last.l1, o.last.l2)
	o.start = time.Time{}
	o.lastResumeAt = c.now
	c.holdOffKind = ""
	if !c.linkUp {
		c.enterDegraded("overload_resume_ha_lost")
		return
	}
	if c.P.Boost.Active {
		c.enterHold(HoldBoost)
		return
	}
	if op, ok := c.bootMayResumeOff(); ok {
		c.offPeriod = op
		c.P.CurrentOffPeriod = op
		c.heatOffCount = 0
		c.setState(StIdleOff, "overload_resume_plan_off")
		c.logf("overload pause: the plan still says OFF, staying OFF")
		return
	}
	c.startOn("overload_resume", true)
}

// cutByOverload: a detector cycle closing within a few minutes of an
// overload-pause OFF was interrupted, not finished: it must not stamp the
// tank clock.
func (c *Controller) cutByOverload(off time.Time) bool {
	t := c.ov.lastPauseAt
	return !t.IsZero() && !off.Before(t.Add(-time.Minute)) && off.Before(t.Add(4*time.Minute))
}

func (c *Controller) publishOverload(force bool) {
	o := &c.ov
	loc := c.cfg.Loc
	f := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.In(loc).Format(time.RFC3339)
	}
	state := "off"
	resumes, earliest := "", ""
	if c.overloadPaused() {
		state = "paused"
		resumes = f(o.start.Add(c.cfg.OverloadMaxPause))
		earliest = f(o.start.Add(c.cfg.OverloadMinHold))
	}
	attrs := map[string]any{
		"enabled": c.cfg.OverloadEnabled, "threshold_a": c.cfg.OverloadAmps,
		"last_pause_at": f(o.lastPauseAt), "phase": o.lastPhase, "amps": math.Round(o.lastAmps*10) / 10,
		"l1_a": o.lastL1, "l2_a": o.lastL2,
		"resumes_at": resumes, "earliest_resume_at": earliest, "last_resume_at": f(o.lastResumeAt),
	}
	c.publish(ObjOverload, state, attrs, force)
}
