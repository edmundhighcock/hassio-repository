package core

import (
	"testing"
	"time"
)

// ovRig: live, IDLE_ON, the heater drawing (residual step ~3 kW seen by the
// step tracker), no Equalizer data yet.
func ovRig(t *testing.T) *rig {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.tankRecent(time.Hour)
	r.calling = true
	r.emitResidual(false)
	r.advance(70 * time.Second)
	r.wantState("IDLE_ON")
	if !r.c.tracker.drawing {
		t.Fatal("rig: heater not seen drawing")
	}
	return r
}

func (r *rig) eq(l1, l2 float64) {
	r.step(PhaseSample{At: r.now, L1: l1, L2: l2})
}

// eqFor feeds one sample every 30 s for d (first sample now), advancing time.
func (r *rig) eqFor(d time.Duration, l1, l2 float64) {
	for el := time.Duration(0); el < d; el += 30 * time.Second {
		r.eq(l1, l2)
		r.advance(30 * time.Second)
	}
}

// trigger: two high samples 30 s apart.
func (r *rig) trigger(l1, l2 float64) {
	r.eq(l1, l2)
	r.advance(30 * time.Second)
	r.eq(l1, l2)
	r.advance(15 * time.Second)
}

func TestOverloadPausesAndPublishes(t *testing.T) {
	r := ovRig(t)
	r.trigger(20.1, 14.2)
	r.wantState("HOLD_OFF(overload)")
	if r.countCmds(0) != 1 {
		t.Fatalf("cmds %v", r.cmds)
	}
	if r.logged("overload pause: L1=20.1A L2=14.2A -> OFF") != 1 {
		t.Fatal("no log line")
	}
	if e, _ := r.lastPublished(ObjOffGate); e.State != "overload_pause" {
		t.Fatalf("off_gate %q", e.State)
	}
	e, ok := r.lastPublished(ObjOverload)
	if !ok || e.State != "paused" || e.Attrs["phase"] != "L1" || e.Attrs["amps"] != 20.1 || e.Attrs["resumes_at"] == "" || e.Attrs["last_pause_at"] == "" {
		t.Fatalf("overload sensor %+v", e)
	}
}

func TestOverloadNotWhenHeaterIdle(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.tankRecent(time.Hour)
	r.advance(70 * time.Second) // heater not calling
	r.trigger(20.5, 12)
	r.trigger(20.5, 12)
	r.wantState("IDLE_ON")
	if len(r.cmds) != 0 {
		t.Fatalf("moved: %v", r.cmds)
	}
	// Once the heater draws, the same currents pause it.
	r.calling = true
	r.emitResidual(false)
	r.advance(70 * time.Second)
	r.trigger(20.5, 12)
	r.wantState("HOLD_OFF(overload)")
}

func TestOverloadNeedsTwoSamplesSpanning30s(t *testing.T) {
	r := ovRig(t)
	r.eq(20.5, 14)
	r.advance(10 * time.Second)
	r.wantState("IDLE_ON") // a single sample
	r.eq(20.5, 14)         // 10 s later: two samples, span < 30 s
	r.advance(5 * time.Second)
	r.wantState("IDLE_ON")
	r.advance(20 * time.Second)
	r.eq(20.5, 14) // 35 s after the first
	r.wantState("HOLD_OFF(overload)")
}

func TestOverloadStaleOrOfflineNeverTriggers(t *testing.T) {
	r := ovRig(t)
	// Two high samples, but already older than 90 s when seen.
	r.step(PhaseSample{At: r.now.Add(-150 * time.Second), L1: 21, L2: 14})
	r.step(PhaseSample{At: r.now.Add(-110 * time.Second), L1: 21, L2: 14})
	r.advance(5 * time.Second)
	r.wantState("IDLE_ON")
	// A gap over 90 s between two high samples breaks the run.
	r.eq(21, 14)
	r.advance(120 * time.Second)
	r.eq(21, 14)
	r.advance(5 * time.Second)
	r.wantState("IDLE_ON")
	// Equalizer offline.
	r.step(PhaseOnline{At: r.now, Online: false})
	r.trigger(21, 14)
	r.wantState("IDLE_ON")
	// Back online: fresh samples trigger.
	r.step(PhaseOnline{At: r.now, Online: true})
	r.trigger(21, 14)
	r.wantState("HOLD_OFF(overload)")
	// A held (stale) value after the pause started does not matter here; and
	// at/below the threshold nothing triggers.
	r2 := ovRig(t)
	r2.trigger(19.8, 19.8)
	r2.wantState("IDLE_ON")
}

func TestOverloadDisabled(t *testing.T) {
	cfg := liveCfg()
	cfg.OverloadEnabled = false
	r := newRig(t, cfg, nil)
	r.boot(true)
	r.calling = true
	r.emitResidual(false)
	r.advance(70 * time.Second)
	r.trigger(22, 14)
	r.wantState("IDLE_ON")
}

func TestOverloadResumeThresholdAndMinHold(t *testing.T) {
	r := ovRig(t)
	r.trigger(20.1, 14.2)
	start := r.c.ov.start
	// 13 A is not room for the heater (12.4 A): still paused after 10 min.
	r.eqFor(10*time.Minute, 13, 9)
	r.wantState("HOLD_OFF(overload)")
	// Room, but the minimum hold counts from the pause start: use a fresh pause.
	r = ovRig(t)
	r.trigger(20.1, 14.2)
	start = r.c.ov.start
	for r.now.Sub(start) < 170*time.Second {
		r.eq(10, 8)
		r.advance(30 * time.Second)
		if r.now.Sub(start) < 180*time.Second {
			r.wantState("HOLD_OFF(overload)")
		}
	}
	r.eq(10, 8)
	r.advance(10 * time.Second)
	if r.logged("overload pause ended (load_cleared)") != 1 {
		t.Fatalf("not resumed at %s: %s", r.now.Sub(start), r.state())
	}
	r.advance(30 * time.Second)
	r.wantState("IDLE_ON")
	if r.countCmds(99) != 1 || !r.switchOn {
		t.Fatalf("no ON: %v", r.cmds)
	}
}

func TestOverloadResumeNeedsFreshCalmSamples(t *testing.T) {
	r := ovRig(t)
	r.trigger(20.1, 14.2)
	r.advance(10 * time.Minute) // no Equalizer data at all
	r.wantState("HOLD_OFF(overload)")
	r.advance(11 * time.Minute) // max pause 20 min
	if r.logged("overload pause ended (max_pause)") != 1 {
		t.Fatalf("no max_pause resume: %s", r.state())
	}
}

func TestOverloadMaxPauseAndRateLimit(t *testing.T) {
	r := ovRig(t)
	r.trigger(20.1, 14.2)
	start := r.c.ov.start
	r.eqFor(21*time.Minute, 15, 13) // never calm
	if r.logged("overload pause ended (max_pause)") != 1 {
		t.Fatalf("no max_pause resume: %s", r.state())
	}
	r.wantState("IDLE_ON")
	// Overloaded again right away: rate limited (one pause per 30 min).
	r.eqFor(5*time.Minute, 20.5, 14)
	r.wantState("IDLE_ON")
	if r.logged("overload pause suppressed") < 1 {
		t.Fatal("rate limit not logged")
	}
	if r.countCmds(0) != 1 {
		t.Fatalf("second pause moved the cover: %v", r.cmds)
	}
	for r.now.Sub(start) < 31*time.Minute {
		r.eqFor(60*time.Second, 20.5, 14)
	}
	r.eqFor(60*time.Second, 20.5, 14)
	r.wantState("HOLD_OFF(overload)")
	if r.logged("overload pause: L1=") != 2 {
		t.Fatalf("pauses %d", r.logged("overload pause: L1="))
	}
}

func TestOverloadRefusedByMoveReserve(t *testing.T) {
	r := ovRig(t)
	for i := 0; i < 3; i++ {
		r.c.P.Moves = append(r.c.P.Moves, r.now.Add(-time.Duration(10+i)*time.Minute))
	}
	r.trigger(21, 14)
	r.wantState("IDLE_ON")
	if len(r.cmds) != 0 || r.logged("overload pause refused: move reserve") != 1 {
		t.Fatalf("cmds %v", r.cmds)
	}
}

func TestOverloadInterruptsBoostAndResumeContinuesIt(t *testing.T) {
	r := ovRig(t)
	r.step(CycleOpened{At: r.now, OnTime: r.now, Magnitude: 3000})
	r.set(RoleBoost, "2026-11-10T09:05:00+00:00")
	r.advance(10 * time.Second)
	r.wantState("HOLD_ON(boost)")
	r.trigger(20.4, 14)
	r.wantState("HOLD_OFF(overload)")
	if !r.c.P.Boost.Active {
		t.Fatal("pause cancelled the boost")
	}
	r.eqFor(4*time.Minute, 10, 8)
	r.eq(10, 8)
	r.advance(30 * time.Second)
	r.wantState("HOLD_ON(boost)")
	if !r.c.P.Boost.Active || r.countCmds(99) < 1 || !r.switchOn {
		t.Fatalf("boost not continued: active=%v cmds=%v on=%v", r.c.P.Boost.Active, r.cmds, r.switchOn)
	}
}

func TestOverloadResumeStaysOffWhenPlanSaysOff(t *testing.T) {
	r := ovRig(t)
	r.planNow(0, 2*time.Hour) // OFF permitted now (the 15 min settle keeps the normal OFF away)
	r.trigger(20.4, 14)
	r.wantState("HOLD_OFF(overload)")
	r.eqFor(5*time.Minute, 10, 8)
	r.eq(10, 8)
	r.advance(30 * time.Second)
	r.wantState("IDLE_OFF")
	if r.countCmds(99) != 0 || r.logged("the plan still says OFF") != 1 {
		t.Fatalf("cmds %v", r.cmds)
	}
}

func TestOverloadOverrides(t *testing.T) {
	// Override On is never interrupted.
	r := ovRig(t)
	r.set(RoleSelect, SelectOverrideOn)
	r.advance(10 * time.Second)
	r.wantState("HOLD_ON(override)")
	r.trigger(22, 14)
	r.wantState("HOLD_ON(override)")
	if r.countCmds(0) != 0 {
		t.Fatalf("moved: %v", r.cmds)
	}
	// Override Off selected during a pause takes the pause over.
	r = ovRig(t)
	r.trigger(22, 14)
	r.wantState("HOLD_OFF(overload)")
	r.set(RoleSelect, SelectOverrideOff)
	r.eqFor(25*time.Minute, 10, 8)
	r.wantState("HOLD_OFF")
	if r.countCmds(99) != 0 {
		t.Fatalf("resumed ON over an Override Off: %v", r.cmds)
	}
	// Another hold (here: a conflicting automation) ends the pause.
	r = ovRig(t)
	r.trigger(22, 14)
	r.step(EntityUpdate{At: r.now, Role: RoleConflict, Entity: "automation.hwc_x", State: "on"})
	r.advance(30 * time.Second)
	r.wantState("HOLD_ON(conflict)")
}

func TestOverloadCutReheatDoesNotStampTankClock(t *testing.T) {
	r := ovRig(t)
	r.step(CycleOpened{At: r.now, OnTime: r.now.Add(-20 * time.Minute), Magnitude: 3000})
	before := r.c.P.LastFinishedReheatEnd
	r.trigger(20.4, 14)
	r.step(CycleClosed{At: r.now, OnTime: r.now.Add(-30 * time.Minute), OffTime: r.now, Accepted: true})
	if !r.c.P.LastFinishedReheatEnd.Equal(before) {
		t.Fatalf("tank clock stamped by a cut reheat: %v -> %v", before, r.c.P.LastFinishedReheatEnd)
	}
}
