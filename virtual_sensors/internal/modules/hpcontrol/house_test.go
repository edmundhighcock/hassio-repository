package hpcontrol

import (
	"testing"
	"time"

	"virtual_sensors/internal/house"
)

func hubSetup(h *house.Hub) func(*rig) {
	return func(r *rig) {
		r.m.hub = h
		r.hpState, r.hpSet = "heat", 29
	}
}

func TestPlannedPowerAccessor(t *testing.T) {
	h := house.NewHub()
	r := newRig(t, "shadow", false, "", at(10, 12, 0), 20, hubSetup(h))
	w := r.m.PlannedPowerW()
	if len(w) < 96 {
		t.Fatalf("plan not exposed: %d slots", len(w))
	}
	for k, v := range w {
		if v != 0 && v != r.m.p.PHoldW && v != r.m.p.PBoostW {
			t.Fatalf("slot %d power %v is not a level's power", k, v)
		}
	}
	// the registered provider carries it to the house planner
	r.m.Init(nil)
	if p, ok := h.HP(); !ok || !p.Valid || len(p.W) != len(w) || p.MaxW != r.m.p.PBoostW {
		t.Fatalf("hub plan %+v %v", p, ok)
	}
	// a copy: mutating it does not touch the module
	for k := range w {
		w[k] = -1
	}
	for _, v := range r.m.PlannedPowerW() {
		if v < 0 {
			t.Fatal("accessor must return a copy")
		}
	}
	// no plan: Valid=false, the planner then assumes the maximum
	m := New()
	m.hub = house.NewHub()
	if p := m.HousePlan(); p.Valid || m.PlannedPowerW() != nil {
		t.Fatal("no plan must be reported as invalid")
	}
}

func TestHouseCapDropsPumpShadowVersusLive(t *testing.T) {
	// Shadow: the would-command shows the cap, nothing is sent.
	h := house.NewHub()
	r := newRig(t, "shadow", true, "", at(10, 12, 0), 17.5, hubSetup(h)) // below the minimum: it would heat
	mk := r.mark()
	h.SetCaps(r.now, -1, house.HPOff)
	r.tick(30 * time.Second)
	if tg := r.m.target(r.now); tg.Level != Off || tg.Kind != "house_budget" {
		t.Fatalf("target %+v", tg)
	}
	if len(r.devCalls(mk)) != 0 {
		t.Fatalf("shadow acted: %v", describe(r.devCalls(mk)))
	}
	// Live and armed: off is sent.
	h2 := house.NewHub()
	r2 := newRig(t, "live", true, "", at(10, 12, 0), 17.5, hubSetup(h2))
	r2.advance(3*time.Minute, true, 17.5)
	h2.SetCaps(r2.now, -1, house.HPOff)
	mk = r2.mark()
	r2.tick(30 * time.Second)
	found := false
	for _, c := range r2.devCalls(mk) {
		if c.Service == "turn_off" {
			found = true
		}
	}
	if !found {
		t.Fatalf("live cap must turn the pump off: %s", describe(r2.devCalls(mk)))
	}
	// Hold cap: boost is lowered to hold, never raised.
	h3 := house.NewHub()
	r3 := newRig(t, "shadow", false, "", at(10, 12, 0), 20, hubSetup(h3))
	h3.SetCaps(r3.now, -1, house.HPHold)
	if tg := r3.m.target(r3.now); tg.Level > Hold {
		t.Fatalf("hold cap exceeded: %+v", tg)
	}
	// The cap lapses when the planner stops refreshing it.
	if tg := r3.m.target(r3.now.Add(5 * time.Minute)); tg.Kind == "house_budget" {
		t.Fatal("stale cap must lapse")
	}
}
