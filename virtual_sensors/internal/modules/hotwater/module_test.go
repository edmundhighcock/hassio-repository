package hotwater

import (
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
