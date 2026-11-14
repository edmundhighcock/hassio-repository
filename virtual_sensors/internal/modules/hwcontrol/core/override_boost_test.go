package core

import (
	"strings"
	"testing"
	"time"
)

func (r *rig) logged(sub string) int {
	n := 0
	for _, e := range r.effects {
		if e.Kind == EffLog && strings.Contains(e.Msg, sub) {
			n++
		}
	}
	return n
}

// Override Off is explicit user intent and wins over Boost.
func TestOverrideOffCancelsBoost(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.tankRecent(time.Hour)
	r.set(RoleBoost, "2026-11-10T09:01:00+00:00")
	r.advance(10 * time.Second)
	r.wantState("HOLD_ON(boost)")
	r.set(RoleSelect, SelectOverrideOff)
	r.advance(2 * time.Minute)
	if r.c.P.Boost.Active {
		t.Fatal("boost still active")
	}
	if r.logged("boost ended: override_off") != 1 {
		t.Fatal("no override_off boost end logged")
	}
	r.wantState("HOLD_OFF")
	if r.countCmds(r.c.cfg.CmdOff) == 0 {
		t.Fatalf("no OFF command: %v", r.cmds)
	}
}

func TestOverrideOffRefusedDuringBoostLeavesNormalOn(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true) // tank clock unknown: Override Off is refused
	r.set(RoleBoost, "2026-11-10T09:01:00+00:00")
	r.advance(10 * time.Second)
	r.wantState("HOLD_ON(boost)")
	r.set(RoleSelect, SelectOverrideOff)
	r.advance(2 * time.Minute)
	if r.c.P.Boost.Active {
		t.Fatal("boost still active")
	}
	if r.notifies(AlertOverrideRefused) != 1 {
		t.Fatalf("want one refusal alert, got %d", r.notifies(AlertOverrideRefused))
	}
	r.wantState("IDLE_ON")
	if !r.switchOn {
		t.Fatal("cover not ON")
	}
}

func TestOverrideOffPendingLoggedOnce(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.tankRecent(time.Hour)
	r.c.P.State = StCmdOn // a non-idle state where it cannot act yet
	r.set(RoleSelect, SelectOverrideOff)
	r.c.evaluate()
	r.c.evaluate()
	if n := r.logged("override_off pending (state"); n != 1 {
		t.Fatalf("pending logged %d times", n)
	}
}

func TestBoostFirstPressAfterUnknownSnapshot(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.step(Detector{At: r.now, Present: true})
	r.set(RoleBoost, "unknown")
	r.set(RoleBoost, "2026-11-10T09:01:00+00:00")
	if !r.c.P.Boost.Active {
		t.Fatal("first press after unknown snapshot swallowed")
	}
}

func TestBoostValidSnapshotOnlyChangePresses(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.set(RoleBoost, "2026-01-01T00:00:00+00:00")
	if r.c.P.Boost.Active {
		t.Fatal("snapshot pressed")
	}
	r.set(RoleBoost, "2026-01-01T00:00:00+00:00")
	if r.c.P.Boost.Active {
		t.Fatal("same value pressed")
	}
	r.set(RoleBoost, "2026-11-10T09:01:00+00:00")
	if !r.c.P.Boost.Active {
		t.Fatal("change did not press")
	}
}

func TestReplayNeverPresses(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.step(EntityUpdate{At: r.now, Role: RoleBoost, State: "unknown"})
	r.step(EntityUpdate{At: r.now, Role: RoleBoost, State: "2026-11-09T09:01:00+00:00", Replay: true})
	r.step(EntityUpdate{At: r.now, Role: RoleTestMove, State: "unknown"})
	r.step(EntityUpdate{At: r.now, Role: RoleTestMove, State: "2026-11-09T09:01:00+00:00", Replay: true})
	if r.c.P.Boost.Active || r.logged("requested") != 0 {
		t.Fatal("replayed value counted as a press")
	}
	// Restart: snapshot is the last press time; must not trigger.
	r2 := newRig(t, liveCfg(), nil)
	r2.set(RoleBoost, "2026-11-09T09:01:00+00:00")
	r2.set(RoleTestProbe, "2026-11-09T09:01:00+00:00")
	if r2.c.P.Boost.Active || r2.c.testPending != "" {
		t.Fatal("restart snapshot triggered")
	}
}

func TestTestButtonFirstPress(t *testing.T) {
	for _, tc := range []struct {
		role Role
		msg  string
	}{{RoleTestMove, "test move requested"}, {RoleTestProbe, "test probe requested"}} {
		r := newRig(t, liveCfg(), nil)
		r.boot(true)
		r.set(tc.role, "unknown")
		r.set(tc.role, "2026-11-10T09:01:00+00:00")
		if r.logged(tc.msg) != 1 {
			t.Fatalf("%s: first press after unknown swallowed", tc.msg)
		}
		r.set(tc.role, "2026-11-10T09:01:00+00:00")
		if r.logged(tc.msg) != 1 {
			t.Fatal("same value pressed")
		}
		r.set(tc.role, "2026-11-10T09:02:00+00:00")
		if r.logged(tc.msg) != 2 {
			t.Fatal("change not a press")
		}
	}
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.set(RoleTestMove, "2026-11-09T00:00:00+00:00")
	if r.logged("test move requested") != 0 {
		t.Fatal("valid snapshot pressed")
	}
}
