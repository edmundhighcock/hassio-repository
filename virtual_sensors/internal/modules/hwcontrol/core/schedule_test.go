package core

import (
	"testing"
	"time"
)

func TestScheduleWindows(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	c := &Controller{now: now}
	s := c.Schedule()
	if s.Planned || len(s.Windows) != 1 || !s.Windows[0].On || s.Heater != HeaterUnknown {
		t.Fatalf("no plan: %+v", s)
	}
	c.plan = &Plan{ValidUntil: now.Add(12 * time.Hour), OffPeriods: []OffPeriod{
		{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		{Start: now.Add(3 * time.Hour), End: now.Add(26 * time.Hour)},
	}}
	s = c.Schedule()
	w := s.Windows
	if !s.Planned || len(w) != 3 {
		t.Fatalf("windows: %+v", w)
	}
	if w[0].On || !w[0].Start.Equal(now) || !w[0].End.Equal(now.Add(time.Hour)) ||
		!w[1].On || !w[1].End.Equal(now.Add(3*time.Hour)) ||
		w[2].On || !w[2].End.Equal(now.Add(ScheduleHorizon)) {
		t.Fatalf("windows: %+v", w)
	}
}
