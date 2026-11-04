// Package backfill is a Go port of the hot-water-estimate Python
// build.py's pure numeric helpers (staleness, LOCF merge, trapezoidal
// hourly energy integration, line-protocol writing) plus the pipeline
// that wires them to detect.DetectCyclesArrays. Used by the `backfill`
// CLI subcommand (cmd/virtual_sensors) to reproduce the Python reference
// output exactly - see tests/parity_test.go. It is READ-ONLY with
// respect to InfluxDB: nothing here issues a write.
package backfill

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"virtual_sensors/internal/modules/hotwater/detect"
)

// StaleRun records one run of an exactly-repeated, non-zero value long
// enough to be treated as stale - build.py's destale_series return value.
type StaleRun struct {
	Value float64
	Start float64
	End   float64
}

// StaleAfterS is build.py's STALE_AFTER_S: a known load holding an
// identical non-zero reading this long is zeroed from this point into the
// run onward. See build.py's own long comment on heat_pump_power_power
// freezing at 1049.6W for 1035 days straight.
const StaleAfterS = 6 * 3600.0

// Destale is a port of build.py's destale_series.
func Destale(times, values []float64, staleAfterS float64) (outT, outV []float64, runs []StaleRun) {
	n := len(times)
	if n < 2 {
		return append([]float64{}, times...), append([]float64{}, values...), nil
	}
	outT = append(outT, times[0])
	outV = append(outV, values[0])
	i := 0
	for i < n-1 {
		j := i
		for j+1 < n && values[j+1] == values[i] {
			j++
		}
		runDur := times[j] - times[i]
		if runDur > staleAfterS && values[i] != 0.0 {
			staleT := times[i] + staleAfterS
			outT = append(outT, staleT)
			outV = append(outV, 0.0)
			runs = append(runs, StaleRun{Value: values[i], Start: times[i], End: times[j]})
		}
		if j+1 < n {
			outT = append(outT, times[j+1])
			outV = append(outV, values[j+1])
		}
		i = j + 1
	}
	return outT, outV, runs
}

// LocfMerge is a port of build.py's locf_merge: resample load values onto
// gridTimes via last-observation-carried-forward; before the first load
// sample (or an empty load series), the value is 0 (missing/unavailable
// treated as 0, per the brief).
func LocfMerge(gridTimes, loadTimes, loadValues []float64) []float64 {
	out := make([]float64, len(gridTimes))
	if len(loadTimes) == 0 {
		return out
	}
	for i, t := range gridTimes {
		idx := sort.Search(len(loadTimes), func(k int) bool { return loadTimes[k] > t }) - 1
		if idx >= 0 {
			if idx >= len(loadValues) {
				idx = len(loadValues) - 1
			}
			out[i] = loadValues[idx]
		}
	}
	return out
}

// HourCoverageGapS is build.py's HOUR_COVERAGE_GAP_S.
const HourCoverageGapS = 300.0

// HourCoverage is a port of build.py's hour_coverage.
func HourCoverage(gridTimes []float64, hourStarts []float64, hourS float64) []bool {
	covered := make([]bool, len(hourStarts))
	if len(gridTimes) == 0 {
		return covered
	}
	for i, h0 := range hourStarts {
		h1 := h0 + hourS
		lo := sort.SearchFloat64s(gridTimes, h0-HourCoverageGapS)
		hi := sort.SearchFloat64s(gridTimes, h1+HourCoverageGapS)
		seg := gridTimes[lo:hi]
		if len(seg) < 2 {
			continue
		}
		if seg[0] > h0+HourCoverageGapS || seg[len(seg)-1] < h1-HourCoverageGapS {
			continue
		}
		maxGap := 0.0
		for k := 1; k < len(seg); k++ {
			if d := seg[k] - seg[k-1]; d > maxGap {
				maxGap = d
			}
		}
		if len(seg) > 1 && maxGap > HourCoverageGapS {
			continue
		}
		covered[i] = true
	}
	return covered
}

// TrapzHourlyKwh is a port of build.py's trapz_hourly_kwh.
func TrapzHourlyKwh(times, watts, hourStarts []float64, hourS float64) []float64 {
	kwh := make([]float64, len(hourStarts))
	if len(times) < 2 || len(hourStarts) == 0 {
		return kwh
	}
	h0 := hourStarts[0]
	for i := 0; i < len(times)-1; i++ {
		t0, t1 := times[i], times[i+1]
		if t1 <= t0 {
			continue
		}
		dt := t1 - t0
		if dt > HourCoverageGapS*4 {
			continue // real data gap, not a load
		}
		w0, w1 := watts[i], watts[i+1]
		curT := t0
		curW := w0
		for curT < t1 {
			hb := int((curT - h0) / hourS)
			if curT < h0 {
				hb = -1 // matches python's negative floor division for curT<h0
			}
			if hb < 0 || hb >= len(hourStarts) {
				break
			}
			boundary := h0 + float64(hb+1)*hourS
			segEnd := t1
			if boundary < segEnd {
				segEnd = boundary
			}
			if segEnd > curT {
				wEnd := w0
				if dt > 0 {
					wEnd = w0 + (w1-w0)*((segEnd-t0)/dt)
				}
				avg := (curW + wEnd) / 2.0
				kwh[hb] += avg * (segEnd - curT) / 3600.0 / 1000.0
				curW = wEnd
			}
			curT = segEnd
		}
	}
	return kwh
}

// CyclesToHourlyKwh is a port of build.py's cycles_to_hourly_kwh: integrates
// accepted heater cycles (piecewise-constant power_estimate_w from on_time
// to off_time) into watt-hours per hour bucket.
func CyclesToHourlyKwh(cycles []detect.Cycle, hourStarts []float64, hourS float64) []float64 {
	kwh := make([]float64, len(hourStarts))
	if len(hourStarts) == 0 {
		return kwh
	}
	h0 := hourStarts[0]
	for _, c := range cycles {
		if c.OffTime == nil || c.PowerEstimateW == nil {
			continue
		}
		t0, t1, p := c.OnTime, *c.OffTime, *c.PowerEstimateW
		cur := t0
		for cur < t1 {
			hb := int((cur - h0) / hourS)
			if cur < h0 {
				hb = -1
			}
			if hb < 0 || hb >= len(hourStarts) {
				break
			}
			boundary := h0 + float64(hb+1)*hourS
			segEnd := t1
			if boundary < segEnd {
				segEnd = boundary
			}
			kwh[hb] += p * (segEnd - cur) / 3600.0 / 1000.0
			cur = segEnd
		}
	}
	return kwh
}

// StretchGapS is build.py's STRETCH_GAP_S.
const StretchGapS = 3600.0

// FindStretches is a port of build.py's find_stretches.
func FindStretches(times []float64, gapS float64) []float64 {
	if len(times) == 0 {
		return nil
	}
	starts := []float64{times[0]}
	for i := 1; i < len(times); i++ {
		if times[i]-times[i-1] > gapS {
			starts = append(starts, times[i])
		}
	}
	return starts
}

// LPPoint is one (time, power) line-protocol point.
type LPPoint struct {
	T float64
	P float64
}

// HWEntityID / HWFriendlyName mirror build.py's HW_ENTITY_ID / HW_FRIENDLY_NAME.
const (
	HWEntityID     = "hot_water_estimated_power"
	HWFriendlyName = "Hot water estimated power"
)

// WriteLP is a port of build.py's write_lp: line protocol matching the
// existing grid_meter_power series in each bucket (measurement = unit
// "W", tags domain=sensor + entity_id, fields value/friendly_name_str/
// device_class_str/state_class_str as HA's influxdb-integration "_str"
// convention).
func WriteLP(path string, points []LPPoint) (int, error) {
	tags := fmt.Sprintf("domain=sensor,entity_id=%s", HWEntityID)
	var b strings.Builder
	for _, pt := range points {
		ns := int64(pt.T*1e9 + sign(pt.T)*0.5) // round-half-away-from-zero, matching Python's round()
		fields := fmt.Sprintf(`value=%s,friendly_name_str="%s",device_class_str="power",state_class_str="measurement"`,
			formatFloat(pt.P), HWFriendlyName)
		b.WriteString(fmt.Sprintf("W,%s %s %d\n", tags, fields, ns))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return 0, err
	}
	return len(points), nil
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// formatFloat mirrors Python's str(float) formatting closely enough for
// line-protocol parity (no trailing zeros beyond what's needed, "0.0" for
// zero) - see tests/parity_test.go for a byte-level comparison against the
// Python .lp files.
func formatFloat(v float64) string {
	s := fmt.Sprintf("%g", v)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}
