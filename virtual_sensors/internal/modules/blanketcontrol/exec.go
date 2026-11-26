package blanketcontrol

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hpcontrol"
	"virtual_sensors/internal/modules/hwcontrol/planner"
)

// The executor. The plant: an electric blanket whose single push button
// TOGGLES it, which starts OFF whenever its power is restored and switches
// itself off 3 h after being turned on. A SwitchBot Bot presses the button
// over Bluetooth at the edge of range: a press shows on the plug 8-125 s
// later, a press that does not get through is lost (not queued), a delayed
// press can land late. So every ON is a START: plug off, wait 10 s, plug on,
// wait 5 s, press once, wait for power. Starting from a power cycle means a
// press can only turn the blanket ON, never toggle it off; a lost press is
// retried by repeating the WHOLE start, never by a bare second press.
// Nothing here runs unless BOTH keys are on (bc_mode live AND
// input_boolean.blanket_controller_armed).

const (
	cycleOff     = 10 * time.Second  // plug off this long before it is switched on again
	cycleOn      = 5 * time.Second   // plug on this long before the press
	windowLen    = 170 * time.Minute // 2 h 50: assumed on-time after a confirmed start
	ownCutoff    = 3 * time.Hour     // the blanket's own switch-off
	refreshLead  = 5 * time.Minute   // refresh start this long before the window ends
	lateWindow   = 5 * time.Minute   // a drop below the threshold this soon = a late press toggled it
	retryEvery   = 10 * time.Minute  // full start retry while FAILED
	attemptGap   = 30 * time.Second  // pause after a failed plug command before the next attempt
	ackTimeout   = 20 * time.Second
	offReassert  = 60 * time.Second
	notifyGap    = 15 * time.Minute
	deadmanEvery = 30 * time.Minute
	deadmanFor   = "00:31:00"
	deadmanRetry = 90 * time.Second
)

type phase int

const (
	phIdle    phase = iota
	phOff           // plug-off sent, waiting for the answer and 10 s
	phOn            // plug-on sent, waiting for the answer and 5 s
	phConfirm       // pressed, waiting for power
	phWait          // waiting to begin the next attempt after a failed command
)

type pending struct {
	kind    string // cycle_off, cycle_on, press, off, deadman
	attempt int
	cmd     string
}

type lastCmd struct {
	Cmd, Result, Err string
	At               time.Time
}

// execPersist is the executor's restart state.
type execPersist struct {
	On             bool      `json:"on,omitempty"`
	OnSince        time.Time `json:"on_since,omitempty"`
	WindowEnd      time.Time `json:"window_end,omitempty"`
	BlockStart     time.Time `json:"block_start,omitempty"`
	MinOnEnd       time.Time `json:"min_on_end,omitempty"`
	Failed         bool      `json:"failed,omitempty"`
	FailedNotified bool      `json:"failed_notified,omitempty"`
	NextRetry      time.Time `json:"next_retry,omitempty"`
	SilentStarts   int       `json:"silent_starts,omitempty"`
	LastStart      time.Time `json:"last_start,omitempty"`
	LastKind       string    `json:"last_kind,omitempty"`
}

type desired struct {
	On     bool
	Kind   string // plan, hysteresis, backstop, failsafe, floor, cap, hold
	Reason string
	Force  bool // bypasses the start cap (backstop, fail-safe)
	Hard   bool // the maximum temperature: off at once, ignoring the minimum on-block
}

type exec struct {
	st execPersist // LastStart/LastKind live here

	ready, linkUp bool
	wasArm        bool

	// believed blanket state
	on                             bool
	onSince, windowEnd, blockStart time.Time
	minOnEnd                       time.Time
	lateUntil                      time.Time
	lateFlag                       bool
	lateCount                      int
	pendingKind                    string
	silentStarts                   int
	// start procedure
	phase      phase
	kind       string
	tries      int
	maxTries   int
	attempt    int
	contBlock  bool
	acked      bool
	sentAt     time.Time
	ackAt      time.Time
	plugOnAt   time.Time
	pressAt    time.Time
	waitUntil  time.Time
	episodeEnd time.Time
	// failure
	failed, failedNotified bool
	failWhy                string
	nextRetry              time.Time

	manualReq   bool
	lastOffCmd  time.Time
	lastPlugCmd time.Time
	bsLatch     bool
	note        string
	desired     desired

	deadmanAt, deadmanOK time.Time
	lastNotify           map[string]time.Time
	corr, notifyC, fcorr uint64
	pend                 map[uint64]pending
	effects              []modules.Effect
	last                 lastCmd
}

func newExec() exec {
	return exec{lastNotify: map[string]time.Time{}, pend: map[uint64]pending{}}
}

func (m *Module) persistExec() execPersist {
	x := &m.x
	p := x.st
	p.On, p.OnSince, p.WindowEnd, p.BlockStart, p.MinOnEnd = x.on, x.onSince, x.windowEnd, x.blockStart, x.minOnEnd
	p.Failed, p.FailedNotified, p.NextRetry, p.SilentStarts = x.failed, x.failedNotified, x.nextRetry, x.silentStarts
	return p
}

func (m *Module) restoreExec() {
	x, p := &m.x, m.x.st
	x.on, x.onSince, x.windowEnd, x.blockStart, x.minOnEnd = p.On, p.OnSince, p.WindowEnd, p.BlockStart, p.MinOnEnd
	x.failed, x.failedNotified, x.nextRetry, x.silentStarts = p.Failed, p.FailedNotified, p.NextRetry, p.SilentStarts
	if x.failed {
		x.failWhy = "failed before the restart"
	}
}

// ---- service calls ----

func (m *Module) call(domain, service string, data map[string]any, p pending) {
	x := &m.x
	x.corr++
	x.pend[x.corr] = p
	x.effects = append(x.effects, modules.Effect{Kind: modules.EffectCallService, Domain: domain, Service: service, Data: data, CorrID: x.corr, Tag: "blanket_" + p.kind})
}

func (m *Module) plugCmd(service, kind, cmd string, now time.Time) {
	m.x.lastPlugCmd = now
	m.x.last = lastCmd{Cmd: cmd, Result: "sent", At: now}
	m.call("switch", service, map[string]any{"entity_id": m.plugEnt}, pending{kind: kind, attempt: m.x.attempt, cmd: cmd})
}

func (m *Module) notifyNow(kind, title, text string) {
	dom, svc, _ := strings.Cut(m.notify, ".")
	x := &m.x
	x.notifyC++
	x.effects = append(x.effects, modules.Effect{Kind: modules.EffectCallService, Domain: dom, Service: svc,
		Data: map[string]any{"title": title, "message": text}, CorrID: notifyCorr + x.notifyC})
	m.logbook(kind, title, text)
}

func (m *Module) logbook(kind, title, text string) {
	x := &m.x
	x.notifyC++
	x.effects = append(x.effects, modules.Effect{Kind: modules.EffectCallService, Domain: "logbook", Service: "log",
		Data: map[string]any{"name": title, "message": text, "entity_id": "sensor." + ObjState}, CorrID: notifyCorr + x.notifyC, Tag: "blanket_" + kind})
}

func (m *Module) notifyKind(kind, title, text string, now time.Time) {
	if t, ok := m.x.lastNotify[kind]; ok && now.Sub(t) < notifyGap {
		return
	}
	m.x.lastNotify[kind] = now
	m.notifyNow(kind, title, text)
}

// ---- the decision: should heat be on now? ----

func (m *Module) desire(now time.Time) desired {
	x := &m.x
	known := m.t.seen && !m.t.at.IsZero()
	fresh := m.tempFresh(now)
	T := m.t.v
	if m.hotFresh(now) && m.hot.v >= m.hotMax {
		return desired{Kind: "cap", Hard: true, Reason: fmt.Sprintf("hot spot %.1f C is at or above its maximum %.1f C: never heat", m.hot.v, m.hotMax)}
	}
	if known && T >= m.p.MaxC {
		// The hard cap overrides the planner, the backstop, start now and the fail-safe.
		return desired{Kind: "cap", Hard: true, Reason: fmt.Sprintf("%.1f C is at or above the maximum %.1f C: never heat", T, m.p.MaxC)}
	}
	if !known {
		if now.Sub(m.bootAt) < bootGrace {
			return desired{On: x.on, Kind: "hold", Reason: "waiting for the first storage temperature reading"}
		}
		return desired{On: x.silentStarts == 0 || x.on || x.phase != phIdle, Kind: "failsafe", Force: true,
			Reason: "fail-safe: no storage temperature reading at all; heating one on-window"}
	}
	if !fresh {
		if T >= m.p.TargetC {
			return desired{Kind: "failsafe", Reason: fmt.Sprintf("temperature sensor silent; last reading %.1f C is not below the target %.1f C: not heating", T, m.p.TargetC)}
		}
		return desired{On: x.silentStarts == 0 || x.on || x.phase != phIdle, Kind: "failsafe", Force: true,
			Reason: fmt.Sprintf("fail-safe: temperature sensor silent or unavailable; last reading %.1f C is below the target: one on-window only", T)}
	}
	if T <= m.backstop {
		x.bsLatch = true
	} else if T >= m.backstop+0.5 {
		x.bsLatch = false
	}
	if x.bsLatch {
		return desired{On: true, Kind: "backstop", Force: true, Reason: fmt.Sprintf("frost backstop: %.1f C at or below %.1f C, heating regardless of price", T, m.backstop)}
	}
	if T < m.p.FloorC {
		return desired{On: true, Kind: "floor", Reason: fmt.Sprintf("%.1f C is below the floor %.1f C", T, m.p.FloorC)}
	}
	if m.havePlan && now.Sub(m.planAt) < 30*time.Minute {
		if s, ok := m.slotAt(now); ok {
			return desired{On: s.On, Kind: "plan", Reason: fmt.Sprintf("planned slot (%.1f C, predicted %.1f C at slot end)", T, s.TEnd)}
		}
	}
	return m.hysteresis(T)
}

func (m *Module) hysteresis(T float64) desired {
	if T < m.p.FloorC+1 {
		m.hystOn = true
	} else if T >= math.Min(m.p.FloorC+1.5, m.p.MaxC-0.5) {
		m.hystOn = false
	}
	why := "no usable plan (prices or outdoor temperature missing): hysteresis around floor+1 C"
	return desired{On: m.hystOn, Kind: "hysteresis", Reason: why}
}

// wantedAt: is heat still wanted at time t (for the refresh decision)?
func (m *Module) wantedAt(t time.Time, d desired) bool {
	if d.Kind == "plan" {
		if s, ok := m.slotAt(t); ok {
			return s.On
		}
	}
	return d.On
}

// ---- the control loop ----

func (m *Module) plugKnown() bool { return m.plugState == "on" || m.plugState == "off" }

func (m *Module) minOnDur() time.Duration { return time.Duration(m.p.MinOnSlots) * quarter }

// control runs every tick. It is the only place that starts procedures.
func (m *Module) control(now time.Time) {
	x := &m.x
	d := m.desire(now)
	x.desired = d
	al := m.armedLive()
	armEdge := al && !x.wasArm && x.ready
	disarmEdge := !al && x.wasArm && x.ready
	if armEdge {
		m.notifyNow("armed", "Blanket controller armed", "blanket_control is LIVE and armed: it now switches the plug and presses the blanket button. The HA dead-man timer is postponed every 30 min.")
		x.deadmanAt = time.Time{}
	}
	if disarmEdge {
		x.deadmanAt = time.Time{}
		x.phase = phIdle
		x.pend = map[uint64]pending{}
		m.logbook("disarmed", "Blanket controller disarmed", "blanket_control no longer commands the plug or the Bot; the dead-man timer will run out and the Home Assistant fallback takes over.")
	}
	if x.ready {
		x.wasArm = al
	}
	if !al || !x.ready || !x.linkUp {
		return
	}
	m.deadman(now)

	// the blanket's own 3 h cut-off has passed
	if x.on && now.After(x.onSince.Add(ownCutoff)) {
		x.on = false
		log.Printf("%s: the blanket's own 3 h cut-off has passed: assumed off", Name)
	}
	if x.on && x.lateCount > 0 && now.After(x.lateUntil) {
		x.lateCount = 0
	}
	if x.phase == phWait && !now.Before(x.waitUntil) {
		m.beginAttempt(now)
		return
	}

	if x.manualReq {
		x.manualReq = false
		if d.Hard {
			x.note = "start now refused: " + d.Reason
			m.logbook("start_now_refused", "Blanket start now refused", d.Reason)
		} else if x.phase == phIdle && m.plugKnown() {
			m.beginStart(now, "manual", m.tries)
			return
		} else {
			x.note = "start now ignored: a start is already running or the plug is unavailable"
		}
	}
	if x.lateFlag {
		x.lateFlag = false
		if x.on {
			x.on = false
			x.lateCount++
			log.Printf("%s: power fell below the threshold within %s of a confirmed start (%d): a delayed press landed; restarting", Name, lateWindow, x.lateCount)
			if x.lateCount >= m.tries {
				m.episodeFailed(now, fmt.Sprintf("the blanket switched itself off within %d min of %d starts in a row", int(lateWindow.Minutes()), x.lateCount))
				return
			}
			x.pendingKind = "late"
		}
	}

	if x.phase != phIdle {
		if !d.On && (d.Hard || !now.Before(x.minOnEnd)) {
			log.Printf("%s: start cancelled (%s)", Name, d.Reason)
			x.phase = phIdle
			x.episodeEnd = now
			m.sendOff(now)
			return
		}
		m.advanceStart(now)
		return
	}

	if !d.On {
		x.note = d.Reason
		switch {
		case x.on:
			if d.Hard || !now.Before(x.minOnEnd) {
				m.sendOff(now)
			} else {
				x.note = "holding the minimum on-block until " + x.minOnEnd.In(m.p.Loc).Format("15:04")
			}
		case m.plugState == "on" && now.Sub(x.lastOffCmd) >= offReassert:
			m.sendOff(now) // off is cheap and certain: assert it
		}
		return
	}
	x.note = ""
	if !m.plugKnown() {
		x.note = "heat wanted but the plug is " + m.plugState
		return
	}
	if x.on {
		if !now.Before(x.windowEnd.Add(-refreshLead)) && m.wantedAt(x.windowEnd, d) && !(!m.tempFresh(now) && x.silentStarts > 0) {
			m.startGate(now, d, "refresh")
		}
		return
	}
	kind := "start"
	if x.pendingKind != "" {
		kind = x.pendingKind
	}
	if x.failed {
		kind = "retry"
		if now.Before(x.nextRetry) {
			x.note = "failed; next full start at " + x.nextRetry.In(m.p.Loc).Format("15:04")
			return
		}
	}
	m.startGate(now, d, kind)
}

func (m *Module) startGate(now time.Time, d desired, kind string) {
	x := &m.x
	if kind != "refresh" && !x.episodeEnd.IsZero() && now.Sub(x.episodeEnd) < attemptGap {
		return
	}
	if kind != "manual" && kind != "retry" && !d.Force && m.day(now).Starts >= m.p.MaxStartsDay {
		x.note = fmt.Sprintf("start cap reached (%d today): not starting", m.p.MaxStartsDay)
		return
	}
	if !m.tempFresh(now) {
		if x.silentStarts > 0 && kind != "manual" {
			x.note = "temperature sensor silent: one on-window already used"
			return
		}
		x.silentStarts++
	}
	tries := m.tries
	if kind == "retry" {
		tries = 1
	}
	m.beginStart(now, kind, tries)
}

func (m *Module) beginStart(now time.Time, kind string, tries int) {
	x := &m.x
	x.kind, x.maxTries, x.tries = kind, tries, 0
	x.contBlock = x.on && kind == "refresh"
	if !x.contBlock {
		x.minOnEnd = now.Add(m.minOnDur())
	}
	x.on = false
	x.pendingKind = ""
	x.note = ""
	log.Printf("%s: start (%s)", Name, kind)
	m.beginAttempt(now)
}

func (m *Module) beginAttempt(now time.Time) {
	x := &m.x
	x.tries++
	x.attempt++
	x.phase, x.acked, x.sentAt = phOff, false, now
	m.day(now).Starts++
	m.plugCmd("turn_off", "cycle_off", fmt.Sprintf("plug off (start %s, try %d)", x.kind, x.tries), now)
}

func (m *Module) advanceStart(now time.Time) {
	x := &m.x
	switch x.phase {
	case phOff:
		if !x.acked {
			if now.Sub(x.sentAt) > ackTimeout {
				m.attemptFailed(now, "plug off: no answer", attemptGap)
			}
			return
		}
		if now.Sub(x.ackAt) >= cycleOff {
			x.phase, x.acked, x.sentAt, x.plugOnAt = phOn, false, now, now
			m.plugCmd("turn_on", "cycle_on", fmt.Sprintf("plug on (start %s, try %d)", x.kind, x.tries), now)
		}
	case phOn:
		if !x.acked {
			if now.Sub(x.sentAt) > ackTimeout {
				m.attemptFailed(now, "plug on: no answer", attemptGap)
			}
			return
		}
		if now.Sub(x.ackAt) >= cycleOn {
			if m.plugState == "unavailable" || m.plugState == "unknown" {
				m.attemptFailed(now, "plug unavailable before the press", attemptGap)
				return
			}
			x.phase, x.pressAt = phConfirm, now
			m.day(now).Presses++
			x.last = lastCmd{Cmd: fmt.Sprintf("press (start %s, try %d)", x.kind, x.tries), Result: "sent", At: now}
			m.call("switch", "turn_on", map[string]any{"entity_id": m.botEnt}, pending{kind: "press", attempt: x.attempt, cmd: x.last.Cmd})
		}
	case phConfirm:
		if m.checkConfirm(now) {
			return
		}
		if m.plugState == "unavailable" || m.plugState == "off" {
			m.attemptFailed(now, "plug "+m.plugState+" during the start", attemptGap)
			return
		}
		if now.Sub(x.pressAt) >= m.confirm {
			m.attemptFailed(now, fmt.Sprintf("no power above %.0f W within %d s of the press", m.onW, int(m.confirm.Seconds())), 0)
		}
	}
}

// checkConfirm: power above the threshold, read after the plug came back on.
func (m *Module) checkConfirm(now time.Time) bool {
	x := &m.x
	if x.phase != phConfirm || !m.pw.ok || m.pw.v <= m.onW || m.pw.at.Before(x.plugOnAt) {
		return false
	}
	at := m.pw.at
	if at.After(now) {
		at = now
	}
	m.startSucceeded(at)
	return true
}

func (m *Module) startSucceeded(t time.Time) {
	x := &m.x
	x.phase = phIdle
	x.on, x.onSince, x.windowEnd = true, t, t.Add(windowLen)
	if !x.contBlock {
		x.blockStart, x.minOnEnd = t, t.Add(m.minOnDur())
	}
	x.lateUntil, x.lateFlag = t.Add(lateWindow), false
	x.st.LastStart, x.st.LastKind = t, x.kind
	x.episodeEnd = t
	x.last = lastCmd{Cmd: x.last.Cmd, Result: "confirmed", At: t}
	log.Printf("%s: start confirmed (%s, try %d): on until about %s", Name, x.kind, x.tries, x.windowEnd.In(m.p.Loc).Format("15:04"))
	if x.failed {
		x.failed, x.failedNotified, x.failWhy = false, false, ""
		m.notifyNow("recovered", "Blanket controller recovered", fmt.Sprintf("The blanket started again (power above %.0f W after %s, try %d).", m.onW, x.kind, x.tries))
	}
	x.effects = append(x.effects, modules.Effect{Kind: modules.EffectPersistNow})
	m.lastPlanAt = time.Time{}
}

func (m *Module) attemptFailed(now time.Time, why string, delay time.Duration) {
	x := &m.x
	log.Printf("%s: start attempt %d/%d failed: %s", Name, x.tries, x.maxTries, why)
	x.failWhy = why
	if x.tries < x.maxTries {
		if delay > 0 {
			x.phase, x.waitUntil = phWait, now.Add(delay)
			return
		}
		m.beginAttempt(now)
		return
	}
	m.episodeFailed(now, why)
}

func (m *Module) episodeFailed(now time.Time, why string) {
	x := &m.x
	x.phase, x.on, x.minOnEnd = phIdle, false, time.Time{}
	x.failed, x.nextRetry, x.episodeEnd = true, now.Add(retryEvery), now
	x.failWhy = why
	if !x.failedNotified {
		x.failedNotified = true
		m.notifyNow("failed", "Blanket controller FAILED", fmt.Sprintf("The blanket could not be started (%s). The dahlia tubers are not being heated. A full start is retried every %d min.", why, int(retryEvery.Minutes())))
	}
	x.effects = append(x.effects, modules.Effect{Kind: modules.EffectPersistNow})
}

func (m *Module) sendOff(now time.Time) {
	x := &m.x
	x.on, x.minOnEnd, x.lateFlag = false, time.Time{}, false
	x.lastOffCmd = now
	m.plugCmd("turn_off", "off", "plug off", now)
	x.effects = append(x.effects, modules.Effect{Kind: modules.EffectPersistNow})
}

// startNow is the "start now" button: a start through the full protocol,
// only while armed and live, never at or above the maximum temperature.
func (m *Module) startNow(now time.Time) {
	if !m.armedLive() {
		why := "the controller is not armed and live"
		m.x.note = "start now not started: " + why
		m.notifyNow("start_now_disarmed", "Blanket start now not started", "Start now did nothing: "+why+". Arm it (input_boolean.blanket_controller_armed, bc_mode live) or switch the blanket yourself.")
		return
	}
	log.Printf("%s: start now requested (button)", Name)
	m.x.manualReq = true
}

// ---- input hooks ----

func (m *Module) onPlug(prev string, u modules.StateUpdate) {
	x := &m.x
	lost := u.State == "off" || u.State == "unavailable" || u.State == "unknown"
	if x.on && lost && u.Time.After(x.onSince) && (x.lastPlugCmd.IsZero() || u.Time.Sub(x.lastPlugCmd) > 20*time.Second) {
		x.on = false
		x.pendingKind = "plug_lost"
		log.Printf("%s: plug went %s while the blanket was on: the blanket is OFF", Name, u.State)
		if !u.Replay && m.armedLive() {
			m.logbook("plug_lost", "Blanket plug lost", fmt.Sprintf("The plug went %s (power loss or switched off): the blanket is off; it is started again if heat is wanted.", u.State))
		}
	}
}

func (m *Module) onPower(u modules.StateUpdate) {
	x := &m.x
	if u.Replay {
		return
	}
	m.checkConfirm(u.Time)
	if x.on && x.phase == phIdle && u.Available && u.Value <= m.onW && u.Time.After(x.onSince) && !u.Time.After(x.lateUntil) {
		x.lateFlag = true
	}
}

// ---- dead-man ----

func (m *Module) deadman(now time.Time) {
	x := &m.x
	if x.failed {
		return // not healthy: let the timer run out so the HA fallback acts
	}
	if !x.deadmanAt.IsZero() && now.Sub(x.deadmanAt) < deadmanEvery {
		return
	}
	x.deadmanAt = now
	m.call("timer", "start", map[string]any{"entity_id": DeadmanEntity, "duration": deadmanFor}, pending{kind: "deadman", cmd: "postpone " + DeadmanEntity})
}

// ---- host interfaces ----

// Effects implements modules.Actor.
func (m *Module) Effects() []modules.Effect {
	e := m.x.effects
	m.x.effects = nil
	return e
}

// OnCommandResult implements modules.Actor.
func (m *Module) OnCommandResult(r modules.CommandResult) []modules.OutputUpdate {
	x := &m.x
	switch {
	case r.CorrID >= forecastCorr:
		m.forecastBusy = false
		if !r.OK {
			m.forecastReqAt = r.At.Add(-25 * time.Minute)
			return nil
		}
		if f := hpcontrol.ParseForecast(r.Response, m.fcastEnt); len(f) > 0 {
			m.forecast, m.forecastAt = f, r.At
			m.replan(r.At)
			return m.publishAll(r.At, true)
		}
		m.forecastReqAt = r.At.Add(-25 * time.Minute)
		return nil
	case r.CorrID >= notifyCorr:
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
			x.deadmanAt = at.Add(deadmanRetry - deadmanEvery)
			m.notifyKind("fail_deadman", "Blanket controller command failed", fmt.Sprintf("%s failed: %s", p.cmd, r.Err), at)
		}
		return nil
	}
	res := "ok"
	if !r.OK {
		res = "failed"
	}
	x.last = lastCmd{Cmd: p.cmd, Result: res, Err: r.Err, At: at}
	switch p.kind {
	case "cycle_off":
		if p.attempt == x.attempt && x.phase == phOff {
			if r.OK {
				x.acked, x.ackAt = true, at
			} else {
				m.attemptFailed(at, "plug off failed: "+r.Err, attemptGap)
			}
		}
	case "cycle_on":
		if p.attempt == x.attempt && x.phase == phOn {
			if r.OK {
				x.acked, x.ackAt = true, at
			} else {
				m.attemptFailed(at, "plug on failed: "+r.Err, attemptGap)
			}
		}
	case "press":
		if !r.OK {
			// Ambiguous (the host times out slow Bluetooth calls): keep waiting for power.
			log.Printf("%s: press call reported %s; still waiting for power", Name, r.Err)
		}
	case "off":
		if !r.OK {
			x.lastOffCmd = at.Add(-offReassert + attemptGap) // re-assert soon
			m.notifyKind("fail_off", "Blanket controller command failed", fmt.Sprintf("%s failed: %s", p.cmd, r.Err), at)
		}
	}
	return nil
}

// OnLink implements modules.LinkAware.
func (m *Module) OnLink(up bool, now time.Time) []modules.OutputUpdate {
	x := &m.x
	if now.After(m.now) {
		m.now = now
	}
	if m.bootAt.IsZero() {
		m.bootAt = now
	}
	if up {
		x.ready, x.linkUp = true, true
	} else {
		x.linkUp = false
		x.pend = map[uint64]pending{}
		m.forecastBusy = false
	}
	return nil
}

// stateOf is the honest state, its reason and what it would do.
func (m *Module) stateOf(now time.Time) (state, reason, would string) {
	x := &m.x
	d := x.desired
	would = "off"
	if d.On {
		would = "on"
		if d.Kind == "backstop" {
			would = "backstop"
		}
	}
	reason = d.Reason
	switch {
	case !m.armedKey:
		return "disarmed", "disarmed: the armed switch is off; nothing is commanded. " + d.Reason, would
	case !m.live:
		return "shadow", "shadow: bc_mode is shadow; nothing is commanded. " + d.Reason, would
	case x.failed:
		return "failed", "failed: " + x.failWhy, would
	case d.On && d.Kind == "backstop":
		return "backstop", d.Reason, would
	case x.phase != phIdle:
		return fmt.Sprintf("starting(try %d)", x.tries), fmt.Sprintf("start %s: %s", x.kind, d.Reason), would
	case x.on:
		return "on", fmt.Sprintf("%s (on-window to %s)", d.Reason, x.windowEnd.In(m.p.Loc).Format("15:04")), would
	}
	if x.note != "" {
		reason = x.note
	}
	return "off", reason, would
}

type pq struct {
	k int64
	v float64
}

func plannerParse(u modules.StateUpdate) []pq {
	var out []pq
	for _, pr := range planner.ParseNordpool(u.Attrs, u.Time, loc()) {
		n := int(pr.Dur / quarter)
		for k := 0; k < n; k++ {
			out = append(out, pq{pr.Start.Add(time.Duration(k) * quarter).Unix(), pr.SEK})
		}
	}
	return out
}

var stockholm = DefaultParams().Loc

func loc() *time.Location { return stockholm }
