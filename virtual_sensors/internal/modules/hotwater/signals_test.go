package hotwater

import (
	"reflect"
	"testing"
)

// TestSignalsMirrorTheCycle: with a sink set, a clean cycle emits residual
// samples, one MergedStep each way, CycleOpened and an accepted
// CycleClosed, and the module's own outputs are identical to a run with no
// sink (the hook is observation only).
func TestSignalsMirrorTheCycle(t *testing.T) {
	feed := func(m *Module) [][]any {
		var outs [][]any
		for _, s := range [][2]float64{{0, 500}, {20, 500}, {40, 3500}, {140, 3480}, {240, 3510}, {340, 520}, {440, 500}, {530, 500}, {620, 500}} {
			o := pushGrid(t, m, s[0], s[1])
			row := make([]any, len(o))
			for i := range o {
				row[i] = o[i]
			}
			outs = append(outs, row)
		}
		return outs
	}
	plain := newTestModule(t)
	withSink := newTestModule(t)
	var sigs []any
	withSink.SetSignalSink(func(s any) { sigs = append(sigs, s) })
	a, b := feed(plain), feed(withSink)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("outputs differ with a signal sink set")
	}

	var samples, steps, opened, closed int
	for _, s := range sigs {
		switch v := s.(type) {
		case ResidualSample:
			samples++
			if !v.Available {
				t.Fatalf("unexpected unavailable sample %+v", v)
			}
		case MergedStep:
			steps++
			if v.Rejected {
				t.Fatalf("clean step rejected %+v", v)
			}
		case CycleOpened:
			opened++
			if v.Magnitude != 3000 || v.OnTime.Unix() != 40 {
				t.Fatalf("CycleOpened %+v", v)
			}
		case CycleClosed:
			closed++
			if !v.Accepted || v.OffTime.Unix() != 240 || v.Reason != "dwell_confirmed" {
				t.Fatalf("CycleClosed %+v", v)
			}
		}
	}
	// 8, not 9: the module skips a sample at t <= processedUntil (0).
	if samples != 8 || steps != 2 || opened != 1 || closed != 1 {
		t.Fatalf("samples=%d steps=%d opened=%d closed=%d", samples, steps, opened, closed)
	}
}

func TestSignalsDiscontinuityAndUnavailable(t *testing.T) {
	m := newTestModule(t)
	var sigs []any
	m.SetSignalSink(func(s any) { sigs = append(sigs, s) })
	pushLoad(t, m, "fridge_power", 100, 80)
	pushGrid(t, m, 101, 500)
	pushLoadUnavailable(t, m, "fridge_power", 110)
	pushGridUnavailable(t, m, 120)
	var lost *Discontinuity
	var unavailable bool
	for _, s := range sigs {
		switch v := s.(type) {
		case Discontinuity:
			if v.Entity == "fridge_power" && v.Kind == "lost" {
				vv := v
				lost = &vv
			}
		case ResidualSample:
			if !v.Available {
				unavailable = true
			}
		}
	}
	if lost == nil || lost.DeltaW != 80 || lost.Seq == 0 {
		t.Fatalf("lost discontinuity %+v", lost)
	}
	if !unavailable {
		t.Fatal("no unavailable residual sample")
	}
}
