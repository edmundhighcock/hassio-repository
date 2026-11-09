// Package hotwater is the live virtual-sensor module for the household's
// hot-water immersion heater (Taiga #237). Step detection mirrors
// detect.FindMergedEvents (see onNewGridSample's doc comment) so a real
// step is found the same way live and historical, including the 0.1.4
// fix that a raw gap only hard-breaks a chain if it's real missing data
// (see onNewGridSample and hadActivity). PAIRING is a deliberately
// simplified, no-lookahead version of detect.PairEvents, documented below
// and in DOCS.md "Live vs backfill": the batch pairer can look ahead
// across several off-candidates for the best magnitude match before
// closing a cycle, which a live stream cannot do without delaying every
// publication. What live CAN do without delaying every publication is
// defer just the ONE off-candidate it has found: handleEvent holds a
// tentative off (pendingOff) for PostOffDwellS before crediting energy,
// mirroring detect.go's post-off dwell check (0.1.5) - see OnStateUpdate's
// pendingOff resolution block.
package hotwater

import (
	"encoding/json"
	"log"
	"math"
	"sort"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hotwater/detect"
)

// Entity ids, ported verbatim from build.py's GRID_ENTITY / KNOWN_LOAD_ENTITIES.
const (
	GridEntity = "grid_meter_power"
)

// KnownLoadEntities are the metered loads the LIVE residual subtracts.
var KnownLoadEntities = []string{
	"loaver_power",
	"washing_machine_power",
	"sonoff_100168af89_power",
	"guest_house_oil_heater_power",
	"heat_pump_power_power",
	"fridge_power",
}

// LegacyLoadEntities (0.2.0) are metered loads that have left Home
// Assistant but still have history in the older InfluxDB buckets. The live
// residual no longer subscribes to them (a departed device would sit in
// missing_loads forever), but the backfill CLI still subtracts them so a
// re-run over the period when they reported reproduces the same figures.
// sonoff_1001721983_power is the spa floor-heating Sonoff, gone since 2024.
var LegacyLoadEntities = []string{
	"sonoff_1001721983_power",
}

// BackfillLoadEntities is every load the backfill subtracts: the live set
// plus the legacy ones.
func BackfillLoadEntities() []string {
	return append(append([]string{}, KnownLoadEntities...), LegacyLoadEntities...)
}

const (
	OutPowerObjectID  = "hot_water_estimated_power"
	OutEnergyObjectID = "hot_water_estimated_energy"
	// OutResidualObjectID is the live residual (grid minus every known
	// load, held-value LOCF) in W, published on every fresh grid sample -
	// independent of, and with no effect on, the detector's own
	// power/energy outputs or pairing state above. See
	// OnStateUpdate's residual computation and DOCS.md "Entities
	// published".
	OutResidualObjectID = "hot_water_residual_power"
)

// activityRetentionS bounds how far back each known load's ring (loadRing)
// remembers samples, used by hadActivity to tell a benign quiet gap from a
// true outage (see onNewGridSample). Must comfortably exceed RealGapMinS
// (1800s/30min) so the activity check can see all the way back across any
// gap that's still a candidate for being "benign"; set well short of it at
// 45 min per the add-on's memory budget (a few MiB total) rather than
// right at the edge.
const activityRetentionS = 45 * 60.0

// discontinuityWindowS is how long a known-load loss/regain event stays
// in the published recent_discontinuities attribute (0.1.7) - see
// residualAttributesJSON. Comfortably longer than RejectWindowS (60s),
// which is the only other place discontinuities are consulted
// internally (rejectOK), so this one prune covers both uses.
const discontinuityWindowS = 600.0

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

// DiscontinuityEvent (0.1.7) records one known-load loss/regain
// transition: a jump in the published residual caused ENTIRELY by
// removing/re-adding that load from the live sum (knownSum), not by any
// real change in consumption - see OnStateUpdate's load-availability
// handling and DOCS.md "Entities published". Published (as part of
// residualAttributes, below) via hot_water_residual_power's
// json_attributes_topic so a consumer can tell a real jump apart from
// this administrative one.
type DiscontinuityEvent struct {
	At     time.Time `json:"at"`
	Entity string    `json:"entity"`
	Kind   string    `json:"kind"` // "lost" | "regained"
	// DeltaW is the signed change in the published residual that this
	// event ALONE caused: +the load's last known value on loss (it
	// stops being subtracted, so residual jumps up by that much), -the
	// load's newly reported value on regain (it starts being subtracted
	// again, so residual drops by that much). Zero for the synthetic
	// "lost" events recorded for a load that has simply never reported
	// yet (see logStartupMissingLoadsOnce) - nothing has actually
	// changed yet, so there is no real jump to flag; this also means
	// such startup-only events are invisible to rejectOK's and
	// handleEvent's discontinuity checks (see there), on purpose.
	DeltaW float64 `json:"delta_w"`
}

// residualAttributes is hot_water_residual_power's json_attributes_topic
// payload shape - see residualAttributesJSON.
type residualAttributes struct {
	MissingLoads          []string             `json:"missing_loads"`
	DiscontinuitySeq      int                  `json:"discontinuity_seq"`
	LastDiscontinuity     *DiscontinuityEvent  `json:"last_discontinuity,omitempty"`
	RecentDiscontinuities []DiscontinuityEvent `json:"recent_discontinuities"`
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
	// PendingOff/PendingOffDeadline/OnBaseline persist the post-off dwell
	// confirmation (see handleEvent, OnStateUpdate) so a restart
	// mid-confirmation doesn't drop a genuine close or double-count one
	// that was about to commit.
	PendingOff         *persistedMergedEvent `json:"pending_off,omitempty"`
	PendingOffDeadline float64               `json:"pending_off_deadline,omitempty"`
	OnBaseline         float64               `json:"on_baseline,omitempty"`
	// OnLoadCorrection (0.1.7) - see Module.onLoadCorrection's doc
	// comment. Persisted alongside OnBaseline/OnMagnitude so a restart
	// mid-cycle doesn't lose a correction already accumulated for THIS
	// open cycle. Deliberately NOT extended to the discontinuity log
	// itself (discontinuities/discontinuitySeq/missing-tracking all
	// begin fresh every start - see logStartupMissingLoadsOnce's doc
	// comment and DOCS.md "Entities published" for why).
	OnLoadCorrection float64 `json:"on_load_correction,omitempty"`
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
	onBaseline   float64 // residual just before onTime - the post-off dwell check's baseline
	belowFloor   bool
	belowSince   float64
	maxDeficit   float64
	dipTolerated bool

	// onLoadCorrection (0.1.7) is the running sum of DeltaW for every
	// known-load discontinuity (loss/regain) seen WHILE this cycle has
	// been open, reset to 0 every time onBaseline is (re)anchored (a
	// fresh open or a re-anchor on a later on-step - see handleEvent).
	// correctedResidual (OnStateUpdate) subtracts it from the published
	// residual before comparing against floor/ceiling, so those
	// comparisons stay on the SAME known-load basis onBaseline/
	// onMagnitude were captured on, instead of being fooled by a jump
	// that is administrative, not physical - see OnStateUpdate's
	// correctedResidual doc comment for the full rationale and why this
	// is the chosen alternative to rejectOK-style window exclusion for
	// this (continuous, not discrete-candidate) check.
	onLoadCorrection float64

	// discontinuities is every known-load loss/regain event (and the
	// synthetic startup "lost" ones - see logStartupMissingLoadsOnce)
	// still within discontinuityWindowS, oldest first. discontinuitySeq
	// is a monotonic counter, incremented on every event regardless of
	// pruning (so a consumer can detect a gap in its own polling).
	// Neither is persisted - see persistedState.OnLoadCorrection's doc
	// comment.
	discontinuities      []DiscontinuityEvent
	discontinuitySeq     int
	startupMissingLogged bool

	// pendingOff holds a tentative off-candidate awaiting PostOffDwellS of
	// confirmation that the residual actually stays down - the live,
	// streaming equivalent of detect.go's post-off dwell check
	// (MakePostOffLookup). See handleEvent and OnStateUpdate.
	pendingOff         *detect.MergedEvent
	pendingOffDeadline float64

	energyKwh      float64
	processedUntil float64

	// sink/replay (0.2.0): the in-process signal hook (signals.go). Not
	// persisted; never consulted by the module's own logic.
	sink   func(any)
	replay bool
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
	nowT := epochSeconds(now)
	power := 0.0
	if m.open {
		power = m.onMagnitude
	}
	out := []modules.OutputUpdate{
		{ObjectID: OutPowerObjectID, Time: now, Value: power},
		{ObjectID: OutEnergyObjectID, Time: now, Value: m.energyKwh},
	}
	// The discontinuity LOG is always empty at this point (it is never
	// persisted - see persistedState.OnLoadCorrection's doc comment),
	// but missing_loads is already accurate from whatever lastLoad
	// restored (or didn't).
	attrs := m.residualAttributesJSON(nowT)
	if m.haveLastGrid {
		out = append(out, modules.OutputUpdate{
			ObjectID: OutResidualObjectID, Time: now,
			Value: math.Round(m.lastGrid.V - m.knownSum()), AttributesJSON: attrs,
		})
	} else {
		out = append(out, modules.OutputUpdate{ObjectID: OutResidualObjectID, Time: now, Unavailable: true, AttributesJSON: attrs})
	}
	return out
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
		{
			ObjectID: OutResidualObjectID, UniqueID: "virtual_sensors_" + OutResidualObjectID,
			Name: "Hot water residual power", DeviceClass: "power",
			StateClass: "measurement", Unit: "W",
			// Its own availability topic: this sensor goes unavailable
			// when the grid input itself is unavailable (see
			// OnStateUpdate), independent of the detector outputs above,
			// which keep publishing from held/last-known values exactly
			// as before - see DOCS.md "Entities published".
			PerEntityAvailability: true,
			// json_attributes_topic (0.1.7): missing_loads/discontinuity
			// bookkeeping - see residualAttributesJSON and DOCS.md
			// "Entities published".
			HasAttributes: true,
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
	m.onBaseline = ps.OnBaseline
	m.onLoadCorrection = ps.OnLoadCorrection
	m.pendingOffDeadline = ps.PendingOffDeadline
	if ps.PendingOff != nil {
		ev := detect.MergedEvent{TStart: ps.PendingOff.TStart, TEnd: ps.PendingOff.TEnd, Magnitude: ps.PendingOff.Magnitude}
		m.pendingOff = &ev
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
	ps.OnBaseline = m.onBaseline
	ps.OnLoadCorrection = m.onLoadCorrection
	ps.PendingOffDeadline = m.pendingOffDeadline
	if m.pendingOff != nil {
		ps.PendingOff = &persistedMergedEvent{TStart: m.pendingOff.TStart, TEnd: m.pendingOff.TEnd, Magnitude: m.pendingOff.Magnitude}
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
// the full series (see loadRing) - AND (0.1.7) also rejects if any REAL
// known-load discontinuity (a loss or regain edge with a nonzero DeltaW -
// see DiscontinuityEvent) falls in that same window.
//
// This is the chosen way to satisfy "a discontinuity must not count as
// an on/off step" for candidate steps specifically (the continuous
// floor/pendingOff-ceiling checks are handled separately - see
// correctedResidual in OnStateUpdate): a load whose signal just vanished
// or just came back is, for that moment, an unverifiable swing of
// unknown size in the RAW GRID series too (its real consumption doesn't
// stop just because its sensor did) - exactly the kind of ambiguity
// rejectOK already exists to reject, so this reuses its window and
// verdict rather than inventing a second mechanism. The synthetic
// startup "lost" events (DeltaW==0 - see logStartupMissingLoadsOnce) are
// deliberately excluded: nothing has actually changed at module start,
// so there is nothing for a step to be confused with.
func (m *Module) rejectOK(tStart, tEnd float64) bool {
	loT, hiT := tStart-m.params.RejectWindowS, tEnd+m.params.RejectWindowS
	for _, ev := range m.discontinuities {
		if ev.DeltaW == 0 {
			continue
		}
		evT := epochSeconds(ev.At)
		if evT >= loT && evT <= hiT {
			return false
		}
	}
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

// hadActivity reports whether ANY known load has a sample within
// [tStart, tEnd] - the live equivalent of detect.go's
// MakeArrayActivityCheck, used by onNewGridSample to tell a benign quiet
// gap (the recorder had nothing new to log, but the household wasn't
// otherwise quiet) from a true outage.
func (m *Module) hadActivity(tStart, tEnd float64) bool {
	for _, r := range m.loads {
		for _, lt := range r.t {
			if lt >= tStart && lt <= tEnd {
				return true
			}
		}
	}
	return false
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
//
// A raw gap (dt > MaxGapS) does NOT, on its own, hard-break the run: Home
// Assistant's recorder only logs a sensor when its value changes, so a
// gap at an unchanging reading means "nothing happened", not "data
// lost" (see detect.go's FindGapMarkers for the evidence this was
// verified against). The gap only forces a flush if it's REAL missing
// data - long enough that continuity can't be assumed (>= RealGapMinS)
// or no OTHER known load reported anything either (hadActivity, a true
// outage) - otherwise the delta across it is computed normally, exactly
// like FindMergedEvents.
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
	if dt <= 0 {
		flush()
		m.lastGrid = detect.Point{T: t, V: v}
		return
	}
	if dt > m.params.MaxGapS {
		isRealGap := dt >= m.params.RealGapMinS || !m.hadActivity(m.lastGrid.T, t)
		if isRealGap {
			flush()
			m.lastGrid = detect.Point{T: t, V: v}
			return
		}
		// else: benign quiet gap - fall through to normal delta handling
		// below (it can start a new chain, though it still can't EXTEND
		// an existing one, since that needs
		// m.lastGrid.T-m.lastSeedT <= MergeWindowS regardless).
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

// epochSeconds is the inverse of unixTime: fractional Unix seconds for a
// time.Time, matching how every other timestamp in this module (t) is
// computed from a modules.StateUpdate.Time.
func epochSeconds(tm time.Time) float64 {
	return float64(tm.UnixNano()) / 1e9
}

// pruneDiscontinuities drops anything older than discontinuityWindowS
// relative to nowT from m.discontinuities. discontinuitySeq is NOT
// affected - it is a monotonic "how many events ever" counter, not a
// count of what's currently buffered.
func (m *Module) pruneDiscontinuities(nowT float64) {
	cut := nowT - discontinuityWindowS
	i := 0
	for i < len(m.discontinuities) && epochSeconds(m.discontinuities[i].At) < cut {
		i++
	}
	if i > 0 {
		m.discontinuities = m.discontinuities[i:]
	}
}

// recordDiscontinuity appends one known-load loss/regain event, prunes
// the log, bumps discontinuitySeq, and - while a cycle is open - folds
// deltaW into onLoadCorrection (see that field's doc comment). Returns
// an attributes-only OutputUpdate for hot_water_residual_power so a
// consumer sees the new missing_loads list/event immediately, without
// waiting for the next grid sample.
func (m *Module) recordDiscontinuity(t float64, entity, kind string, deltaW float64) []modules.OutputUpdate {
	m.discontinuities = append(m.discontinuities, DiscontinuityEvent{
		At: unixTime(t).UTC(), Entity: entity, Kind: kind, DeltaW: deltaW,
	})
	m.pruneDiscontinuities(t)
	m.discontinuitySeq++
	if m.open && deltaW != 0 {
		m.onLoadCorrection += deltaW
	}
	m.signal(Discontinuity{T: unixTime(t).UTC(), Entity: entity, Kind: kind, DeltaW: deltaW, Seq: m.discontinuitySeq, Replay: m.replay})
	return []modules.OutputUpdate{{
		ObjectID: OutResidualObjectID, Time: unixTime(t),
		AttributesOnly: true, AttributesJSON: m.residualAttributesJSON(t),
	}}
}

// residualAttributesJSON builds hot_water_residual_power's
// json_attributes_topic payload: which known loads are currently
// excluded from knownSum (missing_loads), and the discontinuity log
// (see DiscontinuityEvent, residualAttributes). missing_loads is
// computed fresh from m.lastLoad every call, so it is always accurate
// even if recordDiscontinuity was never called (e.g. right at Init,
// before logStartupMissingLoadsOnce has run).
func (m *Module) residualAttributesJSON(nowT float64) string {
	m.pruneDiscontinuities(nowT)
	missing := make([]string, 0, len(KnownLoadEntities))
	for _, name := range KnownLoadEntities {
		if _, ok := m.lastLoad[name]; !ok {
			missing = append(missing, name)
		}
	}
	attrs := residualAttributes{
		MissingLoads:          missing,
		DiscontinuitySeq:      m.discontinuitySeq,
		RecentDiscontinuities: append([]DiscontinuityEvent{}, m.discontinuities...),
	}
	if len(m.discontinuities) > 0 {
		last := m.discontinuities[len(m.discontinuities)-1]
		attrs.LastDiscontinuity = &last
	}
	b, err := json.Marshal(attrs)
	if err != nil {
		log.Printf("hot_water: WARNING could not marshal residual attributes: %v", err)
		return ""
	}
	return string(b)
}

// logStartupMissingLoadsOnce runs exactly once per process start, on the
// very first OnStateUpdate call (whichever entity triggers it - grid or
// a known load). For every known load NOT already in m.lastLoad (either
// genuinely never seen, e.g. a fresh install, or already excluded at
// the moment state was last persisted, since a missing load is never
// written back into LastLoadT/LastLoadV - see persistedState's doc
// comment), it records a synthetic "lost" event with DeltaW=0 (nothing
// has actually jumped yet - see DiscontinuityEvent's doc comment).
//
// Chosen design (0.1.7, "Restored state at add-on start"): the
// discontinuity LOG and missing-tracking always begin fresh - this is
// what makes an ordinary restart (where most/all known loads WERE
// restored into lastLoad) silent here, while a load that is new or was
// already excluded is flagged as missing from the very first published
// residual, and its eventual first real sample is then a normal
// "regained" transition (see the load branch of OnStateUpdate) -
// completing a coherent lost-then-regained lifecycle rather than
// silently starting "already known".
func (m *Module) logStartupMissingLoadsOnce(t float64) []modules.OutputUpdate {
	if m.startupMissingLogged {
		return nil
	}
	m.startupMissingLogged = true
	var out []modules.OutputUpdate
	for _, name := range KnownLoadEntities {
		if _, ok := m.lastLoad[name]; !ok {
			out = append(out, m.recordDiscontinuity(t, name, "lost", 0)...)
		}
	}
	return out
}

// handleLoadUpdate applies one known-load (non-grid) state update: the
// existing held-value rule for a quiet-but-available load (HA only logs
// on change, so simply not updating lastLoad already holds it), and
// (0.1.7) immediate removal from knownSum - no held value at all - the
// moment this load's own state explicitly reports unavailable/unknown,
// restored the moment it next reports a numeric value. Each such
// transition is a discontinuity (see recordDiscontinuity); a quiet
// available->available update (even a real value change) is not.
func (m *Module) handleLoadUpdate(t float64, u modules.StateUpdate) []modules.OutputUpdate {
	prev, wasPresent := m.lastLoad[u.EntityID]
	if u.Available {
		m.lastLoad[u.EntityID] = detect.Point{T: t, V: u.Value}
		if m.loads[u.EntityID] == nil {
			m.loads[u.EntityID] = &loadRing{}
		}
		m.loads[u.EntityID].push(t, u.Value, activityRetentionS)
		if !wasPresent {
			return m.recordDiscontinuity(t, u.EntityID, "regained", -u.Value)
		}
		return nil
	}
	if wasPresent {
		delete(m.lastLoad, u.EntityID)
		return m.recordDiscontinuity(t, u.EntityID, "lost", prev.V)
	}
	// Already missing (another unavailable/unknown update) - no change.
	return nil
}

// OnStateUpdate implements modules.Module.
func (m *Module) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	t := float64(u.Time.UnixNano()) / 1e9
	if t <= m.processedUntil {
		return nil // already applied (warm-up replay overlapping persisted state)
	}
	m.processedUntil = t
	m.replay = u.Replay

	var out []modules.OutputUpdate
	out = append(out, m.logStartupMissingLoadsOnce(t)...)

	// loaver_power (the EV charger) reports kW; every other input is W.
	// The backfill converts by InfluxDB measurement; live has no unit, so
	// it is fixed here.
	if u.EntityID == "loaver_power" {
		u.Value *= 1000
	}
	if u.EntityID != GridEntity {
		// A known load changing does not, on its own, step the grid
		// residual - only a fresh grid sample re-evaluates the residual
		// below. The reject-check rings above are consulted when a grid
		// step is later seen. handleLoadUpdate may still return an
		// attributes-only update of its own, e.g. this load just went
		// missing/came back (0.1.7).
		out = append(out, m.handleLoadUpdate(t, u)...)
		return out
	}

	if !u.Available {
		// The detector's own state (open cycle, pairing, run-in-progress)
		// is untouched here, exactly as before 0.1.6 - only the new
		// residual sensor reacts to a grid outage, going unavailable so a
		// downstream consumer can fail safe rather than read a stale or
		// fabricated number.
		out = append(out, modules.OutputUpdate{
			ObjectID: OutResidualObjectID, Time: u.Time, Unavailable: true,
			AttributesJSON: m.residualAttributesJSON(t),
		})
		m.signal(ResidualSample{T: u.Time, Available: false, MissingLoads: m.missingLoads(), DiscontinuitySeq: m.discontinuitySeq, Replay: m.replay})
		return out
	}

	// residual (0.1.7): grid minus the LIVE sum of every CURRENTLY
	// PRESENT known load - a load explicitly reporting
	// unavailable/unknown is excluded immediately, not held at its last
	// value (see handleLoadUpdate); this is the one, single residual
	// definition, used both for the published sensor below and for the
	// detector's own floor/pendingOff-ceiling checks further down (via
	// correctedResidual) - see OnStateUpdate's correctedResidual doc
	// comment for why those need a correction rather than using residual
	// directly, even though it is "the same residual semantics".
	residual := u.Value - m.knownSum()
	m.residual.push(t, residual, m.params.StageWindowS)

	// Published on every fresh grid sample, independent of step
	// detection/pairing below: the live residual rounded to the nearest
	// whole W, plus the current missing_loads/discontinuity attributes
	// (0.1.7) - see residualAttributesJSON. A known load that is merely
	// QUIET but still available keeps contributing its last-seen value
	// here (handleLoadUpdate's held-value rule); one that has explicitly
	// gone unavailable/unknown does not, and is flagged in missing_loads
	// instead - see DOCS.md "Entities published".
	out = append(out, modules.OutputUpdate{
		ObjectID: OutResidualObjectID, Time: u.Time,
		Value: math.Round(residual), AttributesJSON: m.residualAttributesJSON(t),
	})
	if m.sink != nil {
		m.signal(ResidualSample{T: u.Time, W: math.Round(residual), Available: true, MissingLoads: m.missingLoads(), DiscontinuitySeq: m.discontinuitySeq, Replay: m.replay})
	}

	// correctedResidual (0.1.7): residual with every known-load
	// discontinuity since the CURRENT open cycle's on-step/re-anchor
	// backed out (see onLoadCorrection's doc comment) - used ONLY for
	// the floor/pendingOff-ceiling comparisons below, which are
	// continuous (evaluated every sample, not at discrete candidate-step
	// boundaries) and compare against a threshold (onBaseline/
	// onMagnitude) captured under a specific, earlier known-load-
	// exclusion basis. Without this correction, a load regaining signal
	// mid-cycle (residual drops by its newly-reported value) could blow
	// through FloorTol and force-close a cycle the heater never actually
	// left, and a load LOSING signal while a tentative off is pending
	// (residual rises) could likewise be mistaken for the bounce-back
	// that cancels it. The PUBLISHED residual above is never corrected -
	// only this internal comparison is, so a consumer still sees the
	// true (documented) jump. Candidate on/off STEPS themselves are
	// protected separately, by rejectOK (see its doc comment) - this is
	// the chosen alternative to that same window-exclusion approach for
	// a continuous check, where a single short window cannot undo an
	// administrative shift that, unlike a step, does not resolve itself
	// a few samples later.
	correctedResidual := residual - m.onLoadCorrection

	m.onNewGridSample(t, u.Value, func(e detect.MergedEvent) {
		m.admitMergedEvent(e, func(staged detect.MergedEvent) {
			if m.sink != nil && abs(staged.Magnitude) >= m.params.StepLo {
				m.signal(MergedStep{TStart: unixTime(staged.TStart).UTC(), TEnd: unixTime(staged.TEnd).UTC(),
					Magnitude: staged.Magnitude, Rejected: !m.rejectOK(staged.TStart, staged.TEnd), Replay: m.replay})
			}
			out = append(out, m.handleEvent(staged)...)
		})
	})

	// Resolving a tentative off (see handleEvent) takes priority over the
	// generic below-floor tracker below: once a specific off-candidate
	// has been found, whether it's a real close is answered by the
	// dedicated post-off dwell check against the ACTUAL pre-cycle
	// baseline, not the generic "did the ON plateau dip" one.
	if m.pendingOff != nil {
		ceiling := m.onBaseline + m.params.FloorTol
		if correctedResidual > ceiling {
			// Bounce-back within the dwell window: this wasn't the
			// heater turning off, just a dip in a different load's own
			// activity (see detect.go's MakePostOffLookup). Cancel and
			// resume the cycle exactly as it was.
			m.pendingOff = nil
			m.dipTolerated = true
			m.belowFloor = false
		} else if t >= m.pendingOffDeadline {
			// Confirmed: commit the close, crediting energy up to the
			// off time (not "now" - the boundary is the original
			// off-event, same as backfill).
			off := *m.pendingOff
			dur := off.TStart - m.onTime
			out = append(out, modules.OutputUpdate{ObjectID: OutPowerObjectID, Time: unixTime(off.TStart), Value: 0})
			closed := CycleClosed{OnTime: unixTime(m.onTime).UTC(), OffTime: unixTime(off.TStart).UTC(), Reason: "short", Replay: m.replay}
			if dur >= m.params.MinOnDurationS {
				power := (abs(m.onMagnitude) + abs(off.Magnitude)) / 2.0
				kwh := power * (dur / 3600.0) / 1000.0
				m.energyKwh += kwh
				out = append(out, modules.OutputUpdate{ObjectID: OutEnergyObjectID, Time: unixTime(off.TStart), Value: m.energyKwh})
				closed.Accepted, closed.Kwh, closed.Reason = true, kwh, "dwell_confirmed"
			}
			m.signal(closed)
			m.open = false
			m.pendingOff = nil
		}
		return out
	}

	// Floor check for an open cycle, evaluated on every grid sample (not
	// just at merged-event boundaries) so a floor violation zeroes live
	// power promptly rather than waiting for the next step.
	if m.open {
		floor := m.onMagnitude - m.params.FloorTol
		deficit := floor - correctedResidual
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
				m.signal(CycleClosed{OnTime: unixTime(m.onTime).UTC(), OffTime: u.Time.UTC(), Reason: "floor", Replay: m.replay})
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
			m.onBaseline = m.residualAt(e.TStart)
			m.onLoadCorrection = 0
			m.maxDeficit = 0
			m.belowFloor = false
			m.dipTolerated = false
			m.pendingOff = nil
			out = append(out, modules.OutputUpdate{ObjectID: OutPowerObjectID, Time: unixTime(e.TEnd), Value: e.Magnitude})
			m.signal(CycleOpened{OnTime: unixTime(e.TEnd).UTC(), Magnitude: e.Magnitude, Replay: m.replay})
		}
		return out
	}

	// Already open.
	if e.TStart > m.onTime+m.params.MaxOnDurationS {
		// Timeout: force-close, unaccepted, no energy. Any tentative off
		// in progress is discarded along with the rest of the cycle -
		// matches detect.PairEvents, which never retroactively credits a
		// cycle it's about to force-close for running too long.
		out = append(out, modules.OutputUpdate{ObjectID: OutPowerObjectID, Time: unixTime(m.onTime + m.params.MaxOnDurationS), Value: 0})
		m.signal(CycleClosed{OnTime: unixTime(m.onTime).UTC(), OffTime: unixTime(m.onTime + m.params.MaxOnDurationS).UTC(), Reason: "timeout", Replay: m.replay})
		m.open = false
		m.pendingOff = nil
		// Fall through: still evaluate e fresh, as if from OFF (matches
		// detect.PairEvents's "continue // re-examine e from OFF state").
		return append(out, m.handleEvent(e)...)
	}

	if m.isOn(e.Magnitude) {
		if m.rejectOK(e.TStart, e.TEnd) {
			// Re-anchor to the newer on-step. A fresh, genuine on-step is
			// at least as strong a signal that the heater is still
			// running as the post-off dwell check would need, so any
			// tentative off in progress is cancelled (same as a
			// bounce-back - see OnStateUpdate's pendingOff resolution).
			m.onTime = e.TEnd
			m.onMagnitude = e.Magnitude
			m.onBaseline = m.residualAt(e.TStart)
			m.onLoadCorrection = 0
			m.pendingOff = nil
			out = append(out, modules.OutputUpdate{ObjectID: OutPowerObjectID, Time: unixTime(e.TEnd), Value: e.Magnitude})
		}
		return out
	}

	if m.isOff(e.Magnitude) && m.rejectOK(e.TStart, e.TEnd) {
		// Don't replace an off-candidate already being confirmed -
		// mirrors detect.PairEvents, which evaluates one chosen
		// off-candidate's floor rather than restarting the search when a
		// later one arrives before the first has resolved.
		if m.pendingOff != nil {
			return out
		}
		// Defer the close: hold this tentative off for PostOffDwellS
		// before crediting energy or publishing power=0, confirming via
		// OnStateUpdate's pendingOff resolution that the residual
		// actually STAYS down (see detect.go's MakePostOffLookup) rather
		// than closing on the first dip the way pre-0.1.5 live did. No
		// output is published yet; the published power stays at the
		// on-level until this resolves one way or the other.
		ev := e
		m.pendingOff = &ev
		m.pendingOffDeadline = e.TEnd + m.params.PostOffDwellS
	}
	return out
}

func unixTime(epochS float64) time.Time {
	return time.Unix(int64(epochS), int64((epochS-float64(int64(epochS)))*1e9))
}
