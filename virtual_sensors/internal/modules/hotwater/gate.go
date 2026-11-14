package hotwater

// 0.6.2: the three additions that stop the detector crediting things that
// are not the heater and stop it missing the heater when another load
// moves at the same moment:
//
//   - the switch gate: never open a cycle while the motorised switch is
//     known OFF (cover "closed"), and close an open cycle when it has
//     been OFF for a while (a kettle was credited 2026-11-13 06:32 with
//     the switch closed);
//   - the phase gate: the heater is a two-phase 400 V load, +7.3 A on L1
//     AND L2 and nothing on L3 (36 clean steps 2026-11-10..13: L1 7.37 A,
//     L2 7.25 A, L3 0.0 A). The Equalizer's per-phase current confirms an
//     ON step found on the 5 s grid meter; the kettle (L1 only), the
//     dishwasher heater (L3 only) and the heat pump boost (L2 only) fail it;
//   - the residual plateau fallback: if no step was ever seen (the EV
//     started 22 s before the heater, 2026-11-13 20:24, chaining into one
//     7 kW event) a residual that sits at heater level for plateauS opens
//     the cycle anyway.

import (
	"encoding/json"
	"log"
	"sort"
	"strings"
	"time"

	"virtual_sensors/internal/modules"
)

const (
	// DefaultCoverEntity is the motorised switch (same default as hwcontrol's hwc_cover_entity).
	DefaultCoverEntity = "cover.hot_water_switch"
	defaultEqEntity    = "equalizer_current"
	defaultEqOnline    = "binary_sensor.equalizer_online"

	// coverClosedSlackS: the cover reports "closed" for a second at the
	// command before it goes "closing"; only a closed state held this long
	// counts as known OFF.
	coverClosedSlackS = 5.0
	// switchOffForceCloseS: an open cycle with the switch known OFF this
	// long is closed (the real off step is normally seen within seconds).
	switchOffForceCloseS = 180.0
	// switchOffCreditS: energy is credited up to this long after the
	// switch reported closed (the motor takes about 7 s).
	switchOffCreditS = 15.0

	// Phase gate.
	phaseWaitS      = 20.0 // the Equalizer reports 5-10 s after the grid step
	phaseRetentionS = 45 * 60.0
	phaseFreshS     = 15 * 60.0
	phaseL3MaxRise  = 2.5 // A: L3 may not rise more than this
	phaseEVLoW      = 500.0

	// Residual plateau fallback.
	plateauMinW  = 2700.0
	plateauS     = 120.0
	plateauMaxW  = 3900.0 // median above this: heater plus something big, not a clean heater
	plateauBaseW = 300.0  // the usual unmetered base, taken off the level to get the heater's step
)

// phaseSample is one Equalizer reading (A per phase).
type phaseSample struct {
	T          float64
	L1, L2, L3 float64
}

// coverTransition is one cover state change.
type coverTransition struct {
	T     float64
	State string
}

// gateOptions are the 0.6.2 options of this module. Absent = default.
type gateOptions struct {
	CoverEntity   *string  `json:"hwc_cover_entity"`
	EqEntity      *string  `json:"ev_equalizer_entity"`
	EqOnline      *string  `json:"ev_equalizer_online_entity"`
	PhaseGate     *bool    `json:"hw_phase_gate"`
	PhaseStepA    *float64 `json:"hw_phase_step_a"`
	PhaseTolA     *float64 `json:"hw_phase_tol_a"`
	SwitchGate    *bool    `json:"hw_switch_gate"`
	PlateauRescue *bool    `json:"hw_plateau_rescue"`
}

// Configure implements modules.Configurable (0.6.2).
func (m *Module) Configure(raw []byte) error {
	var o gateOptions
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
	}
	if o.CoverEntity != nil {
		m.coverEnt = *o.CoverEntity // "" disables the switch gate
	}
	if o.EqEntity != nil && *o.EqEntity != "" {
		m.eqEnt = strings.TrimPrefix(*o.EqEntity, "sensor.")
	}
	if o.EqOnline != nil && *o.EqOnline != "" {
		m.eqOnE = strings.TrimPrefix(*o.EqOnline, "sensor.")
	}
	if o.PhaseGate != nil {
		m.phaseGate = *o.PhaseGate
	}
	if o.PhaseStepA != nil {
		if *o.PhaseStepA >= 3 && *o.PhaseStepA <= 15 {
			m.phaseStepA = *o.PhaseStepA
		} else {
			log.Printf("hot_water: option hw_phase_step_a out of range 3..15; default applied")
		}
	}
	if o.PhaseTolA != nil {
		if *o.PhaseTolA >= 0.5 && *o.PhaseTolA <= 6 {
			m.phaseTolA = *o.PhaseTolA
		} else {
			log.Printf("hot_water: option hw_phase_tol_a out of range 0.5..6; default applied")
		}
	}
	if o.SwitchGate != nil {
		m.switchGate = *o.SwitchGate
	}
	if o.PlateauRescue != nil {
		m.plateauRescue = *o.PlateauRescue
	}
	return nil
}

// ---- inputs ----

// gateInputs are the extra entities this module reads.
func (m *Module) gateInputs() []modules.InputSpec {
	var in []modules.InputSpec
	if m.coverEnt != "" {
		in = append(in, modules.InputSpec{EntityID: m.coverEnt})
	}
	if m.phaseGate {
		in = append(in,
			modules.InputSpec{EntityID: m.eqEnt, Attributes: []string{"state_currentL1", "state_currentL2", "state_currentL3"}},
			modules.InputSpec{EntityID: m.eqOnE})
	}
	return in
}

// handleGateInput consumes a cover / Equalizer update. It reports whether
// the update was one of them.
func (m *Module) handleGateInput(t float64, u modules.StateUpdate) bool {
	switch {
	case m.coverEnt != "" && u.EntityID == m.coverEnt:
		st := u.State
		if !u.Available && st == "" {
			st = "unavailable"
		}
		if n := len(m.cover); n == 0 || m.cover[n-1].State != st {
			m.cover = append(m.cover, coverTransition{T: t, State: st})
			if len(m.cover) > 16 {
				m.cover = m.cover[len(m.cover)-16:]
			}
		}
		return true
	case m.phaseGate && u.EntityID == m.eqEnt:
		var a [3]float64
		ok := true
		for i, k := range []string{"state_currentL1", "state_currentL2", "state_currentL3"} {
			raw, has := u.Attrs[k]
			if !has || json.Unmarshal(raw, &a[i]) != nil {
				ok = false
			}
		}
		if ok {
			m.eq = append(m.eq, phaseSample{T: t, L1: a[0], L2: a[1], L3: a[2]})
			m.eqAt = t
			cut := t - phaseRetentionS
			i := 0
			for i < len(m.eq) && m.eq[i].T < cut {
				i++
			}
			m.eq = m.eq[i:]
		}
		return true
	case m.phaseGate && u.EntityID == m.eqOnE:
		m.eqOnline = u.State == "on"
		m.eqOnlineSeen = true
		return true
	}
	return false
}

// ---- switch gate ----

// switchKnownOffAt reports whether the switch is known OFF at time t, and
// since when: the latest cover state at or before t is "closed", has held
// at least coverClosedSlackS, and the gate is enabled. An unknown / never
// seen / unavailable cover is NOT known OFF.
func (m *Module) switchKnownOffAt(t float64) (bool, float64) {
	if !m.switchGate || m.coverEnt == "" {
		return false, 0
	}
	i := sort.Search(len(m.cover), func(i int) bool { return m.cover[i].T > t }) - 1
	if i < 0 || m.cover[i].State != "closed" {
		return false, 0
	}
	if t-m.cover[i].T < coverClosedSlackS {
		return false, 0
	}
	return true, m.cover[i].T
}

// ---- phase gate ----

type phaseVerdict int

const (
	pvUnknown phaseVerdict = iota // no usable Equalizer evidence: fall back to the step logic
	pvConfirm
	pvReject
)

func (m *Module) phaseAt(t float64) (phaseSample, bool) {
	i := sort.Search(len(m.eq), func(i int) bool { return m.eq[i].T > t }) - 1
	if i < 0 {
		return phaseSample{}, false
	}
	return m.eq[i], true
}

func (m *Module) evActiveBetween(lo, hi float64) bool {
	r := m.loads["loaver_power"]
	if r == nil {
		return false
	}
	for i, t := range r.t {
		if t >= lo && t <= hi && r.v[i] >= phaseEVLoW {
			return true
		}
	}
	// the value held into the window
	last := -1
	for i, t := range r.t {
		if t < lo {
			last = i
		}
	}
	return last >= 0 && r.v[last] >= phaseEVLoW
}

// phaseFresh: the Equalizer is online and was heard from inside phaseFreshS
// (the same rule as ev_control: it reports on change only, so a steady
// house is silent for minutes).
func (m *Module) phaseFresh(now float64) bool {
	return m.phaseGate && m.eqOnlineSeen && m.eqOnline && len(m.eq) > 0 && now-m.eqAt < phaseFreshS
}

// phaseCheck judges a candidate heater step whose per-phase change is read
// between tBefore (just before the step) and tAfter. now is the clock of
// the judgement. It rejects only on POSITIVE evidence of another pattern:
// when the Equalizer is offline or stale, has no reading before tBefore, or
// reported nothing at all in the window, the verdict is unknown and the
// existing logic stands (missing a real heater costs more than crediting a
// stray load).
func (m *Module) phaseCheck(now, tBefore, tAfter float64) (phaseVerdict, [3]float64, bool) {
	var d [3]float64
	if !m.phaseFresh(now) {
		return pvUnknown, d, false
	}
	b, ok := m.phaseAt(tBefore)
	if !ok {
		return pvUnknown, d, false
	}
	a, _ := m.phaseAt(tAfter)
	updated := false
	for _, s := range m.eq {
		if s.T > tBefore && s.T <= tAfter {
			updated = true
			break
		}
	}
	if !updated {
		return pvUnknown, d, false
	}
	d = [3]float64{a.L1 - b.L1, a.L2 - b.L2, a.L3 - b.L3}
	step, tol := m.phaseStepA, m.phaseTolA
	ev := m.evActiveBetween(tBefore-30, tAfter)
	if ev {
		// A three-phase EV moves all three phases: look at each of L1 and
		// L2 relative to L3. The EV's phases are not equal enough to hold
		// the +-tol band (L1 +4.9 A beside L2 +7.7 A with the heater on
		// under a charging EV, 2026-11-13 19:24Z), so the band is wider.
		x1, x2 := d[0]-d[2], d[1]-d[2]
		lo, hi := step-tol-1.8, step+tol+1.5
		if x1 >= lo && x1 <= hi && x2 >= lo && x2 <= hi {
			return pvConfirm, d, true
		}
		return pvReject, d, true
	}
	if abs(d[0]-step) <= tol && abs(d[1]-step) <= tol && d[2] <= phaseL3MaxRise {
		return pvConfirm, d, false
	}
	return pvReject, d, false
}

// phaseEndDropA is the per-phase drop that marks the heater leaving.
const (
	phaseEndDropA = 5.0
	phaseEndWinS  = 60.0
)

// phaseEnd reports the Equalizer evidence for a heater end at tOff: 1 when
// L1 and L2 each dropped by >= phaseEndDropA between the reading at tOff and
// the last reading within phaseEndWinS after it (the Equalizer reports about
// every 30 s), 2 when readings exist but show no such drop, 0 (unknown) when
// the gate is off, the switch is known OFF, or there is no reading before or
// after tOff. Only used to confirm a thermostat end, never to reject a cycle.
func (m *Module) phaseEnd(tOff float64) int {
	if !m.phaseGate {
		return 0
	}
	if off, _ := m.switchKnownOffAt(tOff); off {
		return 0
	}
	b, ok := m.phaseAt(tOff)
	if !ok {
		return 0
	}
	var a phaseSample
	found := false
	for _, s := range m.eq {
		if s.T > tOff && s.T <= tOff+phaseEndWinS {
			a, found = s, true
		}
	}
	if !found {
		return 0
	}
	if b.L1-a.L1 >= phaseEndDropA && b.L2-a.L2 >= phaseEndDropA {
		return 1
	}
	return 2
}

// pendingOn is an ON step waiting for the Equalizer to confirm it.
type pendingOnStep struct {
	TStart, TEnd, Magnitude float64
}

// lostSum is the held last value of every known load that has gone
// unavailable: its real consumption is still in the grid reading, so the
// plateau level (unlike the published residual) keeps subtracting it.
func (m *Module) lostSum() float64 {
	s := 0.0
	for _, v := range m.lostHeld {
		s += v
	}
	return s
}

// residualMedianSince is the median published residual since t0, less the
// known-load discontinuity offset.
func (m *Module) residualMedianSince(t0 float64) float64 {
	var v []float64
	for i, t := range m.residual.t {
		if t >= t0 {
			v = append(v, m.residual.v[i])
		}
	}
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	return v[len(v)/2] - m.lostSum()
}

// gateStep runs once per fresh grid sample, after step detection: the
// switch-OFF close of an open cycle, the pending-ON phase confirmation and
// the residual plateau fallback.
func (m *Module) gateStep(t float64, tm time.Time, residual float64) []modules.OutputUpdate {
	var out []modules.OutputUpdate

	// 1. A cycle still open long after the switch went OFF is not the heater.
	if m.open {
		if off, since := m.switchKnownOffAt(t); off && t-since >= switchOffForceCloseS {
			endT := since + switchOffCreditS
			if endT < m.onTime {
				endT = m.onTime
			}
			if endT > t {
				endT = t
			}
			dur := endT - m.onTime
			closed := CycleClosed{OnTime: unixTime(m.onTime).UTC(), OffTime: unixTime(endT).UTC(), Reason: "switch_off", Replay: m.replay}
			out = append(out, modules.OutputUpdate{ObjectID: OutPowerObjectID, Time: unixTime(endT), Value: 0})
			if dur >= m.params.MinOnDurationS {
				kwh := abs(m.onMagnitude) * (dur / 3600.0) / 1000.0
				m.energyKwh += kwh
				out = append(out, modules.OutputUpdate{ObjectID: OutEnergyObjectID, Time: unixTime(endT), Value: m.energyKwh})
				closed.Accepted, closed.Kwh = true, kwh
			}
			m.signal(closed)
			m.open = false
			m.pendingOff = nil
			m.belowFloor = false
		}
	}

	// 2. A step waiting for the Equalizer.
	if m.pendingOn != nil && t >= m.pendingOn.TEnd+phaseWaitS {
		e := *m.pendingOn
		m.pendingOn = nil
		v, d, ev := m.phaseCheck(t, e.TStart, e.TEnd+phaseWaitS)
		if v == pvReject {
			log.Printf("hot_water: step %+.0f W at %s is not the heater: phase change L1 %+.1f L2 %+.1f L3 %+.1f A (ev=%v)",
				e.Magnitude, unixTime(e.TEnd).UTC().Format(time.RFC3339), d[0], d[1], d[2], ev)
		} else if !m.open {
			out = append(out, m.beginCycle(e.TEnd, e.Magnitude, m.residualAt(e.TStart), false)...)
		}
	}

	// 3. The residual plateau fallback.
	if m.plateauRescue {
		level := residual - m.lostSum()
		if level >= plateauMinW {
			if !m.plateauActive {
				m.plateauActive, m.plateauDone, m.plateauT0 = true, false, t
			}
		} else {
			m.plateauActive = false
		}
		if m.plateauActive && !m.plateauDone && !m.open && m.pendingOff == nil && m.pendingOn == nil &&
			t-m.plateauT0 >= plateauS {
			if med := m.residualMedianSince(m.plateauT0); med <= plateauMaxW {
				m.plateauDone = true // judged once per plateau
				off, _ := m.switchKnownOffAt(t)
				v, d, ev := m.phaseCheck(t, m.plateauT0-20, m.plateauT0+25)
				switch {
				case off:
					log.Printf("hot_water: residual plateau %.0f W since %s ignored: the switch is known OFF", med, unixTime(m.plateauT0).UTC().Format(time.RFC3339))
				case v == pvReject:
					log.Printf("hot_water: residual plateau %.0f W since %s is not the heater: phase change L1 %+.1f L2 %+.1f L3 %+.1f A (ev=%v)",
						med, unixTime(m.plateauT0).UTC().Format(time.RFC3339), d[0], d[1], d[2], ev)
				default:
					mag := med - plateauBaseW
					if mag < m.params.StepLo {
						mag = m.params.StepLo
					}
					if mag > m.params.StepHi {
						mag = m.params.StepHi
					}
					log.Printf("hot_water: residual plateau %.0f W since %s opened a cycle (no step was seen)", med, unixTime(m.plateauT0).UTC().Format(time.RFC3339))
					out = append(out, m.beginCycle(m.plateauT0, mag, med-mag, true)...)
				}
			}
		}
	}
	return out
}
