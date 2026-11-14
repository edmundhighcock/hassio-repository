package core

import (
	"testing"
	"time"
)

func gatePubs(r *rig) (n int, last Effect) {
	for _, e := range r.effects {
		if e.Kind == EffPublish && e.ObjectID == ObjOffGate {
			n++
			last = e
		}
	}
	return
}

func TestOffGateOutsideThenPlanPeriod(t *testing.T) {
	r := readyForOff(t)
	r.advance(time.Minute)
	if g := r.c.OffGate(); g != "outside_plan_period" {
		t.Fatalf("gate %q", g)
	}
	e, _ := r.lastPublished(ObjOffGate)
	if e.State != "outside_plan_period" || e.Attrs["plan_off_now"] != false {
		t.Fatalf("published %+v", e)
	}
	// Block the OFF with the deliberate gap, then inside the period.
	r.c.P.Moves = append(r.c.P.Moves, r.now.Add(18*time.Minute))
	r.advance(16 * time.Minute)
	e, _ = r.lastPublished(ObjOffGate)
	if e.State != "deliberate_gap" || e.Attrs["plan_off_now"] != true {
		t.Fatalf("want deliberate_gap with plan_off_now, got %+v", e)
	}
	r.c.P.Moves = nil
	if g := r.c.OffGate(); g != "plan_period" {
		t.Fatalf("gate %q", g)
	}
}

func TestOffGateIdleOffAndDedupe(t *testing.T) {
	r := readyForOff(t)
	r.advance(20 * time.Minute) // OFF commanded at the period start
	for i := 0; i < 40 && r.c.P.State != StIdleOff; i++ {
		r.advance(10 * time.Second)
	}
	if r.c.P.State != StIdleOff {
		t.Fatalf("state %s", r.state())
	}
	if g := r.c.OffGate(); g != "off" {
		t.Fatalf("gate %q", g)
	}
	r.advance(time.Minute)
	n0, e := gatePubs(r)
	if e.State != "off" {
		t.Fatalf("last published %q", e.State)
	}
	r.advance(14*time.Minute - r.now.Sub(r.c.offGateAt)) // unchanged: no republish
	if n1, _ := gatePubs(r); n1 != n0 {
		t.Fatalf("republished while unchanged: %d -> %d", n0, n1)
	}
	r.advance(2 * time.Minute) // past the 15 min refresh
	if n2, _ := gatePubs(r); n2 != n0+1 {
		t.Fatalf("want one refresh after 15 min, got %d -> %d", n0, n2)
	}
}

func TestOffGateDegraded(t *testing.T) {
	r := readyForOff(t)
	r.step(Link{At: r.now, Up: false})
	r.advance(10 * time.Second)
	if r.c.P.State != StDegraded {
		t.Fatalf("state %s", r.state())
	}
	g := r.c.OffGate()
	if len(g) < 10 || g[:9] != "degraded:" {
		t.Fatalf("gate %q", g)
	}
	if e, _ := r.lastPublished(ObjOffGate); e.State != g {
		t.Fatalf("published %q want %q", e.State, g)
	}
}

func TestOffGateBoot(t *testing.T) {
	r := newRig(t, liveCfg(), nil)
	r.step(Tick{At: r.now})
	if g := r.c.OffGate(); g != "boot" {
		t.Fatalf("gate %q", g)
	}
}
