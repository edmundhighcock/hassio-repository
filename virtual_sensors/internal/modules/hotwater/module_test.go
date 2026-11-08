package hotwater

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"virtual_sensors/internal/modules"
)

// pushGrid feeds one grid_meter_power sample through OnStateUpdate at
// unix second t (fractional seconds allowed) with value v, returning
// whatever outputs it produced.
func pushGrid(t *testing.T, m *Module, tSec, v float64) []modules.OutputUpdate {
	t.Helper()
	sec := int64(tSec)
	nsec := int64((tSec - float64(sec)) * 1e9)
	return m.OnStateUpdate(modules.StateUpdate{
		EntityID: GridEntity, Time: time.Unix(sec, nsec).UTC(), Value: v, Available: true,
	})
}

// pushLoad feeds one known-load sample (e.g. "washing_machine_power") at
// unix second t with value v.
func pushLoad(t *testing.T, m *Module, entityID string, tSec, v float64) []modules.OutputUpdate {
	t.Helper()
	sec := int64(tSec)
	nsec := int64((tSec - float64(sec)) * 1e9)
	return m.OnStateUpdate(modules.StateUpdate{
		EntityID: entityID, Time: time.Unix(sec, nsec).UTC(), Value: v, Available: true,
	})
}

func newTestModule(t *testing.T) *Module {
	t.Helper()
	m := New()
	if err := m.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return m
}

func lastPower(outs [][]modules.OutputUpdate) (float64, bool) {
	val, found := 0.0, false
	for _, batch := range outs {
		for _, u := range batch {
			if u.ObjectID == OutPowerObjectID {
				val = u.Value
				found = true
			}
		}
	}
	return val, found
}

// pushGridUnavailable feeds a grid_meter_power state with Available:false
// (e.g. "unavailable"/"unknown" in Home Assistant) through OnStateUpdate.
func pushGridUnavailable(t *testing.T, m *Module, tSec float64) []modules.OutputUpdate {
	t.Helper()
	sec := int64(tSec)
	nsec := int64((tSec - float64(sec)) * 1e9)
	return m.OnStateUpdate(modules.StateUpdate{
		EntityID: GridEntity, Time: time.Unix(sec, nsec).UTC(), Available: false,
	})
}

// pushLoadUnavailable feeds a known-load state with Available:false.
func pushLoadUnavailable(t *testing.T, m *Module, entityID string, tSec float64) []modules.OutputUpdate {
	t.Helper()
	sec := int64(tSec)
	nsec := int64((tSec - float64(sec)) * 1e9)
	return m.OnStateUpdate(modules.StateUpdate{
		EntityID: entityID, Time: time.Unix(sec, nsec).UTC(), Available: false,
	})
}

// lastResidual returns the last hot_water_residual_power OutputUpdate in
// outs, if any.
func lastResidual(outs []modules.OutputUpdate) (val float64, unavailable bool, found bool) {
	for _, u := range outs {
		if u.ObjectID == OutResidualObjectID {
			val, unavailable, found = u.Value, u.Unavailable, true
		}
	}
	return
}

// lastResidualAttributes parses the last non-empty AttributesJSON for
// OutResidualObjectID in outs - fails the test if there isn't one.
func lastResidualAttributes(t *testing.T, outs []modules.OutputUpdate) residualAttributes {
	t.Helper()
	var raw string
	for _, u := range outs {
		if u.ObjectID == OutResidualObjectID && u.AttributesJSON != "" {
			raw = u.AttributesJSON
		}
	}
	if raw == "" {
		t.Fatalf("no %s attributes published in %+v", OutResidualObjectID, outs)
	}
	var attrs residualAttributes
	if err := json.Unmarshal([]byte(raw), &attrs); err != nil {
		t.Fatalf("could not parse residual attributes JSON %q: %v", raw, err)
	}
	return attrs
}

// containsMissing reports whether entity is in attrs.MissingLoads. Every
// known load not explicitly pushed a real value in a given test is
// missing from module start (see logStartupMissingLoadsOnce) - tests
// check membership, not the exact set, so they don't need to enumerate
// every OTHER known load they never touch.
func containsMissing(attrs residualAttributes, entity string) bool {
	for _, e := range attrs.MissingLoads {
		if e == entity {
			return true
		}
	}
	return false
}

func anyPowerZero(outs [][]modules.OutputUpdate) bool {
	for _, batch := range outs {
		for _, u := range batch {
			if u.ObjectID == OutPowerObjectID && u.Value == 0 {
				return true
			}
		}
	}
	return false
}

// TestCleanOnOffCycle: a plain 3kW on-step followed by a matching
// off-step, no gaps, no staging, no dips - the baseline case.
func TestCleanOnOffCycle(t *testing.T) {
	m := newTestModule(t)
	var outs [][]modules.OutputUpdate
	outs = append(outs, pushGrid(t, m, 0, 500))
	outs = append(outs, pushGrid(t, m, 20, 500))
	outs = append(outs, pushGrid(t, m, 40, 3500)) // +3000 on-step
	outs = append(outs, pushGrid(t, m, 140, 3480))
	outs = append(outs, pushGrid(t, m, 240, 3510))
	outs = append(outs, pushGrid(t, m, 340, 520)) // -2990 off-step
	outs = append(outs, pushGrid(t, m, 440, 500))
	outs = append(outs, pushGrid(t, m, 530, 500))
	outs = append(outs, pushGrid(t, m, 620, 500))

	// Should have resolved (closed) by now - 280s after the off-step,
	// comfortably past PostOffDwellS (90s).
	if m.open {
		t.Fatalf("cycle still open, want closed")
	}
	if m.energyKwh <= 0 {
		t.Fatalf("energyKwh = %v, want > 0 (a clean 5-minute 3kW cycle)", m.energyKwh)
	}
	if !anyPowerZero(outs) {
		t.Fatalf("never published power=0")
	}
}

// TestFlatStretchWithGapsDoesNotForceClose: a flat ~3kW ON stretch
// sampled only every 140s (> MaxGapS=120s, < RealGapMinS=1800s), with
// another known load also reporting during the gaps (so it's not an
// outage) - the cycle must stay open throughout, not force-close on any
// of these benign gaps (0.1.5's live port of the 0.1.4 gap fix).
func TestFlatStretchWithGapsDoesNotForceClose(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 0, 500)
	pushGrid(t, m, 20, 500)
	pushGrid(t, m, 40, 3500) // +3000 on-step
	pushGrid(t, m, 45, 3490) // -10, reversal -> flushes the on-step chain
	if !m.open {
		t.Fatalf("cycle did not open")
	}
	tt := 45.0
	for i := 0; i < 10; i++ {
		tt += 140 // gap > MaxGapS, well under RealGapMinS
		pushLoad(t, m, "washing_machine_power", tt-70, float64(i)+1)
		outs := pushGrid(t, m, tt, 3500)
		if anyPowerZero([][]modules.OutputUpdate{outs}) {
			t.Fatalf("force-closed at t=%.0f on a benign 140s gap with other activity", tt)
		}
		if !m.open {
			t.Fatalf("cycle closed at t=%.0f on a benign 140s gap with other activity", tt)
		}
	}
	// Now the real off-step, and confirm it closes normally afterward.
	tt += 20
	pushGrid(t, m, tt, 520)
	pushGrid(t, m, tt+100, 510)
	pushGrid(t, m, tt+200, 515)
	if m.open {
		t.Fatalf("cycle never closed after the real off-step")
	}
	if m.energyKwh <= 0 {
		t.Fatalf("energyKwh = %v, want > 0", m.energyKwh)
	}
}

// TestStagedOnFourMinutesApartIsDetected: the heater switching on in two
// stages (+2.08kW then +0.85kW) four minutes apart, as it did live
// 2026-11-05, must be recognised as one combined on-event - not read as
// 0W throughout.
func TestStagedOnFourMinutesApartIsDetected(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 0, 380)
	pushGrid(t, m, 115, 800)  // +420 (sub-seed on its own)
	pushGrid(t, m, 120, 2460) // +1660 -- chain totals +2080
	wobble := []struct{ t, v float64 }{
		{145, 2500}, {150, 2530}, {155, 2570}, {160, 2590}, {165, 2560}, {170, 2550},
		{175, 2600}, {180, 2590}, {185, 2580}, {190, 2550}, {195, 2560}, {200, 2590},
		{205, 2560}, {210, 2550}, {215, 2560}, {220, 2570}, {225, 2530}, {230, 2560},
		{235, 2540}, {240, 2520}, {245, 2560}, {250, 2520}, {255, 2550}, {260, 2510},
	}
	for _, s := range wobble {
		pushGrid(t, m, s.t, s.v)
	}
	pushGrid(t, m, 266, 2550) // +40
	pushGrid(t, m, 271, 2710) // +160
	pushGrid(t, m, 276, 3390) // +680 -- none of these individually >= SeedDeltaMin
	pushGrid(t, m, 281, 3480)
	pushGrid(t, m, 286, 3500)
	pushGrid(t, m, 291, 3520)
	pushGrid(t, m, 296, 3530)

	if !m.open {
		t.Fatalf("staged on-step was not recognised (cycle not open)")
	}
	if m.onMagnitude < 2500 || m.onMagnitude > 3600 {
		t.Fatalf("onMagnitude = %v, want [2500,3600] (the combined staged on-step)", m.onMagnitude)
	}
}

// TestOscillatingLoadDoesNotCauseAPrematureOff: modelled on the real
// 2026-10-20 Case B regression - a spurious off-looking dip that bounces
// back up within the post-off dwell window must not close the cycle; it
// should stay open and close on the REAL off-step further ahead.
func TestOscillatingLoadDoesNotCauseAPrematureOff(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 0, 500)
	pushGrid(t, m, 20, 500)
	pushGrid(t, m, 40, 3500) // +3000 on-step, baseline=500
	pushGrid(t, m, 140, 3480)
	pushGrid(t, m, 240, 3510)
	pushGrid(t, m, 300, 900) // -2610 spurious dip (settles at 900)
	if !m.open {
		t.Fatalf("cycle closed prematurely on the spurious dip alone")
	}
	pushGrid(t, m, 320, 3800) // +2900 bounces back up within the dwell window (20s later)
	pushGrid(t, m, 420, 3780)
	pushGrid(t, m, 520, 3810)
	if !m.open {
		t.Fatalf("cycle closed even though the residual bounced back up - premature off")
	}
	if m.energyKwh != 0 {
		t.Fatalf("energyKwh = %v, want 0 (nothing should have been credited for the spurious dip)", m.energyKwh)
	}
	pushGrid(t, m, 600, 520) // -3290 real off-step
	pushGrid(t, m, 620, 510)
	pushGrid(t, m, 720, 515)
	if m.open {
		t.Fatalf("cycle never closed on the real off-step")
	}
	if m.energyKwh <= 0 {
		t.Fatalf("energyKwh = %v, want > 0 after the real off-step resolved", m.energyKwh)
	}
}

// TestResidualPublishedOnEveryFreshGridSample: with no known loads
// reporting anything, the residual output must equal the grid value
// itself, and must be published afresh on every single grid sample
// (independent of step detection/pairing, which these grid values never
// trigger).
func TestResidualPublishedOnEveryFreshGridSample(t *testing.T) {
	m := newTestModule(t)
	// t starts at 10, not 0: OnStateUpdate drops any sample with
	// t <= processedUntil (initially 0) as "already applied" - see its
	// warm-up-replay-overlap guard - so a first call at t=0 would be a
	// silent no-op, same as every other test in this file.
	for i, v := range []float64{500, 510, 505, 520} {
		outs := pushGrid(t, m, float64(i+1)*10, v)
		val, unavailable, found := lastResidual(outs)
		if !found {
			t.Fatalf("sample %d: no %s output published", i, OutResidualObjectID)
		}
		if unavailable {
			t.Fatalf("sample %d: residual marked unavailable, want available", i)
		}
		if val != v {
			t.Fatalf("sample %d: residual = %v, want %v (no known loads)", i, val, v)
		}
	}
}

// TestResidualConvertsLoaverKwToW: loaver_power (the EV charger) reports
// kW, like every other live input to this module - the residual must
// subtract it in W, same conversion OnStateUpdate already applies for
// the detector itself.
func TestResidualConvertsLoaverKwToW(t *testing.T) {
	m := newTestModule(t)
	pushLoad(t, m, "loaver_power", 1, 2.5) // kW -> held as 2500W
	outs := pushGrid(t, m, 10, 3000)
	val, unavailable, found := lastResidual(outs)
	if !found {
		t.Fatalf("no %s output published", OutResidualObjectID)
	}
	if unavailable {
		t.Fatalf("residual marked unavailable, want available")
	}
	if val != 500 {
		t.Fatalf("residual = %v, want 500 (3000 grid - 2500W from loaver_power's 2.5kW)", val)
	}
}

// TestKnownLoadLossRemovesImmediatelyFromResidual (0.1.7): this
// supersedes 0.1.6's held-value claim for an EXPLICIT unavailable/
// unknown state. A known load going unavailable must be excluded from
// knownSum IMMEDIATELY - no held value at all - so the residual jumps
// up by exactly that load's last value, and the loss is recorded as a
// discontinuity. The residual SENSOR itself stays available throughout
// (only the grid going unavailable does that - see
// TestResidualGoesUnavailableWhenGridUnavailableDetectorUnaffected).
func TestKnownLoadLossRemovesImmediatelyFromResidual(t *testing.T) {
	m := newTestModule(t)
	pushLoad(t, m, "fridge_power", 1, 150)
	outs := pushGrid(t, m, 5, 1150)
	if val, _, found := lastResidual(outs); !found || val != 1000 {
		t.Fatalf("setup: residual = %v found=%v, want 1000", val, found)
	}

	lossOuts := pushLoadUnavailable(t, m, "fridge_power", 10)
	attrs := lastResidualAttributes(t, lossOuts)
	if !containsMissing(attrs, "fridge_power") {
		t.Fatalf("missing_loads = %v, want fridge_power in it", attrs.MissingLoads)
	}
	if attrs.LastDiscontinuity == nil || attrs.LastDiscontinuity.Kind != "lost" ||
		attrs.LastDiscontinuity.Entity != "fridge_power" || attrs.LastDiscontinuity.DeltaW != 150 {
		t.Fatalf("last_discontinuity = %+v, want {lost fridge_power +150}", attrs.LastDiscontinuity)
	}

	outs = pushGrid(t, m, 15, 1150)
	val, unavailable, found := lastResidual(outs)
	if !found {
		t.Fatalf("no %s output published after a known load went unavailable", OutResidualObjectID)
	}
	if unavailable {
		t.Fatalf("residual itself must stay available - only the GRID going unavailable does that")
	}
	if val != 1150 {
		t.Fatalf("residual = %v, want 1150 (fridge_power's 150W no longer held/subtracted - 0.1.7)", val)
	}
}

// TestQuietButAvailableLoadStillKeepsHeldValue: a load that simply has
// nothing NEW to report (Home Assistant only logs a sensor on change) -
// as opposed to one that explicitly reports unavailable/unknown - still
// keeps contributing its last numeric value, exactly as before 0.1.7.
func TestQuietButAvailableLoadStillKeepsHeldValue(t *testing.T) {
	m := newTestModule(t)
	pushLoad(t, m, "fridge_power", 1, 150)
	for _, s := range []struct{ t, v float64 }{{5, 1150}, {100, 1150}, {400, 1180}} {
		outs := pushGrid(t, m, s.t, s.v)
		val, unavailable, found := lastResidual(outs)
		if !found || unavailable {
			t.Fatalf("t=%.0f: residual missing/unavailable (found=%v unavailable=%v)", s.t, found, unavailable)
		}
		if val != s.v-150 {
			t.Fatalf("t=%.0f: residual = %v, want %v (fridge_power's held 150W - quiet, never unavailable)", s.t, val, s.v-150)
		}
	}
}

// TestKnownLoadRegainDropsResidualByNewValue: a known load's first
// numeric sample after being missing (or never seen) is a "regained"
// discontinuity, and resumes being subtracted from the residual
// immediately.
func TestKnownLoadRegainDropsResidualByNewValue(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 10, 1150)
	// Already missing since module start (never seen) - explicitly
	// reporting unavailable again here is a no-op, not a new event.
	pushLoadUnavailable(t, m, "fridge_power", 15)
	outs := pushLoad(t, m, "fridge_power", 20, 150) // first real value -> "regained"
	attrs := lastResidualAttributes(t, outs)
	if attrs.LastDiscontinuity == nil || attrs.LastDiscontinuity.Kind != "regained" ||
		attrs.LastDiscontinuity.Entity != "fridge_power" || attrs.LastDiscontinuity.DeltaW != -150 {
		t.Fatalf("last_discontinuity = %+v, want {regained fridge_power -150}", attrs.LastDiscontinuity)
	}
	residOuts := pushGrid(t, m, 30, 1150)
	val, _, found := lastResidual(residOuts)
	if !found || val != 1000 {
		t.Fatalf("residual = %v found=%v, want 1000 (1150 grid - 150W fridge_power, subtracted again)", val, found)
	}
}

// TestEVLossInKwRecordedAsWattsAndDoesNotFireDetector: loaver_power (the
// EV charger) reports kW, like every other input - a loss/regain's
// delta_w must be recorded in W (matching the already-converted value),
// and losing its signal must not, by itself, make the detector think
// the heater stepped - the grid itself hasn't moved at all (the EV is
// still really drawing the same power; only its OWN sensor went dark).
func TestEVLossInKwRecordedAsWattsAndDoesNotFireDetector(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 10, 3500)
	pushLoad(t, m, "loaver_power", 15, 2.5) // kW -> held as 2500W
	outs := pushGrid(t, m, 20, 3500)
	if val, _, found := lastResidual(outs); !found || val != 1000 {
		t.Fatalf("setup: residual = %v found=%v, want 1000 (3500 grid - 2500W EV)", val, found)
	}

	lossOuts := pushLoadUnavailable(t, m, "loaver_power", 25)
	attrs := lastResidualAttributes(t, lossOuts)
	if attrs.LastDiscontinuity == nil || attrs.LastDiscontinuity.Kind != "lost" ||
		attrs.LastDiscontinuity.Entity != "loaver_power" || attrs.LastDiscontinuity.DeltaW != 2500 {
		t.Fatalf("last_discontinuity = %+v, want {lost loaver_power +2500} (W, not kW)", attrs.LastDiscontinuity)
	}

	outs = pushGrid(t, m, 30, 3500) // grid unchanged - EV is still really drawing 2.5kW
	if val, _, _ := lastResidual(outs); val != 3500 {
		t.Fatalf("residual = %v, want 3500 (grid unchanged, EV's 2500W no longer subtracted)", val)
	}
	if m.open {
		t.Fatalf("the administrative jump alone must not open a cycle")
	}
}

// TestRealLoadChangeWhileMissingLeaksIntoResidual: once a known load is
// flagged missing, any further REAL change in its consumption shows up
// in the grid reading as always, but there is no sensor value to
// subtract it with - so it leaks straight through into the residual.
// This is expected and documented (DOCS.md), not corrected by this
// release: the detector cannot tell "the invisible load's own draw
// changed" apart from "something else did".
func TestRealLoadChangeWhileMissingLeaksIntoResidual(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 10, 1150)
	pushLoad(t, m, "fridge_power", 15, 150)
	pushGrid(t, m, 20, 1150) // residual settles at 1000
	pushLoadUnavailable(t, m, "fridge_power", 25)

	outs := pushGrid(t, m, 30, 1400) // the fridge's real draw rose while missing
	val, unavailable, found := lastResidual(outs)
	if !found || unavailable {
		t.Fatalf("residual output missing/unavailable: val=%v unavailable=%v found=%v", val, unavailable, found)
	}
	if val != 1400 {
		t.Fatalf("residual = %v, want 1400 (grid's real rise leaks straight through while fridge_power is missing)", val)
	}
	attrs := lastResidualAttributes(t, outs)
	if !containsMissing(attrs, "fridge_power") {
		t.Fatalf("missing_loads = %v, want fridge_power still in it", attrs.MissingLoads)
	}
}

// TestSeveralDiscontinuitiesWithinTenMinutes: a consumer whose own check
// window spans several loss/regain events must be able to sum delta_w
// across all of them from recent_discontinuities.
func TestSeveralDiscontinuitiesWithinTenMinutes(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 10, 1150)
	pushLoad(t, m, "fridge_power", 15, 150)
	pushLoad(t, m, "washing_machine_power", 16, 500)
	setupOuts := pushGrid(t, m, 20, 1150)
	seqBefore := lastResidualAttributes(t, setupOuts).DiscontinuitySeq

	pushLoadUnavailable(t, m, "fridge_power", 30)                 // A: fridge lost (+150)
	pushLoad(t, m, "fridge_power", 60, 160)                       // B: fridge regained (-160)
	pushLoadUnavailable(t, m, "washing_machine_power", 90)        // C: wm lost (+500)
	finalOuts := pushLoad(t, m, "washing_machine_power", 120, 520) // D: wm regained (-520)

	attrs := lastResidualAttributes(t, finalOuts)
	if gotNew := attrs.DiscontinuitySeq - seqBefore; gotNew != 4 {
		t.Fatalf("discontinuity_seq advanced by %d, want 4 (one per loss/regain above)", gotNew)
	}
	if len(attrs.RecentDiscontinuities) < 4 {
		t.Fatalf("recent_discontinuities has only %d entries, want at least the 4 just recorded (well within the 10-minute window)", len(attrs.RecentDiscontinuities))
	}
	last4 := attrs.RecentDiscontinuities[len(attrs.RecentDiscontinuities)-4:]
	wantKinds := []string{"lost", "regained", "lost", "regained"}
	wantEntities := []string{"fridge_power", "fridge_power", "washing_machine_power", "washing_machine_power"}
	wantDeltas := []float64{150, -160, 500, -520}
	for i, ev := range last4 {
		if ev.Kind != wantKinds[i] || ev.Entity != wantEntities[i] || ev.DeltaW != wantDeltas[i] {
			t.Fatalf("event %d = %+v, want {%s %s %v}", i, ev, wantKinds[i], wantEntities[i], wantDeltas[i])
		}
	}
	var sum float64
	for _, ev := range last4 {
		sum += ev.DeltaW
	}
	if sum != 150-160+500-520 {
		t.Fatalf("sum of the 4 delta_w = %v, want %v", sum, 150-160+500-520)
	}
	if attrs.LastDiscontinuity == nil || *attrs.LastDiscontinuity != last4[3] {
		t.Fatalf("last_discontinuity = %+v, want the most recent event %+v", attrs.LastDiscontinuity, last4[3])
	}
}

// TestDiscontinuitiesOlderThanTenMinutesArePruned: recent_discontinuities
// only covers the last discontinuityWindowS (10 minutes);
// discontinuity_seq is unaffected by pruning (it is a monotonic "how
// many ever" counter).
func TestDiscontinuitiesOlderThanTenMinutesArePruned(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 10, 1150)
	pushLoad(t, m, "fridge_power", 15, 150)
	lossOuts := pushLoadUnavailable(t, m, "fridge_power", 20)
	seqAtLoss := lastResidualAttributes(t, lossOuts).DiscontinuitySeq

	laterOuts := pushGrid(t, m, 20+discontinuityWindowS+60, 1150)
	attrs := lastResidualAttributes(t, laterOuts)
	if attrs.DiscontinuitySeq != seqAtLoss {
		t.Fatalf("discontinuity_seq = %d, want unchanged %d (nothing new happened)", attrs.DiscontinuitySeq, seqAtLoss)
	}
	for _, ev := range attrs.RecentDiscontinuities {
		if ev.Entity == "fridge_power" && ev.Kind == "lost" {
			t.Fatalf("the fridge_power loss is still in recent_discontinuities %.0fs later, want pruned", discontinuityWindowS+60)
		}
	}
}

// TestCandidateStepCoincidentWithDiscontinuityIsRejected: the DETECTOR
// must not count a discontinuity as an on/off step. A candidate step
// that falls within RejectWindowS of a known-load loss/regain is
// rejected by rejectOK (0.1.7's extension of its existing coincident-
// load-swing check), same as a too-big a swing in the load's own
// reported values would be.
func TestCandidateStepCoincidentWithDiscontinuityIsRejected(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 10, 500)
	pushLoad(t, m, "fridge_power", 15, 150)
	pushGrid(t, m, 20, 500)
	pushLoadUnavailable(t, m, "fridge_power", 25) // real loss, within 60s of the step below
	pushGrid(t, m, 30, 3500)  // +3000, in-band
	pushGrid(t, m, 130, 3480) // flush the chain into an event
	if m.open {
		t.Fatalf("a candidate step coincident with a known-load discontinuity must be rejected, not opened")
	}
}

// TestCandidateStepFarFromDiscontinuityStillOpens is the control for the
// test above: the SAME kind of step, far enough from any discontinuity,
// must still be detected normally.
func TestCandidateStepFarFromDiscontinuityStillOpens(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 10, 500)
	pushLoad(t, m, "fridge_power", 15, 150)
	pushGrid(t, m, 20, 500)
	pushLoadUnavailable(t, m, "fridge_power", 25) // loss well before the window below
	pushGrid(t, m, 300, 500)                      // let plenty of time pass
	pushGrid(t, m, 320, 3500)                     // +3000, in-band
	pushGrid(t, m, 420, 3480)                      // flush
	if !m.open {
		t.Fatalf("a step far from any discontinuity must still be detected normally")
	}
}

// TestRegainMidCycleDoesNotFalselyCloseViaFloorCheck: the DETECTOR must
// not count a discontinuity as an on/off step - this is the continuous
// (floor/pendingOff-ceiling) half of that requirement, protected by
// correctedResidual rather than rejectOK's window exclusion (see that
// field's doc comment for why). A known load's first-ever sample
// arriving mid-cycle (a "regained" event) can be large enough to blow
// straight through FloorTol on its own; it must not force-close a cycle
// the heater never actually left.
func TestRegainMidCycleDoesNotFalselyCloseViaFloorCheck(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 10, 500)
	pushGrid(t, m, 20, 500)
	pushGrid(t, m, 40, 3500)  // +3000 on-step
	pushGrid(t, m, 140, 3480) // flush -> cycle opens, onBaseline~500, onMagnitude=3000
	if !m.open {
		t.Fatalf("setup: cycle did not open")
	}

	// The EV starts reporting for the first time, mid-cycle: DeltaW is
	// -2500W, far more than FloorTol (600W) - left uncorrected this
	// would read as a floor violation.
	regainOuts := pushLoad(t, m, "loaver_power", 150, 2.5) // kW -> 2500W
	attrs := lastResidualAttributes(t, regainOuts)
	if attrs.LastDiscontinuity == nil || attrs.LastDiscontinuity.Kind != "regained" ||
		attrs.LastDiscontinuity.Entity != "loaver_power" || attrs.LastDiscontinuity.DeltaW != -2500 {
		t.Fatalf("last_discontinuity = %+v, want {regained loaver_power -2500}", attrs.LastDiscontinuity)
	}

	for _, tt := range []float64{160, 200, 260, 300} {
		pushGrid(t, m, tt, 3480) // flat - the heater is still genuinely on
	}
	if !m.open {
		t.Fatalf("cycle closed due to the EV regain's administrative jump - should have been corrected")
	}
	if m.energyKwh != 0 {
		t.Fatalf("energyKwh = %v, want 0 (nothing should have closed yet)", m.energyKwh)
	}

	pushGrid(t, m, 360, 500) // -2980 real off-step
	pushGrid(t, m, 460, 500)
	pushGrid(t, m, 560, 500)
	if m.open {
		t.Fatalf("cycle never closed on the real off-step")
	}
	if m.energyKwh <= 0 {
		t.Fatalf("energyKwh = %v, want > 0", m.energyKwh)
	}
}

// TestResidualGoesUnavailableWhenGridUnavailableDetectorUnaffected: the
// grid input itself going unavailable must mark ONLY the residual
// sensor unavailable - the detector's open-cycle/pairing state (and its
// own outputs) must be completely untouched - and the residual must
// recover as soon as the grid reports available again.
func TestResidualGoesUnavailableWhenGridUnavailableDetectorUnaffected(t *testing.T) {
	m := newTestModule(t)
	pushGrid(t, m, 10, 500)
	pushGrid(t, m, 20, 500)
	pushGrid(t, m, 40, 3500)  // +3000 on-step
	pushGrid(t, m, 140, 3480) // reversal -> flushes the on-step chain, opening the cycle
	if !m.open {
		t.Fatalf("setup: cycle did not open")
	}

	outs := pushGridUnavailable(t, m, 160)
	val, unavailable, found := lastResidual(outs)
	if !found || !unavailable {
		t.Fatalf("residual output = %v unavailable=%v found=%v, want Unavailable:true", val, unavailable, found)
	}
	for _, u := range outs {
		if u.ObjectID != OutResidualObjectID {
			t.Fatalf("grid unavailable produced an unexpected output for %s - the detector's own outputs/behaviour must not change", u.ObjectID)
		}
	}
	if !m.open {
		t.Fatalf("grid going unavailable must not itself affect the open detector cycle")
	}

	outs = pushGrid(t, m, 180, 3510)
	val, unavailable, found = lastResidual(outs)
	if !found || unavailable {
		t.Fatalf("residual did not recover (val=%v unavailable=%v found=%v) once the grid reported available again", val, unavailable, found)
	}
}

// TestResidualRoundedToWholeWatt: the residual output must always be a
// whole number of watts.
func TestResidualRoundedToWholeWatt(t *testing.T) {
	m := newTestModule(t)
	pushLoad(t, m, "fridge_power", 1, 100.4)
	outs := pushGrid(t, m, 5, 600.7) // 600.7 - 100.4 = 500.3 -> rounds to 500
	val, _, found := lastResidual(outs)
	if !found {
		t.Fatalf("no %s output published", OutResidualObjectID)
	}
	if val != math.Round(val) {
		t.Fatalf("residual %v is not a whole number of watts", val)
	}
	if val != 500 {
		t.Fatalf("residual = %v, want 500 (rounded from 500.3)", val)
	}
}
