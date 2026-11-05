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
	// The chain-based merge (see FindMergedEvents) also folds the adjacent
	// same-sign baseline wobbles (+20 before the on-step, -120 and -30
	// around the off-step) into their respective chains, so the recovered
	// magnitudes run a little wider than the step itself.
	if c.OnStep < 2900 || c.OnStep > 3100 {
		t.Fatalf("OnStep = %v, want [2900,3100]", c.OnStep)
	}
	if *c.OffStep < -3300 || *c.OffStep > -2900 {
		t.Fatalf("OffStep = %v, want [-3300,-2900]", *c.OffStep)
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
	// 2620, not 2590: the chain-based merge also folds the +30 same-sign
	// wobble just before the real step into its chain. Still clearly the
	// later, real step, not the false 2500 one.
	if c.OnStep != 2620 {
		t.Fatalf("OnStep = %v, want 2620 (the later, real step)", c.OnStep)
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

// TestStagedReheatTwoSubstepsMinutesApartIsDetected is modelled directly on
// the owner-confirmed, live-missed staged reheat, 2026-11-05 ~19:47-21:27
// UTC: raw grid_meter_power rose 380 -> 800 -> 2460 W (19:47:05-19:49:05,
// net +2080, chained from two deltas of +420 and +1660 -- the first alone
// is below SeedDeltaMin) then wobbled on a plateau for ~3.5 minutes, then
// rose again 2510 -> 3530 W over 19:52:51-19:53:31 (net ~+990 over the
// first MergeWindowS of that rise -- six deltas none of which individually
// reaches SeedDeltaMin: +40,+160,+680,+90,+20,+20,+10). Neither sub-step
// alone is a 2.5-3.3kW step; FindStagedEvents must bridge them into one
// ~3070W on-event.
func TestStagedReheatTwoSubstepsMinutesApartIsDetected(t *testing.T) {
	samples := []Sample{
		mk(0, 380, nil),
		mk(115, 800, nil),  // +420 (sub-seed on its own)
		mk(120, 2460, nil), // +1660 -- chain totals +2080
		mk(145, 2500, nil), mk(150, 2530, nil), mk(155, 2570, nil), mk(160, 2590, nil),
		mk(165, 2560, nil), mk(170, 2550, nil), mk(175, 2600, nil), mk(180, 2590, nil),
		mk(185, 2580, nil), mk(190, 2550, nil), mk(195, 2560, nil), mk(200, 2590, nil),
		mk(205, 2560, nil), mk(210, 2550, nil), mk(215, 2560, nil), mk(220, 2570, nil),
		mk(225, 2530, nil), mk(230, 2560, nil), mk(235, 2540, nil), mk(240, 2520, nil),
		mk(245, 2560, nil), mk(250, 2520, nil), mk(255, 2550, nil), mk(260, 2510, nil),
		mk(266, 2550, nil), // +40
		mk(271, 2710, nil), // +160
		mk(276, 3390, nil), // +680 -- none of these individually >= SeedDeltaMin
		mk(281, 3480, nil), mk(286, 3500, nil), mk(291, 3520, nil), mk(296, 3530, nil),
		mk(356, 3530, nil), // flat hold
		mk(416, 3530, nil), // flat hold
		mk(421, 380, nil),  // off-step: clean single drop back to baseline
		mk(426, 380, nil),
	}
	result := DetectCycles(samples, DefaultParams())
	if len(result.Cycles) != 1 {
		t.Fatalf("len(Cycles) = %d, want 1 (rejected=%v unmatched=%v)", len(result.Cycles), result.RejectedCycles, result.UnmatchedCycles)
	}
	c := result.Cycles[0]
	if !c.Accepted {
		t.Fatalf("cycle not accepted")
	}
	if c.OnStep < 2500 || c.OnStep > 3300 {
		t.Fatalf("OnStep = %v, want [2500,3300]", c.OnStep)
	}
	if c.OnTime != 286 {
		t.Fatalf("OnTime = %v, want 286 (t_end of the combined staged on-event)", c.OnTime)
	}
	if len(result.RejectedCycles) != 0 || len(result.UnmatchedCycles) != 0 {
		t.Fatalf("expected no rejected/unmatched cycles")
	}
}

// TestStagedSubstepsTooFarApartAreNotBridged checks that two sub-threshold
// same-sign events that would sum into the on/off band, but are more than
// StageWindowS apart, are NOT merged -- a "level shift" has to be reached
// promptly, not accumulate from two coincidentally-timed but unrelated
// events many minutes apart.
func TestStagedSubstepsTooFarApartAreNotBridged(t *testing.T) {
	params := DefaultParams()
	samples := []Sample{
		mk(0, 500, nil),
		mk(20, 500, nil),
		mk(40, 2200, nil), // +1700 sub-threshold on its own
		mk(140, 2200, nil),
		mk(140+params.StageWindowS+60, 4200, nil), // +2000, well beyond the window
		mk(140+params.StageWindowS+160, 4200, nil),
	}
	result := DetectCycles(samples, params)
	if len(result.Cycles) != 0 {
		t.Fatalf("len(Cycles) = %d, want 0", len(result.Cycles))
	}
	if len(result.UnmatchedCycles) != 0 {
		t.Fatalf("len(UnmatchedCycles) = %d, want 0 (neither event qualifies on its own)", len(result.UnmatchedCycles))
	}
}

// TestStagedSubstepsWithUnstablePlateauAreNotBridged checks that two
// sub-threshold same-sign merged events close enough in time to bridge (and
// which would sum into the on-band, 1700+920=2620), but with an intervening
// plateau that DRIFTS by more than PlateauTol (600W, e.g. a different,
// unmetered load's own gradual activity, not mere noise) are NOT bridged.
// The drift itself stays below SeedDeltaMin (so it is correctly invisible
// as its own event, same as ordinary wobble) but still fails the
// plateau-stability check.
func TestStagedSubstepsWithUnstablePlateauAreNotBridged(t *testing.T) {
	samples := []Sample{
		mk(0, 500, nil),
		mk(20, 500, nil),
		mk(40, 2200, nil),  // +1700 (chain start)
		mk(60, 2190, nil),  // -10 reversal -> flushes the +1700 event
		mk(80, 2310, nil),  // +120 (new chain)
		mk(100, 2430, nil), // +120
		mk(120, 2550, nil), // +120
		mk(140, 2670, nil), // +120
		mk(160, 2790, nil), // +120 -- drift totals +600 (> PlateauTol, < SeedDeltaMin)
		mk(180, 2780, nil), // -10 reversal -> flushes the +600 drift (discarded, sub-seed)
		mk(200, 3700, nil), // +920 (new chain)
		mk(220, 3700, nil), // flat -> flushes the +920 event
	}
	result := DetectCycles(samples, DefaultParams())
	if len(result.Cycles) != 0 {
		t.Fatalf("len(Cycles) = %d, want 0", len(result.Cycles))
	}
	if len(result.RejectedCycles) != 0 || len(result.UnmatchedCycles) != 0 {
		t.Fatalf("expected no rejected/unmatched cycles")
	}
}

// TestBriefKettleLikeBlipIsNotAPhantomCycle is modelled on a blip found
// empirically while validating the chain-based merge (2025-10-04
// 07:57:10-07:57:40, legacy bucket): a ~20s kettle/toaster-like blip whose
// single biggest raw delta (+2434W) alone stays BELOW StepLo, but which the
// chain absorbs two tiny same-sign wobbles (+96W before, +45W after) into,
// tipping the combined magnitude to +2575W -- just inside the on-band --
// paired with an equally brief reversal immediately after. MinOnDurationS
// exists specifically to reject this.
func TestBriefKettleLikeBlipIsNotAPhantomCycle(t *testing.T) {
	samples := []Sample{
		mk(0, -740.25, nil),
		mk(10, -644.25, nil), // +96 (wobble)
		mk(15, 1789.75, nil), // +2434 (the blip itself, still < StepLo alone)
		mk(20, 1834.75, nil), // +45 (wobble) -- chain totals +2575, in-band
		mk(25, 958.75, nil),  // -876 (reversal)
		mk(30, -673.25, nil), // -1632 -- chain totals -2532, in-band
		mk(45, -681.25, nil),
	}
	result := DetectCycles(samples, DefaultParams())
	if len(result.Cycles) != 0 {
		t.Fatalf("len(Cycles) = %d, want 0", len(result.Cycles))
	}
	if len(result.RejectedCycles) != 1 {
		t.Fatalf("len(RejectedCycles) = %d, want 1", len(result.RejectedCycles))
	}
	if result.RejectedCycles[0].RejectReason != ReasonTooShort {
		t.Fatalf("RejectReason = %q, want too_short", result.RejectedCycles[0].RejectReason)
	}
}
