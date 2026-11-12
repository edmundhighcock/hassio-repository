package hpcontrol

import (
	"testing"
	"time"
)

// 0.6.1: the minimum applies to each comfort sensor, not their mean.
// 2026-11-10: the bedroom reached 18.77 C below the 19.0 minimum while the
// kitchen+bedroom mean stayed above it.

func TestPlannerFloorBindsOnColdestSensor(t *testing.T) {
	p := testParams()
	// A house that cools to the floor and cannot be heated cheaply: compare
	// the predicted minimum of the mean with and without the cold offset.
	base := synth()
	base.T0 = 19.6
	r0 := run(t, p, base)
	cold := base
	cold.ColdOffset = -0.3
	r1 := run(t, p, cold)
	if r1.MinT < base.Min+0.3-0.06 {
		t.Fatalf("mean fell to %.2f: the coldest sensor (mean-0.3) would be below %.1f", r1.MinT, base.Min)
	}
	if r1.KWh < r0.KWh {
		t.Fatalf("a colder coldest sensor planned less heating: %.2f vs %.2f kWh", r1.KWh, r0.KWh)
	}
	// Positive offsets are ignored (the floor never loosens).
	pos := base
	pos.ColdOffset = 0.5
	if r2 := run(t, p, pos); r2.KWh != r0.KWh {
		t.Fatalf("positive offset changed the plan")
	}
}

func TestFloorAtRiskUsesColdestSensor(t *testing.T) {
	p := testParams()
	p.Model = Model{U: 0.08, Boost5: 0.2, HoldFrac: 0.4, Gain: 0, BedroomOffset: -0.3}
	in := synth()
	in.T0 = 19.2
	in.ColdOffset = -0.3
	r := Plan(in, p)
	if !r.FloorAtRisk {
		t.Fatalf("mean %.2f with coldest sensor 0.3 lower must be at risk against %.1f", r.MinT, in.Min)
	}
}

func TestModuleColdOffsetFromBedroomModelAndLive(t *testing.T) {
	m := New()
	_ = m.Init(nil)
	now := at(10, 12, 0)
	feed(m, now, kitchenEnt, 20.0)
	feed(m, now, bedroomEnt, 19.2)
	mean, _ := m.comfortNow(now)
	if off := m.coldOffset(now, mean); off > -0.4+1e-9 { // live -0.4 beats the model's -0.3
		t.Fatalf("cold offset %.2f", off)
	}
	// Equal sensors: the model's bedroom offset applies.
	feed(m, now, kitchenEnt, 19.5)
	feed(m, now, bedroomEnt, 19.5)
	mean, _ = m.comfortNow(now)
	if off := m.coldOffset(now, mean); off != -0.3 {
		t.Fatalf("cold offset %.2f, want -0.3", off)
	}
}

// Executor: mean above the minimum, bedroom below it -> heat at hold.
func TestBedroomBelowMinimumHeatsWhileMeanIsAbove(t *testing.T) {
	r := newRig(t, "live", true, `,"hp_model_boost_rate":0,"hp_model_gain":0`, at(10, 18, 0), 20)
	if r.hpState != "off" {
		t.Fatalf("setup: %s", r.hpState)
	}
	// kitchen 20.5, bedroom 18.8: mean 19.65 > 19.0, bedroom < 19.0.
	r.outs = append(r.outs, feed(r.m, r.now, kitchenEnt, 20.5)...)
	r.outs = append(r.outs, feed(r.m, r.now, bedroomEnt, 18.8)...)
	r.drain()
	if mean, _ := r.m.comfortNow(r.now); mean < 19 {
		t.Fatalf("mean %.2f", mean)
	}
	r.advance(2*time.Minute, false, 0)
	tg := r.m.target(r.now)
	if tg.Kind != "below_min" || tg.Level < Hold {
		t.Fatalf("target %+v", tg)
	}
	if r.hpState != "heat" {
		t.Fatalf("hp %s", r.hpState)
	}
	// Bedroom recovers: back to the plan (off under this model).
	r.outs = append(r.outs, feed(r.m, r.now, bedroomEnt, 20.0)...)
	r.drain()
	if k := r.m.target(r.now).Kind; k == "below_min" {
		t.Fatalf("still below_min")
	}
}
