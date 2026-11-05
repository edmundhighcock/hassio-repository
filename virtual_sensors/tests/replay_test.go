// Replay test (0.1.5): feeds real raw samples through the LIVE module
// (hotwater.Module.OnStateUpdate), one at a time in time order exactly as
// the live loop would, and compares the cycle it reconstructs against the
// batch backfill detector (detect.DetectCyclesArrays) run over the SAME
// window. This is the acceptance check for bringing the live path's gap
// handling and post-off dwell into line with the 0.1.4 batch fixes (Taiga
// #237 follow-up) - see module.go's package doc and DOCS.md "Live vs
// backfill".
//
// Fixtures: tests/testdata/replay/{1105,1020}_*.csv, narrow time-windowed
// exports (not the whole bucket) covering:
//   - 1105: 2026-11-05 19:20-21:40 UTC, the staged reheat (+2.08kW then
//     +0.85kW four minutes later) that read 0W live throughout before
//     0.1.4/0.1.5.
//   - 1020: 2026-10-20 00:00-20:00 UTC, including the ~68-minute Case B
//     cycle (18:30-19:38) that an unmetered oscillating load regressed to
//     21 minutes before the 0.1.4 post-off dwell fix.
//
// loaver_power (EV) is stored in these fixtures in its LIVE-native unit
// (kW, matching what module.go's OnStateUpdate scales by 1000) - every
// other entity is stored in native W, matching live for those too.
package tests

import (
	"math"
	"sort"
	"testing"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hotwater"
	"virtual_sensors/internal/modules/hotwater/backfill"
	"virtual_sensors/internal/modules/hotwater/detect"
)

// replayCycle is one on/off/kWh triple, from either the live
// reconstruction or the batch backfill reference.
type replayCycle struct {
	onTime, offTime float64
	kwh             float64
}

// runBackfillReference runs detect.DetectCyclesArrays over a replay
// fixture exactly as the backfill CLI/parity test do, and returns the
// accepted cycles as replayCycle.
func runBackfillReference(t *testing.T, prefix string) []replayCycle {
	t.Helper()
	gridT, gridV, gridU := loadCSV(t, "testdata/replay/"+prefix+"_grid_meter_power.csv")
	for i := range gridV {
		if gridU[i] == "kW" {
			gridV[i] *= 1000.0
		}
	}
	loadArrays := map[string]detect.LoadSeries{}
	knownSum := make([]float64, len(gridT))
	for _, name := range hotwater.KnownLoadEntities {
		lt, lv, lu := loadCSV(t, "testdata/replay/"+prefix+"_"+name+".csv")
		for i := range lv {
			if lu[i] == "kW" {
				lv[i] *= 1000.0
			}
		}
		if name == "loaver_power" {
			// Stored in live-native kW in these fixtures (see package
			// doc) - convert to W for the backfill reference, same as
			// build.py/backfill_hotwater.go do from the InfluxDB
			// measurement tag.
			for i := range lv {
				lv[i] *= 1000.0
			}
		}
		dt, dv, _ := backfill.Destale(lt, lv, backfill.StaleAfterS)
		loadArrays[name] = detect.LoadSeries{Times: dt, Values: dv}
		merged := backfill.LocfMerge(gridT, dt, dv)
		for i, v := range merged {
			knownSum[i] += v
		}
	}
	residual := make([]float64, len(gridT))
	for i := range gridT {
		residual[i] = gridV[i] - knownSum[i]
	}
	result := detect.DetectCyclesArrays(gridT, residual, loadArrays, detect.DefaultParams())
	var cycles []replayCycle
	for _, c := range result.Cycles {
		if c.OffTime == nil || c.PowerEstimateW == nil || c.DurationS == nil {
			continue
		}
		cycles = append(cycles, replayCycle{
			onTime: c.OnTime, offTime: *c.OffTime,
			kwh: *c.PowerEstimateW * (*c.DurationS) / 3600.0 / 1000.0,
		})
	}
	return cycles
}

// runLiveReplay feeds a replay fixture through a fresh hotwater.Module,
// one sample at a time in strict time order (grid and every known load
// interleaved, exactly as the live dispatch loop would deliver them), and
// reconstructs the cycles it published.
func runLiveReplay(t *testing.T, prefix string) []replayCycle {
	t.Helper()
	type rawUpdate struct {
		entityID string
		t        float64
		v        float64
	}
	var raw []rawUpdate
	gridT, gridV, _ := loadCSV(t, "testdata/replay/"+prefix+"_grid_meter_power.csv")
	for i := range gridT {
		raw = append(raw, rawUpdate{hotwater.GridEntity, gridT[i], gridV[i]})
	}
	for _, name := range hotwater.KnownLoadEntities {
		lt, lv, _ := loadCSV(t, "testdata/replay/"+prefix+"_"+name+".csv")
		for i := range lt {
			raw = append(raw, rawUpdate{name, lt[i], lv[i]})
		}
	}
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].t < raw[j].t })

	m := hotwater.New()
	if err := m.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}

	var powerUpdates, energyUpdates []modules.OutputUpdate
	for _, r := range raw {
		sec := int64(r.t)
		nsec := int64((r.t - float64(sec)) * 1e9)
		u := modules.StateUpdate{EntityID: r.entityID, Time: time.Unix(sec, nsec).UTC(), Value: r.v, Available: true}
		for _, out := range m.OnStateUpdate(u) {
			if out.ObjectID == hotwater.OutPowerObjectID {
				powerUpdates = append(powerUpdates, out)
			} else if out.ObjectID == hotwater.OutEnergyObjectID {
				energyUpdates = append(energyUpdates, out)
			}
		}
	}

	var cycles []replayCycle
	open := false
	var onTime float64
	prevEnergy := 0.0
	for _, u := range powerUpdates {
		ut := float64(u.Time.UnixNano()) / 1e9
		if u.Value > 0 {
			if !open {
				onTime = ut
				open = true
			} else {
				onTime = ut // re-anchor: track the latest on-step's own time
			}
			continue
		}
		if !open {
			continue
		}
		open = false
		kwh := 0.0
		for _, e := range energyUpdates {
			et := float64(e.Time.UnixNano()) / 1e9
			if math.Abs(et-ut) < 0.001 {
				kwh = e.Value - prevEnergy
				prevEnergy = e.Value
				break
			}
		}
		cycles = append(cycles, replayCycle{onTime: onTime, offTime: ut, kwh: kwh})
	}
	return cycles
}

// closestCycle returns the cycle in cycles whose onTime is closest to
// targetOnTime, for comparing one known cycle of interest across the two
// pipelines (rather than requiring every cycle in the window to line up,
// which live's documented simplifications don't guarantee - see
// DOCS.md "Live vs backfill").
func closestCycle(cycles []replayCycle, targetOnTime float64) (replayCycle, bool) {
	best := replayCycle{}
	found := false
	bestDiff := math.Inf(1)
	for _, c := range cycles {
		d := math.Abs(c.onTime - targetOnTime)
		if d < bestDiff {
			bestDiff = d
			best = c
			found = true
		}
	}
	return best, found
}

func TestLiveReplayMatchesBackfillStagedReheat20261105(t *testing.T) {
	backfillCycles := runBackfillReference(t, "1105")
	liveCycles := runLiveReplay(t, "1105")

	// The staged reheat: backfill recognises the combined on-step around
	// 2026-11-05 19:53 UTC (Unix ~1793901196).
	targetOn := 1793908396.0
	bc, ok := closestCycle(backfillCycles, targetOn)
	if !ok || math.Abs(bc.onTime-targetOn) > 300 {
		t.Fatalf("backfill reference: no cycle near the staged reheat's on-time; got cycles=%v", backfillCycles)
	}
	lc, ok := closestCycle(liveCycles, bc.onTime)
	if !ok {
		t.Fatalf("live replay: found no cycles at all (backfill=%v)", bc)
	}
	t.Logf("backfill: on=%.0f off=%.0f kwh=%.3f", bc.onTime, bc.offTime, bc.kwh)
	t.Logf("live:     on=%.0f off=%.0f kwh=%.3f", lc.onTime, lc.offTime, lc.kwh)
	if math.Abs(lc.onTime-bc.onTime) > 180 {
		t.Errorf("on_time mismatch: live=%.0f backfill=%.0f diff=%.0fs", lc.onTime, bc.onTime, lc.onTime-bc.onTime)
	}
	if math.Abs(lc.offTime-bc.offTime) > 180 {
		t.Errorf("off_time mismatch: live=%.0f backfill=%.0f diff=%.0fs", lc.offTime, bc.offTime, lc.offTime-bc.offTime)
	}
	if bc.kwh > 0 && math.Abs(lc.kwh-bc.kwh)/bc.kwh > 0.2 {
		t.Errorf("kwh mismatch: live=%.3f backfill=%.3f (>20%% relative difference)", lc.kwh, bc.kwh)
	}
}

func TestLiveReplayMatchesBackfillCaseB20261020(t *testing.T) {
	backfillCycles := runBackfillReference(t, "1020")
	liveCycles := runLiveReplay(t, "1020")

	// Case B: the ~68-minute cycle starting ~2026-10-20 18:30 UTC (Unix
	// ~1792520974 for 18:29:34, giving the search a little room either
	// side of the exact on-step).
	targetOn := 1792520974.0
	bc, ok := closestCycle(backfillCycles, targetOn)
	if !ok || math.Abs(bc.onTime-targetOn) > 300 {
		t.Fatalf("backfill reference: no cycle near Case B's on-time; got cycles=%v", backfillCycles)
	}
	if bc.offTime-bc.onTime < 50*60 {
		t.Fatalf("backfill reference itself doesn't show Case B as ~68 minutes (got %.1f min) - fixture or window problem",
			(bc.offTime-bc.onTime)/60)
	}
	lc, ok := closestCycle(liveCycles, bc.onTime)
	if !ok {
		t.Fatalf("live replay: found no cycles at all (backfill=%v)", bc)
	}
	t.Logf("backfill: on=%.0f off=%.0f kwh=%.3f dur_min=%.1f", bc.onTime, bc.offTime, bc.kwh, (bc.offTime-bc.onTime)/60)
	t.Logf("live:     on=%.0f off=%.0f kwh=%.3f dur_min=%.1f", lc.onTime, lc.offTime, lc.kwh, (lc.offTime-lc.onTime)/60)
	if math.Abs(lc.onTime-bc.onTime) > 180 {
		t.Errorf("on_time mismatch: live=%.0f backfill=%.0f diff=%.0fs", lc.onTime, bc.onTime, lc.onTime-bc.onTime)
	}
	if math.Abs(lc.offTime-bc.offTime) > 180 {
		t.Errorf("off_time mismatch: live=%.0f backfill=%.0f diff=%.0fs", lc.offTime, bc.offTime, lc.offTime-bc.offTime)
	}
	if lc.offTime-lc.onTime < 50*60 {
		t.Errorf("live replay cut Case B short: got %.1f min, want ~68min like backfill", (lc.offTime-lc.onTime)/60)
	}
	if bc.kwh > 0 && math.Abs(lc.kwh-bc.kwh)/bc.kwh > 0.2 {
		t.Errorf("kwh mismatch: live=%.3f backfill=%.3f (>20%% relative difference)", lc.kwh, bc.kwh)
	}
}
