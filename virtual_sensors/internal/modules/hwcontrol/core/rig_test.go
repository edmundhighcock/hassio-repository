package core

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// rig is a tiny deterministic plant + virtual clock for unit tests. It is
// NOT the reviewer's simulator (internal/sim); it exists only to exercise
// the transition rows one by one.
type rig struct {
	t   *testing.T
	c   *Controller
	now time.Time

	// plant
	pos, target float64
	motor       string
	switchOn    bool    // physical contact
	baseW       float64 // residual without the heater
	heaterW     float64
	calling     bool // thermostat calling for heat
	lastResW    float64
	lastResAt   time.Time
	sampleEvery time.Duration
	residualOff bool // stop sending samples (grid hole)

	// motor faults
	failCalls int  // next N call_service fail
	retreatN  int  // next N moves retreat after the stop
	stickN    int  // next N moves never move
	noEngage  bool // position reaches 99 but the contact stays open
	// stuckClosed: position reaches 0 but the contact stays closed
	stuckClosed bool
	// restAt100: an ON command comes to rest at target/position 100
	restAt100 bool

	queue   []qev
	effects []Effect
	cmds    []int // every cover position commanded (accepted or not)
	notes   []string
	seqNo   int
}

type qev struct {
	at  time.Time
	seq int
	f   func()
}

var t0 = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) // 10:00 Stockholm

func newRig(t *testing.T, cfg Config, persisted []byte) *rig {
	r := &rig{t: t, now: t0, pos: 99, target: 99, motor: "stopped", switchOn: true,
		baseW: 400, heaterW: 3000, sampleEvery: 60 * time.Second}
	r.c = New(cfg, persisted, "cover.hot_water_switch")
	return r
}

func liveCfg() Config {
	c := Defaults()
	c.Live = true
	c.ShadowSummaryHour = -1
	return c
}

func (r *rig) at(d time.Duration, f func()) {
	r.seqNo++
	r.queue = append(r.queue, qev{at: r.now.Add(d), seq: r.seqNo, f: f})
}

func (r *rig) step(in Input) {
	effs := r.c.Step(in)
	for _, e := range effs {
		r.effects = append(r.effects, e)
		if e.Kind == EffCallService && e.Domain == "cover" {
			r.onCover(e)
		}
		if e.Kind == EffLog && testingVerbose {
			r.t.Logf("%s %s", r.now.Format("15:04:05"), e.Msg)
		}
	}
}

var testingVerbose = false

func (r *rig) onCover(e Effect) {
	p := e.Data["position"].(int)
	r.cmds = append(r.cmds, p)
	corr := e.CorrID
	if r.failCalls > 0 {
		r.failCalls--
		r.at(100*time.Millisecond, func() { r.step(CommandResult{At: r.now, CorrID: corr, OK: false, Err: "boom"}) })
		return
	}
	r.at(200*time.Millisecond, func() { r.step(CommandResult{At: r.now, CorrID: corr, OK: true}) })
	if r.stickN > 0 {
		r.stickN--
		return
	}
	retreat := false
	if r.retreatN > 0 {
		r.retreatN--
		retreat = true
	}
	from := r.pos
	r.at(300*time.Millisecond, func() {
		r.target = float64(p)
		if r.restAt100 && p == 99 {
			r.target = 100 // the real switch rests at 100 when ON
		}
		r.pos = r.target // D-3: the position echo is immediate
		r.motor = "moving"
		r.pushTarget()
		r.pushCover()
		r.pushMotor()
	})
	r.at(8*time.Second, func() {
		r.motor = "stopped"
		r.pushMotor()
		r.setSwitch()
	})
	if retreat {
		r.at(11*time.Second, func() {
			r.target = from
			r.pos = from
			r.pushTarget()
			r.pushCover()
			r.setSwitch()
		})
	}
}

func (r *rig) setSwitch() {
	on := r.pos >= 75
	if r.noEngage && on {
		on = false
	}
	if r.pos <= 20 {
		on = false
	} else if r.pos < 75 {
		on = r.switchOn
	}
	if r.stuckClosed {
		on = true
	}
	r.switchOn = on
	r.emitResidual(true)
}

func (r *rig) residualW() float64 {
	w := r.baseW
	if r.switchOn && r.calling {
		w += r.heaterW
	}
	return w
}

func (r *rig) emitResidual(onlyIfChanged bool) {
	if r.residualOff {
		return
	}
	w := r.residualW()
	if onlyIfChanged && w == r.lastResW && !r.lastResAt.IsZero() {
		return
	}
	r.lastResW, r.lastResAt = w, r.now
	r.step(Sample{At: r.now, W: w, Available: true, DiscSeq: r.c.lastSeq})
}

func (r *rig) pushCover() {
	r.step(EntityUpdate{At: r.now, Role: RoleCover, State: "open", Position: r.pos, PositionOK: true})
}
func (r *rig) pushTarget() {
	r.step(EntityUpdate{At: r.now, Role: RoleTarget, State: fmt.Sprint(r.target), Num: r.target, NumOK: true})
}
func (r *rig) pushMotor() { r.step(EntityUpdate{At: r.now, Role: RoleMotorState, State: r.motor}) }

func (r *rig) set(role Role, state string) {
	var num float64
	ok := false
	if _, err := fmt.Sscan(state, &num); err == nil {
		ok = true
	}
	r.step(EntityUpdate{At: r.now, Role: role, State: state, Num: num, NumOK: ok})
}

// advance runs the clock forward by d: 5 s ticks, plant events in order,
// and a held residual sample every sampleEvery.
func (r *rig) advance(d time.Duration) {
	end := r.now.Add(d)
	nextTick := r.now.Add(5 * time.Second)
	for {
		sort.SliceStable(r.queue, func(i, j int) bool {
			if r.queue[i].at.Equal(r.queue[j].at) {
				return r.queue[i].seq < r.queue[j].seq
			}
			return r.queue[i].at.Before(r.queue[j].at)
		})
		next := nextTick
		if len(r.queue) > 0 && r.queue[0].at.Before(next) {
			next = r.queue[0].at
		}
		if next.After(end) {
			r.now = end
			return
		}
		r.now = next
		if len(r.queue) > 0 && !r.queue[0].at.After(r.now) {
			e := r.queue[0]
			r.queue = r.queue[1:]
			e.f()
			continue
		}
		if !r.residualOff && r.now.Sub(r.lastResAt) >= r.sampleEvery {
			r.emitResidual(false)
		}
		r.step(Tick{At: r.now})
		nextTick = r.now.Add(5 * time.Second)
	}
}

// boot brings the controller up with every input fresh. armed sets the HA
// key. The heater is idle unless r.calling is set first.
func (r *rig) boot(armed bool) {
	r.step(Detector{At: r.now, Present: true})
	r.step(Link{At: r.now, Up: true})
	r.pushCover()
	r.pushTarget()
	r.pushMotor()
	r.set(RoleBattery, "80")
	r.set(RoleSelect, SelectAuto)
	if armed {
		r.set(RoleArmed, "on")
	} else {
		r.set(RoleArmed, "off")
	}
	r.set(RoleLatch, "off")
	r.set(RoleEV, "0")
	r.set(RoleBoost, "2026-01-01T00:00:00+00:00")
	r.emitResidual(false)
	r.advance(5 * time.Second)
}

func (r *rig) state() string { return r.c.stateLabel() }

func (r *rig) wantState(want string) {
	r.t.Helper()
	if got := r.state(); got != want {
		r.t.Fatalf("%s: state %s, want %s (reason %s)", r.now.Format(time.RFC3339), got, want, r.c.reason)
	}
}

// tankRecent marks a finished reheat that ended ago.
func (r *rig) tankRecent(ago time.Duration) {
	r.c.P.LastFinishedReheatEnd = r.now.Add(-ago)
	r.c.P.LastFinishedReheatStart = r.now.Add(-ago - 20*time.Minute)
	r.c.P.LastGenuineStart = r.c.P.LastFinishedReheatStart
}

// planNow permits OFF from now+from for dur.
func (r *rig) planNow(from, dur time.Duration) {
	p := &Plan{PlannerID: "test", GeneratedAt: r.now, ValidUntil: r.now.Add(48 * time.Hour),
		OffPeriods: []OffPeriod{{Start: r.now.Add(from), End: r.now.Add(from + dur), Why: "test"}}}
	r.step(PlanUpdate{At: r.now, Plan: p})
}

func (r *rig) countCmds(pos int) int {
	n := 0
	for _, p := range r.cmds {
		if p == pos {
			n++
		}
	}
	return n
}

func (r *rig) notifies(code string) int {
	n := 0
	for _, e := range r.effects {
		if e.Kind == EffNotify && e.Code == code {
			n++
		}
	}
	return n
}

func (r *rig) lastPublished(obj string) (Effect, bool) {
	for i := len(r.effects) - 1; i >= 0; i-- {
		if r.effects[i].Kind == EffPublish && r.effects[i].ObjectID == obj {
			return r.effects[i], true
		}
	}
	return Effect{}, false
}

// readyForOff boots live, settles, with a recent finished reheat and a
// plan period starting after the settle.
func readyForOff(t *testing.T) *rig {
	r := newRig(t, liveCfg(), nil)
	r.boot(true)
	r.wantState("IDLE_ON")
	r.tankRecent(time.Hour)
	r.planNow(16*time.Minute, 2*time.Hour)
	return r
}
