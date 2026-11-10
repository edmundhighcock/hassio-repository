package core

import (
	"testing"
	"time"
)

// C10 (revised, 0.4.1): a restart with the cover below 90 resumes a valid
// OFF instead of going ON. Each condition failing falls back to boot_below_90.

type resumeOpt struct {
	cfg        func(*Config)
	persisted  func(*Persisted)  // applied to the state file before the restart
	plan       func(*Plan) *Plan // replaces the plan given to the restarted controller
	afterLoad  func(r *rig)      // applied to the restarted rig before boot
	skipBat    bool              // battery entity never reported: stale
	batteryPct string
	latch      string
	boost      bool
	selectVal  string
}

func resumeRig(t *testing.T, o resumeOpt) (r1, r2 *rig) {
	t.Helper()
	r1 = readyForOff(t)
	r1.advance(18 * time.Minute)
	r1.wantState("IDLE_OFF")
	b, _ := r1.c.Persist()
	if o.persisted != nil {
		p, ok := LoadPersisted(b)
		if !ok {
			t.Fatal("persisted state not loadable")
		}
		o.persisted(&p)
		b, _ = p.Marshal()
	}
	cfg := liveCfg()
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	r2 = newRig(t, cfg, b)
	r2.now = r1.now
	r2.pos, r2.target, r2.switchOn = 0, 0, false
	pl := r1.c.plan
	if o.plan != nil {
		pl = o.plan(pl)
	}
	r2.plan(pl)
	if o.afterLoad != nil {
		o.afterLoad(r2)
	}
	bat, latch, sel := "80", "off", SelectAuto
	if o.batteryPct != "" {
		bat = o.batteryPct
	}
	if o.latch != "" {
		latch = o.latch
	}
	if o.selectVal != "" {
		sel = o.selectVal
	}
	r2.step(Detector{At: r2.now, Present: true})
	r2.step(Link{At: r2.now, Up: true})
	r2.pushCover()
	r2.pushTarget()
	r2.pushMotor()
	if !o.skipBat {
		r2.set(RoleBattery, bat)
	}
	r2.set(RoleSelect, sel)
	r2.set(RoleArmed, "on")
	r2.set(RoleLatch, latch)
	r2.set(RoleEV, "0")
	r2.set(RoleBoost, "2026-01-01T00:00:00+00:00")
	if o.boost {
		r2.set(RoleBoost, "2026-11-10T10:00:00+00:00") // a press
	}
	r2.emitResidual(false)
	r2.advance(5 * time.Second)
	return r1, r2
}

func wantWentOn(t *testing.T, r2 *rig, why string) {
	t.Helper()
	r2.advance(time.Minute)
	if r2.countCmds(99) < 1 {
		t.Fatalf("%s: no ON command, state %s", why, r2.state())
	}
	wantNotResumed(t, r2, why)
}

// wantNotResumed: not back in IDLE_OFF and no OFF/move issued (a hold's
// own handling of the switch is unchanged by C10).
func wantNotResumed(t *testing.T, r2 *rig, why string) {
	t.Helper()
	if r2.state() == "IDLE_OFF" || r2.countCmds(0) != 0 {
		t.Fatalf("%s: expected ON as before, state %s cmds %v", why, r2.state(), r2.cmds)
	}
}

func TestC10ResumeOffAfterRestart(t *testing.T) {
	r1, r2 := resumeRig(t, resumeOpt{})
	r2.wantState("IDLE_OFF")
	if r2.c.reason != "boot_resume_off" {
		t.Fatalf("reason %q", r2.c.reason)
	}
	if !r2.c.P.OffSince.Equal(r1.c.P.OffSince) || r2.c.P.OffSince.IsZero() {
		t.Fatalf("OffSince changed: %v vs %v", r2.c.P.OffSince, r1.c.P.OffSince)
	}
	r2.advance(5 * time.Minute)
	r2.wantState("IDLE_OFF")
	if len(r2.cmds) != 0 {
		t.Fatalf("a move was made on resume: %v", r2.cmds)
	}
	if r2.c.P.CurrentOffPeriod == nil {
		t.Fatal("OFF period not restored")
	}
	// The resumed OFF still ends at the period end.
	r2.advance(3 * time.Hour)
	if r2.countCmds(99) < 1 {
		t.Fatalf("resumed OFF never ended: %v state %s", r2.cmds, r2.state())
	}
}

func TestC10NoResumeOutsidePlanPeriod(t *testing.T) {
	_, r2 := resumeRig(t, resumeOpt{plan: func(p *Plan) *Plan {
		q := *p
		now := t0.Add(time.Hour)
		q.OffPeriods = []OffPeriod{{Start: now.Add(5 * time.Hour), End: now.Add(7 * time.Hour)}}
		q.ValidUntil = now.Add(48 * time.Hour)
		return &q
	}})
	wantWentOn(t, r2, "outside plan period")
}

func TestC10NoResumeTankClockExceeded(t *testing.T) {
	_, r2 := resumeRig(t, resumeOpt{persisted: func(p *Persisted) {
		p.LastFinishedReheatEnd = t0.Add(-20 * time.Hour)
		p.LastFinishedReheatStart = t0.Add(-20*time.Hour - 20*time.Minute)
	}})
	wantWentOn(t, r2, "tank clock")
}

func TestC10NoResumeEveningRule(t *testing.T) {
	_, r2 := resumeRig(t, resumeOpt{
		cfg: func(c *Config) { c.EveningOffFromMin = 60 }, // "evening" has begun
		persisted: func(p *Persisted) { // tank clock fine (11 h) but not today
			p.LastFinishedReheatEnd = t0.Add(-11 * time.Hour)
			p.LastFinishedReheatStart = t0.Add(-11*time.Hour - 20*time.Minute)
		},
	})
	if r2.c.tankOK() != true || r2.c.eveningOK() {
		t.Fatalf("test set-up: tankOK %v eveningOK %v", r2.c.tankOK(), r2.c.eveningOK())
	}
	wantWentOn(t, r2, "evening rule")
}

func TestC10NoResumeStaleInputs(t *testing.T) {
	_, r2 := resumeRig(t, resumeOpt{skipBat: true})
	wantWentOn(t, r2, "stale battery")
}

func TestC10NoResumeBatteryBelowFloor(t *testing.T) {
	_, r2 := resumeRig(t, resumeOpt{batteryPct: "10"})
	wantWentOn(t, r2, "battery")
}

func TestC10NoResumeLatchTripped(t *testing.T) {
	_, r2 := resumeRig(t, resumeOpt{latch: "on"})
	wantNotResumed(t, r2, "watchdog latch")
}

func TestC10NoResumeBoost(t *testing.T) {
	_, r2 := resumeRig(t, resumeOpt{boost: true})
	wantWentOn(t, r2, "boost")
}

// The cover below 90 with no controller-commanded OFF recorded (a manual
// OFF, or an interrupted ON) goes ON as before.
func TestC10NoResumeWithoutControllerOff(t *testing.T) {
	_, r2 := resumeRig(t, resumeOpt{persisted: func(p *Persisted) { p.OffSince = time.Time{} }})
	wantWentOn(t, r2, "manual OFF (no OffSince)")
	_, r3 := resumeRig(t, resumeOpt{persisted: func(p *Persisted) { p.LastCommand.Target = 99 }})
	wantWentOn(t, r3, "last command was ON")
}

// An Override Off still valid after the restart is resumed too (no plan
// period needed), and is re-adopted without a move.
func TestC10ResumeOverrideOff(t *testing.T) {
	_, r2 := resumeRig(t, resumeOpt{
		selectVal: SelectOverrideOff,
		plan:      func(p *Plan) *Plan { return &Plan{PlannerID: "none"} },
	})
	r2.advance(time.Minute)
	if len(r2.cmds) != 0 || r2.state() == "HOLD_ON" {
		t.Fatalf("override resume moved or went ON: %v state %s", r2.cmds, r2.state())
	}
}
