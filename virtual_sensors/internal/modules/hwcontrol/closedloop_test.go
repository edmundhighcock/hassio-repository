package hwcontrol

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hotwater"
	"virtual_sensors/internal/modules/hwcontrol/core"
	"virtual_sensors/internal/modules/hwcontrol/planner"
)

// A closed-loop replay: the real hwcontrol Module (and so the real core and
// planners) drives a simulated switch, heater and tank over 48 h of
// quarter-hour prices. The plant is a deficit model of a 200 l tank with a
// 3 kW element: the thermostat calls when the heat deficit reaches
// callAt kWh and stops at zero; the element draws only while the switch is
// ON. Draws and standing losses grow the deficit. It is a model, not a
// measurement: the point is to compare the planners and the two settings of
// hwc_v2_window_resets_clock on the same plant and prices.

var stoLoc = func() *time.Location {
	l, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		panic(err)
	}
	return l
}()

// simPrice is a winter SE3 curve (SEK/kWh excl. VAT): cheap nights with a
// trough 02-04, a morning shoulder, a midday dip, an evening peak 17-20, and
// a small per-quarter wiggle so windows do not tie.
func simPrice(t time.Time) float64 {
	l := t.In(stoLoc)
	h := float64(l.Hour()) + float64(l.Minute())/60
	v := 1.0
	switch {
	case h >= 22 || h < 2:
		v = 0.45
	case h < 4:
		v = 0.25
	case h < 6:
		v = 0.5
	case h < 9:
		v = 1.3
	case h < 12:
		v = 0.95
	case h < 15:
		v = 0.6
	case h < 17:
		v = 1.1
	case h < 20:
		v = 2.3
	}
	return v + float64((int(t.Unix()/900)*7919)%13)/1000
}

func nordpoolAttrs(day time.Time) map[string]json.RawMessage {
	rows := func(d time.Time) json.RawMessage {
		mid := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, stoLoc)
		end := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, stoLoc)
		var out []map[string]any
		for t := mid; t.Before(end); t = t.Add(15 * time.Minute) {
			out = append(out, map[string]any{"start": t.Format(time.RFC3339), "end": t.Add(15 * time.Minute).Format(time.RFC3339), "value": simPrice(t)})
		}
		b, _ := json.Marshal(out)
		return b
	}
	return map[string]json.RawMessage{"raw_today": rows(day), "raw_tomorrow": rows(day.AddDate(0, 0, 1)), "tomorrow_valid": json.RawMessage("true")}
}

type draw struct {
	hh, mm int
	kwh    float64
}

type simEvent struct {
	at  time.Time
	seq int
	f   func()
}

type loop struct {
	t   *testing.T
	m   *Module
	now time.Time

	// plant
	pos, target float64
	motor       string
	switchOn    bool
	deficit     float64 // kWh the tank is below full
	calling     bool
	baseW       float64
	lastW       float64
	lastSample  time.Time
	cycleOn     time.Time
	cycleOpen   bool

	callAt, lossKW, capKWh float64
	draws                  func(day time.Time) []draw

	queue []simEvent
	seq   int

	outs   map[string]modules.OutputUpdate
	cmds   int
	notes  []string
	pCfg   planner.Config
	locked map[string]bool

	// metrics over [from, to)
	from, to         time.Time
	kwh, ore         float64
	peakKWh, peakOre float64 // heater energy and cost 17:00-20:00 local
	maxDef           float64
	coldHours        float64 // hours with the deficit above coldKWh
	clockOutHours    float64 // hours the tank clock was known and expired
	moves            int
	eveningOnHours   float64
	snapshots        map[string]map[string]any
	snapAt           []time.Time
	scheds           []core.HeaterSchedule
	timeline         []string
}

const coldKWh = 4.5

func newLoop(t *testing.T, opts string, draws func(time.Time) []draw, start time.Time) *loop {
	m := New()
	if err := m.Configure([]byte(opts)); err != nil {
		panic(err)
	}
	if err := m.Init(nil); err != nil {
		panic(err)
	}
	m.SignalsConnected()
	l := &loop{t: t, m: m, now: start, pos: 99, target: 99, motor: "stopped", switchOn: true,
		baseW: 400, callAt: 1.0, lossKW: 0.06, capKWh: 9, draws: draws, deficit: 1.3,
		outs: map[string]modules.OutputUpdate{}, pCfg: planner.DefaultConfig(), snapshots: map[string]map[string]any{}}
	return l
}

func (l *loop) collect(outs []modules.OutputUpdate) {
	for _, o := range outs {
		l.outs[o.ObjectID] = o
	}
}

func (l *loop) feed(id, state string, v float64, attrs map[string]json.RawMessage) {
	l.collect(l.m.OnStateUpdate(modules.StateUpdate{EntityID: id, Time: l.now, State: state, Value: v, Available: true, Attrs: attrs}))
	l.effects()
}

func (l *loop) pushCover() {
	b, _ := json.Marshal(l.pos)
	l.feed(l.m.ent.Cover, "open", 0, map[string]json.RawMessage{"current_position": b})
}
func (l *loop) pushTarget() { l.feed(l.m.ent.Target, fmt.Sprint(l.target), l.target, nil) }
func (l *loop) pushMotor()  { l.feed(l.m.ent.MotorState, l.motor, 0, nil) }

func (l *loop) at(d time.Duration, f func()) {
	l.seq++
	l.queue = append(l.queue, simEvent{l.now.Add(d), l.seq, f})
}

// effects executes the module's pending effects against the plant.
func (l *loop) effects() {
	for _, e := range l.m.Effects() {
		if e.Kind != modules.EffectCallService {
			continue
		}
		if e.Domain == "notify" || e.Domain == "persistent_notification" {
			if e.Domain == "notify" {
				l.notes = append(l.notes, fmt.Sprint(e.Data["message"]))
			}
			continue
		}
		if e.Domain != "cover" {
			continue
		}
		p := e.Data["position"].(int)
		l.cmds++
		l.timeline = append(l.timeline, fmt.Sprintf("%s cmd %d (state %s)", l.now.In(stoLoc).Format("01-02 15:04:05"), p, l.outs["hot_water_controller_state"].StateString))
		if !l.now.Before(l.from) && l.now.Before(l.to) {
			l.moves++
		}
		corr := e.CorrID
		l.at(200*time.Millisecond, func() {
			l.collect(l.m.OnCommandResult(modules.CommandResult{CorrID: corr, OK: true, At: l.now}))
			l.effects()
		})
		l.at(300*time.Millisecond, func() {
			l.target, l.pos, l.motor = float64(p), float64(p), "moving"
			l.pushTarget()
			l.pushCover()
			l.pushMotor()
		})
		l.at(8*time.Second, func() {
			l.motor = "stopped"
			l.pushMotor()
			l.switchOn = l.pos >= 75
			l.plantChanged(true)
		})
	}
}

func (l *loop) heaterW() float64 {
	if l.switchOn && l.calling {
		return 3000
	}
	return 0
}

// plantChanged emits a residual sample when the power changed, and the
// detector's cycle signals (a minimal stand-in for hot_water).
func (l *loop) plantChanged(force bool) {
	w := l.baseW + l.heaterW()
	if !force && w == l.lastW && l.now.Sub(l.lastSample) < time.Minute {
		return
	}
	if l.heaterW() > 0 && !l.cycleOpen {
		l.cycleOpen, l.cycleOn = true, l.now
		l.timeline = append(l.timeline, fmt.Sprintf("%s heater ON  deficit %.1f", l.now.In(stoLoc).Format("01-02 15:04:05"), l.deficit))
		l.m.OnSignal(hotwater.CycleOpened{OnTime: l.now, Magnitude: 3000})
	}
	if l.heaterW() == 0 && l.cycleOpen {
		l.cycleOpen = false
		l.timeline = append(l.timeline, fmt.Sprintf("%s heater off (%.0f min)", l.now.In(stoLoc).Format("01-02 15:04:05"), l.now.Sub(l.cycleOn).Minutes()))
		l.m.OnSignal(hotwater.CycleClosed{OnTime: l.cycleOn, OffTime: l.now, Accepted: l.now.Sub(l.cycleOn) >= 6*time.Minute, Reason: "dwell_confirmed"})
	}
	l.lastW, l.lastSample = w, l.now
	l.m.OnSignal(hotwater.ResidualSample{T: l.now, W: w, Available: true})
	l.effects()
}

func (l *loop) boot(opts map[string]string) {
	l.m.OnLink(true, l.now)
	l.pushCover()
	l.pushTarget()
	l.pushMotor()
	l.feed(l.m.ent.Battery, "80", 80, nil)
	l.feed(l.m.ent.Select, "Auto (Cost-saving)", 0, nil)
	l.feed(l.m.ent.Armed, "on", 0, nil)
	l.feed(l.m.ent.Latch, "off", 0, nil)
	l.feed(l.m.ent.EV, "0", 0, nil)
	l.feed(l.m.ent.Boost, "2026-01-01T00:00:00+00:00", 0, nil)
	l.priceUpdate()
	l.plantChanged(true)
}

func (l *loop) priceUpdate() {
	l.feed(l.m.ent.Price, "0.5", 0.5, nordpoolAttrs(l.now.In(stoLoc)))
}

// run advances the closed loop to end in 10 s ticks.
func (l *loop) run(end time.Time) {
	const dt = 10 * time.Second
	nextDraw := map[string]bool{}
	lastDay := l.now.In(stoLoc).YearDay()
	for l.now.Before(end) {
		l.now = l.now.Add(dt)
		// plant events due
		for {
			sort.SliceStable(l.queue, func(i, j int) bool {
				if l.queue[i].at.Equal(l.queue[j].at) {
					return l.queue[i].seq < l.queue[j].seq
				}
				return l.queue[i].at.Before(l.queue[j].at)
			})
			if len(l.queue) == 0 || l.queue[0].at.After(l.now) {
				break
			}
			e := l.queue[0]
			l.queue = l.queue[1:]
			e.f()
		}
		if d := l.now.In(stoLoc).YearDay(); d != lastDay {
			lastDay = d
			l.priceUpdate()
		}
		// household draws
		ln := l.now.In(stoLoc)
		for _, d := range l.draws(ln) {
			key := fmt.Sprintf("%s %02d:%02d", ln.Format("2006-01-02"), d.hh, d.mm)
			at := time.Date(ln.Year(), ln.Month(), ln.Day(), d.hh, d.mm, 0, 0, stoLoc)
			if !l.now.Before(at) && !nextDraw[key] {
				nextDraw[key] = true
				l.deficit += d.kwh
			}
		}
		// thermostat and tank
		h := dt.Hours()
		l.deficit += l.lossKW * h
		kwh := 0.0
		if l.heaterW() > 0 {
			kwh = 3 * h
			l.deficit -= kwh
		}
		if l.deficit > l.capKWh {
			l.deficit = l.capKWh
		}
		if l.deficit <= 0 {
			l.deficit, l.calling = 0, false
		} else if !l.calling && l.deficit >= l.callAt {
			l.calling = true
		}
		l.plantChanged(false)
		if !l.now.Before(l.from) && l.now.Before(l.to) {
			price := l.pCfg.AllInOre(simPrice(l.now))
			l.kwh += kwh
			l.ore += kwh * price
			if hr := ln.Hour(); hr >= 17 && hr < 20 {
				l.peakKWh += kwh
				l.peakOre += kwh * price
			}
			if l.deficit > l.maxDef {
				l.maxDef = l.deficit
			}
			if l.deficit > coldKWh {
				l.coldHours += h
			}
			if l.now.Second() < 10 {
				if f := l.m.Controller().Facts(); !f.LastFinishedReheatEnd.IsZero() && f.TankHeadroom == 0 {
					l.clockOutHours += time.Minute.Hours()
				}
			}
		}
		l.collect(l.m.OnTick(l.now))
		l.effects()
		for _, s := range l.snapAt {
			if !l.now.Before(s) && l.snapshots[s.String()] == nil {
				l.snapshots[s.String()] = l.snapshot()
			}
		}
	}
}

func (l *loop) snapshot() map[string]any {
	out := map[string]any{"at": l.now.In(stoLoc).Format(time.RFC3339)}
	for _, id := range []string{"hot_water_controller_plan", ObjPlanV2} {
		if o, ok := l.outs[id]; ok {
			var a map[string]any
			_ = json.Unmarshal([]byte(o.AttributesJSON), &a)
			out[id] = a
		}
	}
	out["schedule"] = l.m.HeaterSchedule()
	return out
}

func normalDraws(ln time.Time) []draw {
	return []draw{{6, 45, 1.5}, {12, 30, 0.5}, {18, 30, 1.5}, {21, 0, 0.5}}
}

// lightDraws: a house that is away until the afternoon, so nothing calls for
// heat around the middle of the day.
func lightDraws(ln time.Time) []draw { return []draw{{18, 30, 1.2}, {21, 0, 0.3}} }

type result struct {
	name                       string
	kWh, kr, peakKWh, peakKr   float64
	maxDef, coldH, clockOutH   float64
	moves                      int
	notes                      []string
	lp                         *loop
	meanOre                    float64
	fullN, topN, catchN, evenN int
}

func runScenario(t *testing.T, name, opts string, draws func(time.Time) []draw, snap ...time.Time) result {
	start := time.Date(2026, 11, 10, 8, 0, 0, 0, stoLoc)
	l := newLoop(t, opts, draws, start)
	l.from = time.Date(2026, 11, 10, 12, 0, 0, 0, stoLoc)
	l.to = l.from.Add(48 * time.Hour)
	l.snapAt = snap
	l.boot(nil)
	l.run(l.to)
	r := result{name: name, kWh: l.kwh, kr: l.ore / 100, peakKWh: l.peakKWh, peakKr: l.peakOre / 100, maxDef: l.maxDef,
		coldH: l.coldHours, clockOutH: l.clockOutHours, moves: l.moves, notes: l.notes, lp: l}
	if l.kwh > 0 {
		r.meanOre = l.ore / l.kwh
	}
	for _, w := range l.m.Controller().Facts().Windows {
		_ = w
	}
	return r
}

func (r result) String() string {
	return fmt.Sprintf("%-34s %5.1f kWh  %6.2f kr  %5.1f ore/kWh  17-20h %4.1f kWh %5.2f kr  maxdef %.1f kWh  cold %.1f h  clock-out %.1f h  moves %d  alerts %d",
		r.name, r.kWh, r.kr, r.meanOre, r.peakKWh, r.peakKr, r.maxDef, r.coldH, r.clockOutH, r.moves, len(r.notes))
}

const liveOpts = `{"hwc_mode":"live"`

func TestClosedLoop48h(t *testing.T) {
	snapAt := time.Date(2026, 11, 11, 8, 0, 0, 0, stoLoc)
	type job struct {
		name, opts string
		draws      func(time.Time) []draw
		snap       []time.Time
	}
	jobs := []job{
		{"greedy_v1 (default)", liveOpts + `}`, normalDraws, []time.Time{snapAt}},
		{"v2 (clock reset off, default)", liveOpts + `,"hwc_planner":"v2"}`, normalDraws, []time.Time{snapAt}},
		{"v2 + hwc_v2_window_resets_clock", liveOpts + `,"hwc_planner":"v2","hwc_v2_window_resets_clock":true}`, normalDraws, []time.Time{snapAt}},
		{"greedy_v1 (default)", liveOpts + `}`, lightDraws, nil},
		{"v2 (clock reset off, default)", liveOpts + `,"hwc_planner":"v2"}`, lightDraws, nil},
		{"v2 + hwc_v2_window_resets_clock", liveOpts + `,"hwc_planner":"v2","hwc_v2_window_resets_clock":true}`, lightDraws, nil},
	}
	res := make([]result, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i] = runScenario(t, j.name, j.opts, j.draws, j.snap...)
		}()
	}
	wg.Wait()
	g, v, vs := res[0], res[1], res[2]
	gl, vl, vls := res[3], res[4], res[5]
	if testing.Verbose() {
		for _, r := range []result{v, vs} {
			t.Log("TIMELINE " + r.name + "\n" + strings.Join(r.lp.timeline, "\n"))
		}
	}
	t.Log("NORMAL DRAWS (6.5 kWh/day incl. standing loss), 48 h from 2026-11-10 12:00")
	for _, r := range []result{g, v, vs} {
		t.Log(r.String())
	}
	t.Log("LIGHT DRAWS (house away by day: the midday window gets no call for heat)")
	for _, r := range []result{gl, vl, vls} {
		t.Log(r.String())
	}

	// ---- assertions
	if g.kWh <= 0 || v.kWh <= 0 {
		t.Fatalf("no heat bought: greedy %v, v2 %v", g.kWh, v.kWh)
	}
	// v2 buys at a lower average price than greedy_v1 on the same plant.
	if v.meanOre >= g.meanOre {
		t.Fatalf("v2 %.1f ore/kWh is not cheaper than greedy_v1 %.1f", v.meanOre, g.meanOre)
	}
	if v.kr >= g.kr {
		t.Fatalf("v2 cost %.2f kr is not below greedy_v1 %.2f kr", v.kr, g.kr)
	}
	// The tank never runs dry in the normal house: the deficit stays bounded
	// by the plant cap and the tank is not "cold" for most of the time.
	if v.coldH > 8 {
		t.Fatalf("v2 left the tank cold for %.1f h", v.coldH)
	}
	// With the clock reset the evening peak is almost free of heat; without
	// it v2 is still far below greedy_v1 but buys some forced evening heat.
	if vs.peakKWh > 0.25*g.peakKWh+0.5 {
		t.Fatalf("v2 (reset) bought %.1f kWh in 17-20 (greedy %.1f)", vs.peakKWh, g.peakKWh)
	}
	if v.peakKWh >= g.peakKWh {
		t.Fatalf("v2 bought %.1f kWh in 17-20 (greedy %.1f)", v.peakKWh, g.peakKWh)
	}
	// The move budget holds.
	if v.moves > 2*8 {
		t.Fatalf("v2 made %d moves in 48 h", v.moves)
	}
	// The light-draw house: the tank clock trips without the reset, and the
	// reset setting avoids that.
	if vl.clockOutH <= 0 {
		t.Fatalf("without the reset the 14 h clock should trip on a no-call day (clock-out %.1f h)", vl.clockOutH)
	}
	if vls.kr >= vl.kr {
		t.Fatalf("the reset should be cheaper on the light-draw days: %.2f vs %.2f kr", vls.kr, vl.kr)
	}
	if vls.clockOutH >= vl.clockOutH {
		t.Fatalf("the reset did not reduce clock-out time: %.1f vs %.1f h", vls.clockOutH, vl.clockOutH)
	}
	// Shadow: under greedy_v1 the v2 plan is published, and v2 is not the
	// controller's planner.
	o, ok := g.lp.outs[ObjPlanV2]
	if !ok || !strings.Contains(o.AttributesJSON, `"shadow":true`) || !strings.Contains(o.AttributesJSON, `"windows"`) {
		t.Fatalf("no shadow plan published: %+v", o)
	}
	if !strings.Contains(g.lp.outs["hot_water_controller_plan"].AttributesJSON, `"planner":"greedy_v1"`) {
		t.Fatalf("greedy_v1 not driving: %s", g.lp.outs["hot_water_controller_plan"].AttributesJSON)
	}
	if _, ok := v.lp.outs[ObjPlanV2]; ok {
		t.Fatal("shadow published while v2 drives")
	}
	if !strings.Contains(v.lp.outs["hot_water_controller_plan"].AttributesJSON, `"planner":"v2"`) {
		t.Fatalf("v2 not driving: %s", v.lp.outs["hot_water_controller_plan"].AttributesJSON)
	}

	// ---- the example plan: v2's own plan at 2026-11-11 08:00 and the shadow
	// sensor under greedy_v1 at the same moment.
	if s := v.lp.snapshots[snapAt.String()]; s != nil {
		b, _ := json.MarshalIndent(s["hot_water_controller_plan"], "", " ")
		t.Logf("EXAMPLE v2 PLAN at %v:\n%s", snapAt, b)
		sc := s["schedule"].(core.HeaterSchedule)
		for _, w := range sc.Windows {
			t.Logf("  budget feed %s-%s on=%v", w.Start.In(stoLoc).Format("01-02 15:04"), w.End.In(stoLoc).Format("01-02 15:04"), w.On)
		}
	}
	if s := g.lp.snapshots[snapAt.String()]; s != nil {
		b, _ := json.MarshalIndent(s[ObjPlanV2], "", " ")
		t.Logf("SHADOW v2 PLAN (greedy_v1 driving) at %v:\n%s", snapAt, b)
	}
}

// HeaterSchedule under live v2 reports the OFF stretches the controller
// will actually hold, and ON in the windows; under greedy_v1 shadow it is
// ON throughout when the controller is not live.
func TestClosedLoopHeaterScheduleFeed(t *testing.T) {
	start := time.Date(2026, 11, 10, 8, 0, 0, 0, stoLoc)
	l := newLoop(t, liveOpts+`,"hwc_planner":"v2"}`, normalDraws, start)
	l.from, l.to = start.Add(100*time.Hour), start.Add(101*time.Hour)
	l.boot(nil)
	l.run(time.Date(2026, 11, 10, 12, 0, 0, 0, stoLoc))
	s := l.m.HeaterSchedule()
	if !s.Planned {
		t.Fatalf("not planned: %+v", s)
	}
	var off, on time.Duration
	for _, w := range s.Windows {
		if w.On {
			on += w.End.Sub(w.Start)
		} else {
			off += w.End.Sub(w.Start)
		}
	}
	if off < 6*time.Hour || on <= 0 || off+on < 23*time.Hour {
		t.Fatalf("schedule off %v on %v: %+v", off, on, s.Windows)
	}
}

// With greedy_v1 as the default the v2 sensor exists but nothing else
// changes; option parsing.
func TestPlannerOptionParsing(t *testing.T) {
	m := New()
	_ = m.Configure([]byte(`{}`))
	if m.v2 || m.cfg.WindowStamp {
		t.Fatal("v2 or the clock reset on by default")
	}
	m = New()
	_ = m.Configure([]byte(`{"hwc_planner":"v2"}`))
	if !m.v2 || m.cfg.WindowStamp {
		t.Fatalf("v2 %v stamp %v: the clock reset must default to false", m.v2, m.cfg.WindowStamp)
	}
	if m.cfg.GSinceReheat < m.cfg.OffLimitSinceReheatEnd {
		t.Fatal("condition G does not wait for the tank clock under v2")
	}
	m = New()
	_ = m.Configure([]byte(`{"hwc_planner":"v2","hwc_v2_window_resets_clock":true}`))
	if !m.cfg.WindowStamp {
		t.Fatal("clock reset not enabled")
	}
	m = New()
	_ = m.Configure([]byte(`{"hwc_planner":"v3"}`))
	if m.v2 {
		t.Fatal("unknown planner accepted")
	}
	// greedy_v1 with the reset option set: the reset is v2-only.
	m = New()
	_ = m.Configure([]byte(`{"hwc_v2_window_resets_clock":true}`))
	if m.cfg.WindowStamp {
		t.Fatal("clock reset active under greedy_v1")
	}
	_ = math.Pi
}
