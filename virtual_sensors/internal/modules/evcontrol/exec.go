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
// 0.6.17: the controller decides only WHEN to charge. The charger and the
// Easee Equalizer decide HOW MUCH (the Equalizer backs the charger off, and
// with phase mode auto switches to a single phase, on any phase the car
// uses). So while armed and live the only commands are
// easee.action_command start / resume / pause at planned slot boundaries
// (plus Charge now and the fallbacks), and, when the module starts or is
// armed, ONE release of the charger and circuit dynamic limits so no stale
// limit from an older version or a manual action lingers. It never writes
// a dynamic limit, a phase mode, or a breach alert. While armed, live and
// healthy it also postpones the Home Assistant dead-man timer.
//
// Easee integration v2026.11.0 services used: easee.action_command
// {charger_id, action_command}; easee.set_charger_dynamic_limit
// {charger_id, current, time_to_live (min)}; easee.set_circuit_dynamic_limit
// {charger_id, current_p1..p3, time_to_live (min)} (the two limit calls only
// in the one-shot release). charger_id is the `id` attribute of
// sensor.loaver_status ("EH5YL3HF").

// ArmedEntity is the second key.
const ArmedEntity = "input_boolean.ev_controller_armed"

// DeadmanEntity is the Home Assistant timer an automation uses to start a
// waiting car when this controller is dead or disabled (0.6.17).
const DeadmanEntity = "timer.ev_deadman"

// Output object ids added by the executor.
const (
	ObjMode       = "ev_controller_mode"
	ObjArmed      = "ev_controller_armed"
	ObjLastCmd    = "ev_last_command"
	ObjLimitForce = "ev_limit_in_force" // 0.6.17: always 0 A (the controller writes no limit); attributes keep the executor's status
)

const (
	defaultNotify = "notify.mobile_app_edmund15max"
	pauseGap      = 90 * time.Second // between pause/resume attempts
	notifyGap     = 15 * time.Minute // per notification kind
	linkGrace     = 2 * time.Minute
	releaseA      = 16 // the one-shot release: charger and circuit limit, 1 min TTL
	maxRelTries   = 3
	priceLateHour = 14 // tomorrow's prices should have arrived by this local hour
	deadmanEvery  = 30 * time.Minute
	deadmanFor    = "00:31:00" // the timer runs 30 min; each call restarts it with 31
	corrNotify    = uint64(1) << 40
)

type pending struct {
	kind string // pause, resume, start, release_resume, rel_charger, rel_circuit, deadman
	cmd  string
}

// execPersist is the executor's restart state: what we may be holding.
type execPersist struct {
	Paused  bool `json:"paused"`            // we commanded pause and it is in force
	Holding bool `json:"holding,omitempty"` // legacy (pre-0.6.17 dynamic limits); ignored
}

type exec struct {
	live       bool
	notify     string
	chargerOpt string
	chargerID  string

	armedKey   bool
	ready      bool // link up at least once (states loaded)
	linkUp     bool
	linkDownAt time.Time
	wasArmedLv bool
	fbNotified string
	lastNotify map[string]time.Time
	corr       uint64
	notifyC    uint64
	pend       map[uint64]pending
	effects    []modules.Effect
	st         execPersist
	pauseAt    time.Time
	pauseFly   bool
	releaseAt  time.Time
	releaseFly bool
	// The one-shot limit release (module start / arm).
	relDone  bool
	relFail  bool
	relFly   int
	relTries int
	relAt    time.Time
	// Dead-man postponement: last timer.start sent (zero: due at once).
	deadmanAt   time.Time
	deadmanOK   time.Time // last successful postponement
	last        lastCmd
	lastDesired desired
	outDirty    bool
}

type lastCmd struct {
	Cmd, Result, Err string
	At               time.Time
}

type desired struct {
	Mode     string // idle, pause, charge, full
	Reason   string
	Fallback string // "", "deadline", "stale"
}

func newExec() exec {
	return exec{notify: defaultNotify, lastNotify: map[string]time.Time{}, pend: map[uint64]pending{}}
}

func (m *Module) armedLive() bool { return m.x.live && m.x.armedKey }

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
	m.callQuiet(domain, service, data, p)
	m.x.last = lastCmd{Cmd: p.cmd, Result: "sent", At: m.now}
	m.x.outDirty = true
}

// callQuiet is call without touching ev_last_command (the dead-man timer
// would otherwise overwrite the last charger command every 30 minutes).
func (m *Module) callQuiet(domain, service string, data map[string]any, p pending) {
	m.x.corr++
	m.x.pend[m.x.corr] = p
	m.x.effects = append(m.x.effects, modules.Effect{Kind: modules.EffectCallService, Domain: domain, Service: service, Data: data, CorrID: m.x.corr})
}

func (m *Module) cmd(action string, p pending) {
	d := m.x.target()
	d["action_command"] = action
	p.cmd = action
	m.call("easee", "action_command", d, p)
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
	// 0.6.12: the second channel is a Logbook entry, not a persistent
	// notification (those show to every HA user; Edmund's choice).
	m.x.effects = append(m.x.effects, modules.Effect{Kind: modules.EffectCallService, Domain: "logbook", Service: "log",
		Data: map[string]any{"name": title, "message": text, "entity_id": "sensor.ev_last_command"}, CorrID: corrNotify + m.x.notifyC, Tag: "ev_controller_" + kind})
}

// desire is the live decision: what the charger should be doing now. It
// says WHEN only; how much current is the charger's and Equalizer's business.
func (m *Module) desire(now time.Time) desired {
	if !m.plugged() {
		return desired{Mode: "idle", Reason: "car not plugged in"}
	}
	if m.cn.On {
		// Charge-now override: the plan, need and departure are ignored.
		return desired{Mode: "charge", Reason: "charge now"}
	}
	if m.full {
		return desired{Mode: "full", Reason: "car reports charging complete"}
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
	charge := func(fb, why string) desired { return desired{Mode: "charge", Reason: why, Fallback: fb} }
	// Staleness: losing control or data must leave the car charging.
	_, priced := m.prices[now.Truncate(quarter).Unix()]
	linkLost := !m.x.linkUp && now.Sub(m.x.linkDownAt) >= linkGrace
	switch {
	case linkLost:
		return charge("stale", "Home Assistant link lost: charge now")
	case !priced || len(m.prices) == 0:
		return charge("stale", "no price for this quarter: charge now")
	case !m.sessKnown() || !m.havePlan:
		return charge("stale", "no session plan: charge now")
	case m.plan.AwaitingPrices && now.In(m.p.Loc).Hour() >= priceLateHour:
		return charge("stale", "tomorrow's prices are late: charge now")
	}
	if m.plan.DeadlineMode || (!m.plan.Feasible && !m.plan.AwaitingPrices) {
		return charge("deadline", "deadline backstop: plan cannot finish by departure, charge in every usable slot")
	}
	slot, in := m.slotAt(now)
	if !in {
		return desired{Mode: "pause", Reason: "outside the planned slots"}
	}
	return desired{Mode: "charge", Reason: fmt.Sprintf("planned slot (~%.0f A, %.1f kW deliverable)", slot.Amps, slot.KW)}
}

func (m *Module) sessKnown() bool { return m.sess != nil }

// control runs the executor on every tick. It is the only place that
// produces service calls (plus OnLink/OnCommandResult bookkeeping).
func (m *Module) control(now time.Time) {
	x := &m.x
	al := m.armedLive()
	armEdge := al && !x.wasArmedLv && x.ready
	disarmEdge := !al && x.wasArmedLv && x.ready
	if armEdge {
		m.notifyNow("armed", "EV controller armed", "ev_control is LIVE and armed: it now starts and pauses the charger at the planned times. The Car Start Charging automation must be disabled.")
		// Module start or arming: one limit release, and a dead-man
		// postponement at once.
		x.relDone, x.relFail, x.relTries, x.relFly, x.relAt = false, false, 0, 0, time.Time{}
		x.deadmanAt = time.Time{}
		x.fbNotified = ""
	}
	if disarmEdge {
		x.deadmanAt = time.Time{}
		x.fbNotified = ""
	}
	if x.ready {
		x.wasArmedLv = al
	}
	if !x.ready || !x.linkUp {
		if disarmEdge {
			m.notifyDisarm(false, x.st.Paused, "Home Assistant link is down, so no command could be sent")
		}
		return
	}
	if !al {
		sent := m.releasePause(now)
		if disarmEdge {
			m.notifyDisarm(sent, x.st.Paused, "")
		}
		return
	}
	d := m.desire(now)
	x.lastDesired = d
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
	// Healthy (link up, charger known) and armed and live: postpone the
	// dead-man timer and, once, release any dynamic limit.
	m.deadman(now)
	m.limitRelease(now)
	switch d.Mode {
	case "idle":
		m.releasePause(now)
	case "full":
		// No charger commands at all.
	case "pause":
		if m.status == "charging" && !x.pauseFly && now.Sub(x.pauseAt) >= pauseGap {
			x.pauseFly, x.pauseAt = true, now
			m.cmd("pause", pending{kind: "pause"})
		}
	case "charge":
		if m.status == "completed" && m.cn.On {
			// Pressed while the car reads completed: try start/resume once,
			// not every 90 s for the rest of the timeout.
			if m.cn.Kicked {
				return
			}
		} else if m.status == "completed" {
			return
		}
		if m.status == "charging" || x.pauseFly || now.Sub(x.pauseAt) < pauseGap {
			return
		}
		x.pauseFly, x.pauseAt = true, now
		if m.cn.On && m.status == "completed" {
			m.cn.Kicked = true
			m.cmd("start", pending{kind: "start"})
		}
		if m.status == "awaiting_authorization" || m.status == "awaiting_start" {
			m.cmd("start", pending{kind: "start"})
		}
		m.cmd("resume", pending{kind: "resume"})
	}
}

// notifyDisarm pushes the disarm edge. It claims a resume only when one was
// actually sent.
func (m *Module) notifyDisarm(sent, paused bool, why string) {
	text := "ev_control is disarmed: it will not start or pause the charger."
	switch {
	case sent:
		text += " It had paused the charger, so a resume command was sent."
	case paused:
		text += " It had paused the charger and has NOT resumed it yet"
		if why != "" {
			text += " (" + why + ")"
		}
		text += "; it sends the resume as soon as it can."
	default:
		text += " No resume was needed: it had not paused the charger."
	}
	m.notifyNow("disarmed", "EV controller disarmed", text)
}

// deadman postpones the Home Assistant dead-man timer: timer.start with a
// 31 min duration every 30 min, and at once on start/arm. Only called while
// armed, live and healthy.
func (m *Module) deadman(now time.Time) {
	x := &m.x
	if !x.deadmanAt.IsZero() && now.Sub(x.deadmanAt) < deadmanEvery {
		return
	}
	x.deadmanAt = now
	m.callQuiet("timer", "start", map[string]any{"entity_id": DeadmanEntity, "duration": deadmanFor}, pending{kind: "deadman", cmd: "postpone " + DeadmanEntity})
}

// limitRelease sends the ONE release of the charger and circuit dynamic
// limits (16 A, 1 min) after the module starts or is armed. A failed attempt
// is retried a few times; a success is never repeated.
func (m *Module) limitRelease(now time.Time) {
	x := &m.x
	if x.relDone || x.relFly > 0 || x.relTries >= maxRelTries || (!x.relAt.IsZero() && now.Sub(x.relAt) < pauseGap) {
		return
	}
	x.relAt, x.relFail = now, false
	x.relTries++
	x.relFly = 2
	d := x.target()
	d["current"], d["time_to_live"] = releaseA, 1
	m.call("easee", "set_charger_dynamic_limit", d, pending{kind: "rel_charger", cmd: fmt.Sprintf("release charger limit (%d A, 1 min)", releaseA)})
	c := x.target()
	c["current_p1"], c["current_p2"], c["current_p3"] = releaseA, releaseA, releaseA
	c["time_to_live"] = 1
	m.call("easee", "set_circuit_dynamic_limit", c, pending{kind: "rel_circuit", cmd: fmt.Sprintf("release circuit limit (%d A, 1 min)", releaseA)})
}

// releasePause resumes the charger if we paused it (idle, or disarmed). It
// reports whether a resume command was issued on this call.
func (m *Module) releasePause(now time.Time) bool {
	x := &m.x
	if !x.st.Paused {
		return false
	}
	if x.releaseFly || now.Sub(x.releaseAt) < pauseGap && !x.releaseAt.IsZero() {
		return false
	}
	if x.target() == nil {
		return false
	}
	x.releaseAt, x.releaseFly = now, true
	m.cmd("resume", pending{kind: "release_resume"})
	return true
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
	if p.kind == "deadman" {
		if r.OK {
			x.deadmanOK = at
		} else {
			// Retry soon rather than in 30 min: the timer is 30 min long.
			x.deadmanAt = at.Add(pauseGap - deadmanEvery)
			m.notifyKind("fail_deadman", "EV controller command failed", fmt.Sprintf("%s failed: %s", p.cmd, r.Err), at)
		}
		return nil
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
	case "resume":
		x.pauseFly = false
		if r.OK {
			x.st.Paused = false
		}
	case "release_resume":
		if r.OK {
			x.st.Paused = false
		}
		x.releaseFly = false
		x.pauseFly = false
	case "rel_charger", "rel_circuit":
		if x.relFly > 0 {
			x.relFly--
		}
		if !r.OK {
			x.relFail = true
		}
		if x.relFly == 0 && !x.relFail {
			x.relDone = true
		}
	}
	if !r.OK {
		m.notifyKind("fail_"+p.kind, "EV controller command failed", fmt.Sprintf("%s failed: %s", p.cmd, r.Err), at)
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
		x.pauseFly, x.releaseFly, x.relFly = false, false, 0
	} else {
		if x.linkUp || x.linkDownAt.IsZero() {
			x.linkDownAt = now
		}
		x.linkUp = false
		x.pend = map[uint64]pending{}
	}
	return nil
}

func (x *exec) deadmanAtStr(loc *time.Location) string {
	if x.deadmanOK.IsZero() {
		return ""
	}
	return x.deadmanOK.In(loc).Format(time.RFC3339)
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
		// 0.6.17: the controller writes no dynamic limit, so this is always
		// 0 A; the attributes keep the executor's status for the dashboard.
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjLimitForce, Time: now, Value: 0,
			AttributesJSON: jsonStr(map[string]any{"paused": x.st.Paused, "limits_written": false,
				"release_sent": x.relDone, "desired": x.lastDesired.Mode, "reason": x.lastDesired.Reason,
				"fallback": x.lastDesired.Fallback, "charger_id_known": x.target() != nil,
				"deadman_postponed_at": x.deadmanAtStr(m.p.Loc)})})
	}
	return outs
}
