package blanketcontrol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"virtual_sensors/internal/modules"
)

type call struct {
	At      time.Time
	Domain  string
	Service string
	Entity  string
	Data    map[string]any
}

func (c call) String() string { return c.Domain + "." + c.Service + ":" + c.Entity }

// rig drives the module with a virtual clock and simulates the plant: the
// plug, the blanket (toggled by presses, off when power is restored, cut off
// after 3 h) and the Bot (a press lands after a delay, or is lost).
type rig struct {
	t     *testing.T
	m     *Module
	now   time.Time
	calls []call
	outs  []modules.OutputUpdate

	plug        string
	blanketOn   bool
	blanketAt   time.Time
	thermostat  bool // draws 0 W while logically on
	landing     []time.Time
	pressN      int
	pressLost   func(n int) bool
	pressDelay  time.Duration
	failPlugOff bool

	tVal    float64
	tSilent bool
	lastT   time.Time
}

func newRig(t *testing.T, opts string, armed bool) *rig {
	m := New()
	if err := m.Configure([]byte(opts)); err != nil {
		t.Fatal(err)
	}
	if err := m.Init(nil); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, m: m, now: at(26, 3, 0), plug: "off", tVal: 4.6, pressDelay: 23 * time.Second}
	r.m.OnLink(true, r.now)
	arm := "off"
	if armed {
		arm = "on"
	}
	r.send(ArmedEntity, arm, 0)
	r.send(m.plugEnt, "off", 0)
	r.send(m.powerEnt, "0", 0)
	r.sendT()
	return r
}

func (r *rig) send(entity, state string, v float64) {
	avail := state != "unavailable" && state != "unknown"
	if avail && strings.ContainsAny(state, "0123456789") {
		avail = true
	} else if avail && entity != r.m.plugEnt && entity != ArmedEntity {
		avail = false
	}
	if entity == r.m.plugEnt || entity == ArmedEntity {
		avail = false
	}
	u := modules.StateUpdate{EntityID: entity, Time: r.now, State: state, Value: v, Available: avail}
	r.outs = append(r.outs, r.m.OnStateUpdate(u)...)
}

func (r *rig) sendNum(entity string, v float64) {
	r.outs = append(r.outs, r.m.OnStateUpdate(modules.StateUpdate{EntityID: entity, Time: r.now, State: "x", Value: v, Available: true})...)
}

func (r *rig) sendT() {
	r.lastT = r.now
	if r.tSilent {
		return
	}
	r.sendNum(r.m.tempEnt, r.tVal)
}

func (r *rig) power() float64 {
	if r.plug == "on" && r.blanketOn && !r.thermostat {
		return 70
	}
	return 0
}

func (r *rig) setBlanket(on bool) {
	if on != r.blanketOn {
		r.blanketOn, r.blanketAt = on, r.now
	}
	r.sendNum(r.m.powerEnt, r.power())
}

func (r *rig) handle(e modules.Effect) {
	if e.Kind != modules.EffectCallService {
		return
	}
	ent, _ := e.Data["entity_id"].(string)
	c := call{At: r.now, Domain: e.Domain, Service: e.Service, Entity: ent, Data: e.Data}
	r.calls = append(r.calls, c)
	ok := true
	switch {
	case e.Domain == "switch" && ent == r.m.plugEnt:
		if r.failPlugOff && e.Service == "turn_off" {
			ok = false
			break
		}
		r.plug = map[string]string{"turn_on": "on", "turn_off": "off"}[e.Service]
		r.blanketOn = false // power restored: starts off
		r.send(r.m.plugEnt, r.plug, 0)
		r.sendNum(r.m.powerEnt, 0)
	case e.Domain == "switch" && ent == r.m.botEnt:
		r.pressN++
		if r.pressLost == nil || !r.pressLost(r.pressN) {
			r.landing = append(r.landing, r.now.Add(r.pressDelay))
		}
	}
	if e.CorrID >= forecastCorr {
		return
	}
	res := modules.CommandResult{CorrID: e.CorrID, OK: ok, At: r.now}
	if !ok {
		res.Err = "boom"
	}
	r.outs = append(r.outs, r.m.OnCommandResult(res)...)
}

func (r *rig) drain() {
	for i := 0; i < 20; i++ {
		es := r.m.Effects()
		if len(es) == 0 {
			return
		}
		for _, e := range es {
			r.handle(e)
		}
	}
}

func (r *rig) plant() {
	rest := r.landing[:0]
	for _, at := range r.landing {
		if r.now.Before(at) {
			rest = append(rest, at)
			continue
		}
		if r.plug == "on" {
			r.setBlanket(!r.blanketOn)
		}
	}
	r.landing = rest
	if r.blanketOn && r.now.Sub(r.blanketAt) >= 3*time.Hour {
		r.setBlanket(false)
	}
}

// run advances the clock in 5 s ticks.
func (r *rig) run(d time.Duration) {
	end := r.now.Add(d)
	for r.now.Before(end) {
		r.now = r.now.Add(modules.TickInterval)
		r.plant()
		if r.now.Sub(r.lastT) >= 10*time.Minute {
			r.sendT()
		}
		r.outs = append(r.outs, r.m.OnTick(r.now)...)
		r.drain()
	}
}

func (r *rig) mark() int { return len(r.calls) }

// since returns the calls after mark, ignoring read-only weather calls.
func (r *rig) since(mark int) []call {
	var out []call
	for _, c := range r.calls[mark:] {
		if c.Domain != "weather" {
			out = append(out, c)
		}
	}
	return out
}

func names(cs []call) []string {
	var s []string
	for _, c := range cs {
		s = append(s, c.String())
	}
	return s
}

// protocol returns only the plug/bot calls.
func (r *rig) protocol(mark int) []call {
	var out []call
	for _, c := range r.since(mark) {
		if c.Domain == "switch" {
			out = append(out, c)
		}
	}
	return out
}

func (r *rig) pushes(title string) int {
	n := 0
	for _, c := range r.calls {
		if c.Domain == "notify" && c.Data["title"] == title {
			n++
		}
	}
	return n
}

func (r *rig) logbooks(title string) int {
	n := 0
	for _, c := range r.calls {
		if c.Domain == "logbook" && c.Data["name"] == title {
			n++
		}
	}
	return n
}

func (r *rig) state() (string, map[string]any) {
	u := r.m.publishAll(r.now, true)
	for _, o := range u {
		if o.ObjectID == ObjState {
			var a map[string]any
			_ = json.Unmarshal([]byte(o.AttributesJSON), &a)
			return o.StateString, a
		}
	}
	r.t.Fatal("no state output")
	return "", nil
}

// checkCycleBeforeEveryPress asserts the protocol invariant: a plug off then
// a plug on, in that order, between any two presses and before the first.
func checkCycleBeforeEveryPress(t *testing.T, cs []call, plug, bot string) {
	t.Helper()
	sawOff, sawOn := false, false
	presses := 0
	for _, c := range cs {
		switch {
		case c.Entity == plug && c.Service == "turn_off":
			sawOff, sawOn = true, false
		case c.Entity == plug && c.Service == "turn_on":
			if sawOff {
				sawOn = true
			}
		case c.Entity == bot:
			presses++
			if !(sawOff && sawOn) {
				t.Fatalf("press %d without a plug cycle before it: %v", presses, names(cs))
			}
			sawOff, sawOn = false, false
		}
	}
}

// ---- helpers shared with the planner tests ----

func rawPrices(from, to time.Time, f func(h int) float64) map[string]json.RawMessage {
	type row struct {
		Start string  `json:"start"`
		End   string  `json:"end"`
		Value float64 `json:"value"`
	}
	var today, tomorrow []row
	for t := from; t.Before(to); t = t.Add(quarter) {
		rw := row{t.Format(time.RFC3339), t.Add(quarter).Format(time.RFC3339), f(t.In(sto).Hour())}
		if t.In(sto).Day() == from.In(sto).Day() {
			today = append(today, rw)
		} else {
			tomorrow = append(tomorrow, rw)
		}
	}
	a, _ := json.Marshal(today)
	b, _ := json.Marshal(tomorrow)
	return map[string]json.RawMessage{"raw_today": a, "raw_tomorrow": b}
}

func modulesUpdate(entity, state string, at time.Time) modules.StateUpdate {
	return modules.StateUpdate{EntityID: entity, Time: at, State: state, Available: false}
}
