package core

import (
	"fmt"
	"time"
)

// desiredHold returns the HOLD_ON reason demanded right now, in priority
// order (rows 29, 31-34, 38; the backstop hold of §4.4; row 20's
// on_failed latch). "" means no hold.
func (c *Controller) desiredHold() string {
	switch {
	case c.entityFresh(RoleLatch) && c.str(RoleLatch) == "on":
		return HoldWatchdog
	case c.anyConflict():
		return HoldConflict
	case c.P.Strikes >= 3:
		return HoldStrikes
	case c.batteryLow():
		return HoldBattery
	case c.P.Hold.Latched && c.P.Hold.Reason == HoldOnFailed:
		return HoldOnFailed
	case c.P.Boost.Active:
		return HoldBoost
	case c.backstopHold():
		return HoldBackstop
	case c.entityFresh(RoleSelect) && c.str(RoleSelect) == SelectOverrideOn:
		return HoldOverride
	}
	return ""
}

func (c *Controller) anyConflict() bool {
	for _, on := range c.conflicts {
		if on {
			return true
		}
	}
	return false
}

func (c *Controller) batteryLow() bool {
	b, ok := c.num(RoleBattery)
	return ok && b < c.cfg.BatteryFloor
}

// MayGoOff is PLAN §4.1: every condition must hold. No condition reads a
// price. shadow=true evaluates it for the shadow "would" (rule 1's
// LIVE/armed part skipped, and the state is the observed one).
func (c *Controller) MayGoOff(shadow bool) (bool, string) {
	// 1. mode LIVE and armed; select Auto; no boost.
	if !shadow && !c.live {
		return false, "not_live"
	}
	if !c.entityFresh(RoleSelect) || c.str(RoleSelect) != SelectAuto {
		return false, "select_not_auto"
	}
	if c.P.Boost.Active {
		return false, "boost"
	}
	// 2. inside a permitted period of a valid plan.
	period, ok := c.plan.PeriodAt(c.now)
	if !ok {
		if c.plan == nil || !c.plan.Valid(c.now) {
			return false, "no_valid_plan"
		}
		return false, "outside_plan_period"
	}
	// Review fix 5: after an OFF was abandoned (row 14 etc.) the next OFF
	// needs a new planned period, starting at or after the abandoned end.
	if period.Start.Before(c.P.AbandonedUntil) {
		return false, "period_abandoned"
	}
	// 3. IDLE_ON settled; strikes < 3; no hold.
	if c.P.State != StIdleOn {
		return false, "state_" + string(c.P.State)
	}
	if c.now.Sub(c.P.StateSince) < c.cfg.Settle {
		return false, "settling"
	}
	if c.P.Strikes >= 3 {
		return false, "strikes"
	}
	if c.hold != "" || c.desiredHold() != "" {
		return false, "hold"
	}
	// 4. inputs healthy.
	if s := c.offInputsStale(); s != "" {
		return false, "stale_" + s
	}
	for _, d := range c.discs {
		if c.now.Sub(d.T) < c.cfg.DiscGuard {
			return false, "recent_discontinuity"
		}
	}
	if !c.coverOn() || !c.targetIsOn() {
		return false, "cover_not_on"
	}
	// 5. battery and move reserve (and the deliberate-change gap).
	if b, ok := c.num(RoleBattery); !ok || b < c.cfg.BatteryFloor {
		return false, "battery"
	}
	if !c.reserveAfter(1) {
		return false, "move_reserve"
	}
	if lm := c.lastMoveAt(); !lm.IsZero() && c.now.Sub(lm) < c.cfg.DeliberateGap {
		return false, "deliberate_gap"
	}
	// 6. tank clock.
	if !c.tankOK() {
		if c.tankEnd().IsZero() {
			return false, "tank_clock_unknown"
		}
		return false, "tank_limit"
	}
	// 7. evening rule.
	if !c.eveningOK() {
		return false, "evening_no_reheat_today"
	}
	// 8. no probe due.
	if (!shadow && c.probeDue()) || (shadow && c.shadowProbeDue()) {
		return false, "probe_due"
	}
	// 9. EV guard.
	if c.inEVGuard() {
		return false, "ev_guard_time"
	}
	if c.evMovedRecently() {
		return false, "ev_moved"
	}
	return true, "plan_period"
}

// shadowProbeDue: the shadow "would probe" schedule, volatile and separate
// from the persisted one.
func (c *Controller) shadowProbeDue() bool {
	next := c.P.NextProbeOnS
	if c.shadowNextProbe > next {
		next = c.shadowNextProbe
	}
	return c.P.OnSSinceGenuineStart >= next
}

func (c *Controller) inEVGuard() bool {
	m := c.minuteOfDay(c.now)
	hw := int(c.cfg.EVGuardHalfWidth.Minutes())
	for _, g := range c.cfg.EVGuardMinutes {
		d := m - g
		if d < 0 {
			d = -d
		}
		if d > 12*60 {
			d = 24*60 - d
		}
		if d <= hw {
			return true
		}
	}
	return false
}

func (c *Controller) evMovedRecently() bool {
	mn, mx, ok := c.ev.minMax(c.now, c.cfg.EVGuardWindow)
	return ok && mx-mn > c.cfg.EVMoveW
}

// offRevoked returns why a running OFF must end (row 15, 16, 17), or "".
func (c *Controller) offRevoked(override bool) string {
	if !c.actuating() {
		return "disarm"
	}
	if !c.tankOK() && !c.testOff {
		return "tank_limit" // a test OFF is bounded by its own short end instead
	}
	if s := c.offInputsStale(); s != "" {
		return "stale_" + s
	}
	if c.P.Boost.Active {
		return "boost"
	}
	if c.desiredHold() != "" {
		return "hold_" + c.desiredHold()
	}
	if c.heatOffCount >= 2 {
		return "heat_while_off"
	}
	if override {
		if c.str(RoleSelect) != SelectOverrideOff {
			return "override_cleared"
		}
		return ""
	}
	if c.str(RoleSelect) != SelectAuto {
		return "select_changed"
	}
	if c.offPeriod == nil {
		return "no_period"
	}
	if !c.now.Before(c.offPeriod.End) {
		return "period_end"
	}
	if c.testOff {
		return "" // a test OFF is not a plan period: its fixed end governs
	}
	end, why := c.frozenEnd(*c.offPeriod)
	if why != "" {
		return why
	}
	if end.Before(c.offPeriod.End) {
		pp := *c.offPeriod
		pp.End = end
		c.offPeriod = &pp
		c.P.CurrentOffPeriod = &pp
	}
	return ""
}

// frozenEnd applies a (re)plan to an OFF period that has STARTED (review
// fix 4): its start and end are frozen; a revision may only shorten it.
// The plan's period covering now may end earlier (adopted); a later end
// or a moved start is ignored. A plan that no longer covers now at all
// shortens it to now (why != "").
func (c *Controller) frozenEnd(cur OffPeriod) (time.Time, string) {
	p, ok := c.plan.PeriodAt(c.now)
	if !ok {
		if c.plan == nil || !c.plan.Valid(c.now) {
			return cur.End, "plan_expired"
		}
		return cur.End, "plan_revised" // the revised plan no longer covers now
	}
	if p.End.Before(cur.End) {
		return p.End, ""
	}
	return cur.End, ""
}

// ---- the state machine ----

func (c *Controller) evaluate() {
	c.updateMode()
	if !c.actuating() {
		c.evaluateShadow()
		return
	}
	hold := c.desiredHold()
	if c.probeUnderHold != "" {
		if c.P.State == StProbe && hold == c.probeUnderHold {
			c.runProbe() // row 29: a backstop probe running under the hold
			return
		}
		c.probeUnderHold = ""
		if c.P.State == StProbe && hold != "" {
			c.mv = nil
			c.ver = nil
		}
	}
	if hold != "" {
		if c.P.State != StHoldOn || c.hold != hold {
			c.enterHold(hold)
			return
		}
		c.runHold()
		return
	}
	if c.P.State == StHoldOn {
		c.leaveHold()
		return
	}
	if c.P.State == StHoldOff {
		c.runHoldOff()
		return
	}
	if c.maybeOverrideOff() {
		return
	}
	if !c.linkUp {
		switch c.P.State {
		case StCmdOff, StVerifyOff, StIdleOff, StCmdOn, StVerifyOn, StRecycleOn, StProbe:
			// Commands cannot be sent: DEGRADED (heartbeat withheld, so the
			// watchdog forces ON); the reconnect goes through BOOT.
			c.enterDegraded("ha_connection_lost")
			return
		}
	}
	switch c.P.State {
	case StBoot:
		c.runBoot()
	case StDegraded:
		c.runDegraded()
	case StIdleOn:
		c.runIdleOn()
	case StCmdOff:
		c.runCmdOff()
	case StVerifyOff:
		c.runVerifyOff()
	case StIdleOff:
		c.runIdleOff()
	case StCmdOn:
		c.runCmdOn()
	case StVerifyOn:
		c.runVerifyOn()
	case StRecycleOn:
		c.runRecycle()
	case StProbe:
		c.runProbe()
	}
}

// bootPath is rows 36/37 ("BOOT path, rule 3 or 4"): back through BOOT,
// which re-checks inputs and the switch.
func (c *Controller) bootPath(reason string) {
	c.bootSince = c.now
	c.setState(StBoot, reason)
}

// bootTimedOut: the boot timer (row 5) runs only once the inputs have
// first been populated (the first link-up, which follows the snapshot).
// Before that the controller holds quietly in BOOT: no DEGRADED, no
// count, no alert (review fix 6). The heartbeat is withheld meanwhile
// because inputs are stale, so the watchdog still covers a start-up that
// never completes.
func (c *Controller) bootTimedOut() bool {
	return c.populated && c.now.Sub(c.bootSince) >= c.cfg.BootTimeout
}

func (c *Controller) runBoot() {
	if s := c.bootStale(); s != "" {
		if c.bootTimedOut() {
			c.enterDegraded("boot_timeout_stale_" + s)
		}
		return
	}
	if c.coverOn() {
		c.setState(StIdleOn, "boot_on") // row 3: settle before any OFF
		return
	}
	// Row 4 / C10 (revised 0.4.1): below 90 at boot, including a crash or
	// reconnect mid-OFF. A valid OFF is RESUMED (IDLE_OFF, no motor move,
	// OffSince and the clocks kept) only if the controller itself put the
	// switch OFF (OffSince set and the last command was OFF) and every
	// condition the normal OFF path applies still holds (bootMayResumeOff).
	// Otherwise the OFF is abandoned and ON is commanded (boot_below_90);
	// G may still hold for that ON.
	if op, ok := c.bootMayResumeOff(); ok {
		c.offPeriod = op
		c.P.CurrentOffPeriod = op // nil for an Override Off
		c.heatOffCount = 0
		c.setState(StIdleOff, "boot_resume_off")
		return
	}
	endsOff := !c.P.OffSince.IsZero()
	c.abandonOff()
	c.startOn("boot_below_90", endsOff)
}

// bootMayResumeOff decides C10: whether a restart or reconnect with the
// cover below 90 may continue the running OFF. It returns the period to
// continue (nil for an Override Off, which has no plan period). It reuses
// the running-OFF predicate offRevoked (live/armed, tank clock, input
// freshness, boost, holds incl. latch/battery/strikes, selector, period
// end and plan coverage) and the evening rule, so nothing is
// reimplemented.
func (c *Controller) bootMayResumeOff() (*OffPeriod, bool) {
	// The controller must have commanded this OFF: OffSince is set only
	// when its own OFF move completed, and cleared when an ON is verified;
	// the last command must also still be OFF (not an interrupted ON).
	if c.P.OffSince.IsZero() || c.P.LastCommand.Target != c.cfg.CmdOff {
		return nil, false
	}
	if c.P.Strikes >= 3 || !c.eveningOK() || c.testOff {
		return nil, false
	}
	if c.entityFresh(RoleSelect) && c.str(RoleSelect) == SelectOverrideOff {
		saved := c.offPeriod
		c.offPeriod = nil
		why := c.offRevoked(true)
		c.offPeriod = saved
		if why != "" {
			return nil, false
		}
		c.overrideOffDone = false // maybeOverrideOff re-adopts it (no move: already OFF)
		return nil, true
	}
	cur, ok := c.plan.PeriodAt(c.now)
	if !ok || cur.Start.Before(c.P.AbandonedUntil) {
		return nil, false
	}
	if pp := c.P.CurrentOffPeriod; pp != nil && pp.Start.Equal(cur.Start) && pp.End.Before(cur.End) {
		cur = *pp // frozen: a replan may only shorten
	}
	saved := c.offPeriod
	c.offPeriod = &cur
	why := c.offRevoked(false)
	if why != "" {
		c.offPeriod = saved
		return nil, false
	}
	return c.offPeriod, true
}

func (c *Controller) enterDegraded(reason string) {
	c.abandonOff()
	c.healthySince = time.Time{}
	c.setState(StDegraded, reason)
}

func (c *Controller) runDegraded() {
	// I1: a degraded state that finds the cover not ON first issues ON.
	c.ensureOn(false)
	if c.criticalStale() != "" {
		c.healthySince = time.Time{}
		return
	}
	if c.healthySince.IsZero() {
		c.healthySince = c.now
	}
	if c.now.Sub(c.healthySince) >= c.cfg.DegradedRecover && c.mv == nil {
		c.bootPath("degraded_recovered") // row 36
	}
}

func (c *Controller) runIdleOn() {
	if s := c.criticalStale(); s != "" {
		c.enterDegraded("stale_" + s)
		return
	}
	// Row 8: drift below 90, or the target no longer ON (< 99; 100 is ON,
	// fix 2) with no command of ours.
	if c.mv == nil && (!c.coverOn() || !c.targetIsOn()) {
		c.P.Retreats++
		c.logf("cover drifted (position/target no longer ON) with no command of ours")
		c.startOn("drift", false)
		return
	}
	// R3 manual test move / probe (PLAN §8), behind the same arming, move
	// caps and reserve.
	if c.testPending != "" && c.mv == nil {
		c.startTest()
		return
	}
	// Row 7: backstop probe.
	if c.probeDue() && c.mv == nil && c.reserveAfter(1) && c.offInputsStale() == "" {
		c.subStep = 0
		c.setState(StProbe, "probe_due")
		c.runProbe()
		return
	}
	// Row 6: OFF only through MayGoOff (I2).
	if ok, _ := c.MayGoOff(false); ok && c.mv == nil {
		p, _ := c.plan.PeriodAt(c.now)
		pp := p
		c.offPeriod = &pp
		c.P.CurrentOffPeriod = &pp
		c.beginVerification(false)
		if !c.startMove(c.cfg.CmdOff, "off") {
			c.ver = nil
			c.offPeriod, c.P.CurrentOffPeriod = nil, nil
			return
		}
		c.setState(StCmdOff, "plan_period")
	}
}

func (c *Controller) runCmdOff() {
	m := c.takeMove()
	if m == nil {
		if c.mv == nil {
			c.startOn("cmd_off_lost", false)
		}
		return
	}
	switch m.outcome {
	case mvOK:
		c.P.OffSince = m.stopAt
		c.heatOffCount = 0
		c.ver.stop = m.stopAt
		c.ver.started = true
		c.setState(StVerifyOff, "motor_ok")
	case mvRetreat, mvStuck:
		c.strike("off_" + map[int]string{mvRetreat: "retreat", mvStuck: "stuck"}[m.outcome]) // row 10
		c.abandonPeriod("off_motor_failure")
		c.startOn("off_motor_failure", false)
	case mvCmdFailed:
		c.strike("off_call_failed_x3") // row 11, C11
		c.abandonPeriod("off_call_failed")
		c.startOn("off_call_failed", false)
	}
}

func (c *Controller) runVerifyOff() {
	v := c.ver
	if v == nil {
		c.setState(StIdleOff, "no_verification")
		return
	}
	r := c.offResult()
	switch r {
	case vrPending:
		return
	case vrDrop:
		c.ver = nil
		if v.inconclusive() || v.discAny || v.ambiguous {
			c.setVerdict(VOffUnverified, c.verAttrs(v)) // row 26: never confirmed
		} else {
			c.setVerdict(VOffConfirmed, c.verAttrs(v)) // row 12
		}
		c.setState(StIdleOff, "verified")
	case vrAmbiguous:
		// Fix 3: heater drawing (or a discontinuity-flagged step) at
		// baseline, no drop, and the window or the step in doubt: genuinely
		// ambiguous, so return ON (the safe direction), no strike; the
		// rest of the period is abandoned (fix 5).
		c.ver = nil
		c.setVerdict(VOffUnverified, c.verAttrs(v))
		c.abandonPeriod("off_ambiguous")
		c.setState(StIdleOn, "off_ambiguous")
	case vrIdle:
		c.ver = nil
		c.setVerdict(VOffUnverified, c.verAttrs(v)) // row 13, no recycle
		c.setState(StIdleOff, "heater_idle_or_unclear")
	case vrNoDrop:
		c.ver = nil
		if v.inconclusive() {
			c.setVerdict(VOffUnverified, c.verAttrs(v)) // row 26
			c.setState(StIdleOff, "inconclusive")
			return
		}
		// Row 14: off_failed; no strike, no recycle; block abandoned, and
		// with it the rest of the period (fix 5).
		c.setVerdict(VOffFailed, c.verAttrs(v))
		c.abandonPeriod("off_failed")
		c.setState(StIdleOn, "off_failed")
	}
}

func (c *Controller) runIdleOff() {
	if c.coverOn() && c.mv == nil {
		c.abandonOff()
		c.bootPath("cover_on_externally")
		return
	}
	c.heatWhileOff()
	if why := c.offRevoked(false); why != "" {
		if !c.linkUp {
			c.enterDegraded("off_but_ha_lost") // row 17: ON cannot be commanded
			return
		}
		if why == "heat_while_off" {
			c.abandonPeriod(why) // the tank is calling: no re-OFF in this period
		}
		c.startOn("end_off_"+why, true)
	}
}

// heatWhileOff is row 16 / §4.6: an up-step >= 2.7 kW held >= 3 min
// while OFF; the 2nd in one OFF period commands ON.
func (c *Controller) heatWhileOff() {
	k := &c.tracker
	if !k.drawing || k.step < c.cfg.HeatWhileOffW || k.upAt.Before(c.P.OffSince) {
		return
	}
	if k.upAt.Equal(c.heatOffCounted) || c.now.Sub(k.upAt) < c.cfg.HeatWhileOffFor {
		return
	}
	c.heatOffCounted = k.upAt
	c.heatOffCount++
	c.logf("heat_while_off #%d (step %.0f W at %s)", c.heatOffCount, k.step, k.upAt.Format(time.RFC3339))
}

// startTest runs a pending R3 test from IDLE_ON (PLAN §8: supervised live
// actuation, "manual-trigger test moves"). It needs LIVE (both keys),
// select Auto, no hold (we are in IDLE_ON), every OFF input fresh, the
// battery at or above the floor, the cover ON and the move reserve. It
// does not need a plan period, the settle, the tank clock or the evening
// rule: a human asked, with the test OFF bounded by TestOffDuration.
func (c *Controller) startTest() {
	kind := c.testPending
	c.testPending = ""
	why := ""
	switch {
	case !c.live:
		why = "not LIVE"
	case !c.entityFresh(RoleSelect) || c.str(RoleSelect) != SelectAuto:
		why = "selector not on Auto"
	case c.offInputsStale() != "":
		why = "input stale: " + c.offInputsStale()
	case c.batteryLow():
		why = "battery below the floor"
	case !c.coverOn() || !c.targetIsOn():
		why = "cover not ON"
	case !c.reserveAfter(1):
		why = "move reserve not available"
	}
	if why != "" {
		c.alert(AlertTestRefused, "Hot water: test "+kind+" refused: "+why+".", 0)
		return
	}
	if kind == "probe" {
		c.testProbe = true
		c.subStep = 0
		c.setState(StProbe, "test_probe")
		c.runProbe()
		return
	}
	op := OffPeriod{Start: c.now, End: c.now.Add(c.cfg.TestOffDuration), Why: "manual test move (R3)"}
	c.offPeriod = &op
	c.P.CurrentOffPeriod = &op
	c.testOff = true
	c.beginVerification(false)
	if !c.startMove(c.cfg.CmdOff, "test_off") {
		c.ver = nil
		c.offPeriod, c.P.CurrentOffPeriod, c.testOff = nil, nil, false
		c.alert(AlertTestRefused, "Hot water: test move refused: move cap reached.", 0)
		return
	}
	c.setState(StCmdOff, "test_move")
}

// abandonPeriod (review fix 5) abandons the rest of the running OFF
// period: no further OFF until a planned period starting at or after its
// end. Used where the OFF failed or was cut short by the heater itself
// (rows 10, 11, 14, an ambiguous verification, heat while OFF).
func (c *Controller) abandonPeriod(why string) {
	if c.offPeriod != nil && c.offPeriod.End.After(c.P.AbandonedUntil) {
		c.P.AbandonedUntil = c.offPeriod.End
		c.logf("rest of OFF period abandoned until %s (%s)", c.offPeriod.End.Format(time.RFC3339), why)
	}
	c.abandonOff()
}

// abandonOff clears the running OFF period's bookkeeping.
func (c *Controller) abandonOff() {
	c.offPeriod = nil
	c.P.CurrentOffPeriod = nil
	c.testOff = false
	if c.P.State == StCmdOff || c.P.State == StVerifyOff {
		c.ver = nil
	}
}

// startOn enters CMD_ON (rows 4, 8, 10, 11, 15, 17, 35). endsOff marks an
// ON that ends an OFF, for condition G.
func (c *Controller) startOn(reason string, endsOff bool) {
	c.onAttempts = 0
	c.onEndsOff = endsOff
	c.onOffDur = 0
	if endsOff && !c.P.OffSince.IsZero() {
		c.onOffDur = c.now.Sub(c.P.OffSince)
	}
	c.offPeriod = nil
	c.P.CurrentOffPeriod = nil
	c.testOff, c.testProbe = false, false
	c.ver = nil
	c.mv = nil
	c.subStep = 0
	c.setState(StCmdOn, reason)
	c.launchOn()
}

// launchOn issues one ON attempt: a fresh full run, to 0 first if the
// position is > 15 on a retry (§5.2), then 99. Retries need two moves for
// the reset; if only one remains, the 99 alone is sent, so a retry can
// never strand the switch at 0.
func (c *Controller) launchOn() {
	pos, ok := c.coverPosition()
	if c.onAttempts > 0 && ok && pos > float64(c.cfg.OffPos) && c.movesAvailable(2) {
		c.subStep = 1 // reset run to 0 first
		c.startMove(c.cfg.CmdOff, "on_retry_reset")
		return
	}
	c.subStep = 2
	c.beginVerification(true)
	if !c.startMove(c.cfg.CmdOn, "on") {
		c.onFailedFinal("move_cap")
	}
}

func (c *Controller) runCmdOn() {
	if c.mv == nil && !c.onRetryAt.IsZero() {
		if c.now.Before(c.onRetryAt) {
			return
		}
		c.onRetryAt = time.Time{}
		c.launchOn()
		return
	}
	m := c.takeMove()
	if m == nil {
		if c.mv == nil && c.onRetryAt.IsZero() && c.subStep == 0 {
			c.launchOn()
		}
		return
	}
	if c.subStep == 1 {
		// The reset run finished (whatever its outcome); now the 99.
		c.subStep = 2
		c.beginVerification(true)
		if !c.startMove(c.cfg.CmdOn, "on") {
			c.onFailedFinal("move_cap")
		}
		return
	}
	if m.outcome == mvOK {
		c.ver.stop = m.stopAt
		c.ver.started = true
		c.setState(StVerifyOn, "motor_ok") // row 18
		return
	}
	c.onAttempts++ // row 19
	if c.onAttempts >= c.cfg.OnMaxAttempts {
		c.strike("on_" + map[int]string{mvRetreat: "retreat", mvStuck: "stuck", mvCmdFailed: "call_failed"}[m.outcome])
		c.onFailedFinal("3_failures")
		return
	}
	c.onRetryAt = c.now.Add(c.cfg.OnRetryGap)
	c.ver = nil
}

// onFailedFinal is row 20: HOLD_ON, alert ON_FAILED, position unknown. The
// latch clears itself once the cover is seen ON with target 99.
func (c *Controller) onFailedFinal(why string) {
	c.ver = nil
	c.mv = nil
	c.onRetryAt = time.Time{}
	c.alert(AlertOnFailed, fmt.Sprintf("Hot water: could not switch the heater ON (%s). Check the switch; holding.", why), 6*time.Hour)
	c.P.Hold = HoldState{Latched: true, Reason: HoldOnFailed, Since: c.now}
	c.persistNow()
}

func (c *Controller) conditionG() bool {
	if !c.onEndsOff || c.onOffDur < c.cfg.GOff {
		return false
	}
	end := c.P.LastFinishedReheatEnd
	return end.IsZero() || c.now.Sub(end) >= c.cfg.GSinceReheat
}

func (c *Controller) runVerifyOn() {
	v := c.ver
	if v == nil {
		c.toIdleOn("no_verification")
		return
	}
	r := c.onResult()
	if r == vrPending {
		return
	}
	c.ver = nil
	switch {
	case r == vrRise && !v.inconclusive() && !v.discAny:
		c.setVerdict(VOnConfirmed, c.verAttrs(v)) // row 21: no clock stamp
		c.toIdleOn("on_confirmed")
	case v.inconclusive() || v.discAny:
		c.setVerdict(VOnPending, c.verAttrs(v)) // row 26
		c.toIdleOn("on_pending")
	case c.conditionG():
		c.subStep = 0
		c.recycleFrom = "verify_on"
		c.setState(StRecycleOn, "no_rise_g") // row 23
		c.runRecycle()
	default:
		c.setVerdict(VOnPositionOnly, c.verAttrs(v)) // row 22: no strike, no alert
		c.toIdleOn("on_position_only")
	}
}

func (c *Controller) toIdleOn(reason string) {
	c.P.OffSince = time.Time{}
	c.onEndsOff = false
	c.testProbe = false
	c.setState(StIdleOn, reason)
}

// runRecycle is rows 24/25: one cycle 99 -> 0 -> 99, then look for a rise.
// runProbe (rows 7, 27, 28) is the same shape from IDLE_ON.
func (c *Controller) runRecycle() { c.runCycle(false) }
func (c *Controller) runProbe()   { c.runCycle(true) }

func (c *Controller) runCycle(probe bool) {
	purpose := "recycle"
	if probe {
		purpose = "probe"
	}
	switch c.subStep {
	case 0: // send 0
		if !c.reserveAfter(1) {
			if probe {
				c.rescheduleProbe()
				c.toIdleOn("probe_skipped_move_budget")
			} else {
				c.cycleNoRise(probe, "no_move_budget")
			}
			return
		}
		if !c.startMove(c.cfg.CmdOff, purpose+"_off") {
			c.toIdleOn(purpose + "_refused")
			return
		}
		c.subStep = 1
	case 1:
		m := c.takeMove()
		if m == nil {
			return
		}
		c.subStep = 2
		c.beginVerification(true)
		if !c.startMove(c.cfg.CmdOn, purpose+"_on") {
			c.onFailedFinal("move_cap")
			return
		}
		if m.outcome != mvOK {
			c.logf("%s: OFF leg %d, continuing to ON", purpose, m.outcome)
		}
	case 2:
		m := c.takeMove()
		if m == nil {
			return
		}
		if m.outcome != mvOK {
			// The ON leg failed: go through the full ON retry path.
			c.startOn(purpose+"_on_failed", false)
			c.onAttempts = 1
			return
		}
		c.ver.stop = m.stopAt
		c.ver.started = true
		c.subStep = 3
	case 3:
		r := c.onResult()
		if r == vrPending {
			return
		}
		v := c.ver
		c.ver = nil
		if r == vrRise && !v.inconclusive() && !v.discAny {
			c.setVerdict(VOnConfirmed, c.verAttrs(v))
			if probe {
				// Row 27: the clock is stamped only by the qualifier (later);
				// reset the probe schedule meanwhile.
				c.rescheduleProbe()
			}
			c.toIdleOn(purpose + "_rise")
			return
		}
		reason := "no_rise"
		if v.inconclusive() || v.discAny {
			reason = "inconclusive"
		}
		c.cycleNoRise(probe, reason)
	}
}

// rescheduleProbe moves the backstop probe +ProbeRepeat of ON time. A
// manual R3 test probe leaves the backstop schedule alone.
func (c *Controller) rescheduleProbe() {
	if c.testProbe {
		return
	}
	c.P.NextProbeOnS = c.P.OnSSinceGenuineStart + c.cfg.ProbeRepeat.Seconds()
}

func (c *Controller) cycleNoRise(probe bool, reason string) {
	if probe {
		// Row 28: next probe +4 h of ON time; alerts come from the clock.
		c.setVerdict(VOnPositionOnly, map[string]any{"probe": true, "reason": reason})
		c.rescheduleProbe()
		c.toIdleOn("probe_" + reason)
		return
	}
	// Row 25: suspect, no strike; alert only past probe_h of ON time.
	c.setVerdict(VSuspect, map[string]any{"reason": reason})
	if c.P.OnSSinceGenuineStart >= c.cfg.ProbeAfter.Seconds() {
		c.alert(AlertSuspect, fmt.Sprintf("Hot water: switch ON but no heater step after a recycle, and %.1f h of ON time since the last genuine start.", c.P.OnSSinceGenuineStart/3600), 6*time.Hour)
	}
	c.toIdleOn("suspect")
}

// ---- holds (rows 29-35, 38, 39; I1) ----

func (c *Controller) enterHold(reason string) {
	prev := c.hold
	c.hold = reason
	if c.P.State == StHoldOff || c.P.State == StCmdOff || c.P.State == StVerifyOff || c.P.State == StIdleOff {
		c.abandonOff()
	}
	if c.mv != nil && c.mv.target == c.cfg.CmdOff {
		c.mv = nil // never complete an OFF into a hold
	}
	if reason == HoldWatchdog {
		c.mv = nil // the watchdog owns the cover
	}
	c.ver = nil
	c.holdOnFails = 0
	c.holdOnRetry = time.Time{}
	if reason == HoldConflict && prev != HoldConflict {
		c.conflictSent = false
		c.alert(AlertConflict, "Hot water: an old hot-water automation is enabled. The controller made at most one ON command and is holding.", 6*time.Hour)
	}
	c.P.State = ""
	c.setState(StHoldOn, reason)
	c.runHold()
}

func (c *Controller) runHold() {
	switch c.hold {
	case HoldWatchdog:
		return // row 31: no moves at all
	case HoldConflict:
		// Row 32 / C12: one ON command if the cover is not ON, then none.
		if !c.conflictSent && !c.coverOn() && c.mv == nil && c.linkUp {
			c.conflictSent = true
			c.startMove(c.cfg.CmdOn, "conflict_on")
		}
		c.takeMove()
		return
	case HoldOnFailed:
		if c.coverOn() && c.targetIsOn() && c.mv == nil {
			c.P.Hold = HoldState{}
			c.logf("on_failed latch cleared: cover reads ON")
			c.persistNow()
		}
		return
	}
	c.ensureOn(true)
	if (c.hold == HoldOverride || c.hold == HoldBackstop) && c.probeDue() && c.mv == nil && c.coverOn() && c.reserveAfter(1) && c.offInputsStale() == "" {
		// Row 29: backstop probes still run under Override On (and the
		// backstop hold). Done inline as a 0 -> 99 pair.
		c.subStep = 0
		c.probeUnderHold = c.hold
		c.hold = ""
		c.setState(StProbe, "probe_due_in_"+c.probeUnderHold)
		c.runProbe()
	}
}

// ensureOn (I1): if the cover is not ON, command ON with up to
// OnMaxAttempts attempts 30 s apart; the final failure strikes and
// latches on_failed (row 20). The motor check is the confirmation here.
func (c *Controller) ensureOn(strikeOnFail bool) {
	if m := c.takeMove(); m != nil {
		if m.outcome == mvOK {
			c.holdOnFails = 0
			return
		}
		if m.target != c.cfg.CmdOn {
			return
		}
		c.holdOnFails++
		if c.holdOnFails >= c.cfg.OnMaxAttempts {
			if strikeOnFail {
				c.strike("hold_on_failed")
			}
			c.onFailedFinal("3_failures_in_" + string(c.P.State))
			return
		}
		c.holdOnRetry = c.now.Add(c.cfg.OnRetryGap)
		return
	}
	if c.mv != nil || c.coverOn() || !c.linkUp || c.holdOnFails >= c.cfg.OnMaxAttempts {
		return
	}
	if _, known := c.coverPosition(); !known {
		return
	}
	if !c.holdOnRetry.IsZero() && c.now.Before(c.holdOnRetry) {
		return
	}
	c.startMove(c.cfg.CmdOn, "ensure_on")
}

func (c *Controller) leaveHold() {
	prev := c.hold
	c.hold = ""
	c.mv = nil
	c.logf("hold %s cleared", prev)
	if prev == HoldBoost {
		// Row 39: return to Auto (IDLE_ON, settle from now), via the boot
		// check so a not-ON cover is commanded.
		c.bootPath("boost_ended")
		return
	}
	c.bootPath("hold_cleared_" + prev) // row 37
}

// ---- Override Off (row 30, C13, D11) ----

func (c *Controller) maybeOverrideOff() bool {
	if !c.entityFresh(RoleSelect) || c.str(RoleSelect) != SelectOverrideOff || c.overrideOffDone {
		return false
	}
	if c.P.State != StIdleOn && c.P.State != StIdleOff {
		return false
	}
	c.overrideOffDone = true
	if !c.tankOK() {
		why := "tank clock unknown"
		if te := c.tankEnd(); !te.IsZero() {
			why = fmt.Sprintf("last finished reheat ended %.1f h ago", c.now.Sub(te).Hours())
		}
		c.alert(AlertOverrideRefused, "Hot water: Override Off refused, staying ON ("+why+").", time.Hour)
		return false
	}
	if s := c.offInputsStale(); s != "" || c.P.Strikes >= 3 || c.batteryLow() || !c.reserveAfter(1) {
		c.alert(AlertOverrideRefused, "Hot water: Override Off refused, staying ON (inputs, strikes, battery or move budget).", time.Hour)
		return false
	}
	c.offPeriod = nil
	c.subStep = 0
	c.setState(StHoldOff, "override_off")
	c.runHoldOff()
	return true
}

func (c *Controller) runHoldOff() {
	switch c.subStep {
	case 0:
		if c.coverPositionAtMost(c.cfg.OffPos) {
			c.subStep = 2
			if c.P.OffSince.IsZero() {
				c.P.OffSince = c.now
			}
			return
		}
		c.beginVerification(false)
		if !c.startMove(c.cfg.CmdOff, "override_off") {
			c.ver = nil
			c.startOn("override_off_no_moves", false)
			return
		}
		c.subStep = 1
		return
	case 1:
		m := c.takeMove()
		if m == nil {
			return
		}
		if m.outcome != mvOK {
			c.strike("override_off_" + map[int]string{mvRetreat: "retreat", mvStuck: "stuck", mvCmdFailed: "call_failed"}[m.outcome])
			c.startOn("override_off_motor_failure", false)
			return
		}
		c.P.OffSince = m.stopAt
		c.heatOffCount = 0
		c.ver.stop, c.ver.started = m.stopAt, true
		c.subStep = 2
	case 2:
		if c.ver != nil {
			switch r := c.offResult(); r {
			case vrPending:
			case vrDrop:
				if c.ver.inconclusive() || c.ver.discAny {
					c.setVerdict(VOffUnverified, c.verAttrs(c.ver))
				} else {
					c.setVerdict(VOffConfirmed, c.verAttrs(c.ver))
				}
				c.ver = nil
			case vrNoDrop:
				if c.ver.inconclusive() {
					c.setVerdict(VOffUnverified, c.verAttrs(c.ver))
				} else {
					c.setVerdict(VOffFailed, c.verAttrs(c.ver))
				}
				c.ver = nil
			default:
				c.setVerdict(VOffUnverified, c.verAttrs(c.ver))
				c.ver = nil
			}
		}
		c.heatWhileOff()
		if why := c.offRevoked(true); why != "" {
			if why == "tank_limit" {
				c.alert(AlertOverrideExpired, "Hot water: Override Off expired at the tank limit; switching ON. Set the selector back to Auto.", time.Hour)
			}
			if !c.linkUp {
				c.enterDegraded("override_off_ha_lost")
				return
			}
			c.startOn("override_off_end_"+why, true)
		}
	}
}

func (c *Controller) coverPositionAtMost(p int) bool {
	pos, ok := c.coverPosition()
	return ok && pos <= float64(p)
}

// ---- SHADOW (§3): the whole policy runs, nothing moves ----

func (c *Controller) evaluateShadow() {
	hold := c.desiredHold()
	switch {
	case hold != "":
		if c.P.State != StHoldOn || c.hold != hold {
			c.hold = hold
			c.P.State = ""
			c.setState(StHoldOn, hold)
		}
	case c.P.State == StHoldOn:
		c.hold = ""
		c.bootPath("hold_cleared")
	case c.P.State == StBoot:
		if s := c.bootStale(); s != "" {
			if c.bootTimedOut() {
				c.setState(StDegraded, "boot_timeout_stale_"+s)
			}
		} else if c.coverOn() {
			c.setState(StIdleOn, "observed_on")
		} else {
			c.setState(StIdleOff, "observed_off")
		}
	case c.P.State == StDegraded:
		if c.criticalStale() != "" {
			c.healthySince = time.Time{}
		} else {
			if c.healthySince.IsZero() {
				c.healthySince = c.now
			}
			if c.now.Sub(c.healthySince) >= c.cfg.DegradedRecover {
				c.bootPath("degraded_recovered")
			}
		}
	default:
		if s := c.criticalStale(); s != "" {
			c.healthySince = time.Time{}
			c.setState(StDegraded, "stale_"+s)
		} else if c.coverOn() && c.P.State != StIdleOn {
			c.setState(StIdleOn, "observed_on")
		} else if !c.coverOn() && c.P.State != StIdleOff {
			c.setState(StIdleOff, "observed_off")
		}
	}
	c.shadowDecide()
}

// shadowDecide maintains the "would" output: a virtual OFF period begun
// when MayGoOff would allow it, ended by the conditions of row 15.
func (c *Controller) shadowDecide() {
	if !c.detPresent {
		c.shadowWould, c.shadowReason = "none", "degraded:no_detector"
		return
	}
	if c.shadowOff != nil {
		why := ""
		switch {
		case c.P.State != StIdleOn:
			why = "state_" + string(c.P.State)
		case !c.tankOK():
			why = "tank_limit"
		case c.P.Boost.Active:
			why = "boost"
		case c.str(RoleSelect) != SelectAuto:
			why = "select_changed"
		case !c.now.Before(c.shadowOff.End):
			why = "period_end"
		case c.offInputsStale() != "":
			why = "stale_" + c.offInputsStale()
		default:
			// Same frozen-period rule as live (fix 4).
			end, w := c.frozenEnd(*c.shadowOff)
			if w != "" {
				why = w
			} else {
				c.shadowOff.End = end
			}
		}
		if why != "" {
			c.shadowOff = nil
			c.shadowWould, c.shadowReason = "on", "end_off_"+why
		}
		return
	}
	if c.P.State == StIdleOn && c.shadowProbeDue() {
		// Shadow keeps its own schedule: advancing the persisted one here
		// would swallow the real probe when the controller goes live
		// (it is briefly shadow at every start, before the arming input).
		c.shadowWould, c.shadowReason = "probe", "probe_due"
		c.shadowNextProbe = c.P.OnSSinceGenuineStart + c.cfg.ProbeRepeat.Seconds()
		return
	}
	if ok, why := c.MayGoOff(true); ok {
		p, _ := c.plan.PeriodAt(c.now)
		c.shadowOff = &p
		c.shadowOffsToday++
		c.shadowWould, c.shadowReason = "off", why
		return
	} else if c.shadowWould == "off" || c.shadowWould == "" || c.shadowWould == "probe" {
		c.shadowWould, c.shadowReason = "none", why
	} else {
		c.shadowReason = why
	}
}

func (c *Controller) shadowSummary() {
	if c.Live() || c.cfg.ShadowSummaryHour < 0 {
		return
	}
	l := c.now.In(c.cfg.Loc)
	day := l.Format("2006-01-02")
	if l.Hour() != c.cfg.ShadowSummaryHour || c.lastSummaryDay == day || !c.started {
		return
	}
	if c.lastSummaryDay == "" {
		c.lastSummaryDay = day // no summary in the first hour after a start
		return
	}
	c.lastSummaryDay = day
	text := fmt.Sprintf("Hot water controller (shadow): would-OFF periods since last summary %d; state %s; ON hours since genuine start %.1f; hours since last finished reheat %s.",
		c.shadowOffsToday, c.stateLabel(), c.P.OnSSinceGenuineStart/3600, hoursSince(c.now, c.P.LastFinishedReheatEnd))
	c.shadowOffsToday = 0
	c.emit(Effect{Kind: EffNotify, Code: AlertShadowDaily, Title: "Hot water controller (shadow)", Text: text})
}

func hoursSince(now, t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return fmt.Sprintf("%.1f", now.Sub(t).Hours())
}
