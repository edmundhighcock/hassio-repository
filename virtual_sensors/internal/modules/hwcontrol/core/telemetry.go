package core

import (
	"encoding/json"
	"math"
	"time"
)

// ---- verification lifecycle (§5.2 pre, §5.3) ----

// beginVerification takes the baseline at the command instant: the
// time-weighted median of the last 30 s of the held residual, the EV
// reading, the discontinuity sequence, and (OFF) whether the heater is
// drawing per the step tracker.
func (c *Controller) beginVerification(on bool) {
	v := &verification{on: on, baseAt: c.now, baseSeq: c.lastSeq, seqKnown: c.seqKnown}
	if !c.residualStale() {
		v.baseline, v.baselineOK = c.res.median(c.now, c.cfg.BaselineWindow)
	}
	v.baseEV, v.baseEVOK = c.num(RoleEV)
	// Review fix 3: an up-step counts as "drawing" only once SUSTAINED for
	// the D3 genuine-start duration (5 min). A step not yet sustained (a
	// dip-and-recover reads as one) does not count: the heater is idle or
	// unclear (row 13), so no spurious off_failed. A step with a
	// discontinuity within ±60 s cannot be told from an administrative
	// jump: it is ambiguous (see offResult: no drop returns ON).
	k := &c.tracker
	if k.drawing {
		switch {
		case c.discNear(k.upAt):
			v.ambiguous = true
		case c.now.Sub(k.upAt) >= c.cfg.GenuineMinDur:
			v.drawing = true
		}
	}
	c.ver = v
}

// rNow is R(t): the 20 s time-weighted rolling median of the residual,
// with every discontinuity's DeltaW since the baseline subtracted.
func (c *Controller) rNow() (float64, bool) {
	m, ok := c.res.median(c.now, c.cfg.MedianWindow)
	if !ok || c.ver == nil {
		return 0, false
	}
	return m - c.ver.discSum, true
}

func (c *Controller) advanceVerify() {
	v := c.ver
	if v == nil {
		return
	}
	if c.residualStale() {
		v.stale = true
	}
	if !v.started || !v.baselineOK {
		return
	}
	r, ok := c.rNow()
	if !ok {
		v.stale = true
		return
	}
	if v.on {
		rise := r - v.baseline
		v.maxRise = math.Max(v.maxRise, rise)
		if rise >= c.cfg.ConfirmW && !c.now.After(v.stop.Add(c.cfg.OnWindow+c.cfg.OnHold)) {
			if v.aboveSince.IsZero() {
				if !c.now.After(v.stop.Add(c.cfg.OnWindow)) {
					v.aboveSince = c.now
				}
			}
		} else if rise < c.cfg.ConfirmW {
			v.aboveSince = time.Time{}
		}
	} else {
		v.maxDrop = math.Max(v.maxDrop, v.baseline-r)
	}
}

func (c *Controller) onResult() int {
	v := c.ver
	if v == nil || !v.started {
		return vrPending
	}
	if !v.baselineOK {
		return vrNoRise // inconclusive() is true; callers check it first
	}
	if !v.aboveSince.IsZero() && c.now.Sub(v.aboveSince) >= c.cfg.OnHold {
		return vrRise
	}
	if c.now.Sub(v.stop) >= c.cfg.OnWindow+c.cfg.OnHold {
		return vrNoRise
	}
	return vrPending
}

func (c *Controller) offResult() int {
	v := c.ver
	if v == nil || !v.started {
		return vrPending
	}
	if !v.drawing && !v.ambiguous {
		return vrIdle // row 13: idle, off_unverified, stays OFF
	}
	// No drop after a sustained step is row 14 (off_failed, back ON). With
	// an inconclusive window, or a discontinuity-flagged step, it is
	// genuinely ambiguous: back ON too (never left OFF on doubt).
	noDrop := vrNoDrop
	if v.ambiguous || v.inconclusive() {
		noDrop = vrAmbiguous
	}
	if !v.baselineOK {
		return noDrop
	}
	if v.maxDrop >= c.cfg.ConfirmW {
		return vrDrop
	}
	if c.now.Sub(v.stop) >= c.cfg.OffWindow {
		return noDrop
	}
	return vrPending
}

func (c *Controller) verAttrs(v *verification) map[string]any {
	return map[string]any{
		"at": c.now.Format(time.RFC3339), "baseline_w": math.Round(v.baseline),
		"rise_w": math.Round(v.maxRise), "drop_w": math.Round(v.maxDrop),
		"ev_moved": v.evMoved, "disc_in_window": v.discAny, "residual_stale": v.stale,
		"seq_gap": v.seqGap, "burst": v.burst, "drawing_at_baseline": v.drawing,
		"step_ambiguous_at_baseline": v.ambiguous,
	}
}

// ---- telemetry (§2.5) ----

// Healthy is the heartbeat condition (§6.1): the core loop ran, no input
// stale (held values count), the WS connected, not DEGRADED.
func (c *Controller) Healthy() bool {
	return c.P.State != StDegraded && c.criticalStale() == ""
}

func (c *Controller) publish(obj, state string, attrs map[string]any, force bool) {
	b, _ := json.Marshal(attrs)
	key := state + "|" + string(b)
	if !force && c.lastPub[obj] == key {
		return
	}
	c.lastPub[obj] = key
	c.emit(Effect{Kind: EffPublish, ObjectID: obj, State: state, Attrs: attrs})
}

func (c *Controller) modeName() string {
	if c.Live() {
		return "live"
	}
	return "shadow"
}

func (c *Controller) telemetry(tick bool) {
	// Full telemetry on ticks, or at once when something visible changed.
	sig := c.stateLabel() + "|" + c.reason + "|" + c.verdict + "|" + c.lastAlert + "|" + c.shadowWould + "|" + c.shadowReason
	due := c.lastHeartbeat.IsZero() || c.now.Sub(c.lastHeartbeat) >= c.cfg.Heartbeat
	if sig == c.lastSig && !(tick && due) {
		return
	}
	c.lastSig = sig
	refresh := false
	if c.Healthy() && (c.lastHeartbeat.IsZero() || c.now.Sub(c.lastHeartbeat) >= c.cfg.Heartbeat) {
		c.lastHeartbeat = c.now
		refresh = true
		c.publish(ObjHeartbeat, itoa(c.now.Unix()), map[string]any{"mode": c.modeName(), "version": c.cfg.Version}, true)
	}
	stateStr := c.stateLabel()
	if !c.detPresent {
		stateStr = "degraded:no_detector"
	}
	c.publish(ObjState, stateStr, map[string]any{"since": c.P.StateSince.Format(time.RFC3339), "reason": c.reason, "mode": c.modeName()}, refresh)

	desired, dreason := "on", c.reason
	periodEnd := ""
	switch c.P.State {
	case StCmdOff, StVerifyOff, StIdleOff, StHoldOff:
		desired = "off"
		if c.offPeriod != nil {
			periodEnd = c.offPeriod.End.Format(time.RFC3339)
		}
	}
	c.publish(ObjDesired, desired, map[string]any{"reason": dreason, "period_end": periodEnd}, refresh)

	if c.verdict != "" {
		attrs := map[string]any{"at": c.verdictAt.Format(time.RFC3339)}
		for k, v := range c.verdictAttrs {
			attrs[k] = v
		}
		c.publish(ObjVerdict, c.verdict, attrs, refresh)
	}
	c.publish(ObjOnHours, ftoa1(c.P.OnSSinceGenuineStart/3600), map[string]any{
		"last_genuine_start": fmtTime(c.P.LastGenuineStart), "qualifier": c.P.LastGenuineQualifier,
		"next_probe_on_h": ftoa1(c.P.NextProbeOnS / 3600)}, refresh)
	hs := "unknown"
	if !c.P.LastFinishedReheatEnd.IsZero() {
		hs = ftoa1(c.now.Sub(c.P.LastFinishedReheatEnd).Hours())
	}
	c.publish(ObjHoursSinceEnd, hs, map[string]any{
		"last_finished_reheat_end": fmtTime(c.P.LastFinishedReheatEnd), "last_finished_reheat_start": fmtTime(c.P.LastFinishedReheatStart),
		"satisfied_window_end": fmtTime(c.P.SatisfiedEnd)}, refresh && tick)
	c.publish(ObjMovesToday, itoa(int64(c.movesInLast(24*time.Hour))), map[string]any{
		"last_move_at": fmtTime(c.lastMoveAt()), "moves_last_hour": c.movesInLast(time.Hour)}, refresh)
	c.publishPlan(refresh)
	if !c.Live() {
		c.publish(ObjShadowWould, orNone(c.shadowWould), map[string]any{"reason": c.shadowReason}, refresh)
	} else {
		c.publish(ObjShadowWould, "none", map[string]any{"reason": "live"}, refresh)
	}
	if c.lastAlert != "" {
		c.publish(ObjAlert, c.lastAlert, map[string]any{"text": c.lastAlertText, "at": c.lastAlertAt.Format(time.RFC3339)}, refresh)
	}
	c.publish(ObjStrikes, itoa(int64(c.P.Strikes)), map[string]any{}, refresh)
}

func (c *Controller) publishPlan(force bool) {
	var periods []map[string]any
	planner, gen, valid := "", "", ""
	n := 0
	if c.plan != nil {
		planner = c.plan.PlannerID
		gen = c.plan.GeneratedAt.Format(time.RFC3339)
		valid = c.plan.ValidUntil.Format(time.RFC3339)
		for _, p := range c.plan.OffPeriods {
			if p.End.Before(c.now) {
				continue
			}
			periods = append(periods, map[string]any{"start": p.Start.Format(time.RFC3339), "end": p.End.Format(time.RFC3339), "why": p.Why})
		}
		if c.plan.Valid(c.now) {
			n = len(periods)
		}
	}
	attrs := map[string]any{"periods": periods, "planner": planner, "generated_at": gen, "valid_until": valid}
	if c.plan != nil {
		for k, v := range c.plan.Extra {
			attrs[k] = v
		}
	}
	c.publish(ObjPlan, itoa(int64(n)), attrs, force)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func ftoa1(v float64) string {
	b, _ := json.Marshal(math.Round(v*10) / 10)
	return string(b)
}
