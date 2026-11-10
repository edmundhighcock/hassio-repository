package hwcontrol

import (
	"testing"
	"time"
)

// Before the controller has stepped there is no schedule: the house
// planner must see Valid=false and assume the heater is ON (worst case).
func TestHouseHeaterPlanInvalidBeforeFirstStep(t *testing.T) {
	m := New()
	if err := m.Init(nil); err != nil {
		t.Fatal(err)
	}
	p := m.HouseHeaterPlan()
	if p.Valid {
		t.Fatalf("%+v", p)
	}
	now := time.Date(2026, 11, 10, 22, 0, 0, 0, time.UTC)
	if !p.OnDuring(now, now.Add(15*time.Minute)) {
		t.Fatal("an unknown heater plan must count as ON")
	}
}
