// Package house is the shared electrical budget of the house planner (ham
// energy design, section "house planner"): one budget per phase, priority
// manual loads > hot water > heat pump > EV. It is a LEAF package: it
// imports no module. The modules in the one add-on process meet through a
// Hub: hwcontrol and hpcontrol register read-only providers (heater ON
// windows, heat-pump planned power), evcontrol publishes the measured
// per-phase current (its guard already reads the Easee Equalizer and falls
// back to the grid-power estimate) and reads back the headroom forecast
// and any curtailment, and the house_planner module computes the forecast
// and the curtailment. Pure logic here; no I/O, no clock.
package house

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// Quarter is the forecast slot.
const Quarter = 15 * time.Minute

// Volts is the nominal single-phase voltage.
const Volts = 230.0

// Load names in the phase map.
const (
	LoadHeater = "heater"
	LoadHP     = "hp"
	LoadEV     = "ev"
)

// PhaseSet marks which of L1, L2, L3 a load is wired across.
type PhaseSet [3]bool

// Count is the number of phases in the set.
func (s PhaseSet) Count() int {
	n := 0
	for _, b := range s {
		if b {
			n++
		}
	}
	return n
}

// String renders "L1+L2".
func (s PhaseSet) String() string {
	var p []string
	for i, b := range s {
		if b {
			p = append(p, fmt.Sprintf("L%d", i+1))
		}
	}
	return strings.Join(p, "+")
}

// PhaseMap is the per-load wiring (provisional, D32).
type PhaseMap map[string]PhaseSet

// DefaultPhaseMapString is D32: heater across L1+L2, heat pump on L3, EV on all.
const DefaultPhaseMapString = "heater=L1+L2;hp=L3;ev=L1+L2+L3"

// DefaultPhaseMap is D32.
func DefaultPhaseMap() PhaseMap {
	m, _ := ParsePhaseMap(DefaultPhaseMapString)
	return m
}

// ParsePhaseMap parses "heater=L1+L2;hp=L3;ev=L1+L2+L3". Loads left out
// keep their default; an unknown load, phase or an empty set is an error
// (the caller then uses the default and logs it).
func ParsePhaseMap(s string) (PhaseMap, error) {
	out := PhaseMap{}
	def := map[string]PhaseSet{LoadHeater: {true, true, false}, LoadHP: {false, false, true}, LoadEV: {true, true, true}}
	for k, v := range def {
		out[k] = v
	}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, val, ok := strings.Cut(part, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if !ok {
			return nil, fmt.Errorf("%q: want load=L1+L2", part)
		}
		if _, known := def[name]; !known {
			return nil, fmt.Errorf("unknown load %q (heater, hp, ev)", name)
		}
		var set PhaseSet
		for _, ph := range strings.Split(val, "+") {
			switch strings.ToUpper(strings.TrimSpace(ph)) {
			case "L1", "A":
				set[0] = true
			case "L2", "B":
				set[1] = true
			case "L3", "C":
				set[2] = true
			default:
				return nil, fmt.Errorf("load %s: unknown phase %q (L1, L2, L3)", name, ph)
			}
		}
		if set.Count() == 0 {
			return nil, fmt.Errorf("load %s has no phase", name)
		}
		out[name] = set
	}
	return out, nil
}

// AmpsPerPhase is the current each of n phases carries for w watts: one
// phase at 230 V, two phases across 400 V, three phases at 3 x 230 V.
func AmpsPerPhase(w float64, n int) float64 {
	switch n {
	case 1:
		return w / Volts
	case 2:
		return w / 400
	case 3:
		return w / (3 * Volts)
	}
	return 0
}

// HeaterWindow is one span of the heater's plan.
type HeaterWindow struct {
	Start, End time.Time
	On         bool
}

// HeaterPlan is the heater's next 24 h. Valid=false (no provider): the
// heater is assumed ON throughout (worst case).
type HeaterPlan struct {
	Valid    bool
	Windows  []HeaterWindow
	NominalW float64
}

// OnDuring reports whether the heater may draw at any time in [a, b):
// true unless a window that certainly says OFF covers the whole span.
func (h HeaterPlan) OnDuring(a, b time.Time) bool {
	if !h.Valid {
		return true
	}
	for _, w := range h.Windows {
		if w.On && w.Start.Before(b) && w.End.After(a) {
			return true
		}
	}
	// Not covered by any ON window. A gap (outside the schedule) is
	// treated as ON: conservative.
	covered := false
	for _, w := range h.Windows {
		if !w.On && !w.Start.After(a) && !w.End.Before(b) {
			covered = true
		}
	}
	return !covered
}

// HPPlan is the heat pump's planned electrical power per slot start.
// Valid=false: no plan, so MaxW is assumed in every slot.
type HPPlan struct {
	Valid bool
	W     map[int64]float64 // slot start (unix) -> planned W
	MaxW  float64           // the pump's maximum, the worst-case fallback
}

// At is the planned power for the slot starting at t (MaxW when unknown).
func (h HPPlan) At(t time.Time) float64 {
	if !h.Valid {
		return h.MaxW
	}
	if w, ok := h.W[t.Unix()]; ok {
		return w
	}
	return h.MaxW
}

// Slot is one forecast quarter.
type Slot struct {
	Start    time.Time
	HeadA    [3]float64 // headroom per phase, amps (may be negative)
	HeaterOn bool
	HPW      float64
}

// Forecast is the per-slot, per-phase headroom for the planning horizon.
type Forecast struct {
	At       time.Time
	BudgetA  float64
	EVPhases PhaseSet
	Slots    []Slot
	// Heater/HP source flags, for the published attributes.
	HeaterKnown, HPKnown bool
}

// MinHeadroom is the smallest headroom over the phases in set at slot t.
func (f Forecast) MinHeadroom(t time.Time, set PhaseSet) (float64, bool) {
	q := t.Truncate(Quarter)
	for _, s := range f.Slots {
		if s.Start.Equal(q) {
			min := math.Inf(1)
			for i, on := range set {
				if on && s.HeadA[i] < min {
					min = s.HeadA[i]
				}
			}
			if math.IsInf(min, 1) {
				return 0, false
			}
			return min, true
		}
	}
	return 0, false
}

// EVHeadroom is the EV's per-slot current ceiling before clamping: the
// minimum headroom over its phases.
func (f Forecast) EVHeadroom(t time.Time) (float64, bool) { return f.MinHeadroom(t, f.EVPhases) }

// ForecastIn is everything ComputeForecast needs.
type ForecastIn struct {
	Now     time.Time
	Horizon time.Duration
	BudgetA float64
	BaseA   [3]float64 // base house load per phase
	Map     PhaseMap
	Heater  HeaterPlan
	HP      HPPlan
}

// ComputeForecast: per 15-minute slot, per phase,
// headroom = budget - base - heater (if its plan has it ON, on its phases)
// - heat pump (planned W, on its phases). Pure.
func ComputeForecast(in ForecastIn) Forecast {
	f := Forecast{At: in.Now, BudgetA: in.BudgetA, EVPhases: in.Map[LoadEV], HeaterKnown: in.Heater.Valid, HPKnown: in.HP.Valid}
	hp, hh := in.Map[LoadHP], in.Map[LoadHeater]
	n := int(in.Horizon / Quarter)
	for k := 0; k < n; k++ {
		t := in.Now.Truncate(Quarter).Add(time.Duration(k) * Quarter)
		s := Slot{Start: t}
		s.HeaterOn = in.Heater.NominalW > 0 && in.Heater.OnDuring(t, t.Add(Quarter))
		s.HPW = in.HP.At(t)
		for p := 0; p < 3; p++ {
			a := in.BaseA[p]
			if s.HeaterOn && hh[p] {
				a += AmpsPerPhase(in.Heater.NominalW, hh.Count())
			}
			if hp[p] {
				a += AmpsPerPhase(s.HPW, hp.Count())
			}
			s.HeadA[p] = in.BudgetA - a
		}
		f.Slots = append(f.Slots, s)
	}
	return f
}

// ---- overload handling ----

// HP levels as caps: the hpcontrol Level values (off 0, hold 1; boost 2 is "no cap").
const (
	HPOff  = 0
	HPHold = 1
)

// States of sensor.house_budget_state.
const (
	StateOK         = "ok"
	StateCurtailEV  = "curtailing_ev"
	StateCurtailHP  = "curtailing_hp"
	StateNoData     = "no_data"
	evHold          = 2 * time.Minute // EV cap kept this long after the last overload
	evMinA          = 6               // below this the charger pauses
	measureMaxAge   = time.Minute
	DefaultDelay    = 30 * time.Second
	DefaultCooldown = 15 * time.Minute
)

// Measure is the live per-phase current, published by the EV guard.
type Measure struct {
	At       time.Time
	PhaseA   [3]float64 // measured (Equalizer) or estimated (grid power) amps at the fuse, EV included
	EVA      float64    // the EV's current per phase
	Measured bool       // true: the Equalizer; false: the grid-power estimate
}

// OverloadCfg configures the handler.
type OverloadCfg struct {
	BudgetA  float64
	Delay    time.Duration // overload must last this long before acting (30 s)
	Cooldown time.Duration // the heat pump stays curtailed this long after the last overload
	Map      PhaseMap
}

// Decision is what the handler wants done right now.
type Decision struct {
	State   string
	EVCapA  int // -1: no cap; 0: pause; else the limit in A
	HPCap   int // -1: no cap; HPOff, HPHold
	Over    [3]bool
	Reason  string
	NoData  bool
	Changed bool // the caps changed on this step
}

// Overload is the curtailment state machine. Order: EV first (lower its
// limit, down to pause); only with the EV at zero, and the heat pump on an
// overloaded phase, the heat pump from boost to hold, then to off, kept for
// the cool-down. The hot-water heater is never touched, by design: there is
// no output for it.
type Overload struct {
	cfg       OverloadCfg
	overSince time.Time
	lastOver  time.Time
	lastAct   time.Time
	evCap     int
	evCapAt   time.Time
	hpCap     int
	hpAt      time.Time
}

// NewOverload returns a handler with no caps.
func NewOverload(c OverloadCfg) *Overload {
	if c.Delay <= 0 {
		c.Delay = DefaultDelay
	}
	if c.Cooldown <= 0 {
		c.Cooldown = DefaultCooldown
	}
	return &Overload{cfg: c, evCap: -1, hpCap: -1}
}

// Caps are the caps in force.
func (o *Overload) Caps() (ev, hp int) { return o.evCap, o.hpCap }

// Step advances the machine with the latest measure (ok=false: none).
func (o *Overload) Step(now time.Time, m Measure, ok bool) Decision {
	ev0, hp0 := o.evCap, o.hpCap
	d := Decision{}
	valid := ok && !m.At.IsZero() && now.Sub(m.At) <= measureMaxAge
	if !valid {
		o.overSince = time.Time{}
		d.NoData = true
		d.Reason = "no fresh current reading"
		// Caps already in force are kept until their hold lapses on the
		// next good reading; nothing new is done blind.
		return o.finish(d, ev0, hp0)
	}
	budget := o.cfg.BudgetA
	maxExcess := 0.0
	for p, a := range m.PhaseA {
		if a > budget {
			d.Over[p] = true
			if a-budget > maxExcess {
				maxExcess = a - budget
			}
		}
	}
	over := maxExcess > 0
	if over {
		if o.overSince.IsZero() {
			o.overSince = now
		}
		o.lastOver = now
	} else {
		o.overSince = time.Time{}
	}
	evSet, hpSet := o.cfg.Map[LoadEV], o.cfg.Map[LoadHP]
	overlaps := func(s PhaseSet) bool {
		for p := range s {
			if s[p] && d.Over[p] {
				return true
			}
		}
		return false
	}
	if over && now.Sub(o.overSince) >= o.cfg.Delay && (o.lastAct.IsZero() || now.Sub(o.lastAct) >= o.cfg.Delay) {
		evNonzero := m.EVA >= 1 && o.evCap != 0
		evAtZero := (o.evCap == 0 && now.Sub(o.evCapAt) >= o.cfg.Delay) || m.EVA < 1 || !overlaps(evSet)
		switch {
		case overlaps(evSet) && (evNonzero || (o.evCap > 0)):
			cur := m.EVA
			if o.evCap >= 0 && float64(o.evCap) < cur {
				cur = float64(o.evCap)
			}
			c := int(math.Floor(cur - maxExcess))
			if c < evMinA {
				c = 0
			}
			if o.evCap >= 0 && c >= o.evCap {
				c = o.evCap - 1
				if c < evMinA {
					c = 0
				}
			}
			o.evCap, o.evCapAt, o.lastAct = c, now, now
			d.Reason = fmt.Sprintf("phase over budget %.0f s: EV limit %d A", now.Sub(o.overSince).Seconds(), c)
		case evAtZero && overlaps(hpSet) && o.hpCap != HPOff:
			if o.hpCap == -1 {
				o.hpCap = HPHold
			} else {
				o.hpCap = HPOff
			}
			o.hpAt, o.lastAct = now, now
			d.Reason = fmt.Sprintf("EV at zero, phase still over budget: heat pump to %s", hpName(o.hpCap))
		default:
			d.Reason = "over budget; nothing left to curtail (manual load or the heater)"
		}
	}
	if !over {
		if o.hpCap >= 0 && now.Sub(latest(o.lastOver, o.hpAt)) >= o.cfg.Cooldown {
			o.hpCap = -1
		}
		if o.evCap >= 0 && o.hpCap < 0 && now.Sub(latest(o.lastOver, o.evCapAt)) >= evHold {
			o.evCap = -1
		}
	}
	return o.finish(d, ev0, hp0)
}

func hpName(l int) string {
	if l == HPOff {
		return "off"
	}
	return "hold"
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (o *Overload) finish(d Decision, ev0, hp0 int) Decision {
	d.EVCapA, d.HPCap = o.evCap, o.hpCap
	d.Changed = o.evCap != ev0 || o.hpCap != hp0
	switch {
	case o.hpCap >= 0:
		d.State = StateCurtailHP
	case o.evCap >= 0:
		d.State = StateCurtailEV
	case d.NoData:
		d.State = StateNoData
	default:
		d.State = StateOK
	}
	return d
}

// ---- hub ----

// capLife is how long a cap lasts without being refreshed by the planner:
// a dead planner must not leave a load throttled.
const capLife = 2 * time.Minute

// Hub is the in-process meeting point. All methods are safe for concurrent
// use. Default is the one the modules use; tests make their own.
type Hub struct {
	mu       sync.RWMutex
	heater   func() HeaterPlan
	hp       func() HPPlan
	measure  Measure
	haveMeas bool
	fc       Forecast
	haveFc   bool
	evCap    int
	hpCap    int
	capAt    time.Time
	evArmed  bool
	hpArmed  bool
}

// Default is the process-wide hub.
var Default = NewHub()

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{evCap: -1, hpCap: -1} }

// SetHeater registers the heater-plan provider (hwcontrol).
func (h *Hub) SetHeater(f func() HeaterPlan) { h.mu.Lock(); h.heater = f; h.mu.Unlock() }

// SetHP registers the heat-pump plan provider (hpcontrol).
func (h *Hub) SetHP(f func() HPPlan) { h.mu.Lock(); h.hp = f; h.mu.Unlock() }

// Heater returns the heater plan, Valid=false when no provider.
func (h *Hub) Heater() HeaterPlan {
	h.mu.RLock()
	f := h.heater
	h.mu.RUnlock()
	if f == nil {
		return HeaterPlan{}
	}
	return f()
}

// HP returns the heat-pump plan; ok=false when no provider is registered.
func (h *Hub) HP() (HPPlan, bool) {
	h.mu.RLock()
	f := h.hp
	h.mu.RUnlock()
	if f == nil {
		return HPPlan{}, false
	}
	return f(), true
}

// PublishMeasure is called by the EV guard on every evaluation.
func (h *Hub) PublishMeasure(m Measure) { h.mu.Lock(); h.measure, h.haveMeas = m, true; h.mu.Unlock() }

// Measure returns the latest measure.
func (h *Hub) Measure() (Measure, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.measure, h.haveMeas
}

// SetForecast is called by the house planner.
func (h *Hub) SetForecast(f Forecast) { h.mu.Lock(); h.fc, h.haveFc = f, true; h.mu.Unlock() }

// EVCeilingA is the forecast ceiling for the EV at slot t, in amps
// (unclamped, floor applied): ok=false when there is no forecast, it is
// older than maxAge at now, or it does not cover t.
func (h *Hub) EVCeilingA(now, t time.Time, maxAge time.Duration) (float64, bool) {
	h.mu.RLock()
	f, have := h.fc, h.haveFc
	h.mu.RUnlock()
	if !have || now.Sub(f.At) > maxAge {
		return 0, false
	}
	v, ok := f.EVHeadroom(t)
	if !ok {
		return 0, false
	}
	return math.Floor(v), true
}

// SetCaps is called by the house planner on every step (refreshing them).
func (h *Hub) SetCaps(now time.Time, evA, hpLevel int) {
	h.mu.Lock()
	h.evCap, h.hpCap, h.capAt = evA, hpLevel, now
	h.mu.Unlock()
}

// EVCap is the EV current cap (A, 0 = pause) when one is in force.
func (h *Hub) EVCap(now time.Time) (int, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.evCap < 0 || now.Sub(h.capAt) > capLife {
		return 0, false
	}
	return h.evCap, true
}

// HPCap is the heat-pump level cap (HPOff, HPHold) when one is in force.
func (h *Hub) HPCap(now time.Time) (int, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.hpCap < 0 || now.Sub(h.capAt) > capLife {
		return 0, false
	}
	return h.hpCap, true
}

// SetArmed records whether the EV / heat-pump executors are live and armed.
func (h *Hub) SetArmed(ev, hp *bool) {
	h.mu.Lock()
	if ev != nil {
		h.evArmed = *ev
	}
	if hp != nil {
		h.hpArmed = *hp
	}
	h.mu.Unlock()
}

// Armed reports the executors' arming.
func (h *Hub) Armed() (ev, hp bool) { h.mu.RLock(); defer h.mu.RUnlock(); return h.evArmed, h.hpArmed }
