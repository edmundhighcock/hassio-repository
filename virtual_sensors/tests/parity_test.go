// Package tests holds the acceptance-gate parity test: run this add-on's
// Go detector/backfill pipeline over the SAME cached raw history
// (exported from the parquet cache under
// /home/edmundhighcock/Code/ham/services/hot-water-estimate/data/raw_cache/)
// as the validated Python reference (detect.py/build.py, Taiga #237),
// and compare accepted cycles exactly.
//
// Fixtures here (tests/testdata/homeassistant__*.csv) are the `live`
// InfluxDB bucket - small enough to commit (5,035 grid samples) - and
// tests/testdata/homeassistant_expected.json is the Python reference's
// own detect_cycles_arrays() output on that same data, dumped by
// scratchpad script dump_expected.py (see the task notes; not part of
// this repo). The much larger `homeassistant_2026` bucket (796,688 grid
// samples, 847 accepted cycles) was checked the same way during
// development but is too large to commit - see this add-on's hand-off
// report for those numbers.
package tests

import (
	"encoding/csv"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"virtual_sensors/internal/modules/hotwater"
	"virtual_sensors/internal/modules/hotwater/backfill"
	"virtual_sensors/internal/modules/hotwater/detect"
)

type expectedCycle struct {
	OnTime         float64  `json:"on_time"`
	OffTime        *float64 `json:"off_time"`
	OnStep         float64  `json:"on_step"`
	OffStep        *float64 `json:"off_step"`
	PowerEstimateW *float64 `json:"power_estimate_w"`
	DurationS      *float64 `json:"duration_s"`
}

type expectedResult struct {
	Bucket string          `json:"bucket"`
	NGrid  int             `json:"n_grid"`
	Stats  map[string]int  `json:"stats"`
	Cycles []expectedCycle `json:"cycles"`
}

func loadCSV(t *testing.T, path string) (times, values []float64, units []string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for i, row := range rows {
		if i == 0 {
			continue // header: time_unix,value,unit
		}
		tv, err := strconv.ParseFloat(row[0], 64)
		if err != nil {
			t.Fatalf("%s row %d: bad time %q: %v", path, i, row[0], err)
		}
		vv, err := strconv.ParseFloat(row[1], 64)
		if err != nil {
			t.Fatalf("%s row %d: bad value %q: %v", path, i, row[1], err)
		}
		unit := ""
		if len(row) > 2 {
			unit = row[2]
		}
		times = append(times, tv)
		values = append(values, vv)
		units = append(units, unit)
	}
	return
}

func TestHotWaterParityAgainstPythonReference(t *testing.T) {
	testdata := "testdata"
	gridT, gridV, gridU := loadCSV(t, filepath.Join(testdata, "homeassistant__grid_meter_power.csv"))
	for i := range gridV {
		if gridU[i] == "kW" {
			gridV[i] *= 1000.0
		}
	}

	loadArrays := map[string]detect.LoadSeries{}
	for _, name := range hotwater.KnownLoadEntities {
		path := filepath.Join(testdata, "homeassistant__"+name+".csv")
		lt, lv, lu := loadCSV(t, path)
		for i := range lv {
			if lu[i] == "kW" {
				lv[i] *= 1000.0
			}
		}
		dt, dv, _ := backfill.Destale(lt, lv, backfill.StaleAfterS)
		loadArrays[name] = detect.LoadSeries{Times: dt, Values: dv}
	}

	knownSum := make([]float64, len(gridT))
	for _, name := range hotwater.KnownLoadEntities {
		ls := loadArrays[name]
		merged := backfill.LocfMerge(gridT, ls.Times, ls.Values)
		for i, v := range merged {
			knownSum[i] += v
		}
	}
	residual := make([]float64, len(gridT))
	for i := range gridT {
		residual[i] = gridV[i] - knownSum[i]
	}

	params := detect.DefaultParams()
	result := detect.DetectCyclesArrays(gridT, residual, loadArrays, params)

	b, err := os.ReadFile(filepath.Join(testdata, "homeassistant_expected.json"))
	if err != nil {
		t.Fatalf("read expected fixture: %v", err)
	}
	var want expectedResult
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatalf("parse expected fixture: %v", err)
	}

	if len(gridT) != want.NGrid {
		t.Fatalf("n_grid = %d, want %d (fixture mismatch - re-export testdata)", len(gridT), want.NGrid)
	}

	if len(result.Cycles) != len(want.Cycles) {
		t.Fatalf("accepted cycle count = %d, want %d (PARITY FAILURE)", len(result.Cycles), len(want.Cycles))
	}
	const timeTol = 1.0 // seconds
	for i, c := range result.Cycles {
		wc := want.Cycles[i]
		if math.Abs(c.OnTime-wc.OnTime) > timeTol {
			t.Errorf("cycle %d: OnTime = %v, want %v (diff %.3fs)", i, c.OnTime, wc.OnTime, c.OnTime-wc.OnTime)
		}
		if (c.OffTime == nil) != (wc.OffTime == nil) {
			t.Errorf("cycle %d: OffTime nil-ness mismatch", i)
		} else if c.OffTime != nil && math.Abs(*c.OffTime-*wc.OffTime) > timeTol {
			t.Errorf("cycle %d: OffTime = %v, want %v (diff %.3fs)", i, *c.OffTime, *wc.OffTime, *c.OffTime-*wc.OffTime)
		}
		if c.OnStep != wc.OnStep {
			t.Errorf("cycle %d: OnStep = %v, want %v", i, c.OnStep, wc.OnStep)
		}
	}

	// Total accepted-cycle kWh must match within float rounding.
	gotKwh := 0.0
	for _, c := range result.Cycles {
		if c.PowerEstimateW != nil && c.DurationS != nil {
			gotKwh += *c.PowerEstimateW * (*c.DurationS) / 3600.0 / 1000.0
		}
	}
	wantKwh := 0.0
	for _, c := range want.Cycles {
		if c.PowerEstimateW != nil && c.DurationS != nil {
			wantKwh += *c.PowerEstimateW * (*c.DurationS) / 3600.0 / 1000.0
		}
	}
	if math.Abs(gotKwh-wantKwh) > 0.001 {
		t.Errorf("total accepted-cycle kWh = %.5f, want %.5f", gotKwh, wantKwh)
	}

	// Stats parity (every rejection/acceptance bucket, not just the
	// headline accepted count).
	gotStats := map[string]int{
		"steps_total":                        result.Stats.StepsTotal,
		"steps_rejected_coincident_load":     result.Stats.StepsRejectedCoincidentLoad,
		"steps_superseded":                   result.Stats.StepsSuperseded,
		"cycles_accepted":                    result.Stats.CyclesAccepted,
		"cycles_accepted_with_tolerated_dip": result.Stats.CyclesAcceptedWithToleratedDip,
		"cycles_rejected_magnitude":          result.Stats.CyclesRejectedMagnitude,
		"cycles_rejected_floor":              result.Stats.CyclesRejectedFloor,
		"cycles_unmatched_timeout":           result.Stats.CyclesUnmatchedTimeout,
		"cycles_unmatched_data_gap":          result.Stats.CyclesUnmatchedDataGap,
		"gaps_skipped":                       result.Stats.GapsSkipped,
	}
	for k, wv := range want.Stats {
		if gotStats[k] != wv {
			t.Errorf("stats[%s] = %d, want %d", k, gotStats[k], wv)
		}
	}

	t.Logf("PARITY OK: %d/%d accepted cycles match Python reference exactly (within %.0fs), total kWh got=%.5f want=%.5f",
		len(result.Cycles), len(want.Cycles), timeTol, gotKwh, wantKwh)
}
