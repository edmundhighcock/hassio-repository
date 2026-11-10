package core

import (
	"testing"
	"time"
)

// Review fix 7: PLAN §2.6's example writes "" for unset times; null is
// accepted too. Both parse as unset instead of failing the whole file.
func TestFix7PersistedEmptyAndNullTimes(t *testing.T) {
	b := []byte(`{"schema":1,"state":"IDLE_ON","state_since":"",
		"last_genuine_start":null,"last_finished_reheat_end":"2026-11-10T08:00:00Z",
		"boost":{"active":false,"pressed_at":"","reheat_seen_start":false},
		"hold":{"latched":false,"reason":"","since":""},
		"moves":["2026-11-10T07:00:00Z",""],"strikes":2,"current_off_period":null}`)
	p, ok := LoadPersisted(b)
	if !ok {
		t.Fatal("file with \"\" and null times rejected")
	}
	if !p.LastGenuineStart.IsZero() || !p.StateSince.IsZero() || !p.Boost.PressedAt.IsZero() {
		t.Fatal("unset times not zero")
	}
	if p.Strikes != 2 || len(p.Moves) != 1 || !p.LastFinishedReheatEnd.Equal(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("set fields lost: %+v", p)
	}
}

// restartWithReplay rebuilds a controller from persisted state with
// on_s already accumulated up to tDown, then feeds a host-shaped start:
// a wall-clock tick first (must not move the clock), the warm-up replay
// of [tUp-4h, tUp] (cover history + residual samples every minute, plus
// extra replay inputs), then the snapshot and link-up at tUp.
func restartWithReplay(t *testing.T, onS float64, tDown, tUp time.Time, coverClosed func(time.Time) bool, extra func(r *rig, at time.Time)) *rig {
	t.Helper()
	p := newPersisted()
	p.OnSSinceGenuineStart = onS
	p.OnClockAt = tDown
	p.NextProbeOnS = 10 * 3600
	p.NextBackstopAlertOnS = 18 * 3600
	b, _ := p.Marshal()
	r := newRig(t, liveCfg(), b)
	r.now = tUp
	r.step(Detector{At: tUp, Present: true})
	r.step(Tick{At: tUp})
	lastClosed := false
	for at := tUp.Add(-4 * time.Hour); at.Before(tUp); at = at.Add(time.Minute) {
		closed := coverClosed(at)
		if at.Equal(tUp.Add(-4*time.Hour)) || closed != lastClosed {
			st := "open"
			if closed {
				st = "closed"
			}
			r.step(EntityUpdate{At: at, Role: RoleCover, State: st, Replay: true})
			lastClosed = closed
		}
		if extra != nil {
			extra(r, at)
		}
		r.step(Sample{At: at, W: 400, Available: true, Replay: true})
	}
	if len(r.cmds) != 0 {
		t.Fatalf("replay actuated: %v", r.cmds)
	}
	r.boot(true)
	return r
}

// Review fix 1: ON time in the replayed window counts. 9 h of ON time at
// shutdown, down for 1 h with the switch ON: the probe is due at once
// after the restart (10 h), not 1 h later; replay itself never actuates.
func TestFix1ReplayAdvancesOnClock(t *testing.T) {
	tUp := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	r := restartWithReplay(t, 9*3600, tUp.Add(-time.Hour), tUp, func(time.Time) bool { return false }, nil)
	if h := r.c.P.OnSSinceGenuineStart / 3600; h < 9.95 || h > 10.05 {
		t.Fatalf("ON clock %.2f h after replay, want 10 h", h)
	}
	r.advance(2 * time.Minute)
	if r.countCmds(0) != 1 {
		t.Fatalf("probe not issued after the replayed ON time: cmds %v state %s", r.cmds, r.state())
	}
}

// Fix 1: a replayed OFF stretch does not count.
func TestFix1ReplayedOffStretchNotCounted(t *testing.T) {
	tUp := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	closedFrom, closedTo := tUp.Add(-50*time.Minute), tUp.Add(-20*time.Minute)
	r := restartWithReplay(t, 3600, tUp.Add(-time.Hour), tUp, func(at time.Time) bool {
		return !at.Before(closedFrom) && at.Before(closedTo)
	}, nil)
	// 60 min since shutdown, 30 of them closed.
	if m := (r.c.P.OnSSinceGenuineStart - 3600) / 60; m < 29 || m > 31.5 {
		t.Fatalf("replay counted %.1f min of ON time, want 30", m)
	}
}

// Fix 1: a genuine start in the replayed window restamps the clock.
func TestFix1ReplayedGenuineStartRestamps(t *testing.T) {
	tUp := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	on := tUp.Add(-40 * time.Minute)
	r := restartWithReplay(t, 9*3600, tUp.Add(-3*time.Hour), tUp, func(time.Time) bool { return false }, func(r *rig, at time.Time) {
		if at.Equal(on) {
			r.step(CycleOpened{At: at, OnTime: on, Magnitude: 3000, Replay: true})
		}
		if at.Equal(on.Add(20 * time.Minute)) {
			r.step(CycleClosed{At: at, OnTime: on, OffTime: at, Accepted: true, Replay: true})
		}
	})
	if !r.c.P.LastGenuineStart.Equal(on) || !r.c.P.LastFinishedReheatEnd.Equal(on.Add(20*time.Minute)) {
		t.Fatalf("replayed reheat not stamped: start %v end %v", r.c.P.LastGenuineStart, r.c.P.LastFinishedReheatEnd)
	}
	if m := r.c.P.OnSSinceGenuineStart / 60; m < 39 || m > 42 {
		t.Fatalf("ON clock %.1f min after the replayed start, want ~40", m)
	}
}

// Review fix 2: the switch resting at 100/100 is ON. No spurious ON at
// boot, and OFF is still permitted.
func TestFix2HundredIsOn(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.pos, r.target = 100, 100
	r.boot(true)
	r.advance(10 * time.Minute)
	if len(r.cmds) != 0 || r.state() != "IDLE_ON" {
		t.Fatalf("spurious command at 100: %v state %s", r.cmds, r.state())
	}
	r.tankRecent(time.Hour)
	r.planNow(6*time.Minute, time.Hour)
	r.advance(7 * time.Minute)
	if r.countCmds(0) != 1 {
		t.Fatalf("OFF refused with target 100: %s", r.c.reason)
	}
}

// Fix 2: a 99 command that comes to rest at 100 is a clean ON, not a
// retreat.
func TestFix2CommandRestingAt100IsNotRetreat(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.pos, r.target, r.switchOn = 0, 0, false
	r.restAt100 = true
	r.boot(true)
	r.advance(5 * time.Minute)
	if r.countCmds(99) != 1 || r.c.P.Strikes != 0 || r.state() != "IDLE_ON" {
		t.Fatalf("cmds %v strikes %d state %s verdict %s", r.cmds, r.c.P.Strikes, r.state(), r.c.verdict)
	}
}

// Review fix 6: before the first snapshot/link-up the controller holds
// quietly in BOOT (no DEGRADED, no alert); after it, a real staleness
// still gives DEGRADED on the boot timer.
func TestFix6NoDegradedBeforeFirstPopulate(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.step(Detector{At: r.now, Present: true})
	for i := 0; i < 120; i++ { // 10 min of ticks, HA never reached
		r.now = r.now.Add(5 * time.Second)
		r.step(Tick{At: r.now})
	}
	if r.state() != "BOOT" {
		t.Fatalf("state %s before inputs were populated", r.state())
	}
	for _, e := range r.effects {
		if e.Kind == EffNotify || (e.Kind == EffCallService && e.Domain == "cover") {
			t.Fatalf("acted before inputs were populated: %+v", e)
		}
	}
	r.residualOff = true
	r.step(Link{At: r.now, Up: true})
	r.pushCover()
	r.pushTarget()
	r.set(RoleSelect, SelectAuto)
	r.advance(4 * time.Minute)
	r.wantState("DEGRADED")
}

// Review fix 3: a dip-and-recover in another load just before the OFF
// reads as a ~2.6 kW up-step; unsustained, it is not "drawing", so no
// spurious off_failed: off_unverified and the OFF continues.
func TestFix3UnsustainedStepIsNotDrawing(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.baseW = 3000 // a steady non-heater load; the heater is idle
	r.boot(true)
	r.tankRecent(time.Hour)
	r.planNow(16*time.Minute, 2*time.Hour)
	r.advance(15 * time.Minute)
	r.baseW = 400
	r.emitResidual(true)
	r.advance(20 * time.Second)
	r.baseW = 3000
	r.emitResidual(true)
	r.advance(3 * time.Minute)
	if r.c.verdict != VOffUnverified || r.state() != "IDLE_OFF" {
		t.Fatalf("verdict %s state %s, want off_unverified IDLE_OFF", r.c.verdict, r.state())
	}
	if !r.c.P.AbandonedUntil.IsZero() {
		t.Fatal("block abandoned on an unsustained step")
	}
}

// Fix 3, safe direction kept: the heater genuinely drawing (sustained),
// no drop, and the window inconclusive (EV moved): back ON, never left
// OFF on doubt; no strike.
func TestFix3AmbiguousDrawingReturnsOn(t *testing.T) {
	r := readyForOff(t)
	r.calling = true
	r.stuckClosed = true
	r.emitResidual(true)
	r.advance(16*time.Minute + 2*time.Second) // OFF just commanded
	r.set(RoleEV, "7000")
	r.advance(3 * time.Minute)
	if r.c.verdict == VOffConfirmed || r.countCmds(99) != 1 || r.c.P.Strikes != 0 {
		t.Fatalf("verdict %s cmds %v strikes %d", r.c.verdict, r.cmds, r.c.P.Strikes)
	}
	// A sustained step with a discontinuity near it is ambiguous too.
	r = readyForOff(t)
	r.advance(14 * time.Minute)
	r.calling = true
	// A load lost at the same moment: the residual also jumps by its
	// DeltaW (the core subtracts it back out).
	r.step(Discontinuity{At: r.now, Entity: "heat_pump_power_power", DeltaW: 900, Seq: 1})
	r.baseW += 900
	r.emitResidual(true)
	r.c.cfg.GenuineMinDur = time.Minute // make the step "sustained" by the OFF
	r.stuckClosed = true
	// Rule 4 holds the OFF for 5 min after the discontinuity (~19 min),
	// then the verification window runs.
	r.advance(8 * time.Minute)
	if r.countCmds(0) != 1 || r.countCmds(99) != 1 || r.c.verdict != VOffUnverified {
		t.Fatalf("flagged step: verdict %s cmds %v", r.c.verdict, r.cmds)
	}
}

// Review fix 4: once an OFF period has started, a re-plan may shorten it
// but never extend or move it.
func TestFix4RunningPeriodFrozen(t *testing.T) {
	r := readyForOff(t) // period [16 min, 2 h 16 min]
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	orig := *r.c.offPeriod
	// A rolling re-plan slides the period: later start, later end.
	r.plan(&Plan{PlannerID: "t", GeneratedAt: r.now, ValidUntil: r.now.Add(48 * time.Hour),
		OffPeriods: []OffPeriod{{Start: r.now.Add(-time.Minute), End: orig.End.Add(time.Hour)}}})
	r.advance(time.Minute)
	if !r.c.offPeriod.End.Equal(orig.End) || !r.c.offPeriod.Start.Equal(orig.Start) {
		t.Fatalf("running period moved: %+v, was %+v", *r.c.offPeriod, orig)
	}
	// A re-plan ending earlier is adopted, and the OFF ends there.
	early := r.now.Add(10 * time.Minute)
	r.plan(&Plan{PlannerID: "t", GeneratedAt: r.now, ValidUntil: r.now.Add(48 * time.Hour),
		OffPeriods: []OffPeriod{{Start: orig.Start, End: early}}})
	r.advance(time.Minute)
	if !r.c.offPeriod.End.Equal(early) {
		t.Fatalf("shortening not adopted: %+v", *r.c.offPeriod)
	}
	r.advance(11 * time.Minute)
	if !r.switchOn {
		t.Fatal("OFF ran past the shortened end")
	}
}

// Review fix 5: after a row-14 return to ON the rest of the period is
// abandoned; the next OFF needs a new planned period.
func TestFix5NoReOffAfterRow14(t *testing.T) {
	r := readyForOff(t) // period [16 min, 2 h 16 min]
	periodEnd := r.now.Add(2*time.Hour + 16*time.Minute)
	r.calling = true
	r.stuckClosed = true
	r.emitResidual(true)
	r.advance(18 * time.Minute)
	if r.c.verdict != VOffFailed {
		t.Fatalf("verdict %s", r.c.verdict)
	}
	r.stuckClosed = false
	r.calling = false
	r.emitResidual(true)
	r.advance(time.Hour)
	if r.countCmds(0) != 1 {
		t.Fatalf("re-OFF within the abandoned period: %v", r.cmds)
	}
	if ok, why := r.c.MayGoOff(false); ok || why != "period_abandoned" {
		t.Fatalf("MayGoOff %v %s", ok, why)
	}
	// A new planned period after the abandoned end permits OFF again.
	r.plan(&Plan{PlannerID: "t", GeneratedAt: r.now, ValidUntil: r.now.Add(48 * time.Hour),
		OffPeriods: []OffPeriod{{Start: periodEnd.Add(10 * time.Minute), End: periodEnd.Add(2 * time.Hour)}}})
	r.advance(periodEnd.Add(11 * time.Minute).Sub(r.now))
	if r.countCmds(0) != 2 {
		t.Fatalf("no OFF in the new period: %v (%s)", r.cmds, r.c.reason)
	}
}

// Found during the fix round: losing the HA link while LIVE is
// staleness, not a disarm. No ON is sent into the dead link and no strike
// is counted; on reconnect the controller is still live.
func TestLinkLossIsNotDisarm(t *testing.T) {
	r := readyForOff(t)
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	r.step(Link{At: r.now, Up: false})
	n := len(r.cmds)
	r.advance(5 * time.Minute)
	if len(r.cmds) != n || r.c.P.Strikes != 0 {
		t.Fatalf("commands into a dead link %v, strikes %d", r.cmds[n:], r.c.P.Strikes)
	}
	r.wantState("DEGRADED")
	r.step(Link{At: r.now, Up: true})
	r.pushCover()
	r.advance(time.Minute)
	if !r.c.Live() || !r.switchOn {
		t.Fatalf("after reconnect live=%v on=%v state %s", r.c.Live(), r.switchOn, r.state())
	}
}
