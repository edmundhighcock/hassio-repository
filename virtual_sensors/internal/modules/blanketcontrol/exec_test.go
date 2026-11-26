package blanketcontrol

import (
	"strings"
	"testing"
	"time"
)

const live = `{"bc_mode":"live"}`

func liveRig(t *testing.T) *rig { return newRig(t, live, true) }

func count(cs []call, svc, ent string) int {
	n := 0
	for _, c := range cs {
		if c.Service == svc && c.Entity == ent {
			n++
		}
	}
	return n
}

func TestStartProtocolOrderAndTiming(t *testing.T) {
	r := liveRig(t)
	r.run(10 * time.Minute)
	cs := r.protocol(0)
	if len(cs) < 3 {
		t.Fatalf("calls %v", names(cs))
	}
	if cs[0].String() != "switch.turn_off:"+r.m.plugEnt || cs[1].String() != "switch.turn_on:"+r.m.plugEnt || cs[2].String() != "switch.turn_on:"+r.m.botEnt {
		t.Fatalf("order %v", names(cs))
	}
	if d := cs[1].At.Sub(cs[0].At); d < 10*time.Second || d > 20*time.Second {
		t.Fatalf("off->on %v", d)
	}
	if d := cs[2].At.Sub(cs[1].At); d < 5*time.Second || d > 15*time.Second {
		t.Fatalf("on->press %v", d)
	}
	if len(cs) != 3 {
		t.Fatalf("extra protocol calls %v", names(cs))
	}
	if st, a := r.state(); st != "on" {
		t.Fatalf("state %s %v", st, a["reason"])
	}
	if d := r.m.day(r.now); d.Presses != 1 || d.Starts != 1 {
		t.Fatalf("counters %+v", d)
	}
}

func TestLostPressRestartsWholeStart(t *testing.T) {
	r := liveRig(t)
	r.pressLost = func(n int) bool { return n == 1 || n == 2 }
	r.run(15 * time.Minute)
	cs := r.protocol(0)
	checkCycleBeforeEveryPress(t, cs, r.m.plugEnt, r.m.botEnt)
	if count(cs, "turn_on", r.m.botEnt) != 3 || count(cs, "turn_off", r.m.plugEnt) != 3 {
		t.Fatalf("calls %v", names(cs))
	}
	if st, _ := r.state(); st != "on" {
		t.Fatalf("state %s", st)
	}
}

func TestStateShowsTryNumber(t *testing.T) {
	r := liveRig(t)
	r.pressLost = func(n int) bool { return true }
	r.run(3 * time.Minute) // first confirm window (150 s) over: second try running
	if st, _ := r.state(); st != "starting(try 2)" {
		t.Fatalf("state %q", st)
	}
}

func TestLateToggleWithinFiveMinutesRestarts(t *testing.T) {
	r := liveRig(t)
	r.run(3 * time.Minute)
	if st, _ := r.state(); st != "on" {
		t.Fatalf("not on: %s", st)
	}
	mk := r.mark()
	// a delayed press lands and toggles the blanket off
	r.setBlanket(false)
	r.run(10 * time.Minute)
	cs := r.protocol(mk)
	checkCycleBeforeEveryPress(t, cs, r.m.plugEnt, r.m.botEnt)
	if count(cs, "turn_on", r.m.botEnt) != 1 || count(cs, "turn_off", r.m.plugEnt) != 1 {
		t.Fatalf("restart calls %v", names(cs))
	}
	if st, _ := r.state(); st != "on" {
		t.Fatalf("state %s", st)
	}
	if r.m.x.st.LastKind != "late" {
		t.Fatalf("kind %q", r.m.x.st.LastKind)
	}
}

func TestZeroWattsInWindowNoAction(t *testing.T) {
	r := liveRig(t)
	r.run(3 * time.Minute)
	r.run(6 * time.Minute) // past the late window
	mk := r.mark()
	r.setBlanket(true)
	r.thermostat = true // the blanket's own thermostat: 0 W while logically on
	r.sendNum(r.m.powerEnt, 0)
	r.run(90 * time.Minute)
	if cs := r.protocol(mk); len(cs) != 0 {
		t.Fatalf("acted on 0 W: %v", names(cs))
	}
	if st, _ := r.state(); st != "on" {
		t.Fatalf("state %s", st)
	}
}

func TestRefreshBeforeWindowEnds(t *testing.T) {
	r := liveRig(t)
	r.run(3 * time.Minute)
	confirmed := r.m.x.onSince
	if confirmed.IsZero() {
		t.Fatal("not confirmed")
	}
	mk := r.mark()
	r.run(confirmed.Add(2*time.Hour + 44*time.Minute).Sub(r.now))
	if cs := r.protocol(mk); len(cs) != 0 {
		t.Fatalf("early refresh %v", names(cs))
	}
	r.run(12 * time.Minute)
	cs := r.protocol(mk)
	checkCycleBeforeEveryPress(t, cs, r.m.plugEnt, r.m.botEnt)
	if count(cs, "turn_on", r.m.botEnt) != 1 {
		t.Fatalf("refresh calls %v", names(cs))
	}
	if first := cs[0].At.Sub(confirmed); first >= 2*time.Hour+50*time.Minute {
		t.Fatalf("refresh began at %v, after the 2h50 window", first)
	}
	if st, _ := r.state(); st != "on" || r.m.x.st.LastKind != "refresh" {
		t.Fatalf("state %s kind %s", st, r.m.x.st.LastKind)
	}
	// the block continues over the 3 h cut-off: still on 20 min after the original cut-off
	r.run(10 * time.Minute)
	if !r.blanketOn {
		t.Fatal("the blanket cut itself off despite the refresh")
	}
}

func TestFailureAfterThreeTriesThenRecovery(t *testing.T) {
	r := liveRig(t)
	r.pressLost = func(n int) bool { return true }
	r.run(10 * time.Minute)
	cs := r.protocol(0)
	checkCycleBeforeEveryPress(t, cs, r.m.plugEnt, r.m.botEnt)
	if n := count(cs, "turn_on", r.m.botEnt); n != 3 {
		t.Fatalf("%d presses in the first episode", n)
	}
	if st, _ := r.state(); st != "failed" {
		t.Fatalf("state %s", st)
	}
	if r.pushes("Blanket controller FAILED") != 1 || r.logbooks("Blanket controller FAILED") != 1 {
		t.Fatalf("push %d logbook %d", r.pushes("Blanket controller FAILED"), r.logbooks("Blanket controller FAILED"))
	}
	// a full start every 10 min, one try each, no further push
	mk := r.mark()
	r.run(35 * time.Minute)
	cs = r.protocol(mk)
	checkCycleBeforeEveryPress(t, cs, r.m.plugEnt, r.m.botEnt)
	if n := count(cs, "turn_on", r.m.botEnt); n < 3 || n > 4 {
		t.Fatalf("%d retries in 35 min: %v", n, names(cs))
	}
	if r.pushes("Blanket controller FAILED") != 1 {
		t.Fatalf("failure pushed %d times", r.pushes("Blanket controller FAILED"))
	}
	// the Bot works again
	r.pressLost = nil
	r.run(25 * time.Minute)
	if st, _ := r.state(); st != "on" {
		t.Fatalf("not recovered: %s", st)
	}
	if r.pushes("Blanket controller recovered") != 1 {
		t.Fatalf("recovery pushes %d", r.pushes("Blanket controller recovered"))
	}
	mk = r.mark()
	r.run(30 * time.Minute)
	if r.pushes("Blanket controller recovered") != 1 || len(r.protocol(mk)) != 0 {
		t.Fatal("repeat after recovery")
	}
}

func TestFailedPlugCommandRetriesWholeStart(t *testing.T) {
	r := liveRig(t)
	r.failPlugOff = true
	r.run(4 * time.Minute)
	if st, _ := r.state(); !strings.HasPrefix(st, "starting") && st != "failed" {
		t.Fatalf("state %s", st)
	}
	if count(r.protocol(0), "turn_on", r.m.botEnt) != 0 {
		t.Fatal("pressed although the plug could not be cycled")
	}
	r.failPlugOff = false
	r.run(10 * time.Minute)
	if st, _ := r.state(); st != "on" {
		t.Fatalf("state %s", st)
	}
	checkCycleBeforeEveryPress(t, r.protocol(0), r.m.plugEnt, r.m.botEnt)
}

func TestPlugPowerLossStartsAgain(t *testing.T) {
	r := liveRig(t)
	r.run(3 * time.Minute)
	r.run(10 * time.Minute)
	mk := r.mark()
	// power cut: the plug goes unavailable, then returns (on); the blanket is off
	r.plug, r.blanketOn = "unavailable", false
	r.send(r.m.plugEnt, "unavailable", 0)
	r.run(30 * time.Second)
	if st, _ := r.state(); st == "on" {
		t.Fatal("still believes the blanket is on")
	}
	if len(r.protocol(mk)) != 0 {
		t.Fatalf("commanded while the plug is unavailable: %v", names(r.protocol(mk)))
	}
	r.plug = "on"
	r.send(r.m.plugEnt, "on", 0)
	r.run(10 * time.Minute)
	cs := r.protocol(mk)
	checkCycleBeforeEveryPress(t, cs, r.m.plugEnt, r.m.botEnt)
	if count(cs, "turn_on", r.m.botEnt) != 1 {
		t.Fatalf("calls %v", names(cs))
	}
	if st, _ := r.state(); st != "on" || r.m.x.st.LastKind != "plug_lost" {
		t.Fatalf("state %s kind %s", st, r.m.x.st.LastKind)
	}
}

func TestPlugTurnedOffBySomeoneStartsAgain(t *testing.T) {
	r := liveRig(t)
	r.run(15 * time.Minute)
	mk := r.mark()
	r.plug, r.blanketOn = "off", false
	r.send(r.m.plugEnt, "off", 0)
	r.sendNum(r.m.powerEnt, 0)
	r.run(10 * time.Minute)
	if count(r.protocol(mk), "turn_on", r.m.botEnt) != 1 {
		t.Fatalf("calls %v", names(r.protocol(mk)))
	}
}

func TestOffWhenPlanSaysOffAfterMinOn(t *testing.T) {
	r := liveRig(t)
	r.run(3 * time.Minute)
	mk := r.mark()
	r.tVal = 5.8 // hysteresis releases at floor+1.5 (5.5)
	r.sendT()
	r.run(30 * time.Minute)
	if len(r.protocol(mk)) != 0 {
		t.Fatalf("switched off inside the minimum on-block: %v", names(r.protocol(mk)))
	}
	r.run(45 * time.Minute)
	cs := r.protocol(mk)
	if len(cs) != 1 || cs[0].Service != "turn_off" || cs[0].Entity != r.m.plugEnt {
		t.Fatalf("calls %v", names(cs))
	}
	if st, _ := r.state(); st != "off" {
		t.Fatalf("state %s", st)
	}
}

func TestStartCapStopsPlannedStartsButNotBackstop(t *testing.T) {
	r := newRig(t, `{"bc_mode":"live","bc_max_starts_day":2}`, true)
	r.m.day(r.now).Starts = 2 // today's budget is spent
	r.run(30 * time.Minute)
	if len(r.protocol(0)) != 0 {
		t.Fatalf("started beyond the cap: %v", names(r.protocol(0)))
	}
	if st, a := r.state(); st != "off" || !strings.Contains(a["reason"].(string), "start cap") {
		t.Fatalf("state %s %v", st, a["reason"])
	}
	// frost backstop may exceed the cap
	r.tVal = 2.9
	r.sendT()
	r.run(10 * time.Minute)
	if count(r.protocol(0), "turn_on", r.m.botEnt) != 1 {
		t.Fatalf("backstop did not start despite the cap: %v", names(r.protocol(0)))
	}
	if st, _ := r.state(); st != "backstop" {
		t.Fatalf("state %s", st)
	}
}

func TestHardCapBeatsBackstopFailsafeAndManualStart(t *testing.T) {
	r := liveRig(t)
	r.run(3 * time.Minute)
	if !r.m.x.on {
		t.Fatal("not on")
	}
	mk := r.mark()
	// in the backstop band the sensor reads 3.0 and the blanket heats...
	r.tVal = 6.0 // at the cap: off now, even inside the minimum on-block
	r.sendT()
	r.run(time.Minute)
	cs := r.protocol(mk)
	if len(cs) != 1 || cs[0].Service != "turn_off" {
		t.Fatalf("not switched off at the cap: %v", names(cs))
	}
	if st, _ := r.state(); st != "off" {
		t.Fatalf("state %s", st)
	}
	// manual start refused
	mk = r.mark()
	r.m.OnButton(ButtonStartNow, r.now)
	r.run(5 * time.Minute)
	if len(r.protocol(mk)) != 0 {
		t.Fatalf("start now ignored the cap: %v", names(r.protocol(mk)))
	}
	// the backstop latch from earlier does not override the cap
	r.m.x.bsLatch = true
	r.run(time.Minute)
	if len(r.protocol(mk)) != 0 {
		t.Fatalf("latched backstop overrode the cap: %v", names(r.protocol(mk)))
	}
	// fail-safe: sensor silent with the last reading at the cap: still no heat
	r.tSilent = true
	r.run(3 * time.Hour)
	if len(r.protocol(mk)) != 0 {
		t.Fatalf("fail-safe heated at the cap: %v", names(r.protocol(mk)))
	}
}

func TestSensorSilentHeatsOneWindowOnlyAndOnlyBelowTarget(t *testing.T) {
	r := liveRig(t)
	r.tVal = 4.2 // below the 4.5 target; the sensor then goes silent
	r.sendT()
	r.run(2 * time.Minute)
	r.tSilent = true
	r.run(2*time.Hour + 30*time.Minute) // silent > 2 h from the last reading
	if st, a := r.state(); st != "on" && !strings.HasPrefix(st, "starting") {
		t.Fatalf("fail-safe did not heat: %s %v", st, a["reason"])
	}
	starts := count(r.protocol(0), "turn_on", r.m.botEnt)
	// one on-window without a fresh reading, then no refresh and no new start
	r.run(5 * time.Hour)
	if got := count(r.protocol(0), "turn_on", r.m.botEnt); got > starts+1 {
		t.Fatalf("heated more than one on-window while silent: %d presses (%d before)", got, starts)
	}
	// a fresh reading re-arms it
	r.tSilent = false
	r.tVal = 4.2
	r.sendT()
	if r.m.x.silentStarts != 0 {
		t.Fatal("fresh reading did not reset the silent-start budget")
	}

	// silent with the last reading at or above the target: not heating
	r2 := liveRig(t)
	r2.tVal = 5.2
	r2.sendT()
	r2.tSilent = true
	r2.run(4 * time.Hour)
	if len(r2.protocol(0)) != 0 {
		t.Fatalf("heated while silent above target: %v", names(r2.protocol(0)))
	}
}

func TestSensorUnavailableIsFailSafe(t *testing.T) {
	r := liveRig(t)
	r.tVal = 4.2
	r.sendT()
	r.m.OnStateUpdate(modulesUpdate(r.m.tempEnt, "unavailable", r.now))
	r.run(5 * time.Minute)
	if count(r.protocol(0), "turn_on", r.m.botEnt) == 0 {
		t.Fatalf("no heat with the sensor unavailable: %v", names(r.protocol(0)))
	}
}

func TestHotSpotCap(t *testing.T) {
	// with a hot-spot sensor: the cap uses max(storage, hot spot) each against its own limit
	r := newRig(t, `{"bc_mode":"live","bc_hot_sensor":"sensor.tool_shed_temperature"}`, true)
	if r.m.hotEnt != "tool_shed_temperature" || r.m.hotMax != 8 || r.m.hotAlarm != 10 {
		t.Fatalf("hot config %q %v %v", r.m.hotEnt, r.m.hotMax, r.m.hotAlarm)
	}
	found := false
	for _, in := range r.m.Inputs() {
		found = found || in.EntityID == "tool_shed_temperature"
	}
	if !found {
		t.Fatal("hot-spot sensor is not an input")
	}
	r.sendNum("tool_shed_temperature", 7.0) // below its 8 C cap: heating proceeds
	r.run(5 * time.Minute)
	if st, _ := r.state(); st != "on" {
		t.Fatalf("state %s", st)
	}
	mk := r.mark()
	r.sendNum("tool_shed_temperature", 8.2) // storage is cold (4.6) but the hot spot is over its cap
	r.run(time.Minute)
	cs := r.protocol(mk)
	if len(cs) != 1 || cs[0].Service != "turn_off" {
		t.Fatalf("hot spot over its cap did not switch off: %v", names(cs))
	}
	if _, a := r.state(); !strings.Contains(a["reason"].(string), "hot spot") {
		t.Fatalf("reason %v", a["reason"])
	}
	r.sendNum("tool_shed_temperature", 6.0)
	r.run(10 * time.Minute)
	if st, _ := r.state(); st != "on" {
		t.Fatalf("did not resume: %s", st)
	}
	// without the sensor configured the same reading is ignored
	r2 := liveRig(t)
	r2.sendNum("tool_shed_temperature", 9.0)
	r2.run(5 * time.Minute)
	if st, _ := r2.state(); st != "on" {
		t.Fatalf("unconfigured hot spot affected it: %s", st)
	}
	// the floor and backstop still use the storage sensor: a hot spot at 7 never blocks the backstop
	r3 := newRig(t, `{"bc_mode":"live","bc_hot_sensor":"tool_shed_temperature"}`, true)
	r3.tVal = 2.9
	r3.sendT()
	r3.sendNum("tool_shed_temperature", 7.9)
	r3.run(5 * time.Minute)
	if st, _ := r3.state(); st != "on" && st != "backstop" {
		t.Fatalf("state %s", st)
	}
}

func TestStartNowButtonRunsFullProtocol(t *testing.T) {
	r := liveRig(t)
	r.tVal = 5.4 // hysteresis: heat not wanted
	r.sendT()
	r.run(5 * time.Minute)
	if len(r.protocol(0)) != 0 {
		t.Fatalf("started without need: %v", names(r.protocol(0)))
	}
	r.m.OnButton(ButtonStartNow, r.now)
	r.run(5 * time.Minute)
	cs := r.protocol(0)
	checkCycleBeforeEveryPress(t, cs, r.m.plugEnt, r.m.botEnt)
	if count(cs, "turn_on", r.m.botEnt) != 1 {
		t.Fatalf("calls %v", names(cs))
	}
	if st, _ := r.state(); st != "on" {
		t.Fatalf("state %s", st)
	}
}

func TestStartNowWhileDisarmedDoesNotPretend(t *testing.T) {
	r := newRig(t, live, false)
	r.m.OnButton(ButtonStartNow, r.now)
	r.drain()
	r.run(5 * time.Minute)
	if len(r.protocol(0)) != 0 || r.pushes("Blanket start now not started") != 1 {
		t.Fatalf("calls %v pushes %d", names(r.protocol(0)), r.pushes("Blanket start now not started"))
	}
}
