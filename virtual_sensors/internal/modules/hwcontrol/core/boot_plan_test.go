package core

import (
	"testing"
	"time"
)

// 0.6.1: at process start BOOT holds, moving nothing, until the planner has
// produced a plan (or BootPlanWait passes), then decides with the plan in
// force. Evidence: 2026-11-10 (OFF period running, boot_on, reheat) and
// 2026-11-12 (v2 default-OFF plan, boot_on at 12:56).

func bootWaitCfg() Config {
	c := liveCfg()
	c.BootPlanWait = 3 * time.Minute
	return c
}

// bootNoPlan boots live with the cover ON and no plan yet.
func bootNoPlan(t *testing.T, cfg Config) *rig {
	r := newRig(t, cfg, nil)
	r.boot(true)
	r.tankRecent(time.Hour)
	return r
}

func TestBootHoldsWithoutMoveUntilPlan(t *testing.T) {
	r := bootNoPlan(t, bootWaitCfg())
	r.advance(2 * time.Minute)
	r.wantState("BOOT")
	if len(r.cmds) != 0 {
		t.Fatalf("moved while waiting: %v", r.cmds)
	}
}

func TestBootUnderGreedyPlanGoesOffWithoutSettle(t *testing.T) {
	r := bootNoPlan(t, bootWaitCfg())
	r.advance(40 * time.Second)
	r.wantState("BOOT")
	r.planNow(-time.Minute, 30*time.Minute) // OFF period already in force
	r.advance(30 * time.Second)
	if r.countCmds(0) != 1 || r.switchOn {
		t.Fatalf("plan said OFF at boot; state %s cmds %v", r.state(), r.cmds)
	}
}

func TestBootUnderV2DefaultOffGoesOff(t *testing.T) {
	r := bootNoPlan(t, bootWaitCfg())
	r.advance(10 * time.Second)
	r.step(PlanUpdate{At: r.now, Plan: v2Plan(r, -time.Minute, 9*time.Hour)})
	r.advance(30 * time.Second)
	if r.countCmds(0) != 1 || r.switchOn {
		t.Fatalf("v2 default-OFF not applied at boot; state %s (%s) cmds %v", r.state(), r.c.reason, r.cmds)
	}
}

// A plan with no period now (a heating window) leaves the switch ON.
func TestBootPlanWithoutPeriodStaysOn(t *testing.T) {
	r := bootNoPlan(t, bootWaitCfg())
	r.planNow(2*time.Hour, time.Hour)
	r.advance(time.Minute)
	r.wantState("IDLE_ON")
	if len(r.cmds) != 0 {
		t.Fatalf("moved: %v", r.cmds)
	}
}

func TestBootPlanArrivesAfterDelay(t *testing.T) {
	r := bootNoPlan(t, bootWaitCfg())
	r.advance(150 * time.Second) // inside the 3 min wait
	r.wantState("BOOT")
	r.step(PlanUpdate{At: r.now, Plan: v2Plan(r, -time.Minute, 9*time.Hour)})
	r.advance(30 * time.Second)
	if r.countCmds(0) != 1 {
		t.Fatalf("late plan not applied: state %s cmds %v", r.state(), r.cmds)
	}
}

func TestBootTimeoutFailsOnAndSettles(t *testing.T) {
	r := bootNoPlan(t, bootWaitCfg())
	r.advance(4 * time.Minute)
	r.wantState("IDLE_ON")
	if r.c.reason != "boot_on" || len(r.cmds) != 0 {
		t.Fatalf("reason %s cmds %v", r.c.reason, r.cmds)
	}
	// A plan arriving later still has to wait out the normal settle.
	r.step(PlanUpdate{At: r.now, Plan: v2Plan(r, -time.Minute, 9*time.Hour)})
	r.advance(5 * time.Minute)
	r.wantState("IDLE_ON")
	r.advance(12 * time.Minute)
	if r.countCmds(0) != 1 {
		t.Fatalf("no OFF after settle: %s %v", r.state(), r.cmds)
	}
}

// Cover below 90 at boot (a persisted OFF): holds, then a plan covering now
// resumes it (C10), no move.
func TestBootResumesPersistedOffOnlyOncePlanArrives(t *testing.T) {
	r1 := readyForOff(t)
	r1.advance(18 * time.Minute)
	r1.wantState("IDLE_OFF")
	b, _ := r1.c.Persist()
	r2 := newRig(t, bootWaitCfg(), b)
	r2.now = r1.now
	r2.pos, r2.target, r2.switchOn = 0, 0, false
	r2.boot(true)
	r2.advance(time.Minute)
	r2.wantState("BOOT")
	if len(r2.cmds) != 0 {
		t.Fatalf("moved during the wait: %v", r2.cmds)
	}
	r2.step(PlanUpdate{At: r2.now, Plan: r1.c.plan})
	r2.advance(10 * time.Second)
	r2.wantState("IDLE_OFF")
	if r2.c.reason != "boot_resume_off" || len(r2.cmds) != 0 {
		t.Fatalf("reason %s cmds %v", r2.c.reason, r2.cmds)
	}
}

func TestBootBelow90WithoutPlanFailsOnAfterTimeout(t *testing.T) {
	r := newRig(t, bootWaitCfg(), nil)
	r.pos, r.target, r.switchOn = 0, 0, false
	r.boot(true)
	r.advance(time.Minute)
	if len(r.cmds) != 0 {
		t.Fatalf("moved early: %v", r.cmds)
	}
	r.advance(3 * time.Minute)
	if r.countCmds(99) != 1 {
		t.Fatalf("no ON after the timeout: %s %v", r.state(), r.cmds)
	}
}

// Safety rules still force ON at boot, plan or not.
func TestBootSafetyRulesStillForceOn(t *testing.T) {
	cases := map[string]func(r *rig){
		"low_battery": func(r *rig) { r.set(RoleBattery, "29") },
		"override_on": func(r *rig) { r.set(RoleSelect, SelectOverrideOn) },
		"select_off":  func(r *rig) { r.set(RoleSelect, "something else") },
		"tank_clock":  func(r *rig) { r.c.P.LastFinishedReheatEnd = r.now.Add(-15 * time.Hour) },
		"boost":       func(r *rig) { r.set(RoleBoost, "2026-11-10T09:00:00+00:00") },
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			r := bootNoPlan(t, bootWaitCfg())
			f(r)
			r.step(PlanUpdate{At: r.now, Plan: v2Plan(r, -time.Minute, 9*time.Hour)})
			r.advance(2 * time.Minute)
			if r.countCmds(0) != 0 || !r.switchOn {
				t.Fatalf("went OFF despite %s: %s %v", name, r.state(), r.cmds)
			}
		})
	}
}

// 2026-11-12: boot with a default-OFF plan and the move budget spent (6 moves
// in 24 h, reserve 3, cap 8): the OFF is blocked by move_reserve, not by boot.
// It must go OFF as soon as the budget allows.
func TestBootV2DefaultOffBlockedOnlyByMoveReserve(t *testing.T) {
	r := bootNoPlan(t, bootWaitCfg())
	for i := 6; i >= 1; i-- {
		r.c.P.Moves = append(r.c.P.Moves, r.now.Add(-time.Duration(17+i)*time.Hour))
	}
	r.step(PlanUpdate{At: r.now, Plan: v2Plan(r, -time.Minute, 20*time.Hour)})
	r.advance(5 * time.Minute)
	if ok, why := r.c.MayGoOff(false); ok || why != "move_reserve" {
		t.Fatalf("MayGoOff %v %s", ok, why)
	}
	r.wantState("IDLE_ON")
	r.advance(6 * time.Hour) // the oldest two moves age out
	if r.countCmds(0) != 1 {
		t.Fatalf("not OFF once budget freed: %s %v", r.state(), r.cmds)
	}
}

// A boot (wait included) is not a reheat: no clock moves.
func TestBootDoesNotResetClocks(t *testing.T) {
	r := bootNoPlan(t, bootWaitCfg())
	end, start, on := r.c.P.LastFinishedReheatEnd, r.c.P.LastGenuineStart, r.c.P.OnSSinceGenuineStart
	r.advance(2 * time.Minute)
	r.step(PlanUpdate{At: r.now, Plan: v2Plan(r, -time.Minute, 9*time.Hour)})
	r.advance(time.Minute)
	if !r.c.P.LastFinishedReheatEnd.Equal(end) || !r.c.P.LastGenuineStart.Equal(start) || r.c.P.OnSSinceGenuineStart < on {
		t.Fatalf("clocks changed: end %v->%v start %v->%v on %v->%v", end, r.c.P.LastFinishedReheatEnd, start, r.c.P.LastGenuineStart, on, r.c.P.OnSSinceGenuineStart)
	}
	if len(r.c.P.Reheats) != 0 {
		t.Fatalf("a reheat was logged at boot: %v", r.c.P.Reheats)
	}
}
