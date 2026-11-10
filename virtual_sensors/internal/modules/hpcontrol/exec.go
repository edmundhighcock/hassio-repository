package hpcontrol

// The live executor (hp_mode live AND input_boolean.hp_controller_armed on).
// Shape follows hwcontrol: the module returns modules.Effect service calls,
// the host executes them and feeds each outcome back through
// OnCommandResult. Nothing here runs on a Replay update.
//
// Rules (Edmund):
//   - the heat pump beeps at every IR command: send only on change, and never
//     re-send a state it is already in;
//   - while timer.tv_quiet is active send NOTHING to either device;
//   - a change of either device the module did not cause is a manual
//     override: hold off for hp_override_hold_h and notify;
//   - stale data, or a comfort temperature below the minimum, falls to heat
//     at the hold set-point, never to off;
//   - disarming leaves the devices as they are and clears any override;
//   - a state change within hp_command_grace_s of our own command is not an
//     override; if the target is still not reached after the window the
//     command is repeated (twice at most), then reported as failed.

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"virtual_sensors/internal/modules"
)

const (
	startupGrace   = 30 * time.Second // after the link comes up, before acting
	quietSettle    = 90 * time.Second // after TV quiet ends: the package restores the devices
	expectFor      = 2 * time.Minute  // a state change this soon after our call is ours
	inFlight       = 30 * time.Second
	retryAfterFail = 5 * time.Minute
	repeatGuard    = 30 * time.Minute // same command again while the state still differs
	notifyCooldown = time.Hour
	maxRetries     = 2 // re-sends of an unapplied command
	heartbeatEvery = 30 * time.Second
	planStaleAfter = 45 * time.Minute

	notifyCorr   = uint64(1) << 40
	forecastCorr = uint64(2) << 40
	fanHigh      = "high"
)

// Target is what the controller wants of both devices right now.
type Target struct {
	Level  Level
	Pct    int    // Dyson percent, 0 = off
	Kind   string // plan | safe_hold | below_min | bedroom_cap
	Reason string
}

type call struct {
	Domain, Service string
	Data            map[string]any
	Desc            string
	Sig             string // the device's signature once this call has taken effect
}

type sentRec struct {
	key          string
	at           time.Time
	pendingUntil time.Time
	failed       bool
	tries        int
	warned       bool
}

// graceRec is the window after our command during which state changes of that
// device are ours (SmartIR is optimistic, the Dyson echoes stale status).
type graceRec struct {
	until   time.Time
	target  string // the signature the command should bring about
	retries int
}

type executor struct {
	armed, armedOK bool
	wasActive      bool
	linkUp         bool
	linkAt         time.Time

	timerActive, tvBool bool
	quietEnded          time.Time

	overrideUntil time.Time
	overrideWhy   string

	lastSig map[string]string
	expect  map[string]time.Time
	sent    map[string]*sentRec
	grace   map[string]*graceRec
	corr    map[uint64]string

	effects  []modules.Effect
	nextCorr uint64

	lastCmd    string
	lastCmdAt  time.Time
	lastCmdOK  bool
	lastNotify map[string]time.Time
	pub        map[string]string
	hbAt       time.Time
}

func (m *Module) applyExecDefaults() {
	m.comfortIDs = []string{kitchenEnt, bedroomEnt}
	m.forecastEnt = "weather.smhi_home"
	m.floorMargin = 0.3
	m.live = false
	m.notifySvc = "notify.mobile_app_edmund15max"
	m.holdSet = 22 // a power dial: 22 C gave ~0.86 kW on 2026-11-10 (hold is 0.7 kW)
	m.boostSet = 30
	m.overrideFor = 2 * time.Hour
	m.cmdGrace = 120 * time.Second
	m.staleAfter = 90 * time.Minute
	m.sensorStale = 180 * time.Minute
	e := &m.ex
	e.lastSig = map[string]string{}
	e.expect = map[string]time.Time{}
	e.sent = map[string]*sentRec{}
	e.grace = map[string]*graceRec{}
	e.corr = map[uint64]string{}
	e.lastNotify = map[string]time.Time{}
	e.pub = map[string]string{}
}

func (m *Module) configureExec(o options) []string {
	var msgs []string
	m.applyExecDefaultsConfigOnly()
	if o.Comfort != nil {
		var ids []string
		for _, s := range strings.Split(*o.Comfort, ",") {
			if s = strings.TrimPrefix(strings.TrimSpace(s), "sensor."); s != "" {
				ids = append(ids, s)
			}
		}
		if len(ids) > 0 {
			m.comfortIDs = ids
		} else {
			msgs = append(msgs, "hp_comfort_sensors is empty; default applied")
		}
	}
	if o.ForecastEnt != nil {
		m.forecastEnt = strings.TrimSpace(*o.ForecastEnt) // "" disables the forecast
	}
	if o.FloorMargin != nil {
		if *o.FloorMargin >= 0 && *o.FloorMargin <= 3 {
			m.floorMargin = *o.FloorMargin
		} else {
			msgs = append(msgs, "hp_floor_margin_c out of range; default applied")
		}
	}
	if o.Mode != nil {
		m.live = *o.Mode == "live"
	}
	if o.NotifyService != nil && strings.HasPrefix(*o.NotifyService, "notify.") {
		m.notifySvc = *o.NotifyService
	}
	if o.HoldSet != nil {
		if *o.HoldSet >= 17 && *o.HoldSet <= 30 {
			m.holdSet = math.Round(*o.HoldSet)
		} else {
			msgs = append(msgs, "hp_hold_setpoint_c out of range (17-30); default applied")
		}
	}
	if o.BoostSet != nil {
		if *o.BoostSet >= 17 && *o.BoostSet <= 30 {
			m.boostSet = math.Round(*o.BoostSet)
		} else {
			msgs = append(msgs, "hp_boost_setpoint_c out of range (17-30); default applied")
		}
	}
	if m.boostSet <= m.holdSet {
		msgs = append(msgs, "hp_boost_setpoint_c must exceed hp_hold_setpoint_c; defaults applied")
		m.holdSet, m.boostSet = 22, 30
	}
	if o.OverrideH != nil {
		if *o.OverrideH >= 0.25 && *o.OverrideH <= 24 {
			m.overrideFor = time.Duration(*o.OverrideH * float64(time.Hour))
		} else {
			msgs = append(msgs, "hp_override_hold_h out of range (0.25-24); default applied")
		}
	}
	if o.GraceS != nil {
		if *o.GraceS >= 10 && *o.GraceS <= 900 {
			m.cmdGrace = time.Duration(*o.GraceS) * time.Second
		} else {
			msgs = append(msgs, "hp_command_grace_s out of range (10-900); default applied")
		}
	}
	if o.StaleMin != nil {
		if *o.StaleMin >= 10 && *o.StaleMin <= 720 {
			m.staleAfter = time.Duration(*o.StaleMin) * time.Minute
		} else {
			msgs = append(msgs, "hp_stale_after_min out of range (10-720); default applied")
		}
	}
	if o.SensorStale != nil {
		if *o.SensorStale >= 30 && *o.SensorStale <= 1440 {
			m.sensorStale = time.Duration(*o.SensorStale) * time.Minute
		} else {
			msgs = append(msgs, "hp_sensor_stale_min out of range (30-1440); default applied")
		}
	}
	return msgs
}

// applyExecDefaultsConfigOnly resets the configuration fields (not the
// executor's runtime state) so Configure is idempotent.
func (m *Module) applyExecDefaultsConfigOnly() {
	e := m.ex
	m.applyExecDefaults()
	m.ex = e
}

// active: the two keys are both turned.
func (e *executor) active(m *Module) bool { return m.live && e.armedOK && e.armed }

func (e *executor) quiet(now time.Time) bool {
	return e.timerActive || e.tvBool || now.Before(e.quietEnded.Add(quietSettle))
}

func (e *executor) updateQuiet(was bool, now time.Time) {
	if was && !e.timerActive && !e.tvBool {
		e.quietEnded = now
	}
}

func (e *executor) onTimer(active bool, now time.Time) {
	was := e.timerActive || e.tvBool
	e.timerActive = active
	e.updateQuiet(was, now)
}

func (e *executor) onTVBool(on bool, now time.Time) {
	was := e.timerActive || e.tvBool
	e.tvBool = on
	e.updateQuiet(was, now)
}

// ---- signatures ----

func hpSigOf(state string, set float64, setOK bool, fan string) string {
	if state == "off" {
		return "off"
	}
	if !setOK {
		set = math.NaN()
	}
	return fmt.Sprintf("%s|%.0f|%s", state, set, fan)
}

func (m *Module) hpSig() string { return hpSigOf(m.hpState, m.hpSet, m.hpSetOK, m.hpFan) }

func fanSigOf(on string, pct float64, pctOK bool) string {
	if on != "on" {
		return on
	}
	if !pctOK {
		return "on|?"
	}
	return fmt.Sprintf("on|%.0f", pct)
}

func (m *Module) fanSig() string { return fanSigOf(m.dysonOn, m.dysonPct, m.dysonPctOK) }

func (m *Module) setpoint(l Level) float64 {
	if l == Boost {
		return m.boostSet
	}
	return m.holdSet
}

// ---- target ----

// degradedReason is non-empty when the controller cannot trust its plan
// (stale or missing data): the answer is then heat at the hold set-point.
func (m *Module) degradedReason(now time.Time) string {
	if _, ok := m.comfortNow(now); !ok {
		return "comfort sensors stale or unavailable"
	}
	if !m.lastHeard.IsZero() && now.Sub(m.lastHeard) > m.staleAfter {
		return fmt.Sprintf("no Home Assistant updates for %d min", int(now.Sub(m.lastHeard).Minutes()))
	}
	if _, ok := m.toutNow(now); !ok {
		return "no outdoor temperature"
	}
	if !m.havePlan {
		return "no plan (prices missing)"
	}
	if now.Sub(m.lastPlanAt) > planStaleAfter {
		return "plan is stale"
	}
	sl, ok := m.slotAt(now)
	if !ok {
		return "no plan slot for now"
	}
	if !sl.Priced {
		return "prices stale: none for the current slot"
	}
	return ""
}

func (m *Module) target(now time.Time) Target {
	t := m.baseTarget(now)
	// House budget (internal/house): a sustained phase overload that the EV
	// could not cure caps the pump at hold, then off, for the cool-down.
	// It outranks the plan and the safe hold: the fuse is the hard limit.
	// Like the rest of this, it is only ever SENT when armed live.
	if c, ok := m.hub.HPCap(now); ok && int(t.Level) > c {
		pr := m.presenceNow()
		l := Level(c)
		return Target{Level: l, Pct: m.p.DysonPct(now, l, pr), Kind: "house_budget",
			Reason: fmt.Sprintf("house budget overload: capped at %s (was %s)", l, t.Level)}
	}
	return t
}

func (m *Module) baseTarget(now time.Time) Target {
	pr := m.presenceNow()
	mk := func(l Level, kind, why string) Target {
		return Target{Level: l, Pct: m.p.DysonPct(now, l, pr), Kind: kind, Reason: why}
	}
	if r := m.degradedReason(now); r != "" {
		return mk(Hold, "safe_hold", r)
	}
	sl, _ := m.slotAt(now)
	lvl := sl.Level
	mn, _ := m.minNow()
	c, _ := m.comfortNow(now)
	if c < mn {
		if lvl < Hold {
			lvl = Hold
		}
		return mk(lvl, "below_min", fmt.Sprintf("comfort %.1f C below the minimum %.1f C", c, mn))
	}
	if m.p.IsNight(now) {
		if b, ok := m.bedroomNow(now); ok && b > m.p.BedroomCap && lvl != Off {
			return mk(Off, "bedroom_cap", fmt.Sprintf("bedroom %.1f C above the night cap %.1f C", b, m.p.BedroomCap))
		}
	}
	return mk(lvl, "plan", "")
}

// ---- calls ----

func (m *Module) hpCalls(l Level) []call {
	switch m.hpState {
	case "", "unavailable", "unknown":
		return nil // cannot tell what the pump is doing: do not guess
	}
	state, set, setOK, fan := m.hpState, m.hpSet, m.hpSetOK, m.hpFan
	if l == Off {
		if state == "off" {
			return nil
		}
		return []call{{Domain: "climate", Service: "turn_off", Data: map[string]any{"entity_id": climateEnt},
			Desc: "heat pump off", Sig: "off"}}
	}
	x := m.setpoint(l)
	var cs []call
	if state != "heat" {
		state, set, setOK = "heat", x, true
		cs = append(cs, call{Domain: "climate", Service: "set_temperature",
			Data: map[string]any{"entity_id": climateEnt, "temperature": x, "hvac_mode": "heat"},
			Desc: fmt.Sprintf("heat pump heat %.0f C", x), Sig: hpSigOf(state, set, setOK, fan)})
	} else if !setOK || set != x {
		set, setOK = x, true
		cs = append(cs, call{Domain: "climate", Service: "set_temperature",
			Data: map[string]any{"entity_id": climateEnt, "temperature": x},
			Desc: fmt.Sprintf("heat pump set %.0f C", x), Sig: hpSigOf(state, set, setOK, fan)})
	}
	if fan != fanHigh {
		fan = fanHigh
		cs = append(cs, call{Domain: "climate", Service: "set_fan_mode",
			Data: map[string]any{"entity_id": climateEnt, "fan_mode": fanHigh},
			Desc: "heat pump fan high", Sig: hpSigOf(state, set, setOK, fan)})
	}
	return cs
}

func (m *Module) fanCalls(pct int) []call {
	switch m.dysonOn {
	case "", "unavailable", "unknown":
		return nil
	}
	if pct <= 0 {
		if m.dysonOn != "on" {
			return nil
		}
		return []call{{Domain: "fan", Service: "turn_off", Data: map[string]any{"entity_id": dysonEnt},
			Desc: "dyson off", Sig: "off"}}
	}
	if m.dysonOn == "on" && m.dysonPctOK && int(m.dysonPct) == pct {
		return nil
	}
	return []call{{Domain: "fan", Service: "turn_on", Data: map[string]any{"entity_id": dysonEnt, "percentage": pct},
		Desc: fmt.Sprintf("dyson %d%%", pct), Sig: fmt.Sprintf("on|%d", pct)}}
}

// describeCalls is what would be sent now to reach the target.
func (m *Module) describeCalls(tg Target) []string {
	var out []string
	for _, c := range m.hpCalls(tg.Level) {
		out = append(out, c.Desc)
	}
	for _, c := range m.fanCalls(tg.Pct) {
		out = append(out, c.Desc)
	}
	return out
}

func (m *Module) wantedSig(dev string, tg Target) string {
	if dev == "hp" {
		if tg.Level == Off {
			return "off"
		}
		return hpSigOf("heat", m.setpoint(tg.Level), true, fanHigh)
	}
	if tg.Pct <= 0 {
		return "off"
	}
	return fmt.Sprintf("on|%d", tg.Pct)
}

// ---- acting ----

// act is the executor. Call it after every non-replay update and every tick.
func (m *Module) act(now time.Time) {
	e := &m.ex
	if !e.linkUp || now.Before(e.linkAt.Add(startupGrace)) {
		return
	}
	active := e.active(m)
	m.trackArming(now, active)
	if !active || e.quiet(now) {
		return
	}
	if now.Before(e.overrideUntil) {
		if c, ok := m.comfortNow(now); ok {
			if mn, _ := m.minNow(); c < mn {
				m.notify(now, "floor_in_override", "Heat pump controller: house below minimum",
					fmt.Sprintf("Comfort %.1f C is below the minimum %.1f C while the manual override holds the controller off until %s.",
						c, mn, e.overrideUntil.In(m.p.Loc).Format("15:04")))
			}
		}
		return
	}
	tg := m.target(now)
	if tg.Kind == "safe_hold" {
		m.notify(now, "degraded", "Heat pump controller: safe hold",
			fmt.Sprintf("%s. Heating at the hold set-point (%.0f C) until the data is back.", tg.Reason, m.holdSet))
	}
	m.sendDevice(now, "hp", m.hpCalls(tg.Level))
	m.sendDevice(now, "fan", m.fanCalls(tg.Pct))
}

func (m *Module) trackArming(now time.Time, active bool) {
	e := &m.ex
	switch {
	case active && !e.wasActive:
		e.wasActive = true
		m.notify(now, "armed", "Heat pump controller armed",
			"LIVE: the controller now drives the heat pump and the Dyson fan.")
	case !active && e.wasActive:
		e.wasActive = false
		m.notify(now, "disarmed", "Heat pump controller disarmed",
			"The heat pump and the Dyson fan are left as they are.")
	}
}

func (m *Module) sendDevice(now time.Time, dev string, calls []call) {
	e := &m.ex
	if len(calls) == 0 {
		return
	}
	var ds []string
	for _, c := range calls {
		ds = append(ds, c.Desc)
	}
	key := strings.Join(ds, "+")
	prevTries, retries := 0, 0
	if r := e.sent[dev]; r != nil && r.key == key {
		switch {
		case r.pendingUntil.After(now):
			return
		case r.failed && r.tries >= 3:
			if now.Sub(r.at) < repeatGuard {
				return
			}
		case r.failed:
			if now.Sub(r.at) < retryAfterFail {
				return
			}
			prevTries = r.tries
		default:
			// Sent fine, yet the state still differs. Inside the grace
			// window the device may simply not have caught up. After it:
			// the same command again, at most twice; then give up (every IR
			// command beeps) and say so.
			g := e.grace[dev]
			if g != nil && now.Before(g.until) {
				return
			}
			if g != nil && g.retries < maxRetries {
				retries = g.retries + 1
				prevTries = r.tries
			} else if now.Sub(r.at) < repeatGuard {
				if !r.warned {
					r.warned = true
					e.lastCmdOK = false
					m.notify(now, "not_applied", "Heat pump controller: command not applied",
						fmt.Sprintf("%s was sent (and retried %d times) but the device still reports a different state. Not re-sending for %d min.", key, maxRetries, int(repeatGuard.Minutes())))
				}
				return
			}
		}
	}
	e.sent[dev] = &sentRec{key: key, at: now, pendingUntil: now.Add(inFlight), tries: prevTries + 1}
	e.grace[dev] = &graceRec{until: now.Add(m.cmdGrace), target: calls[len(calls)-1].Sig, retries: retries}
	for _, c := range calls {
		e.nextCorr++
		id := e.nextCorr
		e.corr[id] = dev
		e.effects = append(e.effects, modules.Effect{Kind: modules.EffectCallService, Domain: c.Domain,
			Service: c.Service, Data: c.Data, CorrID: id})
		e.expect[dev+":"+c.Sig] = now.Add(expectFor)
	}
	e.lastCmd, e.lastCmdAt, e.lastCmdOK = key, now, true
}

// onDeviceChange classifies a state change of a device: ours (expected after
// a call), the package's (TV quiet), harmless (what we want anyway) or a
// manual override.
func (m *Module) onDeviceChange(replay bool, now time.Time, name string) {
	e := &m.ex
	dev, sig := "hp", m.hpSig()
	if name != "heat pump" {
		dev, sig = "fan", m.fanSig()
	}
	old, had := e.lastSig[dev]
	e.lastSig[dev] = sig
	if !had || old == sig || replay {
		return
	}
	if exp, ok := e.expect[dev+":"+sig]; ok && now.Before(exp) {
		return
	}
	// Inside the grace window after our own command nothing is a hand change:
	// an IR pump has no feedback and the Dyson integration echoes stale
	// status (2026-11-10: off, then on|30 150 ms later).
	if g := e.grace[dev]; g != nil && now.Before(g.until) {
		return
	}
	if !e.active(m) || !e.linkUp || e.quiet(now) {
		return
	}
	if m.wantedSig(dev, m.target(now)) == sig {
		return // someone did what the plan wants anyway
	}
	// A hand change. Forget what we sent so that, once the hold-off ends,
	// the plan can be applied again.
	delete(e.sent, dev)
	e.overrideWhy = fmt.Sprintf("%s changed by hand (%s -> %s)", name, old, sig)
	if now.Before(e.overrideUntil) {
		e.overrideUntil = now.Add(m.overrideFor)
		return
	}
	e.overrideUntil = now.Add(m.overrideFor)
	m.notify(now, "override", "Heat pump controller: manual override",
		fmt.Sprintf("%s. The controller sends nothing until %s.", e.overrideWhy, e.overrideUntil.In(m.p.Loc).Format("15:04")))
}

// ---- notifications ----

// notify sends to the phone and as a persistent notification (as hwcontrol
// does), at most once per code per hour except arm/disarm/override.
func (m *Module) notify(now time.Time, code, title, text string) {
	e := &m.ex
	switch code {
	case "armed", "disarmed", "override":
	default:
		if t, ok := e.lastNotify[code]; ok && now.Sub(t) < notifyCooldown {
			return
		}
	}
	e.lastNotify[code] = now
	dom, svc, _ := strings.Cut(m.notifySvc, ".")
	e.nextCorr++
	e.effects = append(e.effects, modules.Effect{Kind: modules.EffectCallService, Domain: dom, Service: svc,
		Data: map[string]any{"title": title, "message": text}, CorrID: notifyCorr + e.nextCorr})
	e.nextCorr++
	e.effects = append(e.effects, modules.Effect{Kind: modules.EffectCallService, Domain: "persistent_notification",
		Service: "create", Data: map[string]any{"title": title, "message": text, "notification_id": "hp_controller_" + code},
		CorrID: notifyCorr + e.nextCorr})
}

// ---- forecast ----

func (m *Module) requestForecast(now time.Time) {
	e := &m.ex
	if m.forecastEnt == "" || !e.linkUp {
		return
	}
	if m.forecastBusy && now.Sub(m.forecastReqAt) < 5*time.Minute {
		return
	}
	if !m.forecastReqAt.IsZero() && now.Sub(m.forecastReqAt) < 30*time.Minute {
		return
	}
	m.forecastBusy, m.forecastReqAt = true, now
	e.nextCorr++
	e.effects = append(e.effects, modules.Effect{Kind: modules.EffectCallService, Domain: "weather",
		Service: "get_forecasts", Data: map[string]any{"entity_id": m.forecastEnt, "type": "hourly"},
		CorrID: forecastCorr + e.nextCorr, ReturnResponse: true})
}

// parseForecast reads the get_forecasts response (hour start unix -> deg C).
func parseForecast(raw json.RawMessage, ent string) map[int64]float64 {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	if sr, ok := top["service_response"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(sr, &inner) == nil {
			top = inner
		}
	}
	var f struct {
		Forecast []struct {
			Datetime    string   `json:"datetime"`
			Temperature *float64 `json:"temperature"`
		} `json:"forecast"`
	}
	if json.Unmarshal(top[ent], &f) != nil {
		return nil
	}
	out := map[int64]float64{}
	for _, p := range f.Forecast {
		t, err := time.Parse(time.RFC3339, p.Datetime)
		if err != nil || p.Temperature == nil {
			continue
		}
		out[t.Unix()] = *p.Temperature
	}
	return out
}

// ---- host interfaces ----

// Effects implements modules.Actor.
func (m *Module) Effects() []modules.Effect {
	e := m.ex.effects
	m.ex.effects = nil
	return e
}

// OnCommandResult implements modules.Actor.
func (m *Module) OnCommandResult(r modules.CommandResult) []modules.OutputUpdate {
	e := &m.ex
	switch {
	case r.CorrID >= forecastCorr:
		m.forecastBusy = false
		if !r.OK {
			m.forecastReqAt = r.At.Add(-25 * time.Minute) // retry in 5 min
			return nil
		}
		if f := parseForecast(r.Response, m.forecastEnt); len(f) > 0 {
			m.forecast, m.forecastAt = f, r.At
			m.replan(r.At)
			return m.publishAll(r.At, true)
		}
		m.forecastReqAt = r.At.Add(-25 * time.Minute)
		return nil
	case r.CorrID >= notifyCorr:
		return nil
	}
	dev, ok := e.corr[r.CorrID]
	delete(e.corr, r.CorrID)
	if !ok {
		return nil
	}
	if rec := e.sent[dev]; rec != nil && !r.OK {
		rec.failed, rec.pendingUntil = true, time.Time{}
		e.lastCmdOK = false
		m.notify(r.At, "command_failed", "Heat pump controller: command failed",
			fmt.Sprintf("%s failed: %s. Retrying every %d min.", rec.key, r.Err, int(retryAfterFail.Minutes())))
	}
	return m.publishAll(r.At, false)
}

// OnLink implements modules.LinkAware.
func (m *Module) OnLink(up bool, now time.Time) []modules.OutputUpdate {
	e := &m.ex
	e.linkUp = up
	if up {
		e.linkAt = now
		m.forecastBusy = false
		m.forecastReqAt = time.Time{}
		for k := range e.corr {
			delete(e.corr, k)
		}
	}
	return m.publishAll(now, false)
}

// ---- published state ----

func (e *executor) liveOutputs(m *Module, now time.Time, force bool) []modules.OutputUpdate {
	var outs []modules.OutputUpdate
	put := func(obj, state string, attrs map[string]any) {
		js := jsonStr(attrs)
		if force || e.pub[obj] != state+js {
			e.pub[obj] = state + js
			outs = append(outs, modules.OutputUpdate{ObjectID: obj, Time: now, StateString: state, AttributesJSON: js})
		}
	}
	mode := "shadow"
	if m.live {
		mode = "live"
	}
	put(ObjMode, mode, map[string]any{"armed_helper_required": armedEnt})
	armed := "off"
	if e.armedOK && e.armed {
		armed = "on"
	}
	put(ObjArmed, armed, map[string]any{"helper": armedEnt, "helper_known": e.armedOK, "mode": mode, "active": e.active(m)})
	last := e.lastCmd
	if last == "" {
		last = "none"
	}
	la := map[string]any{"ok": e.lastCmdOK}
	if !e.lastCmdAt.IsZero() {
		la["at"] = e.lastCmdAt.In(m.p.Loc).Format(time.RFC3339)
	}
	put(ObjLastCmd, last, la)
	ov := "none"
	oa := map[string]any{}
	if now.Before(e.overrideUntil) {
		ov = "active"
		oa["until"] = e.overrideUntil.In(m.p.Loc).Format(time.RFC3339)
		oa["reason"] = e.overrideWhy
	}
	put(ObjOverride, ov, oa)
	if !now.IsZero() && (e.hbAt.IsZero() || now.Sub(e.hbAt) >= heartbeatEvery || force) {
		e.hbAt = now
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjHeartbeat, Time: now,
			StateString: now.UTC().Format(time.RFC3339)})
	}
	return outs
}
