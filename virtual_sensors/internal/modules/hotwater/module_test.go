package hotwater

import (
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

// TestResidualHoldsLastKnownLoadValueWhenUnavailable: a known load going
// unavailable must NOT make the residual unavailable or drop it from the
// sum - the existing held-value (LOCF) rule applies, same as the
// detector's own knownSum.
func TestResidualHoldsLastKnownLoadValueWhenUnavailable(t *testing.T) {
	m := newTestModule(t)
	pushLoad(t, m, "fridge_power", 1, 150)
	outs := pushGrid(t, m, 5, 1150)
	if val, _, found := lastResidual(outs); !found || val != 1000 {
		t.Fatalf("setup: residual = %v found=%v, want 1000", val, found)
	}

	pushLoadUnavailable(t, m, "fridge_power", 10)
	outs = pushGrid(t, m, 15, 1150)
	val, unavailable, found := lastResidual(outs)
	if !found {
		t.Fatalf("no %s output published after a known load went unavailable", OutResidualObjectID)
	}
	if unavailable {
		t.Fatalf("residual marked unavailable because a KNOWN LOAD (not the grid) went unavailable - the held-value rule should apply instead")
	}
	if val != 1000 {
		t.Fatalf("residual = %v, want 1000 (fridge_power's held/last-known 150W still subtracted)", val)
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
