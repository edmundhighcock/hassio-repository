package priceshed

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hwcontrol/planner"
)

var sto, _ = time.LoadLocation("Europe/Stockholm")

func at(day string, h, m, s int) time.Time {
	d, err := time.ParseInLocation("2006-01-02", day, sto)
	if err != nil {
		panic(err)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), h, m, s, 0, sto)
}

// dayPrices builds quarter-hour prices for the local day, price(hourIndex)
// per elapsed hour of the day (so DST days have 23/25 hours).
func dayPrices(day string, price func(i int) float64) []planner.Price {
	start := at(day, 0, 0, 0)
	end := at(day, 0, 0, 0).AddDate(0, 0, 1)
	end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, sto)
	var out []planner.Price
	for t, i := start, 0; t.Before(end); t, i = t.Add(15*time.Minute), i+1 {
		out = append(out, planner.Price{Start: t, Dur: 15 * time.Minute, SEK: price(i / 4)})
	}
	return out
}

func attrsFor(ps []planner.Price) map[string]json.RawMessage {
	type row struct {
		Start string  `json:"start"`
		End   string  `json:"end"`
		Value float64 `json:"value"`
	}
	var rows []row
	for _, p := range ps {
		rows = append(rows, row{p.Start.In(sto).Format(time.RFC3339), p.Start.Add(p.Dur).In(sto).Format(time.RFC3339), p.SEK})
	}
	b, _ := json.Marshal(rows)
	return map[string]json.RawMessage{"raw_today": b, "tomorrow_valid": json.RawMessage("false")}
}

// expensive: hours 7, 8, 17, 18 are the top four; the rest are cheap.
func standardPrice(i int) float64 {
	switch i {
	case 7, 8:
		return 3 + float64(i)/100
	case 17, 18:
		return 2.5 + float64(i)/100
	}
	return 1 + float64(i)/1000
}

const day = "2026-11-14"

type rig struct {
	t    *testing.T
	m    *Module
	now  time.Time
	cmds []string
	outs []modules.OutputUpdate
}

func newRig(t *testing.T, now time.Time) *rig {
	m := New()
	if err := m.Init(nil); err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, m: m, now: now}
}

func (r *rig) collect(outs []modules.OutputUpdate) {
	r.outs = append(r.outs, outs...)
	for _, e := range r.m.Effects() {
		if e.Kind == modules.EffectCallService {
			r.cmds = append(r.cmds, e.Domain+"."+e.Service)
		}
	}
}

func (r *rig) price(prices []planner.Price, replay bool) {
	r.collect(r.m.OnStateUpdate(modules.StateUpdate{EntityID: defaultPrice, Time: r.now, Available: true, State: "1", Attrs: attrsFor(prices), Replay: replay}))
}

func (r *rig) sw(state string) {
	r.collect(r.m.OnStateUpdate(modules.StateUpdate{EntityID: defaultSwitch, Time: r.now, State: state}))
}

func (r *rig) swReplay(state string) {
	r.collect(r.m.OnStateUpdate(modules.StateUpdate{EntityID: defaultSwitch, Time: r.now.Add(-time.Hour), State: state, Replay: true}))
}

func (r *rig) link(up bool) { r.collect(r.m.OnLink(up, r.now)) }

// run ticks every 5 s until to.
func (r *rig) run(to time.Time) {
	for r.now.Before(to) {
		r.now = r.now.Add(5 * time.Second)
		r.collect(r.m.OnTick(r.now))
	}
}

func (r *rig) goTo(h, m, s int) { r.run(at(day, h, m, s)) }

func (r *rig) wantCmds(want ...string) {
	r.t.Helper()
	if strings.Join(r.cmds, ",") != strings.Join(want, ",") {
		r.t.Fatalf("commands = %v, want %v", r.cmds, want)
	}
}

func (r *rig) state() string {
	var s string
	for _, o := range r.outs {
		s = o.StateString
	}
	return s
}

// started returns a rig at 06:00 with prices, switch on, link up and the
// start assertion done.
func started(t *testing.T) *rig {
	r := newRig(t, at(day, 5, 59, 0))
	r.price(dayPrices(day, standardPrice), true)
	r.swReplay("on")
	r.sw("on")
	r.link(true)
	r.goTo(6, 0, 0)
	r.wantCmds() // already on, on period
	return r
}

func TestRankOffHours(t *testing.T) {
	flat := func(i int) float64 { return 1 }
	cases := []struct {
		name   string
		day    string
		price  func(i int) float64
		n      int
		want   []string // local HH:00 labels, ascending
		wantOK bool
	}{
		{"top4", day, standardPrice, 4, []string{"07:00", "08:00", "17:00", "18:00"}, true},
		{"n0", day, standardPrice, 0, nil, true},
		{"n1", day, standardPrice, 1, []string{"08:00"}, true},
		{"ties earlier hour wins", day, flat, 4, []string{"00:00", "01:00", "02:00", "03:00"}, true},
		{"partial tie", day, func(i int) float64 {
			if i == 10 || i == 12 || i == 14 {
				return 5
			}
			return 1
		}, 2, []string{"10:00", "12:00"}, true},
		{"spring forward 23h", "2026-03-29", func(i int) float64 { return float64(i) }, 4, []string{"20:00", "21:00", "22:00", "23:00"}, true},
		{"fall back 25h", "2026-10-25", func(i int) float64 { return float64(i) }, 4, []string{"20:00", "21:00", "22:00", "23:00"}, true},
		{"fall back repeated 02:00", "2026-10-25", func(i int) float64 {
			if i == 2 || i == 3 { // both local 02:00 hours
				return 100
			}
			return 1
		}, 2, []string{"02:00", "02:00"}, true},
		{"more hours than exist", day, standardPrice, 30, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := dayPrices(c.day, c.price)
			hs, ok := RankOffHours(ps, at(c.day, 12, 0, 0), sto, c.n)
			if ok != c.wantOK {
				t.Fatalf("known=%v", ok)
			}
			var got []string
			for _, h := range hs {
				got = append(got, time.Unix(h, 0).In(sto).Format("15:00"))
			}
			if c.name == "more hours than exist" {
				if len(got) != 24 {
					t.Fatalf("got %d hours, want all 24", len(got))
				}
				return
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
	if _, ok := RankOffHours(nil, at(day, 1, 0, 0), sto, 4); ok {
		t.Fatal("no prices must be unknown")
	}
	// Prices for another day do not count.
	if _, ok := RankOffHours(dayPrices("2026-11-15", standardPrice), at(day, 1, 0, 0), sto, 4); ok {
		t.Fatal("tomorrow's prices must not count for today")
	}
	// Hourly (not quarter-hour) prices work too.
	var hourly []planner.Price
	for i := 0; i < 24; i++ {
		hourly = append(hourly, planner.Price{Start: at(day, i, 0, 0), Dur: time.Hour, SEK: standardPrice(i)})
	}
	if hs, _ := RankOffHours(hourly, at(day, 1, 0, 0), sto, 4); len(hs) != 4 {
		t.Fatalf("hourly: %v", hs)
	}
}

func TestTransitionsCommandExactlyOnce(t *testing.T) {
	r := started(t)
	r.goTo(6, 59, 55)
	r.wantCmds()
	r.goTo(7, 0, 10)
	r.wantCmds("switch.turn_off")
	r.sw("off")
	r.goTo(8, 59, 55) // 08:00 is still off: no new command inside the run
	r.wantCmds("switch.turn_off")
	if r.state() != "off_period" {
		t.Fatalf("state %q", r.state())
	}
	r.goTo(9, 0, 10)
	r.wantCmds("switch.turn_off", "switch.turn_on")
	r.sw("on")
	r.goTo(17, 0, 10)
	r.wantCmds("switch.turn_off", "switch.turn_on", "switch.turn_off")
	r.sw("off")
	r.goTo(19, 0, 10)
	r.wantCmds("switch.turn_off", "switch.turn_on", "switch.turn_off", "switch.turn_on")
	r.sw("on")
	r.run(at(day, 23, 59, 0))
	if len(r.cmds) != 4 || r.state() != "on" {
		t.Fatalf("cmds %v state %q", r.cmds, r.state())
	}
}

func TestOutputAttributes(t *testing.T) {
	r := started(t)
	var last modules.OutputUpdate
	for _, o := range r.outs {
		last = o
	}
	if last.ObjectID != "price_shed_tool_shed_lights" || last.StateString != "on" {
		t.Fatalf("%+v", last)
	}
	var a map[string]any
	if err := json.Unmarshal([]byte(last.AttributesJSON), &a); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(a["off_hours"]) != "[07:00 08:00 17:00 18:00]" {
		t.Fatalf("off_hours %v", a["off_hours"])
	}
	if a["next_change"] != at(day, 7, 0, 0).Format(time.RFC3339) {
		t.Fatalf("next_change %v", a["next_change"])
	}
	r.goTo(7, 0, 10)
	if r.state() != "off_period" {
		t.Fatal(r.state())
	}
	last = r.outs[len(r.outs)-1]
	json.Unmarshal([]byte(last.AttributesJSON), &a)
	if a["last_command"] != "turn_off" || a["next_change"] != at(day, 9, 0, 0).Format(time.RFC3339) {
		t.Fatalf("attrs %v", a)
	}
}

func TestNoPricesStaysOn(t *testing.T) {
	r := newRig(t, at(day, 7, 30, 0)) // would be an off hour
	r.sw("off")                       // e.g. restored by the plug
	r.link(true)
	r.run(at(day, 7, 30, 50))
	r.wantCmds() // still waiting for prices
	r.run(at(day, 7, 31, 10))
	r.wantCmds("switch.turn_on") // a short wait passed, no prices: ON
	if r.state() != "no_prices" {
		t.Fatalf("state %q", r.state())
	}
	r.sw("on")
	r.goTo(9, 0, 0)
	r.wantCmds("switch.turn_on")
}

func TestManualOverrideKeptUntilOnPeriod(t *testing.T) {
	r := started(t)
	r.goTo(7, 0, 10)
	r.wantCmds("switch.turn_off")
	r.sw("off")
	r.goTo(7, 30, 0)
	r.sw("on") // a person
	if r.state() != "override" {
		t.Fatalf("state %q", r.state())
	}
	r.goTo(8, 59, 55)
	r.wantCmds("switch.turn_off") // no second turn_off at 08:00, none while overridden
	var a map[string]any
	json.Unmarshal([]byte(r.outs[len(r.outs)-1].AttributesJSON), &a)
	if a["override_until"] != at(day, 9, 0, 0).Format(time.RFC3339) {
		t.Fatalf("override_until %v", a["override_until"])
	}
	r.goTo(9, 0, 10)
	r.wantCmds("switch.turn_off", "switch.turn_on") // on period start clears it and commands on
	if r.state() != "on" {
		t.Fatalf("state %q", r.state())
	}
	r.goTo(16, 59, 0)
	r.wantCmds("switch.turn_off", "switch.turn_on") // no turn_off in the on period
	r.goTo(17, 0, 10)
	r.wantCmds("switch.turn_off", "switch.turn_on", "switch.turn_off") // next off period acts again
}

func TestManualOffOrOnInOnPeriodLeftAlone(t *testing.T) {
	r := started(t)
	r.goTo(10, 0, 0)
	r.cmds = nil // the 07:00/09:00 transitions are not under test here
	r.sw("off")  // a person turns them off in an on period
	r.goTo(10, 30, 0)
	r.sw("on")
	r.goTo(16, 0, 0)
	r.wantCmds()
	if r.state() != "on" {
		t.Fatal(r.state())
	}
}

func TestOwnCommandBounceNotManual(t *testing.T) {
	r := started(t)
	r.goTo(7, 0, 10)
	r.wantCmds("switch.turn_off")
	cmdAt := r.m.loads[0].lastCmdAt
	// stale ON reported 156 ms after the command, and again 20 s later
	r.now = cmdAt.Add(156 * time.Millisecond)
	r.sw("off")
	r.now = cmdAt.Add(160 * time.Millisecond)
	r.sw("on")
	r.now = cmdAt.Add(170 * time.Millisecond)
	r.sw("off")
	r.now = cmdAt.Add(20 * time.Second)
	r.sw("on")
	if r.state() == "override" {
		t.Fatal("bounce inside the grace window taken for a person")
	}
	r.now = cmdAt.Add(21 * time.Second)
	r.sw("off")
	// the same flip well after the grace window IS a person
	r.now = cmdAt.Add(Grace + 10*time.Second)
	r.sw("on")
	if r.state() != "override" {
		t.Fatalf("state %q", r.state())
	}
}

func TestReassertOnReturnFromUnavailable(t *testing.T) {
	r := started(t)
	r.goTo(7, 0, 10)
	r.sw("off")
	r.goTo(7, 30, 0)
	r.sw("unavailable")
	r.goTo(7, 31, 0)
	r.wantCmds("switch.turn_off") // nothing while unavailable
	r.sw("on")                    // back, remembering nothing: forced on
	r.wantCmds("switch.turn_off", "switch.turn_off")
	if r.state() == "override" {
		t.Fatal("return from unavailable is not a person")
	}
	// in an on period: back as off -> turn_on
	r.sw("off")
	r.goTo(10, 0, 0)
	r.wantCmds("switch.turn_off", "switch.turn_off", "switch.turn_on")
	r.sw("on")
	r.sw("unknown")
	r.sw("off")
	r.wantCmds("switch.turn_off", "switch.turn_off", "switch.turn_on", "switch.turn_on")
}

func TestNoReassertDuringOverride(t *testing.T) {
	r := started(t)
	r.goTo(7, 0, 10)
	r.sw("off")
	r.goTo(7, 30, 0)
	r.sw("on")
	r.sw("unavailable")
	r.sw("on")
	r.goTo(8, 0, 0)
	r.wantCmds("switch.turn_off")
}

func TestStartAssertion(t *testing.T) {
	// start in an off period with the lights on -> turn_off after link up
	r := newRig(t, at(day, 7, 30, 0))
	r.price(dayPrices(day, standardPrice), true)
	r.swReplay("on")
	r.sw("on")
	r.run(at(day, 7, 31, 0))
	r.wantCmds() // link not up yet: nothing
	r.link(true)
	r.run(at(day, 7, 31, 10))
	r.wantCmds("switch.turn_off")
	// failed command is retried
	r.collect(r.m.OnCommandResult(modules.CommandResult{CorrID: r.m.loads[0].corr, OK: false, Err: "boom", At: r.now}))
	r.run(r.now.Add(retryEvery + 10*time.Second))
	r.wantCmds("switch.turn_off", "switch.turn_off")
	// start in an off period with the lights already off: no command
	r = newRig(t, at(day, 7, 30, 0))
	r.price(dayPrices(day, standardPrice), true)
	r.sw("off")
	r.link(true)
	r.run(at(day, 7, 32, 0))
	r.wantCmds()
}

func TestStartKeepsPersistedOverride(t *testing.T) {
	r := started(t)
	r.goTo(7, 0, 10)
	r.sw("off")
	r.goTo(7, 30, 0)
	r.sw("on")
	b, err := r.m.Persist()
	if err != nil {
		t.Fatal(err)
	}
	// restart at 07:40 with prices seeded from replay
	r2 := newRig(t, at(day, 7, 40, 0))
	if err := r2.m.Init(b); err != nil {
		t.Fatal(err)
	}
	r2.price(dayPrices(day, standardPrice), true)
	r2.sw("on")
	r2.link(true)
	r2.run(at(day, 7, 45, 0))
	r2.wantCmds()
	if r2.state() != "override" {
		t.Fatalf("state %q", r2.state())
	}
	// the override ends with the off period
	r2.goTo(9, 0, 10)
	r2.wantCmds("switch.turn_on")
}

func TestReplayNeverCommands(t *testing.T) {
	r := newRig(t, at(day, 7, 30, 0))
	r.link(true)
	r.price(dayPrices(day, standardPrice), true)
	r.swReplay("off")
	r.swReplay("on") // would read as a manual press if it counted
	r.swReplay("unavailable")
	r.swReplay("on")
	r.wantCmds()
	if r.m.loads[0].overrideActive(r.now) {
		t.Fatal("override from replay")
	}
	if r.m.loads[0].sw != "on" {
		t.Fatal("replay should still track the switch state")
	}
}

func TestPersistenceRoundTripAndSavedPlan(t *testing.T) {
	r := started(t)
	r.goTo(7, 0, 10)
	r.sw("off")
	r.goTo(7, 30, 0)
	r.sw("on")
	b, err := r.m.Persist()
	if err != nil {
		t.Fatal(err)
	}
	m2 := New()
	if err := m2.Init(b); err != nil {
		t.Fatal(err)
	}
	l, l2 := r.m.loads[0], m2.loads[0]
	if !l2.overrideUntil.Equal(l.overrideUntil) || l2.lastCmd != "turn_off" || !l2.lastCmdAt.Equal(l.lastCmdAt) {
		t.Fatalf("round trip: %+v vs %+v", l2, l)
	}
	b2, _ := m2.Persist()
	var p persisted
	if err := json.Unmarshal(b2, &p); err != nil || p.Loads[defaultSwitch].PlanDay != day || len(p.Loads[defaultSwitch].PlanOffHours) != 4 {
		t.Fatalf("saved plan lost: %s (%v)", b2, err)
	}
	if err := New().Init([]byte("not json")); err != nil {
		t.Fatal("corrupt state must not fail Init")
	}
	// restart with no prices anywhere: the saved plan still holds the off hours
	r3 := newRig(t, at(day, 7, 30, 0))
	if err := r3.m.Init(b2); err != nil {
		t.Fatal(err)
	}
	r3.m.loads[0].overrideUntil = time.Time{}
	r3.sw("on")
	r3.link(true)
	r3.run(at(day, 7, 32, 0))
	r3.wantCmds("switch.turn_off")
}

func TestReplanButton(t *testing.T) {
	r := started(t)
	// Prices move so that 12:00 is now an off hour; prices update live at 11:59.
	r.goTo(11, 59, 0)
	cheap := func(i int) float64 {
		if i == 12 || i == 13 || i == 14 || i == 15 {
			return 9
		}
		return 1
	}
	r.m.prices = dayPrices(day, cheap) // as if a live update had been parsed, without re-evaluating
	r.sw("on")
	r.cmds = nil
	r.goTo(11, 59, 30)
	r.wantCmds() // cached plan still in force
	r.goTo(12, 0, 10)
	r.wantCmds()
	r.collect(r.m.OnButton(ButtonReplan, r.now))
	r.wantCmds("switch.turn_off") // now an off hour: transition rule applies
	// an unchanged plan does nothing
	r.collect(r.m.OnButton(ButtonReplan, r.now))
	r.wantCmds("switch.turn_off")
	r.collect(r.m.OnButton("other", r.now))
	r.wantCmds("switch.turn_off")
}

func TestConfigure(t *testing.T) {
	m := New()
	if !m.enabled || m.offHours != 4 || m.loads[0].entity != "switch.tool_shed_lights" {
		t.Fatal("defaults")
	}
	if err := m.Configure([]byte(`{"ps_switch":"switch.x_y","ps_off_hours":6,"hwc_price_entity":"sensor.p"}`)); err != nil {
		t.Fatal(err)
	}
	if m.offHours != 6 || m.loads[0].object != "price_shed_x_y" || m.priceEnt != "sensor.p" {
		t.Fatalf("%+v", m)
	}
	m = New()
	m.Configure([]byte(`{"ps_off_hours":40}`))
	if m.offHours != 4 {
		t.Fatal("out of range falls back")
	}
	m = New()
	m.Configure([]byte(`{"ps_enabled":false}`))
	if len(m.Inputs()) != 0 || len(m.Outputs()) != 0 || len(m.Buttons()) != 0 {
		t.Fatal("disabled module must be inert")
	}
	if outs := m.OnTick(at(day, 7, 0, 0)); outs != nil || len(m.Effects()) != 0 {
		t.Fatal("disabled module acted")
	}
	// Specs of the enabled module.
	m = New()
	o := m.Outputs()
	if len(o) != 1 || o[0].ObjectID != "price_shed_tool_shed_lights" || !o[0].HasAttributes {
		t.Fatalf("%+v", o)
	}
	if b := m.Buttons(); len(b) != 1 || b[0].ObjectID != "replan_price_shed" {
		t.Fatalf("%+v", b)
	}
	ids := map[string]bool{}
	for _, in := range m.Inputs() {
		ids[in.EntityID] = true
	}
	if !ids["switch.tool_shed_lights"] || !ids["sensor.nordpool_kwh_se3_sek_3_06_0"] {
		t.Fatalf("%v", ids)
	}
}

func TestDSTDayTransitions(t *testing.T) {
	// Fall-back day 2026-10-25: the repeated 02:00 hour is the most expensive.
	d := "2026-10-25"
	r := newRig(t, at(d, 0, 0, 0).Add(-time.Minute))
	r.m.setSwitch(defaultSwitch)
	r.price(dayPrices(d, func(i int) float64 {
		if i == 2 || i == 3 {
			return 100
		}
		return 1 + float64(i)/1000
	}), true)
	r.sw("on")
	r.link(true)
	r.run(r.now.Add(10 * time.Minute))
	r.wantCmds()
	// one off period spanning both 02:00 hours (two elapsed hours), one command
	r.run(at(d, 0, 0, 0).Add(2*time.Hour + 10*time.Second))
	r.wantCmds("switch.turn_off")
	r.run(at(d, 0, 0, 0).Add(4*time.Hour + 10*time.Second))
	r.wantCmds("switch.turn_off", "switch.turn_on")
}
