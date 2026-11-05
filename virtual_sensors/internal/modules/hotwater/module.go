// Package hotwater is the live virtual-sensor module for the household's
// hot-water immersion heater (Taiga #237). It shares detect.FindMergedEvents
// with the backfill path verbatim (see detect/detect.go) so step detection
// is identical live and historical; PAIRING is a deliberately simplified,
// no-lookahead version of detect.PairEvents, documented below and in
// DOCS.md "Live vs backfill" - the batch pairer can look ahead across
// several off-candidates for the best magnitude match before closing a
// cycle, which a live stream cannot do without delaying every
// publication. The practical effect: live occasionally reports a
// provisional ON that a backfill re-run later rejects or re-times - see
// DOCS.md for when and why.
package hotwater

import (
	"encoding/json"
	"log"
	"sort"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hotwater/detect"
)

// Entity ids, ported verbatim from build.py's GRID_ENTITY / KNOWN_LOAD_ENTITIES.
const (
	GridEntity = "grid_meter_power"
)

var KnownLoadEntities = []string{
	"loaver_power",
	"washing_machine_power",
	"sonoff_100168af89_power",
	"sonoff_1001721983_power",
	"guest_house_oil_heater_power",
	"heat_pump_power_power",
	"fridge_power",
}

const (
	OutPowerObjectID  = "hot_water_estimated_power"
	OutEnergyObjectID = "hot_water_estimated_energy"
)

func init() {
	modules.Register("hot_water", func() modules.Module { return New() })
}

// loadRing is a tiny time-pruned ring of recent (t, v) pairs for one known
// load, used to answer the coincident-load reject check against a live
// stream without keeping unbounded history.
type loadRing struct {
	t []float64
	v []float64
}

func (r *loadRing) push(t, v float64, keepS float64) {
	r.t = append(r.t, t)
	r.v = append(r.v, v)
	cut := t - keepS
	i := 0
	for i < len(r.t) && r.t[i] < cut {
		i++
	}
	if i > 0 {
		r.t = r.t[i:]
		r.v = r.v[i:]
	}
}

func (r *loadRing) rangeOK(loT, hiT, maxDelta float64) bool {
	var vals []float64
	for i, t := range r.t {
		if t >= loT && t <= hiT {
			vals = append(vals, r.v[i])
		}
	}
	if len(vals) < 2 {
		return true
	}
	mn, mx := vals[0], vals[0]
	for _, v := range vals {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return mx-mn <= maxDelta
}

// stable is the live equivalent of detect.go's plateauStable: true if every
// buffered sample with loT < t < hiT stays within tol of anchor. Used by
// the staged-reheat bridge (admitMergedEvent) against a short residual
// history ring, since a restart-safe live stream can't re-query the raw
// grid series the way the batch pipeline can.
func (r *loadRing) stable(loT, hiT, anchor, tol float64) bool {
	for i, t := range r.t {
		if t > loT && t < hiT {
			if abs(r.v[i]-anchor) > tol {
				return false
			}
		}
	}
	return true
}

// persistedMergedEvent is the JSON shape of one detect.MergedEvent, used to
// persist the pending staged-reheat buffer below.
type persistedMergedEvent struct {
	TStart    float64 `json:"t_start"`
	TEnd      float64 `json:"t_end"`
	Magnitude float64 `json:"magnitude"`
}

// persistedState is the module's /data/hot_water-state.json shape.
type persistedState struct {
	EnergyKwh      float64            `json:"energy_kwh"`
	ProcessedUntil float64            `json:"processed_until_unix"`
	Open           bool               `json:"open"`
	OnTime         float64            `json:"on_time,omitempty"`
	OnMagnitude    float64            `json:"on_magnitude,omitempty"`
	LastGridT      float64            `json:"last_grid_t,omitempty"`
	LastGridV      float64            `json:"last_grid_v,omitempty"`
	HaveLastGrid   bool               `json:"have_last_grid,omitempty"`
	LastLoadT      map[string]float64 `json:"last_load_t,omitempty"`
	LastLoadV      map[string]float64 `json:"last_load_v,omitempty"`
	RunActive      bool               `json:"run_active,omitempty"`
	RunStartT      float64            `json:"run_start_t,omitempty"`
	RunSign        int                `json:"run_sign,omitempty"`
	RunMagnitude   float64            `json:"run_magnitude,omitempty"`
	LastSeedT      float64            `json:"last_seed_t,omitempty"`
	// PendingStage is the staged-reheat bridge buffer (see admitMergedEvent)
	// - persisted so a restart mid-bridge (e.g. between the two sub-steps
	// of a staged reheat) doesn't silently drop it, same concern this fix
	// exists for in the first place.
	PendingStage []persistedMergedEvent `json:"pending_stage,omitempty"`
}

// Module is the hot_water virtual sensor.
type Module struct {
	params detect.Params

	lastGrid     detect.Point
	haveLastGrid bool

	lastLoad map[string]detect.Point
	loads    map[string]*loadRing

	// residual is a short time-pruned ring of recent (t, residual) samples
	// (kept back StageWindowS), used by admitMergedEvent's plateau-
	// stability check - the live equivalent of detect.go's plateauStable,
	// which the batch pipeline answers directly from the full raw series.
	residual *loadRing

	// incremental merge-run state - mirrors detect.FindMergedEvents's
	// per-run variables so a restart can resume mid-run (a gap caused by
	// the restart itself will simply flush it, same as any other gap).
	// lastSeedT is the time of the last sample INCLUDED in the run (every
	// included delta refreshes it, not just seed-level ones, matching
	// FindMergedEvents); runStartT is fixed for the run's life and bounds
	// how far a sub-seed (below SeedDeltaMin) delta may extend it.
	runActive    bool
	runStartT    float64
	runSign      int
	runMagnitude float64
	lastSeedT    float64

	// pendingStage holds 1-2 sub-threshold merged events (same sign)
	// waiting to see if a further same-sign event arrives within
	// StageWindowS, bridged by a stable plateau, to land the combined
	// magnitude in the on/off band - the live, streaming equivalent of
	// detect.go's FindStagedEvents. See admitMergedEvent.
	pendingStage []detect.MergedEvent

	// pairing state
	open         bool
	onTime       float64
	onMagnitude  float64
	belowFloor   bool
	belowSince   float64
	maxDeficit   float64
	dipTolerated bool

	energyKwh      float64
	processedUntil float64
}

func New() *Module {
	return &Module{
		params:   detect.DefaultParams(),
		lastLoad: map[string]detect.Point{},
		loads:    map[string]*loadRing{},
		residual: &loadRing{},
	}
}

func (m *Module) Name() string { return "hot_water" }

// InitialStates publishes the restored energy total and the current power
// (the open cycle's step, or 0) as soon as MQTT is connected.
func (m *Module) InitialStates() []modules.OutputUpdate {
	now := time.Now().UTC()
	power := 0.0
	if m.open {
		power = m.onMagnitude
	}
	return []modules.OutputUpdate{
		{ObjectID: OutPowerObjectID, Time: now, Value: power},
		{ObjectID: OutEnergyObjectID, Time: now, Value: m.energyKwh},
	}
}

func (m *Module) Inputs() []modules.InputSpec {
	specs := []modules.InputSpec{{EntityID: GridEntity}}
	for _, e := range KnownLoadEntities {
		specs = append(specs, modules.InputSpec{EntityID: e})
	}
	return specs
}

func (m *Module) Outputs() []modules.OutputSpec {
	return []modules.OutputSpec{
		{
			ObjectID: OutPowerObjectID, UniqueID: "virtual_sensors_" + OutPowerObjectID,
			Name: "Hot water estimated power", DeviceClass: "power",
			StateClass: "measurement", Unit: "W",
		},
		{
			ObjectID: OutEnergyObjectID, UniqueID: "virtual_sensors_" + OutEnergyObjectID,
			Name: "Hot water estimated energy", DeviceClass: "energy",
			StateClass: "total_increasing", Unit: "kWh",
		},
	}
}

func (m *Module) Init(persisted []byte) error {
	if len(persisted) == 0 {
		return nil
	}
	var ps persistedState
	if err := json.Unmarshal(persisted, &ps); err != nil {
		log.Printf("hot_water: WARNING could not parse persisted state, starting fresh: %v", err)
		return nil
	}
	m.energyKwh = ps.EnergyKwh
	m.processedUntil = ps.ProcessedUntil
	m.open = ps.Open
	m.onTime = ps.OnTime
	m.onMagnitude = ps.OnMagnitude
	m.haveLastGrid = ps.HaveLastGrid
	m.lastGrid = detect.Point{T: ps.LastGridT, V: ps.LastGridV}
	for name, t := range ps.LastLoadT {
		m.lastLoad[name] = detect.Point{T: t, V: ps.LastLoadV[name]}
	}
	m.runActive = ps.RunActive
	m.runStartT = ps.RunStartT
	m.runSign = ps.RunSign
	m.runMagnitude = ps.RunMagnitude
	m.lastSeedT = ps.LastSeedT
	for _, pe := range ps.PendingStage {
		m.pendingStage = append(m.pendingStage, detect.MergedEvent{TStart: pe.TStart, TEnd: pe.TEnd, Magnitude: pe.Magnitude})
	}
	return nil
}

func (m *Module) Persist() ([]byte, error) {
	ps := persistedState{
		EnergyKwh: m.energyKwh, ProcessedUntil: m.processedUntil,
		Open: m.open, OnTime: m.onTime, OnMagnitude: m.onMagnitude,
		HaveLastGrid: m.haveLastGrid, LastGridT: m.lastGrid.T, LastGridV: m.lastGrid.V,
		LastLoadT: map[string]float64{}, LastLoadV: map[string]float64{},
		RunActive: m.runActive, RunStartT: m.runStartT, RunSign: m.runSign,
		RunMagnitude: m.runMagnitude, LastSeedT: m.lastSeedT,
	}
	for name, p := range m.lastLoad {
		ps.LastLoadT[name] = p.T
		ps.LastLoadV[name] = p.V
	}
	for _, pe := range m.pendingStage {
		ps.PendingStage = append(ps.PendingStage, persistedMergedEvent{TStart: pe.TStart, TEnd: pe.TEnd, Magnitude: pe.Magnitude})
	}
	return json.Marshal(ps)
}

// knownSum returns the current LOCF sum of every known load.
func (m *Module) knownSum() float64 {
	sum := 0.0
	for _, p := range m.lastLoad {
		sum += p.V
	}
	return sum
}

func (m *Module) isOn(mag float64) bool  { return m.params.StepLo <= mag && mag <= m.params.StepHi }
func (m *Module) isOff(mag float64) bool { return -m.params.StepHi <= mag && mag <= -m.params.StepLo }

// rejectOK checks every known load for movement > RejectLoadDelta within
// RejectWindowS of [tStart, tEnd] - the live equivalent of detect's
// MakeArrayRejectCheck, against the short rolling ring buffers instead of
// the full series (see loadRing).
func (m *Module) rejectOK(tStart, tEnd float64) bool {
	loT, hiT := tStart-m.params.RejectWindowS, tEnd+m.params.RejectWindowS
	names := make([]string, 0, len(m.loads))
	for n := range m.loads {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !m.loads[n].rangeOK(loT, hiT, m.params.RejectLoadDelta) {
			return false
		}
	}
	return true
}

// onNewGridSample feeds one grid sample through the incremental
// FindMergedEvents-equivalent run tracker, calling emit(event) for every
// event that FINALIZES as a result (there may be more than one if a gap
// both flushes the previous run and immediately starts a new seed).
//
// This is a CHAIN, matching detect.go's FindMergedEvents: any nonzero delta
// can start or extend a run (not just one that itself reaches
// SeedDeltaMin), so a transition whose individual samples are each too
// small to seed on their own but whose sum over the window is not is still
// caught (see FindMergedEvents' doc comment for the full rationale,
// including why a sub-seed delta may only extend a run within
// MergeWindowS of the run's OWN start, while a seed-level delta can extend
// it indefinitely as long as consecutive seeds keep arriving within
// MergeWindowS of each other). A flushed run only becomes an event if its
// net magnitude reaches SeedDeltaMin.
func (m *Module) onNewGridSample(t, v float64, emit func(detect.MergedEvent)) {
	if !m.haveLastGrid {
		m.lastGrid = detect.Point{T: t, V: v}
		m.haveLastGrid = true
		return
	}
	dt := t - m.lastGrid.T
	flush := func() {
		if m.runActive && abs(m.runMagnitude) >= m.params.SeedDeltaMin {
			emit(detect.MergedEvent{TStart: m.runStartT, TEnd: m.lastGrid.T, Magnitude: m.runMagnitude})
		}
		m.runActive = false
	}
	if dt <= 0 || dt > m.params.MaxGapS {
		flush()
		m.lastGrid = detect.Point{T: t, V: v}
		return
	}
	delta := v - m.lastGrid.V
	if delta == 0 {
		if m.runActive && (t-m.lastSeedT) > m.params.MergeWindowS {
			flush()
		}
		m.lastGrid = detect.Point{T: t, V: v}
		return
	}
	sign := 1
	if delta <= 0 {
		sign = -1
	}
	isSeed := abs(delta) >= m.params.SeedDeltaMin
	canExtend := m.runActive && sign == m.runSign &&
		(m.lastGrid.T-m.lastSeedT) <= m.params.MergeWindowS &&
		(isSeed || (t-m.runStartT) <= m.params.MergeWindowS)
	if canExtend {
		m.runMagnitude += delta
		m.lastSeedT = t
	} else {
		flush()
		m.runStartT = m.lastGrid.T
		m.runSign = sign
		m.runMagnitude = delta
		m.lastSeedT = t
		m.runActive = true
	}
	m.lastGrid = detect.Point{T: t, V: v}
}

// admitMergedEvent is the live, streaming equivalent of detect.go's
// FindStagedEvents: it tries to bridge up to MaxSubsteps consecutive
// same-sign sub-threshold merged events (buffered in pendingStage), across
// a stable intervening plateau (checked against the short residual ring -
// see loadRing.stable), into one staged event before handing it to
// handleEvent. An already-qualifying event (on/off band on its own) is
// passed straight through, same as the batch pass. Because live can't look
// ahead, a failed bridge (overshoot, unstable plateau, or window expiry)
// emits the FIRST buffered event standalone (it was sub-threshold, so
// handleEvent's isOn/isOff checks simply ignore it) and restarts the
// pending buffer from the SECOND one, mirroring detect.go's own re-
// examination of events[i+1:] after a failed run.
func (m *Module) admitMergedEvent(e detect.MergedEvent, emit func(detect.MergedEvent)) {
	if abs(e.Magnitude) >= m.params.StepLo {
		m.flushPendingStandalone(emit)
		emit(e)
		return
	}

	if len(m.pendingStage) > 0 {
		first := m.pendingStage[0]
		last := m.pendingStage[len(m.pendingStage)-1]
		sameSign := (first.Magnitude > 0) == (e.Magnitude > 0)
		withinWindow := e.TEnd-first.TStart <= m.params.StageWindowS
		if sameSign && withinWindow && len(m.pendingStage) < m.params.MaxSubsteps &&
			m.residual.stable(last.TEnd, e.TStart, m.residualAt(last.TEnd), m.params.PlateauTol) {
			m.pendingStage = append(m.pendingStage, e)
			total := 0.0
			for _, p := range m.pendingStage {
				total += p.Magnitude
			}
			if m.params.StepLo <= abs(total) && abs(total) <= m.params.StepHi {
				combined := detect.MergedEvent{TStart: m.pendingStage[0].TStart, TEnd: e.TEnd, Magnitude: total}
				m.pendingStage = nil
				emit(combined)
				return
			}
			if abs(total) > m.params.StepHi {
				// Overshot: emit the first buffered event standalone (it
				// never qualified alone, so this is a no-op downstream)
				// and re-attempt bridging from the rest, same as
				// detect.go's FindStagedEvents advancing by 1 on failure.
				rest := m.pendingStage[1:]
				m.pendingStage = nil
				emit(first)
				for _, r := range rest {
					m.admitMergedEvent(r, emit)
				}
				return
			}
			return // still accumulating, wait for more
		}
	}

	m.flushPendingStandalone(emit)
	m.pendingStage = []detect.MergedEvent{e}
}

// flushPendingStandalone emits any buffered sub-threshold event(s) unmerged
// when a new event can't be bridged with them (different sign, outside
// StageWindowS, an unstable plateau, or the buffer already at MaxSubsteps).
// Each was already sub-threshold on its own, so handleEvent's isOn/isOff
// checks simply ignore it - this is a no-op downstream, matching detect.go's
// FindStagedEvents leaving a failed run's events unmerged.
func (m *Module) flushPendingStandalone(emit func(detect.MergedEvent)) {
	if len(m.pendingStage) == 0 {
		return
	}
	pending := m.pendingStage
	m.pendingStage = nil
	for _, p := range pending {
		emit(p)
	}
}

// residualAt returns the most recent buffered residual sample at or before
// t - the live equivalent of detect.go's valueAt.
func (m *Module) residualAt(t float64) float64 {
	v := 0.0
	for i, rt := range m.residual.t {
		if rt <= t {
			v = m.residual.v[i]
		} else {
			break
		}
	}
	return v
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// OnStateUpdate implements modules.Module.
func (m *Module) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	t := float64(u.Time.UnixNano()) / 1e9
	if t <= m.processedUntil {
		return nil // already applied (warm-up replay overlapping persisted state)
	}
	m.processedUntil = t

	// loaver_power (the EV charger) reports kW; every other input is W.
	// The backfill converts by InfluxDB measurement; live has no unit, so
	// it is fixed here.
	if u.EntityID == "loaver_power" {
		u.Value *= 1000
	}
	if u.EntityID != GridEntity {
		if u.Available {
			m.lastLoad[u.EntityID] = detect.Point{T: t, V: u.Value}
			if m.loads[u.EntityID] == nil {
				m.loads[u.EntityID] = &loadRing{}
			}
			m.loads[u.EntityID].push(t, u.Value, m.params.RejectWindowS*2)
		}
		// A known load changing does not, on its own, step the grid
		// residual - only a fresh grid sample re-evaluates the residual
		// below. The reject-check rings above are consulted when a grid
		// step is later seen.
		return nil
	}

	if !u.Available {
		return nil
	}

	var out []modules.OutputUpdate
	residual := u.Value - m.knownSum()
	m.residual.push(t, residual, m.params.StageWindowS)

	m.onNewGridSample(t, u.Value, func(e detect.MergedEvent) {
		m.admitMergedEvent(e, func(staged detect.MergedEvent) {
			out = append(out, m.handleEvent(staged)...)
		})
	})

	// Floor check for an open cycle, evaluated on every grid sample (not
	// just at merged-event boundaries) so a floor violation zeroes live
	// power promptly rather than waiting for the next step.
	if m.open {
		floor := m.onMagnitude - m.params.FloorTol
		deficit := floor - residual
		if deficit > m.maxDeficit {
			m.maxDeficit = deficit
		}
		if deficit > 0 {
			if !m.belowFloor {
				m.belowFloor = true
				m.belowSince = t
			} else if t-m.belowSince > m.params.FloorDipMaxS {
				// Floor violated: reject the open cycle now, live.
				out = append(out, modules.OutputUpdate{ObjectID: OutPowerObjectID, Time: u.Time, Value: 0})
				m.open = false
				m.belowFloor = false
			}
		} else {
			if m.belowFloor {
				m.dipTolerated = true
			}
			m.belowFloor = false
		}
	}

	return out
}

// handleEvent applies one finalized merged event to the simplified live
// pairing state machine (see the package doc for how this differs from
// detect.PairEvents's batch lookahead).
func (m *Module) handleEvent(e detect.MergedEvent) []modules.OutputUpdate {
	var out []modules.OutputUpdate
	if !m.open {
		if m.isOn(e.Magnitude) && m.rejectOK(e.TStart, e.TEnd) {
			m.open = true
			m.onTime = e.TEnd
			m.onMagnitude = e.Magnitude
			m.maxDeficit = 0
			m.belowFloor = false
			m.dipTolerated = false
			out = append(out, modules.OutputUpdate{ObjectID: OutPowerObjectID, Time: unixTime(e.TEnd), Value: e.Magnitude})
		}
		return out
	}

	// Already open.
	if e.TStart > m.onTime+m.params.MaxOnDurationS {
		// Timeout: force-close, unaccepted, no energy.
		out = append(out, modules.OutputUpdate{ObjectID: OutPowerObjectID, Time: unixTime(m.onTime + m.params.MaxOnDurationS), Value: 0})
		m.open = false
		// Fall through: still evaluate e fresh, as if from OFF (matches
		// detect.PairEvents's "continue // re-examine e from OFF state").
		return append(out, m.handleEvent(e)...)
	}

	if m.isOn(e.Magnitude) {
		if m.rejectOK(e.TStart, e.TEnd) {
			// Re-anchor to the newer on-step.
			m.onTime = e.TEnd
			m.onMagnitude = e.Magnitude
			out = append(out, modules.OutputUpdate{ObjectID: OutPowerObjectID, Time: unixTime(e.TEnd), Value: e.Magnitude})
		}
		return out
	}

	if m.isOff(e.Magnitude) && m.rejectOK(e.TStart, e.TEnd) {
		// No lookahead live: the FIRST qualifying off-candidate closes the
		// cycle (see package doc). floor state was tracked continuously
		// in OnStateUpdate; a floor violation would already have closed
		// (unaccepted) the cycle before this off-event could arrive, so
		// reaching here with m.open still true means the floor held.
		dur := e.TStart - m.onTime
		out = append(out, modules.OutputUpdate{ObjectID: OutPowerObjectID, Time: unixTime(e.TStart), Value: 0})
		if dur >= m.params.MinOnDurationS {
			// too_short otherwise: a step that reverses within
			// MinOnDurationS is almost certainly a different, fast load,
			// not the immersion heater - see detect.go's PairEvents and
			// DefaultParams.MinOnDurationS. Close the cycle unaccepted,
			// crediting no energy.
			power := (abs(m.onMagnitude) + abs(e.Magnitude)) / 2.0
			m.energyKwh += power * (dur / 3600.0) / 1000.0
			out = append(out, modules.OutputUpdate{ObjectID: OutEnergyObjectID, Time: unixTime(e.TStart), Value: m.energyKwh})
		}
		m.open = false
	}
	return out
}

func unixTime(epochS float64) time.Time {
	return time.Unix(int64(epochS), int64((epochS-float64(int64(epochS)))*1e9))
}
