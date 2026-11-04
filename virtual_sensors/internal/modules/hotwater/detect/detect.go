// Package detect is a faithful Go port of the hot-water-estimate Python
// detector (/home/edmundhighcock/Code/ham/services/hot-water-estimate/detect.py,
// Taiga #237). It is the single piece of this add-on required to agree
// EXACTLY with the validated Python reference on the same input: see
// tests/parity_test.go. Keep this file a line-for-line-equivalent port -
// resist the urge to "improve" it; any behavioural difference from
// detect.py must be deliberate and documented in DOCS.md.
//
// Model: the household's 3kW immersion heater has no meter of its own. It
// is found by looking at residual(t) = grid(t) - sum(known_loads(t)) for
// its characteristic +-3000W step. See detect.py's own module docstring
// and the hot-water-estimate README.md ("Detector") for the full
// rationale behind every threshold below - it is not repeated here.
package detect

import "sort"

// Params mirrors detect.py's DetectionParams, defaults included.
type Params struct {
	// Merged-event step detection.
	SeedDeltaMin float64 // single-sample delta that seeds a cluster
	MergeWindowS float64 // max gap between consecutive same-sign seeds to merge
	StepLo       float64 // merged-event magnitude band (absolute, per-event)
	StepHi       float64
	MaxGapS      float64 // max dt between consecutive raw samples before it's a data gap

	// Coincident-load reject check.
	RejectWindowS   float64
	RejectLoadDelta float64

	// Pairing.
	PairTol        float64 // loose tie-breaker among several off-candidates
	MaxOnDurationS float64

	// Floor check.
	FloorTol     float64
	FloorDipMaxS float64
}

// DefaultParams returns the production thresholds, tuned against this
// household's real data - see detect.py's DetectionParams and the
// hot-water-estimate README.md "Tuning narrative".
func DefaultParams() Params {
	return Params{
		SeedDeltaMin:    800.0,
		MergeWindowS:    30.0,
		StepLo:          2500.0,
		StepHi:          3300.0,
		MaxGapS:         120.0,
		RejectWindowS:   60.0,
		RejectLoadDelta: 800.0,
		PairTol:         900.0,
		MaxOnDurationS:  4 * 3600.0,
		FloorTol:        600.0,
		FloorDipMaxS:    120.0,
	}
}

// Sample is one point on the merged residual timeline (used by the simple,
// test-oriented API) - detect.py's Sample.
type Sample struct {
	T        float64
	Residual float64
	Loads    map[string]float64 // LOCF known-load values at T
}

// MergedEvent is detect.py's MergedEvent.
type MergedEvent struct {
	TStart    float64 // time of the sample just before the transition began
	TEnd      float64 // time of the sample at which the transition had settled
	Magnitude float64 // signed sum of the seed deltas that make up this event
}

// RejectReason values - detect.py's Cycle.reject_reason / RejectedStep.reason.
const (
	ReasonTimeout        = "timeout"
	ReasonDataGap        = "data_gap"
	ReasonFloorViolation = "floor_violation"
	ReasonStreamEnd      = "stream_end"
	ReasonCoincidentLoad = "coincident_load"
	ReasonSuperseded     = "superseded"
)

// Cycle mirrors detect.py's Cycle. OffTime/OffStep/DurationS/PowerEstimateW/
// FloorMaxDeficit are pointers standing in for Python's Optional[float].
type Cycle struct {
	OnTime            float64
	OffTime           *float64
	OnStep            float64
	OffStep           *float64
	DurationS         *float64
	PowerEstimateW    *float64
	FloorMaxDeficit   *float64
	FloorDipTolerated bool
	Accepted          bool
	RejectReason      string // "" when Accepted
}

// RejectedStep mirrors detect.py's RejectedStep.
type RejectedStep struct {
	T         float64
	Delta     float64
	Direction string // "on" | "off"
	Reason    string // ReasonCoincidentLoad | ReasonSuperseded
}

// Stats mirrors detect.py's stats dict.
type Stats struct {
	StepsTotal                     int
	StepsRejectedCoincidentLoad    int
	StepsSuperseded                int
	CyclesAccepted                 int
	CyclesAcceptedWithToleratedDip int
	CyclesRejectedMagnitude        int // kept for backward-compat reporting; always 0
	CyclesRejectedFloor            int
	CyclesUnmatchedTimeout         int
	CyclesUnmatchedDataGap         int
	GapsSkipped                    int
}

// Result mirrors detect.py's DetectionResult.
type Result struct {
	Cycles          []Cycle        // accepted heater cycles only
	RejectedCycles  []Cycle        // candidate on/off pairs that failed the floor check
	UnmatchedCycles []Cycle        // ON runs force-closed by timeout or data gap
	RejectedSteps   []RejectedStep // individual steps voided by a coincident load change
	Stats           Stats
}

// f64p returns a pointer to v - a small helper standing in for Python's
// bare float (vs. None).
func f64p(v float64) *float64 { return &v }

// ---------------------------------------------------------------------------
// Step 1: merge consecutive same-sign single-sample deltas into events.
// Port of detect.py's find_merged_events.
// ---------------------------------------------------------------------------

func FindMergedEvents(times []float64, values []float64, p Params) []MergedEvent {
	n := len(times)
	var events []MergedEvent
	runStartIdx := -1 // -1 == None
	runSign := 0
	runMagnitude := 0.0
	lastSeedTime := 0.0
	haveLastSeedTime := false
	lastIdx := -1

	flush := func(endIdx int) {
		if runStartIdx != -1 {
			events = append(events, MergedEvent{
				TStart:    times[runStartIdx],
				TEnd:      times[endIdx],
				Magnitude: runMagnitude,
			})
			runStartIdx = -1
		}
	}

	i := 1
	for i < n {
		dt := times[i] - times[i-1]
		if dt <= 0 || dt > p.MaxGapS {
			flush(i - 1)
			i++
			continue
		}

		delta := values[i] - values[i-1]
		if abs(delta) >= p.SeedDeltaMin {
			sign := 1
			if delta <= 0 {
				sign = -1
			}
			if runStartIdx != -1 && sign == runSign && haveLastSeedTime &&
				(times[i-1]-lastSeedTime) <= p.MergeWindowS {
				runMagnitude += delta
				lastSeedTime = times[i]
				haveLastSeedTime = true
				lastIdx = i
			} else {
				if lastIdx != -1 {
					flush(lastIdx)
				} else {
					flush(i - 1)
				}
				runStartIdx = i - 1
				runSign = sign
				runMagnitude = delta
				lastSeedTime = times[i]
				haveLastSeedTime = true
				lastIdx = i
			}
		} else {
			if runStartIdx != -1 && haveLastSeedTime && (times[i]-lastSeedTime) > p.MergeWindowS {
				flush(lastIdx)
			}
		}
		i++
	}
	if lastIdx != -1 {
		flush(lastIdx)
	} else {
		flush(n - 1)
	}
	return events
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// FindGapMarkers is a port of detect.py's _find_gap_markers.
func FindGapMarkers(times []float64, maxGapS float64) []float64 {
	var markers []float64
	for i := 1; i < len(times); i++ {
		dt := times[i] - times[i-1]
		if dt <= 0 || dt > maxGapS {
			markers = append(markers, times[i-1])
		}
	}
	return markers
}

// RejectCheck answers whether a candidate step is voided by a known load
// moving too much nearby - detect.py's reject_check(t_start, t_end, direction).
type RejectCheck func(tStart, tEnd float64, direction string) bool

// FloorLookup answers the floor check over [loT, hiT] - detect.py's
// residual_lookup(lo_t, hi_t, floor, dip_max_s) -> (floor_ok, max_deficit, dip_tolerated).
type FloorLookup func(loT, hiT, floor, dipMaxS float64) (floorOK bool, maxDeficit float64, dipTolerated bool)

// bisectLeft/bisectRight mirror Python's bisect.bisect_left/bisect_right on
// a sorted []float64.
func bisectLeft(a []float64, x float64) int {
	return sort.Search(len(a), func(i int) bool { return a[i] >= x })
}

func bisectRight(a []float64, x float64) int {
	return sort.Search(len(a), func(i int) bool { return a[i] > x })
}

// ---------------------------------------------------------------------------
// Step 2: pair merged events into cycles. Port of detect.py's _pair_events.
// ---------------------------------------------------------------------------

func PairEvents(events []MergedEvent, floorLookup FloorLookup, gapMarkers []float64,
	seriesEndT float64, rejectCheck RejectCheck, p Params) Result {

	var cycles, rejectedCycles, unmatchedCycles []Cycle
	var rejectedSteps []RejectedStep
	stats := Stats{GapsSkipped: len(gapMarkers)}

	isOn := func(e MergedEvent) bool { return p.StepLo <= e.Magnitude && e.Magnitude <= p.StepHi }
	isOff := func(e MergedEvent) bool { return -p.StepHi <= e.Magnitude && e.Magnitude <= -p.StepLo }

	nextGapAfter := func(t float64) (float64, bool) {
		idx := bisectRight(gapMarkers, t)
		if idx < len(gapMarkers) {
			return gapMarkers[idx], true
		}
		return 0, false
	}

	floorCheck := func(onEvent, offEvent MergedEvent) (bool, float64, bool) {
		floor := onEvent.Magnitude - p.FloorTol
		loT, hiT := onEvent.TEnd, offEvent.TStart
		return floorLookup(loT, hiT, floor, p.FloorDipMaxS)
	}

	n := len(events)
	i := 0
	state := "OFF"
	var onEvent MergedEvent

	for i < n {
		e := events[i]
		if state == "OFF" {
			if isOn(e) {
				stats.StepsTotal++
				if rejectCheck(e.TStart, e.TEnd, "on") {
					state = "ON"
					onEvent = e
				} else {
					rejectedSteps = append(rejectedSteps, RejectedStep{T: e.TStart, Delta: e.Magnitude, Direction: "on", Reason: ReasonCoincidentLoad})
					stats.StepsRejectedCoincidentLoad++
				}
			}
			i++
			continue
		}

		// state == ON
		gapT, haveGap := nextGapAfter(onEvent.TEnd)
		deadline := onEvent.TEnd + p.MaxOnDurationS
		if haveGap && gapT < deadline {
			deadline = gapT
		}

		if e.TStart > deadline {
			reason := ReasonTimeout
			offT := onEvent.TEnd + p.MaxOnDurationS
			if haveGap && gapT <= onEvent.TEnd+p.MaxOnDurationS {
				reason = ReasonDataGap
				offT = gapT
			}
			dur := offT - onEvent.TEnd
			c := Cycle{OnTime: onEvent.TEnd, OffTime: f64p(offT), OnStep: onEvent.Magnitude,
				DurationS: f64p(dur), Accepted: false, RejectReason: reason}
			unmatchedCycles = append(unmatchedCycles, c)
			if reason == ReasonDataGap {
				stats.CyclesUnmatchedDataGap++
			} else {
				stats.CyclesUnmatchedTimeout++
			}
			state = "OFF"
			continue // re-examine e from OFF state
		}

		if isOn(e) {
			stats.StepsTotal++
			if rejectCheck(e.TStart, e.TEnd, "on") {
				rejectedSteps = append(rejectedSteps, RejectedStep{T: onEvent.TStart, Delta: onEvent.Magnitude, Direction: "on", Reason: ReasonSuperseded})
				stats.StepsSuperseded++
				onEvent = e // re-anchor to the newer, presumably real, on-step
			} else {
				rejectedSteps = append(rejectedSteps, RejectedStep{T: e.TStart, Delta: e.Magnitude, Direction: "on", Reason: ReasonCoincidentLoad})
				stats.StepsRejectedCoincidentLoad++
			}
			i++
			continue
		}

		if isOff(e) {
			stats.StepsTotal++
			if !rejectCheck(e.TStart, e.TEnd, "off") {
				rejectedSteps = append(rejectedSteps, RejectedStep{T: e.TStart, Delta: e.Magnitude, Direction: "off", Reason: ReasonCoincidentLoad})
				stats.StepsRejectedCoincidentLoad++
				i++
				continue
			}

			// Look ahead among subsequent off-candidates (up to the deadline
			// or the next on-candidate) for the best magnitude match; stop
			// early once one is within pair_tol.
			bestI := i
			bestDiff := abs(onEvent.Magnitude + e.Magnitude)
			j := i + 1
			for j < n && events[j].TStart <= deadline && !isOn(events[j]) {
				if isOff(events[j]) && rejectCheck(events[j].TStart, events[j].TEnd, "off") {
					diff := abs(onEvent.Magnitude + events[j].Magnitude)
					if diff < bestDiff {
						bestDiff = diff
						bestI = j
					}
					if diff <= p.PairTol {
						break
					}
				}
				j++
			}
			offEvent := events[bestI]

			floorOK, floorMaxDeficit, dipTolerated := floorCheck(onEvent, offEvent)
			dur := offEvent.TStart - onEvent.TEnd
			power := (abs(onEvent.Magnitude) + abs(offEvent.Magnitude)) / 2.0

			if floorOK {
				c := Cycle{OnTime: onEvent.TEnd, OffTime: f64p(offEvent.TStart),
					OnStep: onEvent.Magnitude, OffStep: f64p(offEvent.Magnitude),
					DurationS: f64p(dur), PowerEstimateW: f64p(power),
					FloorMaxDeficit: f64p(floorMaxDeficit), Accepted: true,
					FloorDipTolerated: dipTolerated}
				cycles = append(cycles, c)
				stats.CyclesAccepted++
				if dipTolerated {
					stats.CyclesAcceptedWithToleratedDip++
				}
			} else {
				c := Cycle{OnTime: onEvent.TEnd, OffTime: f64p(offEvent.TStart),
					OnStep: onEvent.Magnitude, OffStep: f64p(offEvent.Magnitude),
					DurationS: f64p(dur), PowerEstimateW: f64p(power),
					FloorMaxDeficit: f64p(floorMaxDeficit), Accepted: false,
					RejectReason: ReasonFloorViolation}
				rejectedCycles = append(rejectedCycles, c)
				stats.CyclesRejectedFloor++
			}

			state = "OFF"
			i = bestI + 1
			continue
		}

		// Neither on nor off band: irrelevant while ON.
		i++
	}

	if state == "ON" {
		dur := seriesEndT - onEvent.TEnd
		c := Cycle{OnTime: onEvent.TEnd, OffTime: f64p(seriesEndT), OnStep: onEvent.Magnitude,
			DurationS: f64p(dur), Accepted: false, RejectReason: ReasonStreamEnd}
		unmatchedCycles = append(unmatchedCycles, c)
		stats.CyclesUnmatchedTimeout++
	}

	return Result{Cycles: cycles, RejectedCycles: rejectedCycles, UnmatchedCycles: unmatchedCycles,
		RejectedSteps: rejectedSteps, Stats: stats}
}

// MakeFloorLookup is a port of detect.py's _make_floor_lookup.
func MakeFloorLookup(times []float64, residual []float64) FloorLookup {
	return func(loT, hiT, floor, dipMaxS float64) (bool, float64, bool) {
		lo := bisectLeft(times, loT)
		hi := bisectRight(times, hiT)
		segT := times[lo:hi]
		segR := residual[lo:hi]
		if len(segT) == 0 {
			return true, 0.0, false
		}
		maxDeficit := floor - segR[0]
		maxRun := 0.0
		anyDip := false
		runStartIdx := -1
		for k := range segT {
			deficit := floor - segR[k]
			if deficit > maxDeficit {
				maxDeficit = deficit
			}
			if deficit > 0 {
				anyDip = true
				if runStartIdx == -1 {
					runStartIdx = k
				}
			} else {
				if runStartIdx != -1 {
					run := segT[k-1] - segT[runStartIdx]
					if run > maxRun {
						maxRun = run
					}
					runStartIdx = -1
				}
			}
		}
		if runStartIdx != -1 {
			run := segT[len(segT)-1] - segT[runStartIdx]
			if run > maxRun {
				maxRun = run
			}
		}
		floorOK := maxRun <= dipMaxS
		return floorOK, maxDeficit, anyDip && floorOK
	}
}

// ---------------------------------------------------------------------------
// Simple API: list of Sample(t, residual, loads-map). Port of detect.py's
// _make_sample_reject_check / detect_cycles / build_residual_samples.
// ---------------------------------------------------------------------------

func MakeSampleRejectCheck(samples []Sample, times []float64, windowS, maxDelta float64) RejectCheck {
	return func(tStart, tEnd float64, _ string) bool {
		lo := bisectLeft(times, tStart-windowS)
		hi := bisectRight(times, tEnd+windowS)
		if hi-lo <= 1 {
			return true
		}
		window := samples[lo:hi]
		keys := map[string]bool{}
		for _, s := range window {
			for k := range s.Loads {
				keys[k] = true
			}
		}
		for k := range keys {
			var vals []float64
			for _, s := range window {
				if v, ok := s.Loads[k]; ok {
					vals = append(vals, v)
				}
			}
			if len(vals) < 2 {
				continue
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
			if mx-mn > maxDelta {
				return false
			}
		}
		return true
	}
}

// DetectCycles is a port of detect.py's detect_cycles.
func DetectCycles(samples []Sample, p Params) Result {
	times := make([]float64, len(samples))
	residual := make([]float64, len(samples))
	for i, s := range samples {
		times[i] = s.T
		residual[i] = s.Residual
	}
	events := FindMergedEvents(times, residual, p)
	gapMarkers := FindGapMarkers(times, p.MaxGapS)
	rejectCheck := MakeSampleRejectCheck(samples, times, p.RejectWindowS, p.RejectLoadDelta)
	floorLookup := MakeFloorLookup(times, residual)
	seriesEndT := 0.0
	if len(times) > 0 {
		seriesEndT = times[len(times)-1]
	}
	return PairEvents(events, floorLookup, gapMarkers, seriesEndT, rejectCheck, p)
}

// Point is a (t, value) pair - detect.py's bare (t, watts) tuples.
type Point struct {
	T float64
	V float64
}

// BuildResidualSamples is a port of detect.py's build_residual_samples.
func BuildResidualSamples(grid []Point, loads map[string][]Point) []Sample {
	names := make([]string, 0, len(loads))
	for name := range loads {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic iteration only; result doesn't depend on order
	loadIdx := map[string]int{}
	loadCur := map[string]float64{}
	for _, name := range names {
		loadIdx[name] = 0
		loadCur[name] = 0.0
	}
	out := make([]Sample, 0, len(grid))
	for _, g := range grid {
		for _, name := range names {
			series := loads[name]
			idx := loadIdx[name]
			for idx < len(series) && series[idx].T <= g.T {
				loadCur[name] = series[idx].V
				idx++
			}
			loadIdx[name] = idx
		}
		knownSum := 0.0
		for _, name := range names {
			knownSum += loadCur[name]
		}
		snapshot := make(map[string]float64, len(names))
		for _, name := range names {
			snapshot[name] = loadCur[name]
		}
		out = append(out, Sample{T: g.T, Residual: g.V - knownSum, Loads: snapshot})
	}
	return out
}

// LoadSeries is one known load's own raw (sparse) series, NOT expanded/LOCF'd
// to the grid timeline - detect.py's (raw_times, raw_values) tuple.
type LoadSeries struct {
	Times  []float64
	Values []float64
}

// MakeArrayRejectCheck is a port of detect.py's _make_array_reject_check.
func MakeArrayRejectCheck(loadSeries map[string]LoadSeries, windowS, maxDelta float64) RejectCheck {
	return func(tStart, tEnd float64, _ string) bool {
		loT := tStart - windowS
		hiT := tEnd + windowS
		for _, ls := range loadSeries {
			if len(ls.Times) == 0 {
				continue
			}
			lo := bisectLeft(ls.Times, loT)
			hi := bisectRight(ls.Times, hiT)
			var vals []float64
			if lo > 0 {
				vals = append(vals, ls.Values[lo-1]) // LOCF value held entering the window
			}
			vals = append(vals, ls.Values[lo:hi]...)
			if len(vals) >= 2 {
				mn, mx := vals[0], vals[0]
				for _, v := range vals {
					if v < mn {
						mn = v
					}
					if v > mx {
						mx = v
					}
				}
				if mx-mn > maxDelta {
					return false
				}
			}
		}
		return true
	}
}

// DetectCyclesArrays is a port of detect.py's detect_cycles_arrays - the
// large-dataset path, used by backfill.
func DetectCyclesArrays(times []float64, residual []float64, loadSeries map[string]LoadSeries, p Params) Result {
	events := FindMergedEvents(times, residual, p)
	gapMarkers := FindGapMarkers(times, p.MaxGapS)
	rejectCheck := MakeArrayRejectCheck(loadSeries, p.RejectWindowS, p.RejectLoadDelta)
	floorLookup := MakeFloorLookup(times, residual)
	seriesEndT := 0.0
	if len(times) > 0 {
		seriesEndT = times[len(times)-1]
	}
	return PairEvents(events, floorLookup, gapMarkers, seriesEndT, rejectCheck, p)
}
