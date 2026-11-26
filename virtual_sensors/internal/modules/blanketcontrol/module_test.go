package blanketcontrol

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"virtual_sensors/internal/haws"
	"virtual_sensors/internal/host"
	"virtual_sensors/internal/modules"
)

func TestShadowSendsNothing(t *testing.T) {
	r := newRig(t, `{}`, true) // shadow mode, armed key on
	r.tVal = 2.9               // frost: it would heat
	r.sendT()
	r.run(3 * time.Hour)
	if cs := r.since(0); len(cs) != 0 {
		t.Fatalf("shadow sent %v", names(cs))
	}
	st, a := r.state()
	if st != "shadow" || a["would_state"] != "backstop" {
		t.Fatalf("state %s %v", st, a)
	}
	// a start-now press in shadow starts nothing
	r.m.OnButton(ButtonStartNow, r.now)
	r.run(10 * time.Minute)
	if len(r.protocol(0)) != 0 {
		t.Fatalf("shadow start now: %v", names(r.protocol(0)))
	}
}

func TestDisarmedSendsNothing(t *testing.T) {
	r := newRig(t, live, false) // live mode, key off
	r.tVal = 2.9
	r.sendT()
	r.run(3 * time.Hour)
	if cs := r.since(0); len(cs) != 0 {
		t.Fatalf("disarmed sent %v", names(cs))
	}
	if st, a := r.state(); st != "disarmed" || a["would_state"] != "backstop" {
		t.Fatalf("state %s %v", st, a)
	}
	// arming starts it, with one push
	r.send(ArmedEntity, "on", 0)
	r.run(10 * time.Minute)
	if count(r.protocol(0), "turn_on", r.m.botEnt) != 1 || r.pushes("Blanket controller armed") != 1 {
		t.Fatalf("after arming: %v pushes %d", names(r.protocol(0)), r.pushes("Blanket controller armed"))
	}
	// disarm: nothing more is commanded
	r.send(ArmedEntity, "off", 0)
	mk := r.mark()
	r.run(3 * time.Hour)
	for _, c := range r.since(mk) {
		if c.Domain != "logbook" {
			t.Fatalf("call after disarm: %v", names(r.since(mk)))
		}
	}
}

func TestDeadmanCadence(t *testing.T) {
	r := liveRig(t)
	r.run(3*time.Hour + time.Minute)
	var ts []time.Time
	for _, c := range r.calls {
		if c.Domain == "timer" {
			if c.Service != "start" || c.Entity != DeadmanEntity || c.Data["duration"] != "00:31:00" {
				t.Fatalf("dead-man call %+v", c)
			}
			ts = append(ts, c.At)
		}
	}
	if len(ts) != 7 {
		t.Fatalf("%d postponements in 3 h: %v", len(ts), ts)
	}
	if ts[0].Sub(at(26, 3, 0)) > 10*time.Second {
		t.Fatalf("first postponement not at arm: %v", ts[0])
	}
	for i := 1; i < len(ts); i++ {
		if d := ts[i].Sub(ts[i-1]); d < 30*time.Minute || d > 31*time.Minute {
			t.Fatalf("gap %v", d)
		}
	}
	// FAILED is not healthy: the timer is left to run out
	r.pressLost = func(int) bool { return true }
	r.setBlanket(false)
	r.m.x.on, r.m.x.failed = false, true
	n := len(ts)
	mk := r.mark()
	r.run(2 * time.Hour)
	k := 0
	for _, c := range r.calls[mk:] {
		if c.Domain == "timer" {
			k++
		}
	}
	if n != 7 || k != 0 {
		t.Fatalf("postponed while failed: %d", k)
	}
}

func TestDeadmanNotSentInShadowOrDisarmed(t *testing.T) {
	for _, r := range []*rig{newRig(t, `{}`, true), newRig(t, live, false)} {
		r.run(2 * time.Hour)
		for _, c := range r.calls {
			if c.Domain == "timer" {
				t.Fatal("dead-man postponed while not armed+live")
			}
		}
	}
}

func TestNoPricesHysteresisNeverOffBelowFloor(t *testing.T) {
	m := New()
	now := at(26, 3, 0)
	m.bootAt = now
	check := func(T float64, wantOn bool) {
		t.Helper()
		m.t = reading{v: T, ok: true, at: now, seen: true}
		if d := m.desire(now); d.On != wantOn {
			t.Fatalf("T=%.1f: on=%v want %v (%s)", T, d.On, wantOn, d.Reason)
		}
	}
	check(3.9, true)  // below the floor
	check(4.9, true)  // below floor+1
	check(5.2, true)  // hysteresis: stays on until floor+1.5
	check(5.6, false) // released
	check(5.2, false) // and stays off in the band
	check(4.8, true)
	check(6.0, false) // hard cap
}

func TestMagnusAndRHAtReference(t *testing.T) {
	if d := DewPoint(20, 50); math.Abs(d-9.27) > 0.1 {
		t.Fatalf("dew point %v", d)
	}
	if a := AbsHumidity(20, 50); math.Abs(a-8.65) > 0.1 {
		t.Fatalf("abs humidity %v", a)
	}
	// the same air at 5 C reads far wetter than at 20 C
	if v := RHAt(20, 50, 5); math.Abs(v-134) > 3 {
		t.Fatalf("rh at 5 C %v", v)
	}
	// heating 1 K with no moisture lost drops raw RH by ~5 points but not RH at reference
	rh1 := 80.0
	rh2 := 100 * satPressure(4) / satPressure(5) * rh1 / 100 // raw RH after warming 4 -> 5 would drop...
	_ = rh2
	a := RHAt(4, 80, 5)
	b := RHAt(5, 80*satPressure(4)/satPressure(5), 5) // the same air warmed by 1 K
	if math.Abs(a-b) > 1e-9 {
		t.Fatalf("RH at reference changed by heating: %v vs %v", a, b)
	}
	if raw := 80 * satPressure(4) / satPressure(5); 80-raw < 3 {
		t.Fatalf("raw RH should fall several points per K: %v", 80-raw)
	}
	// dew point is the temperature where RH reaches 100
	if v := RHAt(20, 50, DewPoint(20, 50)); math.Abs(v-100) > 0.01 {
		t.Fatalf("rh at dew point %v", v)
	}
}

func (r *rig) humidity(rh, shed float64) {
	r.sendNum(r.m.humEnt, rh)
	r.sendNum(r.m.coldEnt, shed)
}

// holdHum keeps humidity/shed readings fresh while the clock runs.
func (r *rig) holdHum(d time.Duration, rh, shed float64) {
	end := r.now.Add(d)
	for r.now.Before(end) {
		r.humidity(rh, shed)
		r.run(10 * time.Minute)
	}
}

func TestHumidityAlarmsOncePerEpisodeAndRecover(t *testing.T) {
	// dry
	r := liveRig(t)
	r.holdHum(5*time.Hour+30*time.Minute, 45, 4.0)
	if r.pushes("Dahlia tubers may be too dry") != 0 {
		t.Fatal("dry alarm before 6 h")
	}
	r.holdHum(time.Hour, 45, 4.0)
	r.holdHum(3*time.Hour, 45, 4.0)
	if r.pushes("Dahlia tubers may be too dry") != 1 || r.logbooks("Dahlia tubers may be too dry") != 1 {
		t.Fatalf("dry pushes %d logbook %d", r.pushes("Dahlia tubers may be too dry"), r.logbooks("Dahlia tubers may be too dry"))
	}
	r.holdHum(20*time.Minute, 75, 4.0)
	if r.pushes("Dahlia tubers may be too dry: recovered") != 1 {
		t.Fatal("no recovery push")
	}
	// wet
	r2 := liveRig(t)
	r2.holdHum(7*time.Hour, 99.5, 3.0)
	if r2.pushes("Dahlia storage too humid") != 1 {
		t.Fatalf("wet pushes %d", r2.pushes("Dahlia storage too humid"))
	}
	// raw RH alone does not alarm: 99 % raw at a warm-ish 8 C... ref at 5 C is higher still, but 70 raw at 4.6 stays quiet
	r3 := liveRig(t)
	r3.holdHum(8*time.Hour, 80, 4.0)
	for _, n := range []string{"Dahlia storage too humid", "Dahlia tubers may be too dry"} {
		if r3.pushes(n) != 0 {
			t.Fatalf("%s fired in the band", n)
		}
	}
}

func TestCondensationAlarmAfterOneHour(t *testing.T) {
	r := liveRig(t)
	// storage 4.6 C at 92 % RH: dew point ~3.4 C; shed air at 3.6 C is within 0.5 C of it
	r.holdHum(50*time.Minute, 92, 3.6)
	if r.pushes("Dahlia storage: condensation risk") != 0 {
		t.Fatal("alarm before 1 h")
	}
	r.holdHum(20*time.Minute, 92, 3.6)
	if r.pushes("Dahlia storage: condensation risk") != 1 || r.logbooks("Dahlia storage: condensation risk") != 1 {
		t.Fatalf("pushes %d", r.pushes("Dahlia storage: condensation risk"))
	}
	r.holdHum(2*time.Hour, 92, 3.6)
	if r.pushes("Dahlia storage: condensation risk") != 1 {
		t.Fatal("repeated within an episode")
	}
	r.holdHum(20*time.Minute, 92, 9.0) // shed warms up
	if r.pushes("Dahlia storage: condensation risk: recovered") != 1 {
		t.Fatal("no recovery")
	}
	// published humidity entity carries RH at reference, raw RH, dew point, absolute humidity
	var h modules.OutputUpdate
	for _, o := range r.m.publishAll(r.now, true) {
		if o.ObjectID == ObjHumidity {
			h = o
		}
	}
	var a map[string]any
	_ = json.Unmarshal([]byte(h.AttributesJSON), &a)
	for _, k := range []string{"rh_raw_percent", "rh_at_reference_percent", "dew_point_c", "abs_humidity_g_m3", "alarm_condensation"} {
		if _, ok := a[k]; !ok {
			t.Fatalf("missing %s in %v", k, a)
		}
	}
	if a["rh_raw_percent"].(float64) != 92 {
		t.Fatalf("raw RH %v", a["rh_raw_percent"])
	}
}

func TestTemperatureAlarms(t *testing.T) {
	r := liveRig(t)
	// too warm for 30 min (above bc_alarm_high_c 8.0); the cap keeps the blanket off
	r.tVal = 8.6
	r.sendT()
	r.run(20 * time.Minute)
	if r.pushes("Dahlia storage too warm") != 0 {
		t.Fatal("alarm early")
	}
	r.run(20 * time.Minute)
	if r.pushes("Dahlia storage too warm") != 1 || r.logbooks("Dahlia storage too warm") != 1 {
		t.Fatalf("high pushes %d", r.pushes("Dahlia storage too warm"))
	}
	if len(r.protocol(0)) != 0 {
		t.Fatalf("heated above the cap: %v", names(r.protocol(0)))
	}
	r.tVal = 4.6
	r.sendT()
	r.run(time.Minute)
	if r.pushes("Dahlia storage too warm: recovered") != 1 {
		t.Fatal("no recovery push")
	}
	// too cold for 30 min while heating
	r2 := liveRig(t)
	r2.tVal = 2.5
	r2.sendT()
	r2.run(20 * time.Minute)
	if r2.pushes("Dahlia storage too cold") != 0 {
		t.Fatal("alarm early")
	}
	r2.run(20 * time.Minute)
	if r2.pushes("Dahlia storage too cold") != 1 {
		t.Fatalf("low pushes %d", r2.pushes("Dahlia storage too cold"))
	}
	// the hot-spot sensor has its own alarm limit (10 C)
	r3 := newRig(t, `{"bc_mode":"live","bc_hot_sensor":"tool_shed_temperature"}`, true)
	r3.sendNum("tool_shed_temperature", 9.0) // over the 8 C cap, below the 10 C alarm
	r3.run(40 * time.Minute)
	if r3.pushes("Dahlia storage too warm") != 0 {
		t.Fatal("hot alarm below its limit")
	}
	for i := 0; i < 5; i++ {
		r3.sendNum("tool_shed_temperature", 10.5)
		r3.run(10 * time.Minute)
	}
	if r3.pushes("Dahlia storage too warm") != 1 {
		t.Fatalf("hot alarm pushes %d", r3.pushes("Dahlia storage too warm"))
	}
}

func TestAlarmsStaySilentWhenNotArmedLive(t *testing.T) {
	r := newRig(t, `{}`, true)
	r.tVal = 8.6
	r.sendT()
	r.run(2 * time.Hour)
	if len(r.since(0)) != 0 {
		t.Fatalf("calls in shadow: %v", names(r.since(0)))
	}
	if _, a := r.state(); len(a["alarms"].([]any)) != 1 {
		t.Fatalf("alarm state not shown: %v", a["alarms"])
	}
}

func TestConfigDefaultsAndRanges(t *testing.T) {
	m := New()
	if err := m.Configure([]byte(`{"modules":["hot_water"],"log_level":"info","hwc_mode":"shadow","mqtt_port":1883}`)); err != nil {
		t.Fatal(err)
	}
	if m.live || m.p.FloorC != 4 || m.backstop != 3 || m.p.TargetC != 4.5 || m.p.MaxC != 6 || m.alarmHigh != 8 ||
		m.hotMax != 8 || m.hotAlarm != 10 || m.hotEnt != "" || m.confirm != 150*time.Second || m.onW != 30 ||
		m.tries != 3 || m.p.MinOnSlots != 4 || m.p.MaxStartsDay != 12 || m.rhRef != 5 || m.rhLow != 60 || m.rhHi != 95 {
		t.Fatalf("defaults %+v %+v", m.p, m)
	}
	if m.coldEnt != "outdoor_temperature_best" || m.hotEnt != "" {
		t.Fatalf("cold-surface/hot defaults %q %q", m.coldEnt, m.hotEnt)
	}
	for _, in := range m.Inputs() {
		if in.EntityID == "tool_shed_temperature" {
			t.Fatal("sensor.tool_shed_temperature is an input by default")
		}
	}
	if m.tempEnt != "dahlia_storage_temperature" || m.plugEnt != "switch.tool_shed_heater" || m.botEnt != "switch.dahlia_blanket_bot" || m.fcastEnt != "weather.smhi_home" {
		t.Fatal("entity defaults")
	}
	m2 := New()
	m2.Configure([]byte(`{"bc_mode":"live","bc_floor_c":99,"bc_max_c":1,"bc_start_tries":0,"bc_confirm_s":5,"bc_max_starts_day":500}`))
	if !m2.live || m2.p.FloorC != 4 || m2.p.MaxC != 6 || m2.tries != 3 || m2.confirm != 150*time.Second || m2.p.MaxStartsDay != 12 {
		t.Fatalf("range fallback %+v", m2.p)
	}
	m3 := New()
	m3.Configure([]byte(`{"bc_floor_c":5,"bc_target_c":6,"bc_max_c":8,"bc_backstop_c":3.5,"bc_alarm_high_c":10,"bc_min_on_min":90,"bc_temp_entity":"sensor.other"}`))
	if m3.p.FloorC != 5 || m3.p.TargetC != 6 || m3.p.MaxC != 8 || m3.backstop != 3.5 || m3.p.MinOnSlots != 6 || m3.tempEnt != "other" {
		t.Fatalf("custom %+v", m3.p)
	}
}

func TestPersistRoundTrip(t *testing.T) {
	r := liveRig(t)
	r.run(3 * time.Minute)
	b, err := r.m.Persist()
	if err != nil {
		t.Fatal(err)
	}
	m2 := New()
	m2.Configure([]byte(live))
	if err := m2.Init(b); err != nil {
		t.Fatal(err)
	}
	if !m2.x.on || !m2.x.windowEnd.Equal(r.m.x.windowEnd) || m2.x.st.LastKind != "start" {
		t.Fatalf("restored %+v", m2.x)
	}
	if err := New().Init([]byte("not json")); err != nil {
		t.Fatal("bad state must start fresh")
	}
}

func TestPlanPublishedAndUsedThroughModule(t *testing.T) {
	r := liveRig(t)
	r.m.p.StartPenaltyKr = 0.02
	r.sendNum(r.m.outEnt, 3.0)
	pr := rawPrices(at(26, 0, 0), at(28, 0, 0), func(h int) float64 {
		if h >= 17 && h < 21 {
			return 3
		}
		return 0.3
	})
	r.outs = append(r.outs, r.m.OnStateUpdate(modules.StateUpdate{EntityID: r.m.priceEnt, Time: r.now, State: "1", Value: 1, Available: true, Attrs: pr})...)
	r.tVal = 5.0
	r.sendT()
	r.run(20 * time.Minute)
	if !r.m.havePlan {
		t.Fatal("no plan")
	}
	var plan modules.OutputUpdate
	for _, o := range r.m.publishAll(r.now, true) {
		if o.ObjectID == ObjPlan {
			plan = o
		}
	}
	var a struct {
		Slots  [][]any  `json:"slots"`
		Fields []string `json:"slot_fields"`
	}
	if err := json.Unmarshal([]byte(plan.AttributesJSON), &a); err != nil {
		t.Fatal(err)
	}
	if strings.Join(a.Fields, ",") != "start,on,price_kr_per_kwh,t_pred" || len(a.Slots) < 96 || len(a.Slots[0]) != 4 {
		t.Fatalf("plan attrs: %d slots, fields %v", len(a.Slots), a.Fields)
	}
	if _, a2 := r.state(); a2["desire_kind"] != "plan" {
		t.Fatalf("desire %v", a2["desire_kind"])
	}
	// model output carries U, H, G and the error
	for _, o := range r.m.publishAll(r.now, true) {
		if o.ObjectID == ObjModel {
			var ma map[string]any
			_ = json.Unmarshal([]byte(o.AttributesJSON), &ma)
			for _, k := range []string{"u_per_h", "h_k_per_h", "g_k_per_h", "mae_c", "n", "fitted"} {
				if _, ok := ma[k]; !ok {
					t.Fatalf("model attrs missing %s", k)
				}
			}
		}
	}
}

// ---- host routing ----

type hClock struct{ t time.Time }

func (c *hClock) Now() time.Time { return c.t }

type hPub struct{ outs []modules.OutputUpdate }

func (p *hPub) PublishState(u modules.OutputUpdate) error { p.outs = append(p.outs, u); return nil }

type hHA struct {
	calls   []string
	targets []string
	n       int
}

func (f *hHA) CallService(domain, service string, data, target map[string]any) (int, error) {
	f.calls = append(f.calls, domain+"."+service)
	e := ""
	if target != nil {
		e, _ = target["entity_id"].(string)
	}
	f.targets = append(f.targets, e)
	f.n++
	return f.n, nil
}

// The blanket start through the host: inputs arrive as FULL entity ids (the
// host strips "sensor."), commands leave as service calls with the entity as target.
func TestHostRoutedStart(t *testing.T) {
	m := New()
	if err := m.Configure([]byte(live)); err != nil {
		t.Fatal(err)
	}
	m.Init(nil)
	clk := &hClock{t: at(26, 3, 0)}
	pub := &hPub{}
	h := host.New([]modules.Module{m}, pub, clk, "")
	ha := &hHA{}
	h.LinkUp(ha)
	send := func(full, state string) {
		if !h.Match(full) {
			t.Fatalf("host does not route %s", full)
		}
		h.HandleState(haws.StateEvent{EntityID: full, Time: clk.t, State: state}, false)
	}
	send("input_boolean.blanket_controller_armed", "on")
	send("switch.tool_shed_heater", "off")
	send("sensor.tool_shed_heater_power", "0")
	send("sensor.dahlia_storage_temperature", "4.6")
	send("sensor.dahlia_storage_humidity", "85")
	send("sensor.outdoor_temperature_best", "-1")
	answered := 0
	var pressAt time.Time
	for i := 0; i < 12*60/5; i++ {
		clk.t = clk.t.Add(5 * time.Second)
		h.Tick()
		for ; answered < ha.n; answered++ {
			h.HandleResult(haws.Result{ID: answered + 1, Success: true})
			switch ha.calls[answered] + ":" + ha.targets[answered] {
			case "switch.turn_off:switch.tool_shed_heater":
				send("switch.tool_shed_heater", "off")
			case "switch.turn_on:switch.tool_shed_heater":
				send("switch.tool_shed_heater", "on")
			case "switch.turn_on:switch.dahlia_blanket_bot":
				pressAt = clk.t.Add(25 * time.Second)
			}
		}
		if !pressAt.IsZero() && !clk.t.Before(pressAt) {
			pressAt = time.Time{}
			send("sensor.tool_shed_heater_power", "69.5")
		}
		if i%100 == 0 {
			send("sensor.dahlia_storage_temperature", "4.6")
		}
	}
	var proto []string
	for i, c := range ha.calls {
		if c != "timer.start" && !strings.HasPrefix(c, "notify.") && !strings.HasPrefix(c, "logbook.") && c != "weather.get_forecasts" {
			proto = append(proto, c+":"+ha.targets[i])
		}
	}
	want := "switch.turn_off:switch.tool_shed_heater switch.turn_on:switch.tool_shed_heater switch.turn_on:switch.dahlia_blanket_bot"
	if strings.Join(proto, " ") != want {
		t.Fatalf("calls %v", proto)
	}
	st := ""
	for _, o := range pub.outs {
		if o.ObjectID == ObjState {
			st = o.StateString
		}
	}
	if st != "on" {
		t.Fatalf("published state %q", st)
	}
	found := false
	for _, c := range ha.calls {
		found = found || c == "timer.start"
	}
	if !found {
		t.Fatal("no dead-man call through the host")
	}
	// buttons through the host
	h.Press(ButtonReplan)
	if len(h.Buttons()) != 2 {
		t.Fatalf("buttons %v", h.Buttons())
	}
}
