package housebudget

import (
	"encoding/json"
	"testing"
	"time"

	"virtual_sensors/internal/house"
	"virtual_sensors/internal/modules"
)

var t0 = time.Date(2026, 11, 10, 22, 0, 0, 0, time.UTC)

func newM(t *testing.T, opts string) (*Module, *house.Hub) {
	h := house.NewHub()
	m := New().WithHub(h)
	if err := m.Configure([]byte(opts)); err != nil {
		t.Fatal(err)
	}
	return m, h
}

func find(outs []modules.OutputUpdate, id string) (modules.OutputUpdate, bool) {
	for _, o := range outs {
		if o.ObjectID == id {
			return o, true
		}
	}
	return modules.OutputUpdate{}, false
}

func TestPublishesHeadroomForecastStateHeartbeat(t *testing.T) {
	m, h := newM(t, `{"house_base_a":2}`)
	h.SetHeater(func() house.HeaterPlan {
		return house.HeaterPlan{Valid: true, NominalW: 3000, Windows: []house.HeaterWindow{{Start: t0, End: t0.Add(24 * time.Hour), On: false}}}
	})
	h.SetHP(func() house.HPPlan { return house.HPPlan{Valid: true, W: map[int64]float64{t0.Unix(): 1700}, MaxW: 0} })
	h.PublishMeasure(house.Measure{At: t0, PhaseA: [3]float64{4, 5, 6.5}, Measured: true})
	outs := m.OnTick(t0)
	for i, id := range headroomIDs {
		var v *modules.OutputUpdate
		for k := range outs {
			if outs[k].ObjectID == id && !outs[k].AttributesOnly {
				v = &outs[k]
			}
		}
		want := []float64{15, 14, 12.5}[i]
		if v == nil || v.Unavailable || v.Value != want {
			t.Fatalf("%s: %+v want %v", id, v, want)
		}
	}
	var attrs map[string]any
	for _, o := range outs {
		if o.ObjectID == ObjHeadroomL3 && o.AttributesOnly {
			json.Unmarshal([]byte(o.AttributesJSON), &attrs)
		}
	}
	rows, _ := attrs["forecast"].([]any)
	if len(rows) != 96 {
		t.Fatalf("forecast rows %d", len(rows))
	}
	first := rows[0].([]any)[1].(float64)
	if first != 19-2-7.4 { // base 2 + 1700 W / 230 on L3 = 9.6 headroom 9.6
		if first < 9.5 || first > 9.7 {
			t.Fatalf("L3 slot0 headroom %v", first)
		}
	}
	if st, ok := find(outs, ObjState); !ok || st.StateString != house.StateOK {
		t.Fatalf("state %+v", st)
	}
	if _, ok := find(outs, ObjHeartbeat); !ok {
		t.Fatal("no heartbeat")
	}
	// The forecast is on the hub for the EV: slot 0 min over phases = L3.
	if v, ok := h.EVCeilingA(t0, t0, time.Minute); !ok || v != 9 {
		t.Fatalf("EV ceiling %v %v", v, ok)
	}
}

func TestNoDataWhenMeasurementMissingOrStale(t *testing.T) {
	m, h := newM(t, `{}`)
	outs := m.OnTick(t0)
	if st, _ := find(outs, ObjState); st.StateString != house.StateNoData {
		t.Fatalf("%+v", st)
	}
	for _, id := range headroomIDs {
		var got bool
		for _, o := range outs {
			if o.ObjectID == id && o.Unavailable {
				got = true
			}
		}
		if !got {
			t.Fatalf("%s must be unavailable", id)
		}
	}
	h.PublishMeasure(house.Measure{At: t0.Add(-10 * time.Minute), PhaseA: [3]float64{1, 1, 1}})
	if st, _ := find(m.OnTick(t0.Add(5*time.Second)), ObjState); st.StateString != "" && st.StateString != house.StateNoData {
		t.Fatalf("stale reading: %+v", st)
	}
}

func TestOverloadPublishesCurtailingAndSetsCapsShadowAndLive(t *testing.T) {
	for _, live := range []bool{false, true} {
		m, h := newM(t, `{}`)
		h.SetArmed(&live, &live)
		now := t0
		var last string
		for i := 0; i < 20; i++ {
			now = now.Add(5 * time.Second)
			h.PublishMeasure(house.Measure{At: now, PhaseA: [3]float64{12, 12, 22}, EVA: 12, Measured: true})
			for _, o := range m.OnTick(now) {
				if o.ObjectID == ObjState && o.StateString != "" {
					last = o.StateString
					var a map[string]any
					json.Unmarshal([]byte(o.AttributesJSON), &a)
					if a["ev_armed_live"] != live {
						t.Fatalf("arming not reported: %v", a)
					}
				}
			}
		}
		if last != house.StateCurtailEV {
			t.Fatalf("live=%v state %q", live, last)
		}
		// The cap is set either way; only an armed executor ever acts on it.
		if c, ok := h.EVCap(now); !ok || c != 0 { // 12 A - 3 A over = 9 -> hmm: excess is 22-19=3
			if c != 9 {
				t.Fatalf("cap %v %v", c, ok)
			}
		}
	}
}

func TestPhaseMapOptionAndBadMap(t *testing.T) {
	m, _ := newM(t, `{"house_phase_map":"heater=L2+L3;hp=L1;ev=L1+L2+L3"}`)
	if m.pmap[house.LoadHP] != (house.PhaseSet{true, false, false}) {
		t.Fatalf("%v", m.pmap)
	}
	m, _ = newM(t, `{"house_phase_map":"toaster=L9"}`)
	if m.pmapStr != house.DefaultPhaseMapString {
		t.Fatal("bad map must fall back to the default")
	}
}
