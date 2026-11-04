package detect

import "testing"

// Every test here is a direct port of
// /home/edmundhighcock/Code/ham/services/hot-water-estimate/tests/test_detect.py
// - same scenario, same numbers, same assertions, so a human can diff the
// two side by side. Keep them that way.

func mk(t, residual float64, loads map[string]float64) Sample {
	if loads == nil {
		loads = map[string]float64{}
	}
	return Sample{T: t, Residual: residual, Loads: loads}
}

func TestClean3kwCycleAccepted(t *testing.T) {
	samples := []Sample{
		mk(0, 500, nil), mk(20, 500, nil), mk(40, 3500, nil), // on-step +3000
		mk(140, 3480, nil), mk(240, 3510, nil), mk(340, 520, nil), // off-step -2990
		mk(360, 500, nil),
	}
	result := DetectCycles(samples, DefaultParams())
	if len(result.Cycles) != 1 {
		t.Fatalf("len(Cycles) = %d, want 1", len(result.Cycles))
	}
	c := result.Cycles[0]
	if !c.Accepted {
		t.Fatalf("cycle not accepted")
	}
	if *c.PowerEstimateW < 2900 || *c.PowerEstimateW > 3100 {
		t.Fatalf("PowerEstimateW = %v, want [2900,3100]", *c.PowerEstimateW)
	}
	if c.OnTime != 40 {
		t.Fatalf("OnTime = %v, want 40", c.OnTime)
	}
	if *c.OffTime != 240 {
		t.Fatalf("OffTime = %v, want 240", *c.OffTime)
	}
	if result.Stats.CyclesAccepted != 1 {
		t.Fatalf("stats.CyclesAccepted = %d, want 1", result.Stats.CyclesAccepted)
	}
	if len(result.RejectedCycles) != 0 || len(result.UnmatchedCycles) != 0 {
		t.Fatalf("expected no rejected/unmatched cycles")
	}
}

func TestStepCoincidentWithEVStepRejected(t *testing.T) {
	samples := []Sample{
		mk(0, 500, map[string]float64{"ev": 0}),
		mk(20, 500, map[string]float64{"ev": 0}),
		mk(40, 3500, map[string]float64{"ev": 1900}), // EV also changed a lot right at this step
		mk(140, 3480, map[string]float64{"ev": 2000}),
		mk(240, 3510, map[string]float64{"ev": 2000}),
		mk(340, 520, map[string]float64{"ev": 2000}),
		mk(360, 500, map[string]float64{"ev": 2000}),
	}
	result := DetectCycles(samples, DefaultParams())
	if len(result.Cycles) != 0 {
		t.Fatalf("len(Cycles) = %d, want 0", len(result.Cycles))
	}
	if result.Stats.StepsRejectedCoincidentLoad < 1 {
		t.Fatalf("StepsRejectedCoincidentLoad = %d, want >= 1", result.Stats.StepsRejectedCoincidentLoad)
	}
	foundOn := false
	for _, s := range result.RejectedSteps {
		if s.Direction == "on" {
			foundOn = true
		}
	}
	if !foundOn {
		t.Fatalf("expected a rejected 'on' step")
	}
}

func TestUnmatchedOnStepTimesOut(t *testing.T) {
	params := DefaultParams()
	params.MaxOnDurationS = 3600.0
	samples := []Sample{mk(0, 500, nil), mk(20, 500, nil), mk(40, 3500, nil)}
	for tt := 40.0; tt < 5000; tt += 100 {
		samples = append(samples, mk(tt+100, 3500, nil))
	}
	result := DetectCycles(samples, params)
	if len(result.Cycles) != 0 {
		t.Fatalf("len(Cycles) = %d, want 0", len(result.Cycles))
	}
	if len(result.UnmatchedCycles) != 1 {
		t.Fatalf("len(UnmatchedCycles) = %d, want 1", len(result.UnmatchedCycles))
	}
	reason := result.UnmatchedCycles[0].RejectReason
	if reason != ReasonTimeout && reason != ReasonStreamEnd {
		t.Fatalf("RejectReason = %q, want timeout or stream_end", reason)
	}
	if result.Stats.CyclesUnmatchedTimeout != 1 {
		t.Fatalf("CyclesUnmatchedTimeout = %d, want 1", result.Stats.CyclesUnmatchedTimeout)
	}
}

func TestVariableLoadDipBelowFloorRejected(t *testing.T) {
	samples := []Sample{
		mk(0, 500, nil), mk(20, 500, nil), mk(40, 3500, nil), // +3000 on-step
		mk(60, 3400, nil),  // -100
		mk(80, 2900, nil),  // -500 (still above floor 2400)
		mk(100, 2300, nil), // below floor, dip run starts
		mk(160, 1900, nil), // below floor
		mk(220, 2100, nil), // below floor
		mk(280, 2300, nil), // below floor, dip run ends (span 100->280 = 180s)
		mk(340, 2900, nil), // back above floor
		mk(400, 3450, nil),
		mk(420, 505, nil), // -2945 off-step, magnitude matches on-step (3000)
	}
	result := DetectCycles(samples, DefaultParams())
	if len(result.Cycles) != 0 {
		t.Fatalf("len(Cycles) = %d, want 0", len(result.Cycles))
	}
	if len(result.RejectedCycles) != 1 {
		t.Fatalf("len(RejectedCycles) = %d, want 1", len(result.RejectedCycles))
	}
	if result.RejectedCycles[0].RejectReason != ReasonFloorViolation {
		t.Fatalf("RejectReason = %q, want floor_violation", result.RejectedCycles[0].RejectReason)
	}
	if result.Stats.CyclesRejectedFloor != 1 {
		t.Fatalf("CyclesRejectedFloor = %d, want 1", result.Stats.CyclesRejectedFloor)
	}
}

func TestIsolatedSingleSampleDipIsTolerated(t *testing.T) {
	samples := []Sample{
		mk(0, 500, nil), mk(20, 500, nil), mk(40, 3500, nil), mk(60, 3480, nil),
		mk(80, 2300, nil), // one isolated sample below the floor (2400) by skew
		mk(100, 3490, nil), mk(120, 520, nil), mk(140, 500, nil),
	}
	params := DefaultParams()
	params.FloorTol = 600.0
	result := DetectCycles(samples, params)
	if len(result.Cycles) != 1 {
		t.Fatalf("len(Cycles) = %d, want 1", len(result.Cycles))
	}
	if !result.Cycles[0].Accepted {
		t.Fatalf("cycle not accepted")
	}
	if !result.Cycles[0].FloorDipTolerated {
		t.Fatalf("FloorDipTolerated = false, want true")
	}
	if result.Stats.CyclesAcceptedWithToleratedDip != 1 {
		t.Fatalf("CyclesAcceptedWithToleratedDip = %d, want 1", result.Stats.CyclesAcceptedWithToleratedDip)
	}
}

func TestSplitStepIsMergedIntoOneEvent(t *testing.T) {
	samples := []Sample{
		mk(0, 900, nil), mk(20, 900, nil),
		mk(40, 920, nil),  // +20, baseline wobble
		mk(45, 2070, nil), // +1150 (seed, below the 2500-3300 band alone)
		mk(50, 3940, nil), // +1870 more, 5s later -> merges to +3020 (in band)
		mk(150, 4000, nil), mk(250, 4000, nil),
		mk(300, 3880, nil), // -120, baseline wobble
		mk(305, 2620, nil), // -1260 (seed)
		mk(310, 830, nil),  // -1790 more, 5s later -> merges to -3050 (in band)
		mk(400, 800, nil),  // -30, back to baseline
	}
	result := DetectCycles(samples, DefaultParams())
	if len(result.Cycles) != 1 {
		t.Fatalf("len(Cycles) = %d, want 1", len(result.Cycles))
	}
	c := result.Cycles[0]
	if !c.Accepted {
		t.Fatalf("cycle not accepted")
	}
	if c.OnStep < 2900 || c.OnStep > 3100 {
		t.Fatalf("OnStep = %v, want [2900,3100]", c.OnStep)
	}
	if *c.OffStep < -3100 || *c.OffStep > -2900 {
		t.Fatalf("OffStep = %v, want [-3100,-2900]", *c.OffStep)
	}
	if len(result.RejectedCycles) != 0 || len(result.UnmatchedCycles) != 0 {
		t.Fatalf("expected no rejected/unmatched cycles")
	}
}

func TestEarlierSpuriousOnStepIsSupersededByALaterRealOne(t *testing.T) {
	samples := []Sample{
		mk(0, 500, nil), mk(20, 500, nil),
		mk(40, 3000, nil), // false +2500 on-step (some unrelated load)
		mk(140, 2980, nil), mk(240, 3010, nil),
		mk(340, 5600, nil), // real +2590 on-step -- must re-anchor here
		mk(440, 5580, nil), mk(540, 5610, nil),
		mk(640, 2920, nil), // -2690 off-step, matches the SECOND on-step
		mk(740, 2900, nil),
	}
	result := DetectCycles(samples, DefaultParams())
	if len(result.Cycles) != 1 {
		t.Fatalf("len(Cycles) = %d, want 1", len(result.Cycles))
	}
	c := result.Cycles[0]
	if !c.Accepted {
		t.Fatalf("cycle not accepted")
	}
	if c.OnStep != 2590 {
		t.Fatalf("OnStep = %v, want 2590 (the later, real step)", c.OnStep)
	}
	if c.OnTime != 340 {
		t.Fatalf("OnTime = %v, want 340", c.OnTime)
	}
	found := false
	for _, s := range result.RejectedSteps {
		if s.Reason == ReasonSuperseded && s.Delta == 2500 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a superseded step with delta 2500")
	}
}

func TestPairingPrefersCloserMagnitudeMatchOverFirstCandidate(t *testing.T) {
	params := DefaultParams()
	params.PairTol = 100.0
	samples := []Sample{
		mk(0, 7000, nil), mk(20, 7000, nil),
		mk(40, 9600, nil), // +2600 on-step
		mk(140, 9580, nil), mk(240, 9610, nil),
		mk(340, 6310, nil), // -3300 off-candidate #1 (mismatch 700 > pair_tol)
		mk(440, 6290, nil), mk(540, 6300, nil),
		mk(640, 3700, nil), // -2600 off-candidate #2 (mismatch 0 <= pair_tol) -- preferred
		mk(740, 3680, nil),
	}
	result := DetectCycles(samples, params)
	if len(result.Cycles) != 1 {
		t.Fatalf("len(Cycles) = %d, want 1", len(result.Cycles))
	}
	c := result.Cycles[0]
	if !c.Accepted {
		t.Fatalf("cycle not accepted")
	}
	if *c.OffStep != -2600 {
		t.Fatalf("OffStep = %v, want -2600", *c.OffStep)
	}
	if *c.OffTime != 540 {
		t.Fatalf("OffTime = %v, want 540", *c.OffTime)
	}
}

func TestArrayAPIMatchesSimpleAPI(t *testing.T) {
	grid := []Point{{0, 500}, {20, 500}, {40, 3500}, {140, 3480}, {240, 3510}, {340, 520}, {360, 500}}
	ev := []Point{{0, 0}, {500, 0}} // EV present but never changes
	samples := BuildResidualSamples(grid, map[string][]Point{"ev": ev})
	params := DefaultParams()
	simpleResult := DetectCycles(samples, params)

	times := make([]float64, len(grid))
	for i, g := range grid {
		times[i] = g.T
	}
	residual := make([]float64, len(samples))
	for i, s := range samples {
		residual[i] = s.Residual
	}
	evT := make([]float64, len(ev))
	evV := make([]float64, len(ev))
	for i, p := range ev {
		evT[i] = p.T
		evV[i] = p.V
	}
	arrResult := DetectCyclesArrays(times, residual, map[string]LoadSeries{"ev": {Times: evT, Values: evV}}, params)

	if len(arrResult.Cycles) != 1 || len(simpleResult.Cycles) != 1 {
		t.Fatalf("len(arrResult.Cycles)=%d len(simpleResult.Cycles)=%d, want 1 each",
			len(arrResult.Cycles), len(simpleResult.Cycles))
	}
	if arrResult.Cycles[0].OnTime != simpleResult.Cycles[0].OnTime {
		t.Fatalf("OnTime mismatch: %v vs %v", arrResult.Cycles[0].OnTime, simpleResult.Cycles[0].OnTime)
	}
	if *arrResult.Cycles[0].OffTime != *simpleResult.Cycles[0].OffTime {
		t.Fatalf("OffTime mismatch: %v vs %v", *arrResult.Cycles[0].OffTime, *simpleResult.Cycles[0].OffTime)
	}
	if *arrResult.Cycles[0].PowerEstimateW != *simpleResult.Cycles[0].PowerEstimateW {
		t.Fatalf("PowerEstimateW mismatch: %v vs %v", *arrResult.Cycles[0].PowerEstimateW, *simpleResult.Cycles[0].PowerEstimateW)
	}
}

func TestBuildResidualSamplesLOCFAndMissingAsZero(t *testing.T) {
	grid := []Point{{0, 1000}, {10, 1000}, {20, 4000}}
	loads := map[string][]Point{"ev": {{5, 500}}} // missing before t=5 -> treated as 0
	samples := BuildResidualSamples(grid, loads)
	if samples[0].Residual != 1000 {
		t.Fatalf("samples[0].Residual = %v, want 1000", samples[0].Residual)
	}
	if samples[1].Residual != 500 {
		t.Fatalf("samples[1].Residual = %v, want 500", samples[1].Residual)
	}
	if samples[2].Residual != 3500 {
		t.Fatalf("samples[2].Residual = %v, want 3500", samples[2].Residual)
	}
}
