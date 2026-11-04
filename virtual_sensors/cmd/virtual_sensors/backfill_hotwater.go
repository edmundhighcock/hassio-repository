package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"virtual_sensors/internal/influxread"
	"virtual_sensors/internal/modules/hotwater"
	"virtual_sensors/internal/modules/hotwater/backfill"
	"virtual_sensors/internal/modules/hotwater/detect"
)

// runHotWaterBackfill reproduces build.py's process_bucket + the .lp/.csv
// writers for ONE bucket, read-only against InfluxDB. See DOCS.md
// "Backfill mode" and tests/parity_test.go (which runs the SAME
// detect/backfill packages against the cached parquet-derived fixtures
// instead of a live query, as the acceptance gate).
func runHotWaterBackfill(influxURL, org, token, bucket string, from, to time.Time, outDir string) error {
	client := &influxread.Client{URL: influxURL, Org: org, Token: token}
	ctx := context.Background()

	log.Printf("backfill: querying bucket %s for %s..%s", bucket, from.Format(time.RFC3339), to.Format(time.RFC3339))

	gridPoints, err := client.QueryEntity(ctx, bucket, hotwater.GridEntity, from, to)
	if err != nil {
		return fmt.Errorf("querying %s: %w", hotwater.GridEntity, err)
	}
	if len(gridPoints) == 0 {
		log.Printf("backfill: no %s data in bucket %s for this range; nothing to write", hotwater.GridEntity, bucket)
		return nil
	}
	gridT, gridV := pointsToArrays(gridPoints)

	loadArrays := map[string]detect.LoadSeries{}
	for _, name := range hotwater.KnownLoadEntities {
		pts, err := client.QueryEntity(ctx, bucket, name, from, to)
		if err != nil {
			return fmt.Errorf("querying %s: %w", name, err)
		}
		lt, lv := pointsToArrays(pts)
		dt, dv, runs := backfill.Destale(lt, lv, backfill.StaleAfterS)
		loadArrays[name] = detect.LoadSeries{Times: dt, Values: dv}
		for _, r := range runs {
			log.Printf("backfill: %s stale run: %.1fW held %s -> %s (zeroed from %s)",
				name, r.Value, unixToTime(r.Start).Format(time.RFC3339), unixToTime(r.End).Format(time.RFC3339),
				unixToTime(r.Start+backfill.StaleAfterS).Format(time.RFC3339))
		}
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

	log.Printf("backfill: bucket %s: %d accepted cycles, %d rejected (floor), %d unmatched (timeout), %d unmatched (data gap)",
		bucket, result.Stats.CyclesAccepted, result.Stats.CyclesRejectedFloor,
		result.Stats.CyclesUnmatchedTimeout, result.Stats.CyclesUnmatchedDataGap)

	// --- Line protocol, matching build.py's write_lp exactly. ----------
	var points []backfill.LPPoint
	for _, s := range backfill.FindStretches(gridT, backfill.StretchGapS) {
		points = append(points, backfill.LPPoint{T: s, P: 0.0})
	}
	for _, c := range result.Cycles {
		points = append(points, backfill.LPPoint{T: c.OnTime, P: *c.PowerEstimateW})
		points = append(points, backfill.LPPoint{T: *c.OffTime, P: 0.0})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].T < points[j].T })
	points = dedupLP(points)

	lpPath := filepath.Join(outDir, fmt.Sprintf("hot_water_power_%s.lp", bucket))
	n, err := backfill.WriteLP(lpPath, points)
	if err != nil {
		return fmt.Errorf("writing %s: %w", lpPath, err)
	}
	log.Printf("backfill: wrote %s (%d points)", lpPath, n)

	// --- Hourly energy CSV, matching build.py's hour bucketing. --------
	firstHour := float64(int64(gridT[0]/3600.0)) * 3600.0
	lastHour := float64(int64(gridT[len(gridT)-1]/3600.0)) * 3600.0
	var hourStarts []float64
	for h := firstHour; h <= lastHour; h += 3600.0 {
		hourStarts = append(hourStarts, h)
	}
	hwKwh := backfill.CyclesToHourlyKwh(result.Cycles, hourStarts, 3600.0)
	covered := backfill.HourCoverage(gridT, hourStarts, 3600.0)

	csvPath := filepath.Join(outDir, fmt.Sprintf("hot_water_energy_hourly_%s.csv", bucket))
	if err := writeHourlyCSV(csvPath, hourStarts, hwKwh, covered); err != nil {
		return fmt.Errorf("writing %s: %w", csvPath, err)
	}
	log.Printf("backfill: wrote %s (%d hourly rows)", csvPath, len(hourStarts))
	return nil
}

func pointsToArrays(pts []influxread.Point) (times, values []float64) {
	times = make([]float64, len(pts))
	values = make([]float64, len(pts))
	for i, p := range pts {
		times[i] = float64(p.Time.UnixNano()) / 1e9
		values[i] = p.ToWatts()
	}
	return times, values
}

func unixToTime(epochS float64) time.Time {
	return time.Unix(int64(epochS), 0).UTC()
}

func dedupLP(points []backfill.LPPoint) []backfill.LPPoint {
	seen := map[[2]float64]bool{}
	var out []backfill.LPPoint
	for _, p := range points {
		key := [2]float64{roundTo(p.T, 3), roundTo(p.P, 3)}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

func roundTo(v float64, decimals int) float64 {
	m := 1.0
	for i := 0; i < decimals; i++ {
		m *= 10
	}
	r := v * m
	if r >= 0 {
		return float64(int64(r+0.5)) / m
	}
	return float64(int64(r-0.5)) / m
}

func writeHourlyCSV(path string, hourStarts, hwKwh []float64, covered []bool) error {
	var b []byte
	b = append(b, []byte("start_utc,kwh_in_hour,sum,covered\n")...)
	running := 0.0
	for i, h0 := range hourStarts {
		kwhVal := 0.0
		if covered[i] {
			running += hwKwh[i]
			kwhVal = hwKwh[i]
		}
		line := fmt.Sprintf("%s,%.5f,%.5f,%v\n",
			unixToTime(h0).Format("2006-01-02T15:04:05Z"), kwhVal, running, covered[i])
		b = append(b, []byte(line)...)
	}
	return os.WriteFile(path, b, 0o644)
}
