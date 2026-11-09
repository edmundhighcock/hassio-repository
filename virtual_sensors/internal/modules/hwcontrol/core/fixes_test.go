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
