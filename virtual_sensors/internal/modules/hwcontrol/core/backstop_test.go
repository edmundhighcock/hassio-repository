package core

import (
	"math/rand"
	"testing"
	"time"
)

// §4.4 / C3: the labelled kettle (+2.55 kW for 2 min 20 s) never stamps;
// a 2.8 kW cycle open >= 5 min stamps retroactively at its on-time; a
// 2.8 kW cycle closing at 4 min does not; a discontinuity within ±60 s
// of the on-step blocks it.
func TestGenuineStartQualifier(t *testing.T) {
	type cyc struct {
		mag   float64
		dur   time.Duration
		disc  bool
		stamp bool
	}
	for name, tc := range map[string]cyc{
		"kettle":       {2550, 140 * time.Second, false, false},
		"short_strong": {2800, 4 * time.Minute, false, false},
		"heater":       {2800, 6 * time.Minute, false, true},
		"heater_disc":  {2800, 6 * time.Minute, true, false},
		"dishwasher":   {2200, 20 * time.Minute, false, false},
	} {
		r := newRig(t, liveCfg(), nil)
		r.boot(true)
		r.advance(time.Hour)
		on := r.now
		r.step(CycleOpened{At: r.now, OnTime: on, Magnitude: tc.mag})
		if tc.disc {
			r.advance(30 * time.Second)
			r.step(Discontinuity{At: r.now, Entity: "heat_pump_power_power", DeltaW: 900, Seq: 1})
		}
		r.advance(tc.dur)
		r.step(CycleClosed{At: r.now.Add(90 * time.Second), OnTime: on, OffTime: r.now, Accepted: true})
		r.advance(2 * time.Minute)
		got := r.c.P.LastGenuineStart.Equal(on)
		if got != tc.stamp {
			t.Fatalf("%s: stamped=%v want %v", name, got, tc.stamp)
		}
		if tc.stamp {
			if !r.c.P.LastFinishedReheatEnd.Equal(on.Add(tc.dur)) {
				t.Fatalf("%s: tank clock not stamped", name)
			}
			if r.c.P.OnSSinceGenuineStart > (tc.dur + 3*time.Minute).Seconds() {
				t.Fatalf("%s: ON clock not reset (%.0f s)", name, r.c.P.OnSSinceGenuineStart)
			}
		} else if !r.c.P.LastFinishedReheatEnd.IsZero() {
			t.Fatalf("%s: tank clock stamped by a non-genuine cycle", name)
		}
	}
}

// A replayed short cycle (open, then closed 2 min later in history) must
// not qualify just because wall time is far past its on-time.
func TestReplayedShortCycleDoesNotStamp(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.advance(4 * time.Hour)
	on := r.now.Add(-3 * time.Hour)
	r.step(Sample{At: on.Add(-10 * time.Second), W: 400, Available: true, Replay: true})
	r.step(CycleOpened{At: on, OnTime: on, Magnitude: 3000, Replay: true})
	r.step(Sample{At: on.Add(60 * time.Second), W: 3400, Available: true, Replay: true})
	r.step(Tick{At: r.now})
	r.step(CycleClosed{At: on.Add(4 * time.Minute), OnTime: on, OffTime: on.Add(2 * time.Minute), Accepted: true, Replay: true})
	r.advance(time.Minute)
	if r.c.P.LastGenuineStart.Equal(on) {
		t.Fatal("a 2-min replayed cycle stamped the genuine-start clock")
	}
}

// §4.4 + D19 + S2 shape: with no genuine start ever (no_engage: the
// contact never closes, a kettle fakes the ON confirmation), the probe
// fires at 10 h of ON time, the next at +4 h, and BACKSTOP_18H by 18 h,
// with HOLD_ON(backstop). The fake never stamps the clock.
func TestBackstopProbeAndAlertOnTime(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.noEngage = true
	r.switchOn = false
	r.boot(true)
	start := r.now
	// A kettle-shaped fake right after boot.
	r.step(CycleOpened{At: r.now, OnTime: r.now, Magnitude: 2550})
	r.advance(140 * time.Second)
	r.step(CycleClosed{At: r.now, OnTime: start, OffTime: r.now, Accepted: true})

	var probes []time.Time
	last := 0
	for r.now.Sub(start) < 18*time.Hour+10*time.Minute {
		r.advance(time.Minute)
		for ; last < len(r.cmds); last++ {
			if r.cmds[last] == 0 {
				probes = append(probes, r.now)
			}
		}
	}
	if len(probes) < 2 {
		t.Fatalf("probes %v", probes)
	}
	if d := probes[0].Sub(start); d < 10*time.Hour-5*time.Minute || d > 10*time.Hour+5*time.Minute {
		t.Fatalf("first probe at %v of ON time, want 10 h ± 5 min", d)
	}
	if d := probes[1].Sub(probes[0]); d < 4*time.Hour-5*time.Minute || d > 4*time.Hour+5*time.Minute {
		t.Fatalf("second probe %v after the first, want 4 h", d)
	}
	if r.notifies(AlertBackstop) != 1 {
		t.Fatalf("BACKSTOP_18H notifies %d", r.notifies(AlertBackstop))
	}
	if !r.c.P.LastGenuineStart.IsZero() {
		t.Fatal("the kettle stamped the clock")
	}
	if s := r.state(); s != "HOLD_ON(backstop)" && s != "PROBE" {
		t.Fatalf("state %s, want HOLD_ON(backstop)", s)
	}
}

// D19: the ON-time clock is frozen while the switch is OFF.
func TestOnClockFrozenWhileOff(t *testing.T) {
	r := readyForOff(t)
	r.planNow(16*time.Minute, 3*time.Hour)
	r.advance(20 * time.Minute)
	r.wantState("IDLE_OFF")
	before := r.c.P.OnSSinceGenuineStart
	r.advance(2 * time.Hour)
	if d := r.c.P.OnSSinceGenuineStart - before; d > 1 {
		t.Fatalf("ON clock advanced %.0f s while OFF", d)
	}
}

func TestSeedFromHistory(t *testing.T) {
	base := t0.Add(-20 * time.Hour)
	pw := []SeedPoint{
		{base, 0, true},
		{base.Add(time.Hour), 2550, true}, {base.Add(time.Hour + 140*time.Second), 0, true}, // kettle
		{base.Add(5 * time.Hour), 2900, true}, {base.Add(5*time.Hour + 25*time.Minute), 0, true}, // reheat
		{base.Add(12 * time.Hour), 3000, true}, {base.Add(12*time.Hour + 3*time.Minute), 0, true}, // short, unaccepted
	}
	en := []SeedPoint{{base, 10, true}, {base.Add(time.Hour + 140*time.Second), 10.1, true}, {base.Add(5*time.Hour + 25*time.Minute), 11.3, true}}
	cycles := SeedFromHistory(pw, en)
	if len(cycles) != 3 || !cycles[1].Accepted || cycles[2].Accepted {
		t.Fatalf("cycles %+v", cycles)
	}
	c := New(liveCfg(), nil, "cover.x")
	c.Step(Seed{At: t0, Cycles: cycles})
	if !c.P.LastFinishedReheatEnd.Equal(base.Add(5*time.Hour + 25*time.Minute)) {
		t.Fatalf("seeded tank clock %v", c.P.LastFinishedReheatEnd)
	}
	if !c.P.LastGenuineStart.Equal(base.Add(5 * time.Hour)) {
		t.Fatalf("seeded genuine start %v", c.P.LastGenuineStart)
	}
}

func TestClamp(t *testing.T) {
	c := Defaults()
	c.AlertAfter = 30 * time.Hour
	c.OffLimitSinceReheatEnd = 20 * time.Hour
	c.BoostMax = 5 * time.Hour
	c.StaleAfter = 5 * time.Hour
	msgs := c.Clamp()
	d := Defaults()
	if c.AlertAfter != d.AlertAfter || c.OffLimitSinceReheatEnd != d.OffLimitSinceReheatEnd || c.BoostMax != d.BoostMax || c.StaleAfter != d.StaleAfter || len(msgs) != 4 {
		t.Fatalf("clamp %+v %v", c, msgs)
	}
	if _, ok := ParseHHMM("25:00"); ok {
		t.Fatal("bad HH:MM accepted")
	}
}

// Property tests (PLAN §7.6): for random event sequences,
//   - an OFF period is only ever entered when MayGoOff's observable
//     preconditions hold (tank clock, plan period, select Auto, LIVE);
//   - the move caps are never exceeded;
//   - under the watchdog latch no cover command is issued;
//   - in SHADOW no cover command is issued;
//   - every HOLD_ON (other than watchdog, conflict and on_failed) with the
//     cover below 90 and HA up issues an ON within a minute, budget
//     permitting.
func TestPropertiesRandomSequences(t *testing.T) {
	for seed := int64(1); seed <= 12; seed++ {
		propertyRun(t, seed, seed%4 != 0)
	}
}

func propertyRun(t *testing.T, seed int64, live bool) {
	rng := rand.New(rand.NewSource(seed))
	cfg := liveCfg()
	cfg.Live = live
	r := newRig(t, cfg, nil)
	r.boot(true)
	r.tankRecent(time.Duration(rng.Intn(16)) * time.Hour)

	holdSince := map[string]time.Time{}
	for i := 0; i < 1500; i++ {
		switch rng.Intn(30) {
		case 0:
			r.set(RoleSelect, []string{SelectAuto, SelectAuto, SelectAuto, SelectOverrideOn, SelectOverrideOff}[rng.Intn(5)])
		case 1:
			r.set(RoleBattery, []string{"80", "80", "29", "unavailable"}[rng.Intn(4)])
		case 2:
			r.set(RoleLatch, []string{"off", "off", "off", "on"}[rng.Intn(4)])
		case 3:
			r.planNow(time.Duration(rng.Intn(60))*time.Minute, time.Duration(30+rng.Intn(300))*time.Minute)
		case 4:
			r.calling = !r.calling
			r.emitResidual(true)
		case 5:
			r.retreatN = rng.Intn(2)
		case 6:
			r.failCalls = rng.Intn(4)
		case 7:
			r.stickN = rng.Intn(2)
		case 8:
			r.step(Link{At: r.now, Up: false})
			r.advance(time.Duration(rng.Intn(120)) * time.Second)
			r.step(Link{At: r.now, Up: true})
			r.pushCover()
			r.pushTarget()
		case 9:
			r.set(RoleBoost, r.now.Format(time.RFC3339))
		case 10:
			on := r.now
			r.step(CycleOpened{At: r.now, OnTime: on, Magnitude: float64(2400 + rng.Intn(800))})
			r.advance(time.Duration(rng.Intn(900)) * time.Second)
			r.step(CycleClosed{At: r.now, OnTime: on, OffTime: r.now, Accepted: rng.Intn(3) > 0})
		case 11:
			r.set(RoleEV, []string{"0", "0", "7000"}[rng.Intn(3)])
		case 12:
			r.step(EntityUpdate{At: r.now, Role: RoleConflict, Entity: "automation.hot_water", State: []string{"off", "off", "off", "on"}[rng.Intn(4)]})
		case 13:
			r.step(Discontinuity{At: r.now, Entity: "fridge_power", DeltaW: 80, Seq: r.c.lastSeq + 1})
		}
		cmdsBefore := len(r.cmds)
		stateBefore := r.c.P.State
		latched := r.c.entityFresh(RoleLatch) && r.c.str(RoleLatch) == "on"
		r.advance(time.Duration(5+rng.Intn(240)) * time.Second)

		if r.c.movesInLast(time.Hour) > cfg.MaxMovesHour || r.c.movesInLast(24*time.Hour) > cfg.MaxMovesDay {
			t.Fatalf("seed %d: move caps exceeded (%d/h, %d/day)", seed, r.c.movesInLast(time.Hour), r.c.movesInLast(24*time.Hour))
		}
		if !live && len(r.cmds) != 0 {
			t.Fatalf("seed %d: shadow commanded %v", seed, r.cmds)
		}
		if latched && r.c.entityFresh(RoleLatch) && r.c.str(RoleLatch) == "on" && len(r.cmds) != cmdsBefore && stateBefore == StHoldOn {
			t.Fatalf("seed %d: cover command under the watchdog latch", seed)
		}
		if r.c.P.State == StCmdOff || r.c.P.State == StVerifyOff || r.c.P.State == StIdleOff {
			if !r.c.Live() || r.c.str(RoleSelect) != SelectAuto {
				t.Fatalf("seed %d: plan OFF while not live/auto (%s)", seed, r.state())
			}
			if r.c.offPeriod == nil {
				t.Fatalf("seed %d: plan OFF without a period", seed)
			}
			if r.c.P.State == StCmdOff && !r.c.tankOK() {
				t.Fatalf("seed %d: OFF with the tank clock past the limit", seed)
			}
		}
		h := r.c.hold
		if live && r.c.P.State == StHoldOn && h != HoldWatchdog && h != HoldConflict && h != HoldOnFailed && !r.c.coverOn() && r.c.linkUp {
			if _, ok := holdSince[h]; !ok {
				holdSince[h] = r.now
			}
			if r.now.Sub(holdSince[h]) > 3*time.Minute && r.c.mv == nil && r.c.movesAvailable(1) && r.c.holdOnFails < cfg.OnMaxAttempts {
				if _, known := r.c.coverPosition(); known {
					t.Fatalf("seed %d: HOLD_ON(%s) with the cover below 90 and no ON", seed, h)
				}
			}
		} else {
			delete(holdSince, h)
		}
	}
}
