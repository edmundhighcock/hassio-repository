// Package priceshed (0.6.9) keeps a simple on/off load ON except during the N
// most expensive clock hours of each local day, with a manual override: if a
// person switches the load ON during an off period it stays on until that
// off period ends. First and only load: the tool-shed lights
// (switch.tool_shed_lights, left on deliberately to keep the shed slightly
// warmer than outside). The code keeps per-load state in a `load` so more
// loads can be added later; there is deliberately no multi-load config yet.
//
// The module acts on TRANSITIONS only (start of an off period -> turn_off,
// start of an on period -> turn_on and clear any override), plus a
// re-assertion at start and when the switch returns from unavailable.
// Warm-up replay never causes a command.
package priceshed

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hwcontrol/planner"
)

// Name is the registry key (the add-on's `modules` option).
const Name = "price_shed"

// ButtonReplan is the replan button's object id (button.replan_price_shed).
const ButtonReplan = "replan_price_shed"

const (
	defaultSwitch   = "switch.tool_shed_lights"
	defaultPrice    = "nordpool_kwh_se3_sek_3_06_0" // host convention: no "sensor." prefix
	defaultOffHours = 4

	// Grace is how long after our own command switch state changes are
	// ignored (stale reports, the plug's echo).
	Grace = 30 * time.Second
	// startWait is how long after the HA link comes up the module waits for
	// prices before asserting at start.
	startWait = 60 * time.Second
	// retryEvery spaces retries of a failed command.
	retryEvery = 60 * time.Second
)

func init() {
	modules.Register(Name, func() modules.Module { return New() })
}

// load is one price-shed load. Only one is configured today.
type load struct {
	entity string // full entity id, e.g. switch.tool_shed_lights
	name   string // entity id without domain: tool_shed_lights
	object string // sensor object id

	sw string // last known switch state: on|off|unavailable|unknown|"" (never seen)

	overrideUntil time.Time
	lastCmd       string // turn_on|turn_off
	lastCmdAt     time.Time
	lastTarget    string // on|off, the target of lastCmd
	corr          uint64

	asserted   bool // start assertion done (this link session)
	lastOff    bool // desired (off period) at the last evaluation
	needAssert bool
	assertAt   time.Time

	sig string // last published output signature
}

// Module is the price_shed module.
type Module struct {
	enabled  bool
	priceEnt string
	offHours int
	loc      *time.Location
	loads    []*load

	prices     []planner.Price
	pricesLive bool
	plans      map[string]plan // by local day key; reset when prices change

	saved map[string]savedLoad // persisted per-load state, by entity id

	linkUp bool
	linkAt time.Time
	now    time.Time // last non-replay time seen

	corrSeq uint64
	effects []modules.Effect
	dirty   bool
}

type plan struct {
	known bool
	off   map[int64]bool // hour start (unix seconds) -> off
	hours []int64        // off hours, ascending
}

type savedLoad struct {
	OverrideUntil string  `json:"override_until,omitempty"`
	LastCommand   string  `json:"last_command,omitempty"`
	LastCommandAt string  `json:"last_command_at,omitempty"`
	PlanDay       string  `json:"plan_day,omitempty"`
	PlanOffHours  []int64 `json:"plan_off_hours,omitempty"` // hour starts, unix seconds
}

type persisted struct {
	Loads map[string]savedLoad `json:"loads"`
}

// New returns a module with defaults.
func New() *Module {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		loc = time.UTC
	}
	m := &Module{enabled: true, priceEnt: defaultPrice, offHours: defaultOffHours, loc: loc,
		plans: map[string]plan{}, saved: map[string]savedLoad{}}
	m.setSwitch(defaultSwitch)
	return m
}

func (m *Module) setSwitch(entity string) {
	name := entity
	if i := strings.Index(entity, "."); i >= 0 {
		name = entity[i+1:]
	}
	m.loads = []*load{{entity: entity, name: name, object: "price_shed_" + name}}
}

// Name implements modules.Module.
func (m *Module) Name() string { return Name }

type options struct {
	Enabled  *bool   `json:"ps_enabled"`
	Switch   *string `json:"ps_switch"`
	OffHours *int    `json:"ps_off_hours"`
	Price    *string `json:"hwc_price_entity"`
}

// Configure implements modules.Configurable. Every key is optional; an
// out-of-range value is replaced by the default (logged).
func (m *Module) Configure(raw []byte) error {
	var o options
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
	}
	if o.Enabled != nil {
		m.enabled = *o.Enabled
	}
	if o.Switch != nil && strings.Contains(*o.Switch, ".") {
		m.setSwitch(*o.Switch)
	} else if o.Switch != nil && *o.Switch != "" {
		log.Printf("%s: ps_switch %q is not an entity id; using %s", Name, *o.Switch, defaultSwitch)
	}
	if o.OffHours != nil {
		if *o.OffHours < 0 || *o.OffHours > 12 {
			log.Printf("%s: ps_off_hours %d out of range 0..12; using %d", Name, *o.OffHours, defaultOffHours)
		} else {
			m.offHours = *o.OffHours
		}
	}
	if o.Price != nil && *o.Price != "" {
		m.priceEnt = *o.Price
	}
	// Host convention (host.moduleID): sensor inputs are named and delivered
	// WITHOUT the "sensor." prefix. 0.6.9 kept it, so no price ever arrived.
	m.priceEnt = strings.TrimPrefix(m.priceEnt, "sensor.")
	return nil
}

// Inputs implements modules.Module. A disabled module reads nothing.
func (m *Module) Inputs() []modules.InputSpec {
	if !m.enabled {
		return nil
	}
	in := []modules.InputSpec{{EntityID: m.priceEnt, Attributes: []string{"today", "tomorrow", "raw_today", "raw_tomorrow", "tomorrow_valid"}}}
	for _, l := range m.loads {
		in = append(in, modules.InputSpec{EntityID: l.entity})
	}
	return in
}

// Outputs implements modules.Module.
func (m *Module) Outputs() []modules.OutputSpec {
	if !m.enabled {
		return nil
	}
	var out []modules.OutputSpec
	for _, l := range m.loads {
		out = append(out, modules.OutputSpec{ObjectID: l.object, UniqueID: "virtual_sensors_" + l.object,
			Name: "Price shed " + strings.ReplaceAll(l.name, "_", " "), HasAttributes: true})
	}
	return out
}

// Buttons implements modules.Buttoned.
func (m *Module) Buttons() []modules.ButtonSpec {
	if !m.enabled {
		return nil
	}
	return []modules.ButtonSpec{{ObjectID: ButtonReplan, UniqueID: "virtual_sensors_" + ButtonReplan, Name: "Replan price shed"}}
}

// Init implements modules.Module.
func (m *Module) Init(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		log.Printf("%s: state file unreadable (%v); starting fresh", Name, err)
		return nil
	}
	if p.Loads != nil {
		m.saved = p.Loads
	}
	for _, l := range m.loads {
		s, ok := m.saved[l.entity]
		if !ok {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s.OverrideUntil); err == nil {
			l.overrideUntil = t
		}
		l.lastCmd = s.LastCommand
		l.lastTarget = strings.TrimPrefix(s.LastCommand, "turn_")
		if t, err := time.Parse(time.RFC3339, s.LastCommandAt); err == nil {
			l.lastCmdAt = t
		}
	}
	return nil
}

// Persist implements modules.Module.
func (m *Module) Persist() ([]byte, error) {
	out := persisted{Loads: map[string]savedLoad{}}
	for _, l := range m.loads {
		s := m.saved[l.entity]
		s.OverrideUntil, s.LastCommand, s.LastCommandAt = "", l.lastCmd, ""
		if !l.overrideUntil.IsZero() {
			s.OverrideUntil = l.overrideUntil.Format(time.RFC3339)
		}
		if !l.lastCmdAt.IsZero() {
			s.LastCommandAt = l.lastCmdAt.Format(time.RFC3339)
		}
		out.Loads[l.entity] = s
	}
	return json.Marshal(out)
}

// ---------------------------------------------------------------------------
// Planning: which clock hours are off.
// ---------------------------------------------------------------------------

func (m *Module) dayKey(t time.Time) string { return t.In(m.loc).Format("2006-01-02") }

// RankOffHours returns the start instants (ascending) of the n most
// expensive clock hours among prices that start within the local day of t:
// each hour's price is the mean of the prices starting in it; ties go to the
// earlier hour. known is false if the day has no prices at all.
func RankOffHours(prices []planner.Price, t time.Time, loc *time.Location, n int) (hours []int64, known bool) {
	l := t.In(loc)
	start := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	end := time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc)
	type acc struct {
		sum float64
		n   int
	}
	byHour := map[int64]*acc{}
	for _, p := range prices {
		if p.Start.Before(start) || !p.Start.Before(end) {
			continue
		}
		h := p.Start.Truncate(time.Hour).Unix()
		a := byHour[h]
		if a == nil {
			a = &acc{}
			byHour[h] = a
		}
		a.sum += p.SEK
		a.n++
	}
	if len(byHour) == 0 {
		return nil, false
	}
	keys := make([]int64, 0, len(byHour))
	for h := range byHour {
		keys = append(keys, h)
	}
	sort.Slice(keys, func(i, j int) bool {
		mi, mj := byHour[keys[i]].sum/float64(byHour[keys[i]].n), byHour[keys[j]].sum/float64(byHour[keys[j]].n)
		if mi != mj {
			return mi > mj
		}
		return keys[i] < keys[j]
	})
	if n > len(keys) {
		n = len(keys)
	}
	if n < 0 {
		n = 0
	}
	hours = append([]int64(nil), keys[:n]...)
	sort.Slice(hours, func(i, j int) bool { return hours[i] < hours[j] })
	return hours, true
}

// planFor returns the plan of the local day containing t, from prices or,
// failing that, the persisted plan for that day.
func (m *Module) planFor(l *load, t time.Time) plan {
	day := m.dayKey(t)
	if p, ok := m.plans[day]; ok {
		return p
	}
	hours, known := RankOffHours(m.prices, t, m.loc, m.offHours)
	if !known {
		if s := m.saved[l.entity]; s.PlanDay == day {
			hours, known = s.PlanOffHours, true
		}
	}
	p := plan{known: known, off: map[int64]bool{}, hours: hours}
	for _, h := range hours {
		p.off[h] = true
	}
	m.plans[day] = p
	return p
}

func (m *Module) offAt(l *load, t time.Time) bool {
	return m.planFor(l, t).off[t.Truncate(time.Hour).Unix()]
}

// offPeriodEnd is the end of the maximal run of off hours containing t.
func (m *Module) offPeriodEnd(l *load, t time.Time) time.Time {
	h := t.Truncate(time.Hour)
	for i := 0; i < 30; i++ {
		h = h.Add(time.Hour)
		if !m.offAt(l, h) {
			return h
		}
	}
	return h
}

func (m *Module) resetPlans() { m.plans = map[string]plan{} }

// notePlan remembers today's plan for persistence and logs changes.
func (m *Module) notePlan(l *load, now time.Time) {
	p := m.planFor(l, now)
	if !p.known {
		return
	}
	day := m.dayKey(now)
	s := m.saved[l.entity]
	if s.PlanDay == day && fmt.Sprint(s.PlanOffHours) == fmt.Sprint(p.hours) {
		return
	}
	if len(m.prices) > 0 {
		log.Printf("%s: %s off hours for %s: %s", Name, l.name, day, strings.Join(m.labels(p.hours), " "))
	}
	s.PlanDay, s.PlanOffHours = day, append([]int64(nil), p.hours...)
	m.saved[l.entity] = s
	m.dirty = true
}

func (m *Module) labels(hours []int64) []string {
	out := []string{}
	for _, h := range hours {
		out = append(out, time.Unix(h, 0).In(m.loc).Format("15:00"))
	}
	return out
}

// ---------------------------------------------------------------------------
// Inputs.
// ---------------------------------------------------------------------------

func isUnavail(s string) bool { return s == "unavailable" || s == "unknown" }

// OnStateUpdate implements modules.Module.
func (m *Module) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	if !m.enabled {
		return nil
	}
	if !u.Replay && u.Time.After(m.now) {
		m.now = u.Time
	}
	if u.EntityID == m.priceEnt {
		if len(u.Attrs) == 0 {
			return nil
		}
		if !u.Replay {
			m.pricesLive = true
			m.prices = planner.ParseNordpool(u.Attrs, u.Time, m.loc)
			m.resetPlans()
		} else if !m.pricesLive {
			// Seed from the warm-up replay (latest point wins) so a restart
			// has prices at once. Replay only records: never a decision.
			m.prices = planner.ParseNordpool(u.Attrs, u.Time, m.loc)
			m.resetPlans()
			return nil
		} else {
			return nil
		}
		return m.evaluateAll(m.now)
	}
	for _, l := range m.loads {
		if u.EntityID != l.entity {
			continue
		}
		prev := l.sw
		l.sw = u.State
		if u.Replay {
			return nil // state tracking only: replay never acts
		}
		m.onSwitch(l, prev, u.State, u.Time)
		return m.evaluateAll(m.now)
	}
	return nil
}

func (m *Module) onSwitch(l *load, prev, state string, at time.Time) {
	if isUnavail(state) || state == prev {
		return
	}
	if isUnavail(prev) {
		// The plug came back (it forgets its state after a power cut):
		// re-assert the desired state, unless an override is active.
		log.Printf("%s: %s back from %s (%s) -> re-assert", Name, l.name, prev, state)
		l.needAssert, l.assertAt = true, at
		return
	}
	if prev == "" || state != "on" || !m.offAt(l, at) || l.overrideActive(at) {
		return // first sight, an off press, an on press outside an off period, or already overriding: leave alone
	}
	// A switch ON during an off period. Ours (a command echo or stale
	// report) or a person's? Within the grace window of our own command it
	// is ours whatever it says: a Dyson on this estate reported a stale state
	// 156 ms after a command. (HA's event context is not plumbed through
	// StateUpdate, so it is not used as extra evidence.)
	if d := at.Sub(l.lastCmdAt); !l.lastCmdAt.IsZero() && d > -5*time.Second && d < Grace {
		log.Printf("%s: %s on %s after our %s: ignored (own command)", Name, l.name, d.Round(time.Millisecond), l.lastCmd)
		return
	}
	l.overrideUntil = m.offPeriodEnd(l, at)
	m.dirty = true
	m.effects = append(m.effects, modules.Effect{Kind: modules.EffectPersistNow})
	log.Printf("%s: %s manual on during off period -> override until %s", Name, l.name, l.overrideUntil.In(m.loc).Format("15:04"))
}

func (l *load) overrideActive(now time.Time) bool {
	return !l.overrideUntil.IsZero() && now.Before(l.overrideUntil)
}

// OnLink implements modules.LinkAware: re-assert once the link is up.
func (m *Module) OnLink(up bool, now time.Time) []modules.OutputUpdate {
	if !m.enabled {
		return nil
	}
	if now.After(m.now) {
		m.now = now
	}
	m.linkUp = up
	if up {
		m.linkAt = now
		for _, l := range m.loads {
			l.asserted = false
		}
	}
	return m.evaluateAll(now)
}

// OnTick implements modules.Ticker.
func (m *Module) OnTick(now time.Time) []modules.OutputUpdate {
	if !m.enabled {
		return nil
	}
	if now.After(m.now) {
		m.now = now
	}
	return m.evaluateAll(now)
}

// OnButton implements modules.Buttoned: recompute today's off hours now.
func (m *Module) OnButton(objectID string, now time.Time) []modules.OutputUpdate {
	if objectID != ButtonReplan || !m.enabled {
		return nil
	}
	log.Printf("%s: replan requested (button)", Name)
	if now.After(m.now) {
		m.now = now
	}
	m.resetPlans()
	return m.evaluateAll(now)
}

// ---------------------------------------------------------------------------
// Decisions.
// ---------------------------------------------------------------------------

func (m *Module) evaluateAll(now time.Time) []modules.OutputUpdate {
	if now.IsZero() {
		return nil
	}
	var outs []modules.OutputUpdate
	for _, l := range m.loads {
		m.evaluate(l, now)
		if o, ok := m.output(l, now); ok {
			outs = append(outs, o)
		}
	}
	return outs
}

func (m *Module) evaluate(l *load, now time.Time) {
	if now.IsZero() {
		return
	}
	m.notePlan(l, now)
	off := m.offAt(l, now)
	if !l.overrideUntil.IsZero() && (!now.Before(l.overrideUntil) || !off) {
		log.Printf("%s: %s override ended", Name, l.name)
		l.overrideUntil = time.Time{}
		m.dirty = true
	}
	if !m.linkUp {
		return // nothing can be commanded; resynchronise at link up
	}
	if !l.asserted {
		if !m.planFor(l, now).known && now.Sub(m.linkAt) < startWait {
			return // wait for prices a little while
		}
		l.asserted, l.lastOff = true, off
		l.needAssert, l.assertAt = true, now
		log.Printf("%s: %s start: off_period=%v", Name, l.name, off)
	}
	if off != l.lastOff {
		l.lastOff = off
		if off {
			log.Printf("%s: %s off_period start -> turn_off", Name, l.name)
			m.transition(l, now, false)
		} else {
			log.Printf("%s: %s on period start -> turn_on", Name, l.name)
			l.overrideUntil = time.Time{}
			m.transition(l, now, true)
		}
	}
	if l.needAssert && !now.Before(l.assertAt) {
		if l.overrideActive(now) {
			l.needAssert = false
			return
		}
		if isUnavail(l.sw) {
			return // wait for it to return
		}
		want := "off"
		if !off {
			want = "on"
		}
		if l.sw == want {
			l.needAssert = false
			log.Printf("%s: %s already %s", Name, l.name, want)
			return
		}
		log.Printf("%s: %s assert -> turn_%s", Name, l.name, want)
		m.command(l, now, !off)
	}
}

// transition issues the transition command, or defers it to the return of an
// unavailable switch.
func (m *Module) transition(l *load, now time.Time, on bool) {
	if l.overrideActive(now) && !on {
		return
	}
	if isUnavail(l.sw) {
		l.needAssert, l.assertAt = true, now
		return
	}
	m.command(l, now, on)
}

func (m *Module) command(l *load, now time.Time, on bool) {
	svc, target := "turn_off", "off"
	if on {
		svc, target = "turn_on", "on"
	}
	domain, _, _ := strings.Cut(l.entity, ".")
	m.corrSeq++
	l.corr = m.corrSeq
	l.lastCmd, l.lastTarget, l.lastCmdAt = svc, target, now
	l.needAssert = false
	m.dirty = true
	m.effects = append(m.effects, modules.Effect{Kind: modules.EffectCallService, Domain: domain, Service: svc,
		Data: map[string]any{"entity_id": l.entity}, CorrID: l.corr})
	m.effects = append(m.effects, modules.Effect{Kind: modules.EffectPersistNow})
}

// Effects implements modules.Actor.
func (m *Module) Effects() []modules.Effect {
	e := m.effects
	m.effects = nil
	return e
}

// OnCommandResult implements modules.Actor: a failed command is retried.
func (m *Module) OnCommandResult(r modules.CommandResult) []modules.OutputUpdate {
	for _, l := range m.loads {
		if r.CorrID != l.corr || r.OK {
			continue
		}
		log.Printf("%s: %s %s failed: %s (retry in %s)", Name, l.name, l.lastCmd, r.Err, retryEvery)
		l.needAssert, l.assertAt = true, r.At.Add(retryEvery)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Output.
// ---------------------------------------------------------------------------

func (m *Module) output(l *load, now time.Time) (modules.OutputUpdate, bool) {
	p := m.planFor(l, now)
	state := "on"
	switch {
	case l.overrideActive(now):
		state = "override"
	case !p.known:
		state = "no_prices"
	case m.offAt(l, now):
		state = "off_period"
	}
	attrs := map[string]any{"off_hours": m.labels(p.hours), "next_change": "", "override_until": "",
		"last_command": l.lastCmd, "last_command_at": ""}
	if l.overrideActive(now) {
		attrs["override_until"] = l.overrideUntil.In(m.loc).Format(time.RFC3339)
	}
	if !l.lastCmdAt.IsZero() {
		attrs["last_command_at"] = l.lastCmdAt.In(m.loc).Format(time.RFC3339)
	}
	cur := m.offAt(l, now)
	h := now.Truncate(time.Hour)
	for i := 0; i < 48; i++ {
		h = h.Add(time.Hour)
		if m.offAt(l, h) != cur {
			attrs["next_change"] = h.In(m.loc).Format(time.RFC3339)
			break
		}
	}
	b, err := json.Marshal(attrs)
	if err != nil {
		return modules.OutputUpdate{}, false
	}
	sig := state + "|" + string(b)
	if sig == l.sig {
		return modules.OutputUpdate{}, false
	}
	l.sig = sig
	return modules.OutputUpdate{ObjectID: l.object, Time: now, StateString: state, AttributesJSON: string(b)}, true
}

var (
	_ modules.Module       = (*Module)(nil)
	_ modules.Ticker       = (*Module)(nil)
	_ modules.Actor        = (*Module)(nil)
	_ modules.Buttoned     = (*Module)(nil)
	_ modules.LinkAware    = (*Module)(nil)
	_ modules.Configurable = (*Module)(nil)
)
