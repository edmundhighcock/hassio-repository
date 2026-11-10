package evcontrol

import (
	"fmt"
	"log"
	"strings"
	"time"

	"virtual_sensors/internal/modules"
)

// Live executor (phase 3). Everything here is inert unless BOTH keys are
// on: option ev_mode: live AND input_boolean.ev_controller_armed on. It
// mirrors hwcontrol: modules do no I/O, they return Effects the host
// executes, and the host feeds each outcome back as a CommandResult.
//
// Easee integration v2026.11.0 services used (services.yaml / services.py
// read on hass): easee.action_command {charger_id|device_id,
// action_command}, easee.set_charger_dynamic_limit {charger_id|device_id,
// current, time_to_live (min)}, easee.set_circuit_dynamic_limit
// {charger_id|device_id|circuit_id, current_p1..p3, time_to_live (min)}.
// charger_id is the `id` attribute of sensor.loaver_status ("EH5YL3HF"):
// services.py accepts it in place of the UI's device_id, and resolves the
// circuit from the charger.

// ArmedEntity is the second key.
const ArmedEntity = "input_boolean.ev_controller_armed"

// Output object ids added by the executor.
const (
	ObjMode       = "ev_controller_mode"
	ObjArmed      = "ev_controller_armed"
	ObjLastCmd    = "ev_last_command"
	ObjLimitForce = "ev_limit_in_force"
)

const (
	defaultNotify   = "notify.mobile_app_edmund15max"
	defaultTTL      = 15 * time.Minute
	defaultStale    = 10 * time.Minute
	minGap          = 30 * time.Second // between changed limit writes (decreases are immediate)
	pauseGap        = 90 * time.Second // between pause/resume attempts
	notifyGap       = 15 * time.Minute // per notification kind
	linkGrace       = 2 * time.Minute
	releaseA        = 32 // handed back with a 1 min TTL; Easee clamps to its own maxima
	priceLateHour   = 14 // tomorrow's prices should have arrived by this local hour
	limitFailResume = 3  // failed limit writes after which a fallback resume goes anyway
	corrNotify      = uint64(1) << 40
)

type vec [3]int

// sent is what we last wrote for one dynamic limit.
type sent struct {
	v        vec
	known    bool
	at       time.Time // when the last successful write was issued
	attempt  time.Time
	inflight bool
	fails    int
}

type pending struct {
	kind string // pause, resume, start, charger, circuit, release_*
	cmd  string
	v    vec
}

// execPersist is the executor's restart state: what we may be holding.
type execPersist struct {
	Paused  bool `json:"paused"`  // we commanded pause and it is in force
	Holding bool `json:"holding"` // we have written dynamic limits that need releasing
}

type exec struct {
	live       bool
	notify     string
	chargerOpt string
	chargerID  string
	ttl        time.Duration
	stale      time.Duration

	armedKey    bool
	ready       bool // link up at least once (states loaded)
	linkUp      bool
	linkDownAt  time.Time
	gridAt      time.Time
	wasArmedLv  bool
	breachEdge  bool
	fbNotified  string
	lastNotify  map[string]time.Time
	corr        uint64
	notifyC     uint64
	pend        map[uint64]pending
	effects     []modules.Effect
	st          execPersist
	charger     sent
	circuit     sent
	pauseAt     time.Time
	pauseFly    bool
	releaseAt   time.Time
	releaseFly  bool
	last        lastCmd
	lastDesired desired
	outDirty    bool
}

type lastCmd struct {
	Cmd, Result, Err string
	At               time.Time
}

type desired struct {
	Mode     string // idle, pause, charge
	Amps     int
	Circuit  vec
	Reason   string
	Fallback string // "", "deadline", "stale"
}

func newExec() exec {
	return exec{notify: defaultNotify, ttl: defaultTTL, stale: defaultStale,
		lastNotify: map[string]time.Time{}, pend: map[uint64]pending{}}
}

func (m *Module) armedLive() bool { return m.x.live && m.x.armedKey }

func (x *exec) refresh() time.Duration { return x.ttl / 3 }

func (x *exec) target() map[string]any {
	if x.chargerOpt != "" {
		return map[string]any{"charger_id": x.chargerOpt}
	}
	if x.chargerID != "" {
		return map[string]any{"charger_id": x.chargerID}
	}
	return nil
}

func (m *Module) call(domain, service string, data map[string]any, p pending) {
	m.x.corr++
	m.x.pend[m.x.corr] = p
	m.x.effects = append(m.x.effects, modules.Effect{Kind: modules.EffectCallService, Domain: domain, Service: service, Data: data, CorrID: m.x.corr})
	m.x.last = lastCmd{Cmd: p.cmd, Result: "sent", At: m.now}
	m.x.outDirty = true
}

func (m *Module) cmd(action string, p pending) {
	d := m.x.target()
	d["action_command"] = action
	p.cmd = action
	m.call("easee", "action_command", d, p)
}

func (m *Module) sendCharger(a int, now time.Time) {
	d := m.x.target()
	d["current"] = a
	d["time_to_live"] = int(m.x.ttl / time.Minute)
	m.x.charger.inflight, m.x.charger.attempt = true, now
	m.x.st.Holding = true
	m.call("easee", "set_charger_dynamic_limit", d, pending{kind: "charger", cmd: fmt.Sprintf("charger limit %d A", a), v: vec{a, a, a}})
}

func (m *Module) sendCircuit(v vec, now time.Time, ttl time.Duration, kind string) {
	d := m.x.target()
	d["current_p1"], d["current_p2"], d["current_p3"] = v[0], v[1], v[2]
	d["time_to_live"] = int(ttl / time.Minute)
	m.x.circuit.inflight, m.x.circuit.attempt = true, now
	m.x.st.Holding = true
	m.call("easee", "set_circuit_dynamic_limit", d, pending{kind: kind, cmd: fmt.Sprintf("circuit limit %d/%d/%d A", v[0], v[1], v[2]), v: v})
}

// needSend: re-send only on change, plus a refresh well inside the TTL.
func (m *Module) needSend(s sent, want vec, now time.Time) bool {
	if s.inflight {
		return false
	}
	if !s.known {
		return now.Sub(s.attempt) >= minGap
	}
	if want != s.v {
		dec := false
		for i := range want {
			if want[i] < s.v[i] {
				dec = true
			}
		}
		return dec || now.Sub(s.attempt) >= minGap
	}
	return now.Sub(s.at) >= m.x.refresh()
}

func (m *Module) limitOK(s sent, want vec, now time.Time) bool {
	return s.known && s.v == want && now.Sub(s.at) < m.x.ttl
}

// Notify sends to the phone and as a persistent notification, at most once
// per kind per notifyGap (a failure loop must not flood the phone).
func (m *Module) notifyKind(kind, title, text string, now time.Time) {
	if t, ok := m.x.lastNotify[kind]; ok && now.Sub(t) < notifyGap {
		return
	}
	m.x.lastNotify[kind] = now
	m.notifyNow(kind, title, text)
}

func (m *Module) notifyNow(kind, title, text string) {
	dom, svc, _ := strings.Cut(m.x.notify, ".")
	m.x.notifyC++
	m.x.effects = append(m.x.effects, modules.Effect{Kind: modules.EffectCallService, Domain: dom, Service: svc,
		Data: map[string]any{"title": title, "message": text}, CorrID: corrNotify + m.x.notifyC})
	m.x.notifyC++
	m.x.effects = append(m.x.effects, modules.Effect{Kind: modules.EffectCallService, Domain: "persistent_notification", Service: "create",
		Data: map[string]any{"title": title, "message": text, "notification_id": "ev_controller_" + kind}, CorrID: corrNotify + m.x.notifyC})
}

// phaseLimits are the guard's per-phase ceilings for the circuit: the
// budget less the estimated non-EV amps on that phase, floored, [0, MaxA].
func phaseLimits(g GuardOut, p Params) vec {
	var v vec
	for i := range v {
		l := int(floorF(p.BudgetA - g.HouseA[i]))
		if l > int(p.MaxA) {
			l = int(p.MaxA)
		}
		if l < 0 {
			l = 0
		}
		v[i] = l
	}
	return v
}

func floorF(f float64) float64 {
	i := float64(int64(f))
	if f < i {
		return i - 1
	}
	return i
}

// desire is the live decision: what the charger should be doing now.
func (m *Module) desire(now time.Time) desired {
	guardFresh := m.guardFresh(now)
	var limit int
	var circ vec
	if guardFresh {
		limit, circ = m.guard.LimitA, phaseLimits(m.guard, m.p)
	} else {
		limit = int(m.ceilingNow(now))
		circ = vec{limit, limit, limit}
	}
	if limit > int(m.p.MaxA) {
		limit = int(m.p.MaxA)
	}
	// House budget overload (internal/house): the house planner lowers the
	// EV first, down to pause. This outranks every path below, the stale
	// and deadline fallbacks included: the fuse is the hard limit.
	capNote := ""
	if c, ok := m.hub.EVCap(now); ok {
		if c < limit {
			limit = c
			capNote = fmt.Sprintf("; house budget overload caps at %d A", c)
		}
		for i := range circ {
			if circ[i] > c {
				circ[i] = c
			}
		}
	}
	if !m.plugged() {
		return desired{Mode: "idle", Reason: "car not plugged in"}
	}
	dep := m.nextDeparture(now)
	if m.sess != nil {
		dep = m.sess.Departure
	}
	if !now.Before(dep) {
		return desired{Mode: "idle", Reason: "past departure"}
	}
	if m.sess != nil && m.sessionKWh >= m.p.NeedKWh {
		return desired{Mode: "idle", Reason: "need met"}
	}
	if m.sess != nil && m.havePlan && m.plan.NeedKWh <= 0 {
		return desired{Mode: "idle", Reason: "need met"}
	}
	charge := func(fb, why string) desired {
		why += capNote
		if limit < int(m.p.MinA) {
			return desired{Mode: "pause", Circuit: circ, Reason: "no headroom under the budget (" + why + ")", Fallback: fb}
		}
		return desired{Mode: "charge", Amps: limit, Circuit: circ, Reason: why, Fallback: fb}
	}
	// Staleness: losing control or data must leave the car charging.
	_, priced := m.prices[now.Truncate(quarter).Unix()]
	linkLost := !m.x.linkUp && now.Sub(m.x.linkDownAt) >= linkGrace
	switch {
	case linkLost:
		return charge("stale", "Home Assistant link lost: charge now")
	case !guardFresh:
		return charge("stale", "grid reading stale: charge now at the forecast ceiling")
	case !priced || len(m.prices) == 0:
		return charge("stale", "no price for this quarter: charge now")
	case !m.sessKnown() || !m.havePlan:
		return charge("stale", "no session plan: charge now")
	case m.plan.AwaitingPrices && now.In(m.p.Loc).Hour() >= priceLateHour:
		return charge("stale", "tomorrow's prices are late: charge now")
	}
	if m.plan.DeadlineMode || (!m.plan.Feasible && !m.plan.AwaitingPrices) {
		return charge("deadline", "deadline backstop: plan cannot finish by departure, charge at the maximum allowed")
	}
	slot, in := m.slotAt(now)
	if !in {
		return desired{Mode: "pause", Circuit: circ, Reason: "outside the planned slots"}
	}
	a := int(slot.Amps)
	why := fmt.Sprintf("planned slot at %d A", a)
	if limit < a {
		a, why = limit, fmt.Sprintf("guard limits to %d A", limit)
	}
	why += capNote
	if a < int(m.p.MinA) {
		return desired{Mode: "pause", Circuit: circ, Reason: "no headroom (" + why + ")"}
	}
	return desired{Mode: "charge", Amps: a, Circuit: circ, Reason: why}
}

// guardFresh: the guard's inputs are current. The Equalizer path needs a
// fresh online reading; otherwise the grid meter must be fresh.
func (m *Module) guardFresh(now time.Time) bool {
	if !m.haveGuard {
		return false
	}
	if m.guard.Measured {
		return m.eqIsFresh(now)
	}
	return m.gridOK && now.Sub(m.x.gridAt) < m.x.stale
}

func (m *Module) sessKnown() bool { return m.sess != nil }

// control runs the executor on every tick. It is the only place that
// produces service calls (plus OnLink/OnCommandResult bookkeeping).
func (m *Module) control(now time.Time) {
	x := &m.x
	al := m.armedLive()
	if al != x.wasArmedLv && x.ready {
		if al {
			m.notifyNow("armed", "EV controller armed", "ev_control is LIVE and armed: it now drives the charger. The Car Start Charging automation must be disabled.")
		} else if x.wasArmedLv {
			m.notifyNow("disarmed", "EV controller disarmed", "ev_control is disarmed: the charger is resumed and the dynamic limits are released to Easee's defaults.")
		}
		x.charger, x.circuit = sent{}, sent{}
		x.fbNotified = ""
	}
	if x.ready {
		x.wasArmedLv = al
	}
	if !x.ready || !x.linkUp {
		return
	}
	if !al {
		m.release(now)
		return
	}
	d := m.desire(now)
	x.lastDesired = d
	if m.haveGuard && m.inBreach && x.breachEdge {
		x.breachEdge = false
		m.notifyKind("breach", "EV guard breach", fmt.Sprintf("Estimated phase current exceeds the %.0f A budget (A/B/C %.1f/%.1f/%.1f A). The limit is being lowered.",
			m.p.BudgetA, m.guard.PhaseA[0], m.guard.PhaseA[1], m.guard.PhaseA[2]), now)
	}
	if d.Fallback != "" && m.sess != nil {
		if key := fmt.Sprintf("%s@%d", d.Fallback, m.sess.Departure.Unix()); key != x.fbNotified {
			x.fbNotified = key
			title := "EV deadline fallback"
			if d.Fallback == "stale" {
				title = "EV stale-data fallback"
			}
			m.notifyNow("fallback", title, d.Reason)
		}
	}
	if x.target() == nil {
		m.notifyKind("noid", "EV controller cannot command", "The charger id is unknown (sensor.loaver_status has no id attribute and ev_charger_id is not set).", now)
		return
	}
	switch d.Mode {
	case "idle":
		m.release(now)
	case "pause":
		m.ensureCircuit(d, now)
		if m.status == "charging" && !x.pauseFly && now.Sub(x.pauseAt) >= pauseGap {
			x.pauseFly, x.pauseAt = true, now
			m.cmd("pause", pending{kind: "pause"})
		}
	case "charge":
		m.ensureCircuit(d, now)
		want := vec{d.Amps, d.Amps, d.Amps}
		if m.needSend(x.charger, want, now) {
			m.sendCharger(d.Amps, now)
		}
		if m.status == "charging" || x.pauseFly || now.Sub(x.pauseAt) < pauseGap || m.status == "completed" {
			return
		}
		ok := m.limitOK(x.charger, want, now) && m.limitOK(x.circuit, d.Circuit, now)
		if !ok && d.Fallback != "" && (x.charger.fails >= limitFailResume || x.circuit.fails >= limitFailResume) {
			ok = true // control of the limits is lost: charging at Easee's defaults beats an empty car at 06:30
		}
		if !ok {
			return
		}
		x.pauseFly, x.pauseAt = true, now
		if m.status == "awaiting_authorization" || m.status == "awaiting_start" {
			m.cmd("start", pending{kind: "start"})
		}
		m.cmd("resume", pending{kind: "resume"})
	}
}

func (m *Module) ensureCircuit(d desired, now time.Time) {
	if m.needSend(m.x.circuit, d.Circuit, now) {
		m.sendCircuit(d.Circuit, now, m.x.ttl, "circuit")
	}
}

// release hands control back to Easee: resume if we paused, then replace
// the dynamic limits with a high one that lapses after a minute.
func (m *Module) release(now time.Time) {
	x := &m.x
	if !x.st.Paused && !x.st.Holding {
		return
	}
	if x.releaseFly || now.Sub(x.releaseAt) < pauseGap && !x.releaseAt.IsZero() {
		return
	}
	if x.target() == nil {
		return
	}
	x.releaseAt, x.releaseFly = now, true
	if x.st.Paused {
		m.cmd("resume", pending{kind: "release_resume"})
	}
	if x.st.Holding {
		d := m.x.target()
		d["current"], d["time_to_live"] = releaseA, 1
		m.call("easee", "set_charger_dynamic_limit", d, pending{kind: "release_charger", cmd: fmt.Sprintf("release charger limit (%d A, 1 min)", releaseA)})
		m.sendCircuit(vec{releaseA, releaseA, releaseA}, now, time.Minute, "release_circuit")
	}
}

// Effects implements modules.Actor.
func (m *Module) Effects() []modules.Effect {
	e := m.x.effects
	m.x.effects = nil
	return e
}

// OnCommandResult implements modules.Actor.
func (m *Module) OnCommandResult(r modules.CommandResult) []modules.OutputUpdate {
	x := &m.x
	if r.CorrID >= corrNotify {
		if !r.OK {
			log.Printf("%s: notification call failed: %s", Name, r.Err)
		}
		return nil
	}
	p, ok := x.pend[r.CorrID]
	if !ok {
		return nil
	}
	delete(x.pend, r.CorrID)
	at := r.At
	if at.IsZero() {
		at = m.now
	}
	res := "ok"
	if !r.OK {
		res = "failed"
	}
	x.last = lastCmd{Cmd: p.cmd, Result: res, Err: r.Err, At: at}
	x.outDirty = true
	switch p.kind {
	case "pause":
		x.pauseFly = false
		if r.OK {
			x.st.Paused = true
		}
	case "resume", "start":
		if p.kind == "resume" {
			x.pauseFly = false
			if r.OK {
				x.st.Paused = false
			}
		}
	case "charger":
		x.charger.inflight = false
		if r.OK {
			x.charger.v, x.charger.known, x.charger.at, x.charger.fails = p.v, true, at, 0
		} else {
			x.charger.known = false
			x.charger.fails++
		}
	case "circuit":
		x.circuit.inflight = false
		if r.OK {
			x.circuit.v, x.circuit.known, x.circuit.at, x.circuit.fails = p.v, true, at, 0
		} else {
			x.circuit.known = false
			x.circuit.fails++
		}
	case "release_resume":
		if r.OK {
			x.st.Paused = false
		}
		x.releaseFly = false
	case "release_charger":
		if r.OK {
			x.charger = sent{}
		}
	case "release_circuit":
		x.circuit.inflight = false
		if r.OK {
			x.circuit = sent{}
			x.st.Holding = false
		}
		x.releaseFly = false
	}
	if !r.OK {
		m.notifyKind("fail_"+p.kind, "EV controller command failed", fmt.Sprintf("%s failed: %s", p.cmd, r.Err), at)
		if strings.HasPrefix(p.kind, "release") {
			x.releaseFly = false
		}
	}
	return m.execOutputs(at, false)
}

// OnLink implements modules.LinkAware.
func (m *Module) OnLink(up bool, now time.Time) []modules.OutputUpdate {
	x := &m.x
	if now.After(m.now) {
		m.now = now
	}
	if up {
		x.ready = true
		x.linkUp = true
		x.pauseFly, x.releaseFly = false, false
		x.charger.inflight, x.circuit.inflight = false, false
	} else {
		if x.linkUp || x.linkDownAt.IsZero() {
			x.linkDownAt = now
		}
		x.linkUp = false
		x.pend = map[uint64]pending{}
	}
	return nil
}

func (m *Module) modeString() string {
	if m.x.live {
		return "live"
	}
	return "shadow"
}

// execOutputs publishes the executor's entities (on change, or forced).
func (m *Module) execOutputs(now time.Time, force bool) []modules.OutputUpdate {
	x := &m.x
	var outs []modules.OutputUpdate
	al := m.armedLive()
	on := "off"
	if al {
		on = "on"
	}
	if force || x.outDirty || on != m.lastArmedPub {
		m.lastArmedPub = on
		outs = append(outs,
			modules.OutputUpdate{ObjectID: ObjMode, Time: now, StateString: m.modeString(),
				AttributesJSON: jsonStr(map[string]any{"armed_key": x.armedKey, "armed_live": al})},
			modules.OutputUpdate{ObjectID: ObjArmed, Time: now, StateString: on,
				AttributesJSON: jsonStr(map[string]any{"mode": m.modeString(), "armed_key": x.armedKey,
					"note": "on = LIVE and armed: disable the Car Start Charging automation"})})
	}
	if force || x.outDirty {
		x.outDirty = false
		l := x.last
		at := ""
		if !l.At.IsZero() {
			at = l.At.In(m.p.Loc).Format(time.RFC3339)
		}
		cmd := l.Cmd
		if cmd == "" {
			cmd = "none"
		}
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjLastCmd, Time: now, StateString: cmd,
			AttributesJSON: jsonStr(map[string]any{"result": l.Result, "error": l.Err, "at": at})})
		v := 0.0
		if x.charger.known {
			v = float64(x.charger.v[0])
		}
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjLimitForce, Time: now, Value: v,
			AttributesJSON: jsonStr(map[string]any{"paused": x.st.Paused, "holding": x.st.Holding,
				"circuit_a": []int{x.circuit.v[0], x.circuit.v[1], x.circuit.v[2]}, "circuit_known": x.circuit.known,
				"ttl_min": int(x.ttl / time.Minute), "desired": x.lastDesired.Mode, "reason": x.lastDesired.Reason,
				"fallback": x.lastDesired.Fallback, "charger_id_known": x.target() != nil})})
	}
	return outs
}
