package core

import (
	"testing"
	"time"
)

// Row 3: inputs fresh, position >= 90 -> IDLE_ON, settle before any OFF.
// Row 6 + C15: a period that began during the settle is entered when the
// settle ends.
func TestRow3BootOnAndSettleThenRow6Off(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.wantState("IDLE_ON")
	r.tankRecent(time.Hour)
	r.planNow(0, 3*time.Hour) // period already open during the settle
	r.advance(14 * time.Minute)
	if r.countCmds(0) != 0 {
		t.Fatal("OFF during the settle")
	}
	r.advance(2 * time.Minute)
	if r.countCmds(0) != 1 {
		t.Fatalf("no OFF at settle end, state %s reason %s", r.state(), r.c.reason)
	}
}

// Rows 6, 9, 12, 15, 18, 21: the happy OFF/ON cycle with the heater drawing.
func TestHappyOffOnCycle(t *testing.T) {
	r := readyForOff(t)
	r.calling = true
	r.emitResidual(true)
	r.advance(17 * time.Minute)
	r.wantState("IDLE_OFF")
	if r.c.verdict != VOffConfirmed {
		t.Fatalf("verdict %s, want off_confirmed", r.c.verdict)
	}
	r.advance(2 * time.Hour) // period end
	r.advance(3 * time.Minute)
	r.wantState("IDLE_ON")
	if r.c.verdict != VOnConfirmed {
		t.Fatalf("verdict %s, want on_confirmed (%v)", r.c.verdict, r.c.verdictAttrs)
	}
	if !r.switchOn {
		t.Fatal("plant not ON at the end")
	}
	// Row 21: on_confirmed never stamps the genuine-start clock.
	if !r.c.P.LastGenuineStart.Equal(r.c.P.LastFinishedReheatStart) {
		t.Fatal("on_confirmed stamped the clock")
	}
}

// Row 13: heater idle at baseline -> off_unverified, no recycle.
func TestRow13OffUnverifiedWhenIdle(t *testing.T) {
	r := readyForOff(t)
	r.advance(17 * time.Minute)
	r.wantState("IDLE_OFF")
	if r.c.verdict != VOffUnverified {
		t.Fatalf("verdict %s", r.c.verdict)
	}
	if r.countCmds(0) != 1 || r.countCmds(99) != 0 {
		t.Fatalf("cmds %v", r.cmds)
	}
}

// Row 14: drawing at baseline, no drop -> off_failed, IDLE_ON, no strike.
func TestRow14OffFailedNoStrike(t *testing.T) {
	r := readyForOff(t)
	r.calling = true
	r.stuckClosed = true // the motor reaches 0 but the contact stays closed
	r.emitResidual(true)
	r.advance(18 * time.Minute)
	if r.c.verdict != VOffFailed {
		t.Fatalf("verdict %s, state %s", r.c.verdict, r.state())
	}
	if r.c.P.Strikes != 0 {
		t.Fatal("off_failed struck")
	}
	// The block is abandoned with no recycle; with the cover at 0, row 8
	// then brings the cover back to 99.
	r.advance(2 * time.Minute)
	if r.countCmds(0) != 1 || r.countCmds(99) != 1 || r.state() != "IDLE_ON" {
		t.Fatalf("state %s cmds %v", r.state(), r.cmds)
	}
}

// Row 10: retreat on OFF -> strike, CMD_ON.
func TestRow10OffRetreatStrikesAndGoesOn(t *testing.T) {
	r := readyForOff(t)
	r.retreatN = 1
	r.advance(18 * time.Minute)
	if r.c.P.Strikes != 1 {
		t.Fatalf("strikes %d", r.c.P.Strikes)
	}
	if r.countCmds(99) < 1 || !r.switchOn {
		t.Fatalf("not back ON: cmds %v", r.cmds)
	}
}

// Row 11 / C11: three call_service failures on an OFF are a strike.
func TestRow11OffCallFailuresStrike(t *testing.T) {
	r := readyForOff(t)
	r.failCalls = 3
	r.advance(18 * time.Minute)
	if r.c.P.Strikes != 1 {
		t.Fatalf("strikes %d, state %s", r.c.P.Strikes, r.state())
	}
	if len(r.c.P.Moves) == 0 || r.countCmds(99) == 0 {
		t.Fatalf("no ON after the failed OFF: %v", r.cmds)
	}
}

// Rows 19, 20: retreats on ON retry as fresh full runs, <= 3 attempts,
// then HOLD_ON with ON_FAILED and a strike.
func TestRows19And20OnRetriesThenHold(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.pos, r.target, r.switchOn = 0, 0, false
	r.retreatN = 10
	r.boot(true)
	r.advance(5 * time.Minute)
	r.wantState("HOLD_ON(on_failed)")
	if r.notifies(AlertOnFailed) != 1 {
		t.Fatalf("ON_FAILED notifies %d", r.notifies(AlertOnFailed))
	}
	if r.c.P.Strikes != 1 {
		t.Fatalf("strikes %d", r.c.P.Strikes)
	}
	if n := r.countCmds(99); n != 3 {
		t.Fatalf("99 commands %d, want 3 attempts (cmds %v)", n, r.cmds)
	}
	// The latch clears once the cover reads ON with target 99.
	r.retreatN = 0
	r.pos, r.target = 99, 99
	r.pushCover()
	r.pushTarget()
	r.advance(10 * time.Second)
	if r.state() == "HOLD_ON(on_failed)" {
		t.Fatal("on_failed latch did not clear")
	}
}

// Row 4 / C10: a restart with the cover below 90 commands ON and does not
// resume the OFF period, even though the persisted period is still open.
func TestRow4RestartMidOffGoesOn(t *testing.T) {
	r := readyForOff(t)
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	b, _ := r.c.Persist()
	// Rebuild from the persisted bytes (process kill).
	r2 := newRig(t, liveCfg(), b)
	r2.now = r.now
	r2.pos, r2.target, r2.switchOn = 0, 0, false
	r2.plan(r.c.plan)
	r2.boot(true)
	r2.advance(time.Minute)
	if r2.countCmds(99) != 1 || !r2.switchOn {
		t.Fatalf("restart mid-OFF did not go ON: %v state %s", r2.cmds, r2.state())
	}
	if r2.countCmds(0) != 0 {
		t.Fatal("OFF resumed after restart")
	}
}

func (r *rig) plan(p *Plan) { r.step(PlanUpdate{At: r.now, Plan: p}) }

// Row 5 and 36: an input stale at the boot timer -> DEGRADED, heartbeat
// withheld; fresh for 2 min -> BOOT path.
func TestRow5DegradedAndRow36Recover(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.residualOff = true
	r.step(Detector{At: r.now, Present: true})
	r.step(Link{At: r.now, Up: true})
	r.pushCover()
	r.pushTarget()
	r.set(RoleSelect, SelectAuto)
	r.advance(4 * time.Minute)
	r.wantState("DEGRADED")
	if _, ok := r.lastPublished(ObjHeartbeat); ok {
		t.Fatal("heartbeat published while degraded")
	}
	r.residualOff = false
	r.emitResidual(false)
	r.advance(3 * time.Minute)
	r.wantState("IDLE_ON")
	if _, ok := r.lastPublished(ObjHeartbeat); !ok {
		t.Fatal("no heartbeat after recovery")
	}
}

// §4.0 / N9: a quiet residual (held value) for 28 min is not stale; 31
// min with no update is.
func TestHeldValuesNotStaleUntil30Min(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.sampleEvery = 28 * time.Minute
	r.boot(true)
	r.advance(28*time.Minute - 10*time.Second)
	r.wantState("IDLE_ON")
	r.sampleEvery = 10 * time.Hour
	r.advance(4 * time.Minute)
	r.wantState("DEGRADED")
}

// Row 17: an input stale while OFF -> CMD_ON.
func TestRow17StaleWhileOffGoesOn(t *testing.T) {
	r := readyForOff(t)
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	r.set(RoleBattery, "unavailable")
	r.advance(time.Minute)
	if r.countCmds(99) != 1 || !r.switchOn {
		t.Fatalf("not ON after stale input: %v %s", r.cmds, r.state())
	}
}

// Row 17: HA connection lost while OFF -> DEGRADED (commands impossible);
// on reconnect BOOT commands ON.
func TestRow17LinkLostWhileOff(t *testing.T) {
	r := readyForOff(t)
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	r.step(Link{At: r.now, Up: false})
	r.advance(10 * time.Second)
	r.wantState("DEGRADED")
	r.step(Link{At: r.now, Up: true})
	r.pushCover()
	r.advance(time.Minute)
	if !r.switchOn {
		t.Fatalf("not ON after reconnect, state %s", r.state())
	}
}

// Row 16: two heater-shaped steps while OFF -> CMD_ON.
func TestRow16HeatWhileOff(t *testing.T) {
	r := readyForOff(t)
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	step := func() {
		r.baseW = 3300
		r.emitResidual(true)
		r.advance(4 * time.Minute)
		r.baseW = 400
		r.emitResidual(true)
		r.advance(2 * time.Minute)
	}
	step()
	r.wantState("IDLE_OFF")
	step()
	if r.countCmds(99) != 1 {
		t.Fatalf("2nd heat-while-off did not command ON: %v %s", r.cmds, r.state())
	}
}

// Row 15: the plan revised so the period no longer covers now -> ON.
func TestRow15PlanRevokedEndsOff(t *testing.T) {
	r := readyForOff(t)
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	r.plan(nil)
	r.advance(time.Minute)
	if !r.switchOn {
		t.Fatal("OFF continued after the plan was withdrawn")
	}
}

// S11: tank clock edges and an OFF running into the limit.
func TestTankClockEdges(t *testing.T) {
	for _, tc := range []struct {
		ago  time.Duration
		want bool
	}{{13*time.Hour + 59*time.Minute - 16*time.Minute, true}, {14*time.Hour + time.Minute - 16*time.Minute, false}} {
		// The OFF decision falls on the first tick at the period start
		// (16 min after tankRecent), so the age there is ago + ~16 min.
		r := newRig(t, liveCfg(), nil)
		r.boot(true)
		r.tankRecent(tc.ago)
		r.planNow(16*time.Minute, time.Hour)
		r.advance(17 * time.Minute)
		if got := r.countCmds(0) == 1; got != tc.want {
			t.Fatalf("ago %v at OFF time: OFF=%v want %v (%s)", tc.ago+16*time.Minute, got, tc.want, r.c.reason)
		}
	}
	// An OFF running at the limit goes ON.
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.tankRecent(13 * time.Hour)
	r.planNow(16*time.Minute, 5*time.Hour)
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	r.advance(45 * time.Minute)
	if !r.switchOn {
		t.Fatal("OFF ran past the tank limit")
	}
}

// Unknown tank clock (fresh install): no OFF at all.
func TestUnknownTankClockNoOff(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.planNow(0, 5*time.Hour)
	r.advance(time.Hour)
	if r.countCmds(0) != 0 {
		t.Fatal("OFF with the tank clock unknown")
	}
	if ok, why := r.c.MayGoOff(false); ok || why != "tank_clock_unknown" {
		t.Fatalf("MayGoOff %v %s", ok, why)
	}
}

// S12: evening rule.
func TestEveningRule(t *testing.T) {
	// With the default 18:00 the 14 h tank clock already implies the rule
	// (D20), so test the rule on its own with evening_off_from = 10:00: a
	// reheat that ended 22:00 local yesterday is within the tank clock
	// but not "earlier the same local day".
	cfg := liveCfg()
	cfg.EveningOffFromMin = 10 * 60
	r := newRig(t, cfg, nil)
	r.now = time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC) // 10:30 local
	r.boot(true)
	r.tankRecent(12*time.Hour + 30*time.Minute) // 22:00 local yesterday
	r.planNow(16*time.Minute, time.Hour)
	r.advance(20 * time.Minute)
	if r.countCmds(0) != 0 {
		t.Fatal("evening OFF without a reheat today")
	}
	// A finished reheat earlier today permits it.
	r.c.P.LastFinishedReheatEnd = r.now.Add(-3 * time.Hour)
	r.advance(time.Minute)
	if r.countCmds(0) != 1 {
		t.Fatalf("evening OFF refused with a reheat today: %s", r.c.reason)
	}
}

// Rule 9: EV guard window and EV movement.
func TestEVGuard(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.now = time.Date(2026, 11, 10, 20, 40, 0, 0, time.UTC) // 21:40 local
	r.boot(true)
	r.tankRecent(time.Hour)
	r.c.P.LastFinishedReheatEnd = r.now.Add(-time.Hour)
	r.planNow(0, 3*time.Hour)
	r.advance(16 * time.Minute) // 21:56: inside 22:00 ± 10
	if r.countCmds(0) != 0 {
		t.Fatal("OFF inside the EV guard window")
	}
	r.advance(13 * time.Minute) // 22:09: still inside; the EV moves now
	r.set(RoleEV, "7000")
	r.advance(90 * time.Second) // 22:10:30: outside the window, EV moved 90 s ago
	if r.countCmds(0) != 0 {
		t.Fatal("OFF right after an EV move")
	}
	r.advance(2 * time.Minute)
	if r.countCmds(0) != 1 {
		t.Fatalf("no OFF after the EV settled: %s", r.c.reason)
	}
}

// No plan, invalid plan, expired plan -> no OFF.
func TestPlanValidity(t *testing.T) {
	for name, p := range map[string]*Plan{
		"nil":              nil,
		"expired":          {ValidUntil: t0.Add(10 * time.Minute), OffPeriods: []OffPeriod{{Start: t0, End: t0.Add(3 * time.Hour)}}},
		"overlap":          {ValidUntil: t0.Add(48 * time.Hour), OffPeriods: []OffPeriod{{Start: t0, End: t0.Add(3 * time.Hour)}, {Start: t0.Add(time.Hour), End: t0.Add(4 * time.Hour)}}},
		"too_long":         {ValidUntil: t0.Add(48 * time.Hour), OffPeriods: []OffPeriod{{Start: t0, End: t0.Add(25 * time.Hour)}}},
		"end_before_start": {ValidUntil: t0.Add(48 * time.Hour), OffPeriods: []OffPeriod{{Start: t0.Add(time.Hour), End: t0}}},
	} {
		r := newRig(t, liveCfg(), nil)
		r.boot(true)
		r.tankRecent(time.Hour)
		r.plan(p)
		r.advance(30 * time.Minute)
		if r.countCmds(0) != 0 {
			t.Fatalf("%s: OFF issued", name)
		}
	}
}

// Rows 22, 23, 24, 25 and condition G.
func TestVerifyOnBranches(t *testing.T) {
	// No rise, G false (short OFF): on_position_only, no recycle.
	r := readyForOff(t)
	r.planNow(16*time.Minute, 30*time.Minute)
	r.advance(50 * time.Minute)
	r.wantState("IDLE_ON")
	if r.c.verdict != VOnPositionOnly || r.countCmds(0) != 1 {
		t.Fatalf("verdict %s cmds %v", r.c.verdict, r.cmds)
	}

	// No rise, G true (OFF >= 2 h, last reheat >= 4 h ago): recycle; a
	// rise on the recycle -> on_confirmed.
	r = readyForOff(t)
	r.tankRecent(3 * time.Hour)
	r.planNow(16*time.Minute, 2*time.Hour+10*time.Minute)
	// ON at the period end (~2 h 26 min), motor ~25 s, rise window 105 s:
	// by 2 h 28 min 30 s the no-rise verdict has sent it to RECYCLE_ON.
	r.advance(2*time.Hour + 28*time.Minute + 30*time.Second)
	r.wantState("RECYCLE_ON")
	// The thermostat calls during the recycle.
	r.calling = true
	r.advance(3 * time.Minute)
	r.wantState("IDLE_ON")
	if r.c.verdict != VOnConfirmed || r.countCmds(0) != 2 {
		t.Fatalf("verdict %s cmds %v", r.c.verdict, r.cmds)
	}

	// Still no rise after the recycle: suspect, no strike, no alert below
	// probe_h.
	r = readyForOff(t)
	r.tankRecent(3 * time.Hour)
	r.planNow(16*time.Minute, 2*time.Hour+10*time.Minute)
	r.advance(2*time.Hour + 32*time.Minute)
	r.wantState("IDLE_ON")
	if r.c.verdict != VSuspect || r.c.P.Strikes != 0 || r.notifies(AlertSuspect) != 0 {
		t.Fatalf("verdict %s strikes %d alerts %d", r.c.verdict, r.c.P.Strikes, r.notifies(AlertSuspect))
	}
}

// Row 26 / S5 / S6: a discontinuity or an EV move inside the window is
// never on_confirmed.
func TestRow26InconclusiveNeverConfirmed(t *testing.T) {
	for _, kind := range []string{"disc", "ev"} {
		r := readyForOff(t)
		r.planNow(16*time.Minute, 30*time.Minute)
		r.advance(46*time.Minute + 5*time.Second) // ON commanded at 46 min
		r.calling = true                          // a real rise follows
		if kind == "disc" {
			r.step(Discontinuity{At: r.now, Entity: "fridge_power", DeltaW: 80, Seq: r.c.lastSeq + 1})
		} else {
			r.set(RoleEV, "7000")
		}
		r.advance(3 * time.Minute)
		if r.c.verdict == VOnConfirmed {
			t.Fatalf("%s: confirmed despite inconclusive window", kind)
		}
		if r.c.verdict != VOnPending {
			t.Fatalf("%s: verdict %s, want on_pending", kind, r.c.verdict)
		}
	}
}

// Rows 29, 31, 32, 33, 34: holds.
func TestHolds(t *testing.T) {
	// Override On from IDLE_OFF: ensure ON.
	r := readyForOff(t)
	r.advance(18 * time.Minute)
	r.set(RoleSelect, SelectOverrideOn)
	r.advance(time.Minute)
	r.wantState("HOLD_ON(override)")
	if !r.switchOn {
		t.Fatal("override did not ensure ON")
	}

	// Watchdog latch: zero cover commands, even with the cover OFF (S7).
	r = newRig(t, liveCfg(), nil)
	r.pos, r.target, r.switchOn = 0, 0, false
	r.boot(true)
	r.set(RoleLatch, "on")
	n := len(r.cmds)
	r.advance(10 * time.Minute)
	r.set(RoleBoost, "2026-11-10T09:30:00+00:00") // a boost press does not move either
	r.advance(10 * time.Minute)
	if len(r.cmds) != n {
		t.Fatalf("moves under the watchdog latch: %v", r.cmds[n:])
	}

	// Conflict: one ON command if the cover is not ON, then none (C12).
	r = newRig(t, liveCfg(), nil)
	r.pos, r.target, r.switchOn = 0, 0, false
	r.stickN = 5
	r.step(EntityUpdate{At: r.now, Role: RoleConflict, Entity: "automation.hot_water", State: "on"})
	r.boot(true)
	r.advance(20 * time.Minute)
	r.wantState("HOLD_ON(conflict)")
	if len(r.cmds) != 1 || r.cmds[0] != 99 {
		t.Fatalf("conflict commands %v, want exactly one 99", r.cmds)
	}
	if r.notifies(AlertConflict) != 1 {
		t.Fatal("no CONFLICT alert")
	}

	// Battery below the floor: HOLD_ON within one tick (S10), no OFF.
	r = readyForOff(t)
	r.set(RoleBattery, "29")
	r.advance(5 * time.Second)
	r.wantState("HOLD_ON(battery)")
	r.advance(time.Hour)
	if r.countCmds(0) != 0 {
		t.Fatal("OFF with the battery below the floor")
	}

	// Strikes >= 3: HOLD_ON; decays after 24 h.
	r = readyForOff(t)
	r.c.P.Strikes = 3
	r.c.P.LastStrikeAt = r.now
	r.advance(5 * time.Second)
	r.wantState("HOLD_ON(strikes)")
	r.advance(24*time.Hour + time.Minute)
	if r.c.P.Strikes != 2 || r.state() == "HOLD_ON(strikes)" {
		t.Fatalf("strikes %d state %s", r.c.P.Strikes, r.state())
	}
}

// Row 30 / C13: Override Off honoured while the tank clock allows, expires
// at the limit with an alert; refused if the clock is unknown.
func TestOverrideOff(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.set(RoleSelect, SelectOverrideOff)
	r.advance(time.Minute)
	if r.countCmds(0) != 0 || r.notifies(AlertOverrideRefused) != 1 {
		t.Fatalf("unknown tank clock: cmds %v refused alerts %d", r.cmds, r.notifies(AlertOverrideRefused))
	}

	r = newRig(t, liveCfg(), nil)
	r.boot(true)
	r.tankRecent(13 * time.Hour)
	r.set(RoleSelect, SelectOverrideOff)
	r.advance(time.Minute)
	r.wantState("HOLD_OFF")
	if r.switchOn {
		t.Fatal("Override Off did not switch OFF")
	}
	r.advance(62 * time.Minute)
	if !r.switchOn || r.notifies(AlertOverrideExpired) != 1 {
		t.Fatalf("Override Off did not expire at the tank limit: on=%v alerts=%d state=%s", r.switchOn, r.notifies(AlertOverrideExpired), r.state())
	}
	// Not re-honoured while the select still says Override Off.
	r.advance(30 * time.Minute)
	if !r.switchOn {
		t.Fatal("Override Off re-honoured after expiry")
	}
}

// Row 35: disarm while OFF -> ensure ON, then SHADOW.
func TestRow35DisarmEnsuresOnThenShadow(t *testing.T) {
	r := readyForOff(t)
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	r.set(RoleArmed, "off")
	r.advance(time.Minute)
	if !r.switchOn {
		t.Fatal("disarm did not ensure ON")
	}
	if r.c.Live() {
		t.Fatal("still live after disarm")
	}
	n := len(r.cmds)
	r.advance(2 * time.Hour)
	if len(r.cmds) != n {
		t.Fatal("shadow moved the cover")
	}
}

// Rows 38/39 and §4.7: boost.
func TestBoost(t *testing.T) {
	// Press while OFF: ON, overriding the plan.
	r := readyForOff(t)
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	r.set(RoleBoost, "2026-11-10T09:20:00+00:00")
	r.advance(time.Minute)
	r.wantState("HOLD_ON(boost)")
	if !r.switchOn {
		t.Fatal("boost did not switch ON")
	}
	end := r.c.P.LastFinishedReheatEnd

	// No reheat starts within 30 min: exit, tank clock untouched.
	r.advance(30 * time.Minute)
	if r.c.P.Boost.Active || !r.c.P.LastFinishedReheatEnd.Equal(end) {
		t.Fatal("no-start exit wrong")
	}

	// A reheat that starts and finishes ends the boost and stamps the tank clock.
	r = newRig(t, liveCfg(), nil)
	r.boot(true)
	r.set(RoleBoost, "2026-11-10T09:01:00+00:00")
	r.advance(5 * time.Second)
	r.wantState("HOLD_ON(boost)")
	on := r.now
	r.step(CycleOpened{At: r.now, OnTime: on, Magnitude: 3000})
	r.advance(25 * time.Minute)
	if !r.c.P.Boost.Active {
		t.Fatal("boost ended while the reheat ran")
	}
	r.step(CycleClosed{At: r.now, OnTime: on, OffTime: r.now.Add(-90 * time.Second), Accepted: true})
	r.advance(5 * time.Second)
	if r.c.P.Boost.Active || r.c.P.LastFinishedReheatEnd.IsZero() {
		t.Fatal("finished reheat did not end the boost / stamp the tank clock")
	}

	// Cap at 2 h even with a start that never finishes.
	r = newRig(t, liveCfg(), nil)
	r.boot(true)
	r.set(RoleBoost, "2026-11-10T09:01:00+00:00")
	r.step(CycleOpened{At: r.now, OnTime: r.now, Magnitude: 3000})
	r.advance(2*time.Hour + time.Minute)
	if r.c.P.Boost.Active || !r.c.P.LastFinishedReheatEnd.IsZero() {
		t.Fatal("cap exit wrong")
	}

	// Boost beats Override Off.
	r = newRig(t, liveCfg(), nil)
	r.boot(true)
	r.tankRecent(time.Hour)
	r.set(RoleSelect, SelectOverrideOff)
	r.advance(time.Minute)
	r.wantState("HOLD_OFF")
	r.set(RoleBoost, "2026-11-10T09:05:00+00:00")
	r.advance(time.Minute)
	r.wantState("HOLD_ON(boost)")
	if !r.switchOn {
		t.Fatal("boost did not beat Override Off")
	}
}

// §3 SHADOW: no cover command ever; shadow_would reports the would-OFF.
func TestShadowNeverMoves(t *testing.T) {
	cfg := Defaults()
	cfg.ShadowSummaryHour = -1
	r := newRig(t, cfg, nil)
	r.pos, r.target, r.switchOn = 0, 0, false
	r.boot(true) // armed boolean on, but the add-on key is shadow
	r.pos, r.target, r.switchOn = 99, 99, true
	r.pushCover()
	r.pushTarget()
	r.tankRecent(time.Hour)
	r.planNow(16*time.Minute, time.Hour)
	r.advance(20 * time.Minute)
	if len(r.cmds) != 0 {
		t.Fatalf("shadow commanded %v", r.cmds)
	}
	e, ok := r.lastPublished(ObjShadowWould)
	if !ok || e.State != "off" {
		t.Fatalf("shadow_would %+v", e)
	}
	for _, x := range r.effects {
		if x.Kind == EffNotify {
			t.Fatalf("shadow notified %s", x.Code)
		}
	}
}

// Replay updates never actuate.
func TestReplayNeverActuates(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.step(EntityUpdate{At: r.now, Role: RoleCover, State: "closed", Position: 0, PositionOK: true, Replay: true})
	r.step(EntityUpdate{At: r.now, Role: RoleSelect, State: SelectOverrideOff, Replay: true})
	r.advance(time.Minute)
	if len(r.cmds) != 0 {
		t.Fatalf("replay caused commands %v", r.cmds)
	}
}

// The detector hook missing: SHADOW-like, state degraded:no_detector.
func TestNoDetector(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.step(Detector{At: r.now, Present: false})
	r.step(Link{At: r.now, Up: true})
	r.pushCover()
	r.set(RoleArmed, "on")
	r.advance(time.Minute)
	if r.c.Live() {
		t.Fatal("live without the detector")
	}
	if e, _ := r.lastPublished(ObjState); e.State != "degraded:no_detector" {
		t.Fatalf("state %q", e.State)
	}
}

// Persistence: round trip; corrupt file is fail-on.
func TestPersistence(t *testing.T) {
	r := readyForOff(t)
	r.c.P.Strikes = 2
	b, err := r.c.Persist()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := LoadPersisted(b)
	if !ok || p.Strikes != 2 || !p.LastFinishedReheatEnd.Equal(r.c.P.LastFinishedReheatEnd) {
		t.Fatalf("round trip %+v", p)
	}
	q, ok := LoadPersisted([]byte("{garbage"))
	if ok || !q.LastFinishedReheatEnd.IsZero() || !q.LastGenuineStart.IsZero() {
		t.Fatal("corrupt file not fail-on")
	}
	if len(b) > 8192 {
		t.Fatalf("state file %d bytes > 8 KB", len(b))
	}
}
