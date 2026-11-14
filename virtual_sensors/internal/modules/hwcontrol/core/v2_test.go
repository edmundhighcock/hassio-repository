package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Planner v2 (0.6.0) hands the core a DefaultOff plan: permitted-OFF
// periods covering the whole day except a few heating windows. These tests
// check that every safety rule still forces ON under such a plan, that the
// reheat log and the heater schedule behave, and that a restart resumes.

// v2Plan permits OFF from now+from for dur, with a heating window after it.
func v2Plan(r *rig, from, dur time.Duration) *Plan {
	s, e := r.now.Add(from), r.now.Add(from+dur)
	return &Plan{PlannerID: "v2", GeneratedAt: r.now, ValidUntil: r.now.Add(48 * time.Hour), DefaultOff: true,
		OffPeriods: []OffPeriod{{Start: s, End: e, Why: "default OFF"}},
		Windows:    []PlanWindow{{Start: e.Add(30 * time.Second), End: e.Add(3 * time.Hour), Kind: "full"}}}
}

// offUnderV2 boots live with a recent reheat and a long default-OFF period
// and lets the controller go OFF.
func offUnderV2(t *testing.T) *rig {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.wantState("IDLE_ON")
	r.tankRecent(time.Hour)
	r.step(PlanUpdate{At: r.now, Plan: v2Plan(r, 16*time.Minute, 12*time.Hour)})
	r.advance(18 * time.Minute)
	r.wantState("IDLE_OFF")
	if r.switchOn {
		t.Fatal("not OFF")
	}
	return r
}

func TestV2EverySafetyOverrideForcesOn(t *testing.T) {
	cases := []struct {
		name string
		do   func(r *rig)
		want string // expected state prefix
	}{
		{"boost", func(r *rig) { r.set(RoleBoost, "2026-11-10T09:20:00+00:00") }, "HOLD_ON(boost)"},
		{"override_on", func(r *rig) { r.set(RoleSelect, SelectOverrideOn) }, "HOLD_ON(override)"},
		{"battery_low", func(r *rig) { r.set(RoleBattery, "29") }, "HOLD_ON(battery)"},
		{"strikes", func(r *rig) { r.c.P.Strikes = 3; r.c.P.LastStrikeAt = r.now }, "HOLD_ON(strikes)"},
		{"backstop_18h_on_time", func(r *rig) { r.c.P.OnSSinceGenuineStart = 19 * 3600 }, "HOLD_ON(backstop)|PROBE"},
		{"conflict", func(r *rig) {
			r.step(EntityUpdate{At: r.now, Role: RoleConflict, Entity: "automation.hot_water", State: "on"})
		}, "HOLD_ON(conflict)"},
		{"tank_clock_expired", func(r *rig) { r.c.P.LastFinishedReheatEnd = r.now.Add(-15 * time.Hour) }, "IDLE_ON"},
		{"select_not_auto", func(r *rig) { r.set(RoleSelect, "something else") }, "IDLE_ON"},
		{"plan_withdrawn", func(r *rig) { r.plan(nil) }, "IDLE_ON"},
		{"plan_without_this_period", func(r *rig) {
			p := v2Plan(r, 3*time.Hour, 4*time.Hour)
			r.plan(p)
		}, "IDLE_ON"},
		{"disarm", func(r *rig) { r.set(RoleArmed, "off") }, "IDLE_ON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := offUnderV2(t)
			tc.do(r)
			r.advance(2 * time.Minute)
			if !r.switchOn {
				t.Fatalf("not ON after %s (state %s)", tc.name, r.state())
			}
			ok := false
			for _, w := range strings.Split(tc.want, "|") {
				ok = ok || strings.HasPrefix(r.state(), w)
			}
			if !ok {
				t.Fatalf("state %s, want %s", r.state(), tc.want)
			}
		})
	}
	t.Run("watchdog_latch_owns_the_cover", func(t *testing.T) {
		r := offUnderV2(t)
		n := len(r.cmds)
		r.set(RoleLatch, "on")
		r.advance(10 * time.Minute)
		r.wantState("HOLD_ON(watchdog)")
		if len(r.cmds) != n {
			t.Fatalf("moves under the watchdog latch: %v", r.cmds[n:])
		}
	})
	t.Run("stale_residual_30min", func(t *testing.T) {
		r := offUnderV2(t)
		r.residualOff = true
		r.advance(40 * time.Minute)
		if !r.switchOn {
			t.Fatalf("not ON with a stale residual (state %s)", r.state())
		}
	})
	t.Run("ha_link_lost_degrades_so_the_watchdog_forces_on", func(t *testing.T) {
		r := offUnderV2(t)
		r.step(Link{At: r.now, Up: false})
		r.advance(time.Minute)
		r.wantState("DEGRADED")
	})
}

// A v2 plan never lets an OFF start where MayGoOff refuses.
func TestV2MayGoOffRulesUnchanged(t *testing.T) {
	// Tank clock unknown: no OFF at all under a v2 plan.
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.step(PlanUpdate{At: r.now, Plan: v2Plan(r, 16*time.Minute, 12*time.Hour)})
	r.advance(40 * time.Minute)
	if r.countCmds(0) != 0 {
		t.Fatal("OFF with an unknown tank clock")
	}
	if ok, why := r.c.MayGoOff(false); ok || why != "tank_clock_unknown" {
		t.Fatalf("MayGoOff = %v %s", ok, why)
	}
	// Move budget: the reserve refuses an OFF when the day is nearly spent.
	r = newRig(t, liveCfg(), nil)
	r.boot(true)
	r.tankRecent(time.Hour)
	for i := 0; i < 6; i++ {
		r.c.P.Moves = append(r.c.P.Moves, r.now.Add(-time.Duration(i+3)*time.Hour))
	}
	r.step(PlanUpdate{At: r.now, Plan: v2Plan(r, 16*time.Minute, 12*time.Hour)})
	r.advance(40 * time.Minute)
	if r.countCmds(0) != 0 {
		t.Fatal("OFF despite the move reserve")
	}
}

// C10: a restart in the middle of a long default-OFF period resumes it
// without a move, when the plan covers now.
func TestV2RestartMidOffResumes(t *testing.T) {
	_, r2 := resumeRig(t, resumeOpt{plan: func(p *Plan) *Plan {
		return &Plan{PlannerID: "v2", GeneratedAt: p.GeneratedAt, ValidUntil: p.ValidUntil, DefaultOff: true,
			OffPeriods: []OffPeriod{{Start: p.OffPeriods[0].Start.Add(-8 * time.Hour), End: p.OffPeriods[0].End.Add(8 * time.Hour)}}}
	}})
	r2.advance(time.Minute)
	if r2.countCmds(99) != 0 || r2.countCmds(0) != 0 {
		t.Fatalf("restart moved the cover: %v", r2.cmds)
	}
	r2.wantState("IDLE_OFF")
}

func TestReheatLogCutAndKind(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	t0 := r.now
	win := PlanWindow{Start: t0.Add(-time.Hour), End: t0.Add(2 * time.Hour), Kind: "full"}
	r.step(PlanUpdate{At: r.now, Plan: &Plan{PlannerID: "v2", ValidUntil: t0.Add(48 * time.Hour), DefaultOff: true, Windows: []PlanWindow{win}}})
	// A normal reheat in the window.
	on := t0.Add(-50 * time.Minute)
	r.step(CycleOpened{At: on, OnTime: on, Magnitude: 3000})
	off := on.Add(100 * time.Minute)
	r.step(CycleClosed{At: off, OnTime: on, OffTime: off, Accepted: true})
	if n := len(r.c.P.Reheats); n != 1 {
		t.Fatalf("reheats %d", n)
	}
	got := r.c.P.Reheats[0]
	if got.Kind != "full" || got.Cut || got.Duration() != 100*time.Minute {
		t.Fatalf("rec %+v", got)
	}
	// A reheat ended by our own OFF is marked cut.
	r.c.P.LastCommand = LastCommand{Target: 0, At: t0.Add(5 * time.Hour)}
	on2 := t0.Add(4 * time.Hour)
	r.step(CycleOpened{At: on2, OnTime: on2, Magnitude: 3000})
	off2 := t0.Add(5*time.Hour + 20*time.Second)
	r.step(CycleClosed{At: off2, OnTime: on2, OffTime: off2, Accepted: true})
	if n := len(r.c.P.Reheats); n != 2 || !r.c.P.Reheats[1].Cut {
		t.Fatalf("reheats %+v", r.c.P.Reheats)
	}
	// Facts expose the log, and it round-trips through the state file.
	f := r.c.Facts()
	if len(f.Reheats) != 2 || len(f.Windows) != 1 {
		t.Fatalf("facts %+v", f)
	}
	b, _ := r.c.Persist()
	p, ok := LoadPersisted(b)
	if !ok || len(p.Reheats) != 2 || len(p.Windows) != 1 {
		t.Fatalf("persisted %+v", p)
	}
	// The log is bounded.
	for i := 0; i < MaxReheatLog+10; i++ {
		s := t0.Add(time.Duration(10+i) * time.Hour)
		r.c.logReheat(s, s.Add(time.Hour), true, 0)
	}
	if len(r.c.P.Reheats) != MaxReheatLog {
		t.Fatalf("log %d", len(r.c.P.Reheats))
	}
}

func TestScheduleDefaultOff(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	cfg := liveCfg()
	mk := func(live bool, lastEnd time.Time) *Controller {
		c := New(cfg, nil, "cover.x")
		c.now, c.live = now, live
		c.P.LastFinishedReheatEnd = lastEnd
		c.plan = &Plan{DefaultOff: true, ValidUntil: now.Add(30 * time.Hour), OffPeriods: []OffPeriod{
			{Start: now, End: now.Add(5 * time.Hour)},                     // until a 2 h window
			{Start: now.Add(7 * time.Hour), End: now.Add(20 * time.Hour)}, // long OFF after it
		}}
		return c
	}
	// SHADOW: the controller never switches, so the heater is ON throughout.
	s := mk(false, now.Add(-time.Hour)).Schedule()
	if s.Planned || len(s.Windows) != 1 || !s.Windows[0].On {
		t.Fatalf("shadow: %+v", s)
	}
	// Unknown tank clock: the core would refuse every OFF: all ON.
	s = mk(true, time.Time{}).Schedule()
	if len(s.Windows) != 1 || !s.Windows[0].On {
		t.Fatalf("unknown clock: %+v", s)
	}
	// Live with a recent reheat (clock to now+13h): the first OFF holds; the
	// window's reheat is assumed to reset the clock, so the second holds too.
	s = mk(true, now.Add(-time.Hour)).Schedule()
	var offs int
	for _, w := range s.Windows {
		if !w.On {
			offs++
		}
	}
	if !s.Planned || offs != 2 {
		t.Fatalf("live: %+v", s.Windows)
	}
	// Clock nearly out (expires in 2 h): the first OFF is cut to 2 h.
	s = mk(true, now.Add(-12*time.Hour)).Schedule()
	if s.Windows[0].On || !s.Windows[0].End.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("clipped: %+v", s.Windows)
	}
}

func TestPlanAlertSentOnceInLive(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	pa := PlanAlert{Code: "FULL_HEAT_INCOMPLETE", Text: "x", Repeat: 6 * time.Hour}
	r.step(PlanUpdate{At: r.now, Alerts: []PlanAlert{pa}})
	r.step(PlanUpdate{At: r.now, Alerts: []PlanAlert{pa}})
	if r.notifies("FULL_HEAT_INCOMPLETE") != 1 {
		t.Fatalf("notifies %d", r.notifies("FULL_HEAT_INCOMPLETE"))
	}
}

// The window-satisfied stamp (clock.go): a heating window with no call for
// heat restarts the tank clock, on strict evidence only.
func stampRig(t *testing.T, mut func(*Config), lastEndAgo time.Duration, winLen time.Duration) (*rig, PlanWindow) {
	cfg := liveCfg()
	cfg.WindowStamp = true
	if mut != nil {
		mut(&cfg)
	}
	r := newRig(t, cfg, nil)
	r.boot(true)
	r.tankRecent(lastEndAgo)
	w := PlanWindow{Start: r.now.Add(time.Hour), End: r.now.Add(time.Hour + winLen), Kind: "topup"}
	r.step(PlanUpdate{At: r.now, Plan: &Plan{PlannerID: "v2", ValidUntil: r.now.Add(48 * time.Hour), DefaultOff: true, Windows: []PlanWindow{w}}})
	return r, w
}

func TestWindowStampRestartsTankClock(t *testing.T) {
	r, w := stampRig(t, nil, 10*time.Hour, 70*time.Minute)
	r.advance(2*time.Hour + 15*time.Minute)
	if !r.c.P.SatisfiedEnd.Equal(w.End) {
		t.Fatalf("SatisfiedEnd %v, want %v", r.c.P.SatisfiedEnd, w.End)
	}
	// The clock now counts from the window: real reheat end is 12 h+ ago
	// at +2 h, and 14 h+ ago at +4.5 h, yet OFF is still allowed.
	r.advance(2 * time.Hour)
	if !r.c.tankOK() {
		t.Fatal("tank clock tripped although the window restarted it")
	}
	if te := r.c.Facts().TankHeadroom; te <= 0 {
		t.Fatal("no headroom")
	}
}

func TestWindowStampNeedsStrictEvidence(t *testing.T) {
	check := func(name string, r *rig, w PlanWindow, d time.Duration) {
		t.Helper()
		r.advance(d)
		if !r.c.P.SatisfiedEnd.IsZero() {
			t.Fatalf("%s: stamped", name)
		}
	}
	// Disabled (the greedy_v1 default): never.
	r, w := stampRig(t, func(c *Config) { c.WindowStamp = false }, 10*time.Hour, 70*time.Minute)
	check("disabled", r, w, 2*time.Hour+15*time.Minute)
	// A heater step opened during the window: a real reheat, not a no-call.
	r, w = stampRig(t, nil, 10*time.Hour, 70*time.Minute)
	r.advance(75 * time.Minute)
	r.step(CycleOpened{At: r.now, OnTime: r.now, Magnitude: 3000})
	check("cycle opened", r, w, time.Hour)
	// Window shorter than 30 min.
	r, w = stampRig(t, nil, 10*time.Hour, 20*time.Minute)
	check("short window", r, w, 2*time.Hour)
	// No proof of life: the last real reheat is older than 28 h.
	r, w = stampRig(t, func(c *Config) { c.OffLimitSinceReheatEnd = 16 * time.Hour }, 29*time.Hour, 70*time.Minute)
	check("no proof of life", r, w, 2*time.Hour+15*time.Minute)
	// Unknown clock (never saw a reheat).
	r, w = stampRig(t, nil, 10*time.Hour, 70*time.Minute)
	r.c.P.LastFinishedReheatEnd = time.Time{}
	check("unknown clock", r, w, 2*time.Hour+15*time.Minute)
	// The residual went stale inside the window: the no-call is unobserved.
	r, w = stampRig(t, nil, 10*time.Hour, 70*time.Minute)
	r.advance(75 * time.Minute)
	r.residualOff = true
	r.advance(40 * time.Minute)
	r.residualOff = false
	check("stale residual", r, w, 40*time.Minute)
	// Our own OFF command fell inside the window.
	r, w = stampRig(t, nil, 10*time.Hour, 70*time.Minute)
	r.advance(75 * time.Minute)
	r.c.P.LastOffCmdAt = r.now
	check("own OFF in the window", r, w, time.Hour)
	// SHADOW: the controller has no authority, nothing to observe.
	r, w = stampRig(t, func(c *Config) { c.Live = false }, 10*time.Hour, 70*time.Minute)
	check("shadow", r, w, 2*time.Hour+15*time.Minute)
}

// 0.6.6: the phase evidence on CycleClosed reaches the reheat record; a cut
// record is never Satisfied; no evidence leaves both flags false.
func TestReheatLogPhaseEnd(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	t0 := r.now
	for i, pe := range []int{1, 2, 0} {
		on := t0.Add(time.Duration(i) * 6 * time.Hour)
		off := on.Add(100 * time.Minute)
		r.step(CycleOpened{At: on, OnTime: on, Magnitude: 3000})
		r.step(CycleClosed{At: off, OnTime: on, OffTime: off, Accepted: true, PhaseEnd: pe})
	}
	// A cut one with positive phase evidence: not satisfied.
	on := t0.Add(18 * time.Hour)
	r.c.P.LastCommand = LastCommand{Target: 0, At: on.Add(100 * time.Minute)}
	r.step(CycleOpened{At: on, OnTime: on, Magnitude: 3000})
	off := on.Add(100*time.Minute + 20*time.Second)
	r.step(CycleClosed{At: off, OnTime: on, OffTime: off, Accepted: true, PhaseEnd: 1})
	rs := r.c.P.Reheats
	if len(rs) != 4 {
		t.Fatalf("reheats %+v", rs)
	}
	want := []struct{ sat, seen, cut bool }{{true, true, false}, {false, true, false}, {false, false, false}, {false, true, true}}
	for i, w := range want {
		if rs[i].Satisfied != w.sat || rs[i].PhaseSeen != w.seen || rs[i].Cut != w.cut {
			t.Errorf("rec %d: %+v want %+v", i, rs[i], w)
		}
	}
}

func TestReheatRecJSONCompat(t *testing.T) {
	old := `{"start":"2026-11-10T10:00:00Z","end":"2026-11-10T11:00:00Z","kind":"full"}`
	var r ReheatRec
	if err := json.Unmarshal([]byte(old), &r); err != nil || r.Satisfied || r.PhaseSeen {
		t.Fatalf("old record: %v %+v", err, r)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "satisfied") || strings.Contains(string(b), "phase_seen") {
		t.Fatalf("zero fields serialized: %s", b)
	}
	r.Satisfied, r.PhaseSeen = true, true
	b, _ = json.Marshal(r)
	var r2 ReheatRec
	if err := json.Unmarshal(b, &r2); err != nil || !r2.Satisfied || !r2.PhaseSeen || !strings.Contains(string(b), `"satisfied":true`) {
		t.Fatalf("round trip: %s %v %+v", b, err, r2)
	}
}
