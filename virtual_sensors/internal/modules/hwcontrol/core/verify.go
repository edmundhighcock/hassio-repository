package core

import (
	"math"
	"sort"
	"time"
)

// point is one change of a held series.
type point struct {
	T time.Time
	V float64
}

// heldSeries is a change-only series held between changes (§4.0). It
// keeps changes back to keep, plus the last change before that so the
// value at the window start is known.
type heldSeries struct {
	pts  []point
	keep time.Duration
}

func (s *heldSeries) push(t time.Time, v float64) {
	if n := len(s.pts); n > 0 && t.Before(s.pts[n-1].T) {
		return // out of order: ignore (inputs are time-ordered)
	}
	s.pts = append(s.pts, point{t, v})
	cut := t.Add(-s.keep)
	i := 0
	for i+1 < len(s.pts) && !s.pts[i+1].T.After(cut) {
		i++
	}
	if i > 0 {
		s.pts = append(s.pts[:0], s.pts[i:]...)
	}
}

func (s *heldSeries) last() (point, bool) {
	if len(s.pts) == 0 {
		return point{}, false
	}
	return s.pts[len(s.pts)-1], true
}

// segments returns (value, duration) pieces of the held series over
// [t-w, t]. Before the first known point there is no value.
func (s *heldSeries) segments(t time.Time, w time.Duration) [][2]float64 {
	lo := t.Add(-w)
	var segs [][2]float64
	for i, p := range s.pts {
		if p.T.After(t) {
			break
		}
		start := p.T
		if start.Before(lo) {
			start = lo
		}
		end := t
		if i+1 < len(s.pts) && s.pts[i+1].T.Before(t) {
			end = s.pts[i+1].T
		}
		if end.After(start) {
			segs = append(segs, [2]float64{p.V, end.Sub(start).Seconds()})
		} else if end.Equal(start) && i == len(s.pts)-1 && !p.T.Before(lo) {
			segs = append(segs, [2]float64{p.V, 0})
		}
	}
	return segs
}

// median is the time-weighted median over [t-w, t] (§4.0: statistics are
// time-weighted over the held series). ok=false if no value is known.
func (s *heldSeries) median(t time.Time, w time.Duration) (float64, bool) {
	segs := s.segments(t, w)
	if len(segs) == 0 {
		return 0, false
	}
	total := 0.0
	for _, sg := range segs {
		total += sg[1]
	}
	if total == 0 {
		return segs[len(segs)-1][0], true
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i][0] < segs[j][0] })
	acc := 0.0
	for _, sg := range segs {
		acc += sg[1]
		if acc >= total/2 {
			return sg[0], true
		}
	}
	return segs[len(segs)-1][0], true
}

// minMax over [t-w, t] of the held series.
func (s *heldSeries) minMax(t time.Time, w time.Duration) (float64, float64, bool) {
	segs := s.segments(t, w)
	if len(segs) == 0 {
		return 0, 0, false
	}
	mn, mx := math.Inf(1), math.Inf(-1)
	for _, sg := range segs {
		mn = math.Min(mn, sg[0])
		mx = math.Max(mx, sg[0])
	}
	return mn, mx, true
}

// stepTracker is the controller's own fast step tracker over the residual
// (§5.3): "drawing" is an up-step >= DrawingW (merged within 30 s, or
// staged within 450 s), not yet followed by a down-step >= ConfirmW, with
// the level still >= base + step - DrawingLevelTol. It is fed the
// discontinuity-corrected residual. It is NOT the detector's open flag.
type stepTracker struct {
	series  heldSeries
	drawing bool
	base    float64
	step    float64
	upAt    time.Time
}

func (k *stepTracker) feed(cfg *Config, t time.Time, v float64) {
	// Window statistics BEFORE this sample enters the series.
	if k.drawing {
		_, mx, ok := k.series.minMax(t, cfg.MergeWindow)
		if (ok && v-mx <= -cfg.ConfirmW) || v < k.base+k.step-cfg.DrawingLevelTol {
			k.drawing = false
		}
	}
	if !k.drawing {
		if mn, _, ok := k.series.minMax(t, cfg.StagedWindow); ok && v-mn >= cfg.DrawingW {
			k.drawing, k.base, k.step, k.upAt = true, mn, v-mn, t
		}
	}
	k.series.push(t, v)
}

// verification is one electrical check of a move (§5.3).
type verification struct {
	on         bool
	baseAt     time.Time
	baseline   float64
	baselineOK bool
	baseEV     float64
	baseEVOK   bool
	baseSeq    int
	seqKnown   bool
	discSince  int     // discontinuity events received since baseAt
	discSum    float64 // sum of their DeltaW
	discAny    bool
	burst      bool
	evMoved    bool
	stale      bool
	seqGap     bool
	drawing    bool // OFF: heater drawing at baseline
	stop       time.Time
	started    bool
	aboveSince time.Time
	maxRise    float64
	maxDrop    float64
	discTimes  []discRec
}

type discRec struct {
	T      time.Time
	Entity string
	DeltaW float64
}

func (v *verification) inconclusive() bool {
	return !v.baselineOK || v.stale || v.evMoved || v.seqGap || v.burst
}

// verdict outcomes for verification.
const (
	vrPending = iota
	vrRise
	vrNoRise
	vrDrop
	vrNoDrop
	vrIdle
)
