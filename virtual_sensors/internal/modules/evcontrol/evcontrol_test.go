package evcontrol

import (
	"encoding/json"
	"testing"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hwcontrol/planner"
)

var sto = DefaultParams().Loc

func at(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, sto) }

// prices builds quarter prices (SEK/kWh) from from to to via f(local hour).
func prices(from, to time.Time, f func(h int) float64) []planner.Price {
	var out []planner.Price
	for t := from; t.Before(to); t = t.Add(quarter) {
		out = append(out, planner.Price{Start: t, Dur: quarter, SEK: f(t.In(sto).Hour())})
	}
	return out
}

func noHeater(time.Time) bool { return false }

func TestCeilingArithmetic(t *testing.T) {
	p := DefaultParams()
	if got := p.Ceiling(at(10, 23, 30), true); got != 8 { // 19-3-7.4 = 8.6 -> 8
		t.Fatalf("outside heater window: %v", got)
	}
	if got := p.Ceiling(at(10, 20, 0), false); got != 8 {
		t.Fatalf("hot water off, heat pump still on C: %v", got)
	}
	p.HPA = 0
	if got := p.Ceiling(at(10, 20, 0), true); got != 8 { // heater 7.5+3 on A: 8.5 -> 8
		t.Fatalf("heater slot: %v", got)
	}
	if got := p.Ceiling(at(10, 23, 30), true); got != 16 {
		t.Fatalf("clamp to max: %v", got)
	}
	p.HPA = 12
	if got := p.Ceiling(at(10, 23, 30), true); got != 0 {
		t.Fatalf("below min must be 0: %v", got)
	}
}

func TestCheapestSlotChoiceAndTieBreak(t *testing.T) {
	p := DefaultParams()
	p.NeedKWh = 5.5 * 2 // 8 A = 5.52 kW: 2 h at 8 A = 11.04 kWh
	pr := prices(at(10, 19, 0), at(11, 7, 0), func(h int) float64 {
		if h >= 2 && h < 4 {
			return 0.2
		}
		return 1.0
	})
	r := Plan(Input{Now: at(10, 19, 0), Departure: at(11, 6, 30), Prices: pr, HotWaterOn: noHeater}, p)
	if r.DeadlineMode || !r.Feasible {
		t.Fatalf("%+v", r)
	}
	if len(r.Slots) != 8 || !r.Slots[0].Start.Equal(at(11, 2, 0)) || !r.Slots[7].Start.Equal(at(11, 3, 45)) {
		t.Fatalf("expected 02:00-04:00, got %v", r.Slots)
	}
	// Flat prices: ties break earlier.
	flat := prices(at(10, 19, 0), at(11, 7, 0), func(int) float64 { return 0.5 })
	r = Plan(Input{Now: at(10, 19, 0), Departure: at(11, 6, 30), Prices: flat, HotWaterOn: noHeater}, p)
	if !r.Slots[0].Start.Equal(at(10, 19, 0)) {
		t.Fatalf("tie should pick earliest: %v", r.Slots[0])
	}
}

func TestBlockMerging(t *testing.T) {
	p := DefaultParams()
	p.NeedKWh = 5.52 // 4 slots at 8 A
	// Cheap at 01:00-01:15, 01:30-01:45 (one-quarter gap between), plus two more cheap far away.
	pr := prices(at(10, 22, 0), at(11, 7, 0), func(int) float64 { return 1.0 })
	set := func(h, m int, v float64) {
		for i := range pr {
			if pr[i].Start.Equal(at(11, h, m)) {
				pr[i].SEK = v
			}
		}
	}
	set(1, 0, 0.1)
	set(1, 30, 0.1)
	set(1, 45, 0.1)
	set(3, 0, 0.1)
	r := Plan(Input{Now: at(10, 22, 0), Departure: at(11, 6, 30), Prices: pr, HotWaterOn: noHeater}, p)
	got := map[time.Time]bool{}
	for _, s := range r.Slots {
		got[s.Start] = true
	}
	if !got[at(11, 1, 15)] {
		t.Fatalf("one-quarter gap at 01:15 should be filled: %v", r.Slots)
	}
	if got[at(11, 2, 0)] {
		t.Fatalf("unexpected fill: %v", r.Slots)
	}
	if r.PlannedKWh <= p.NeedKWh {
		t.Fatalf("filled gap adds energy: %v", r.PlannedKWh)
	}
}

func TestDeadlineBackstop(t *testing.T) {
	p := DefaultParams()
	// 19:00-23:00 only 4 h at 8 A = 22 kWh < 44: cannot finish.
	pr := prices(at(10, 19, 0), at(11, 6, 30), func(int) float64 { return 0.5 })
	r := Plan(Input{Now: at(10, 19, 0), Departure: at(10, 23, 0), Prices: pr, HotWaterOn: noHeater}, p)
	if !r.DeadlineMode || r.Feasible || len(r.Slots) != 16 {
		t.Fatalf("%+v", r)
	}
}

func TestPartialPrices(t *testing.T) {
	p := DefaultParams()
	pr := prices(at(10, 19, 0), at(11, 0, 0), func(h int) float64 { return 1 }) // today only
	r := Plan(Input{Now: at(10, 19, 0), Departure: at(11, 6, 30), Prices: pr, HotWaterOn: noHeater}, p)
	if !r.PricesPartial {
		t.Fatal("prices_partial not flagged")
	}
	if !r.DeadlineMode {
		t.Fatalf("priced slots (5 h) cannot meet 44 kWh: backstop expected")
	}
	last := r.Slots[len(r.Slots)-1]
	if !last.Start.Equal(at(11, 6, 15)) || last.Priced {
		t.Fatalf("backstop should cover unpriced slots to departure: %v", last)
	}
	// Enough priced slots tonight: no backstop, unpriced slots not chosen.
	p.NeedKWh = 5.52
	r = Plan(Input{Now: at(10, 19, 0), Departure: at(11, 6, 30), Prices: pr, HotWaterOn: noHeater}, p)
	if r.DeadlineMode || !r.PricesPartial {
		t.Fatalf("%+v", r)
	}
	for _, s := range r.Slots {
		if !s.Priced {
			t.Fatal("unpriced slot chosen")
		}
	}
}

func TestNeedAfterPartialDelivery(t *testing.T) {
	p := DefaultParams()
	pr := prices(at(10, 19, 0), at(11, 7, 0), func(int) float64 { return 0.5 })
	r := Plan(Input{Now: at(11, 1, 0), Departure: at(11, 6, 30), DeliveredKWh: 30, Prices: pr, HotWaterOn: noHeater}, p)
	if r.NeedKWh != 14 {
		t.Fatalf("need %v", r.NeedKWh)
	}
	r = Plan(Input{Now: at(11, 1, 0), Departure: at(11, 6, 30), DeliveredKWh: 50, Prices: pr, HotWaterOn: noHeater}, p)
	if r.NeedKWh != 0 || len(r.Slots) != 0 {
		t.Fatalf("need must not go negative: %+v", r)
	}
}

func TestHeaterSlotsLowerCeiling(t *testing.T) {
	p := DefaultParams()
	p.HPA = 0 // isolate the heater
	on := func(time.Time) bool { return true }
	if c := p.Ceiling(at(10, 20, 0), on(at(10, 20, 0))); c != 8 {
		t.Fatalf("%v", c)
	}
	if c := p.Ceiling(at(10, 12, 0), on(at(10, 12, 0))); c != 16 {
		t.Fatalf("outside 16-23 the heater is not assumed: %v", c)
	}
}

func TestGuardPerPhase(t *testing.T) {
	p := DefaultParams()
	// Grid 5.52 kW EV + 1.7 kW heat pump + 3 kW heater + ~0.69 kW rest.
	g := Guard(GuardIn{GridW: 5520 + 1700 + 3450 + 690, EVkW: 5.52, HPW: 1700, ImmersionOn: true}, p)
	if g.EVA < 7.9 || g.EVA > 8.1 {
		t.Fatalf("EV amps %v", g.EVA)
	}
	// A,B: 7.5 + 1 = 8.5 house; C: 7.39 + 1 = 8.39.
	if g.HouseA[0] < 8.4 || g.HouseA[0] > 8.6 || g.HouseA[2] < 8.3 || g.HouseA[2] > 8.5 {
		t.Fatalf("house %v", g.HouseA)
	}
	if g.LimitA != 10 || g.Breach { // floor(19-8.6)=10
		t.Fatalf("limit %d breach %v phases %v", g.LimitA, g.Breach, g.PhaseA)
	}
	// Heavy load: breach and pause.
	g = Guard(GuardIn{GridW: 16000, EVkW: 10, HPW: 1700, ImmersionOn: true}, p)
	if !g.Breach {
		t.Fatalf("expected breach %v", g.PhaseA)
	}
	g = Guard(GuardIn{GridW: 22000, EVkW: 0, HPW: 1700, ImmersionOn: true}, p)
	if g.LimitA != 0 {
		t.Fatalf("limit %d", g.LimitA)
	}
	g = Guard(GuardIn{GridW: 800, EVkW: 0}, p)
	if g.LimitA != 16 {
		t.Fatalf("clamp to max, got %d", g.LimitA)
	}
}

// --- module-level replay over a SYNTHETIC night -------------------------
// The add-on's replay harness holds hot-water fixtures only (no price or
// EV data), so this drives the module through the live interface over a
// synthetic night: evening price spike, plug-in 19:00, departure 06:30.

func rawPrices(from, to time.Time, f func(h int) float64) map[string]json.RawMessage {
	type row struct {
		Start string  `json:"start"`
		End   string  `json:"end"`
		Value float64 `json:"value"`
	}
	var today, tomorrow []row
	for t := from; t.Before(to); t = t.Add(quarter) {
		r := row{t.Format(time.RFC3339), t.Add(quarter).Format(time.RFC3339), f(t.In(sto).Hour())}
		if t.In(sto).Day() == from.In(sto).Day() {
			today = append(today, r)
		} else {
			tomorrow = append(tomorrow, r)
		}
	}
	a, _ := json.Marshal(today)
	b, _ := json.Marshal(tomorrow)
	return map[string]json.RawMessage{"raw_today": a, "raw_tomorrow": b}
}

func TestSyntheticNightReplay(t *testing.T) {
	m := New()
	var lines [][]byte
	m.appendLine = func(_ string, b []byte) error { lines = append(lines, b); return nil }
	if err := m.Init(nil); err != nil {
		t.Fatal(err)
	}
	spike := func(h int) float64 {
		switch {
		case h >= 17 && h < 21:
			return 3.0 // evening spike
		case h >= 1 && h < 5:
			return 0.3
		}
		return 1.0
	}
	t0 := at(10, 19, 0)
	feed := func(u modules.StateUpdate) []modules.OutputUpdate { return m.OnStateUpdate(u) }
	feed(modules.StateUpdate{EntityID: m.priceEnt, Time: t0, Available: true, Attrs: rawPrices(at(10, 0, 0), at(12, 0, 0), spike)})
	outs := feed(modules.StateUpdate{EntityID: "loaver_status", Time: t0, State: "awaiting_start", Available: false})
	state := ""
	for _, o := range outs {
		if o.ObjectID == ObjState {
			state = o.StateString
		}
	}
	if state != "planned" {
		t.Fatalf("state after plug-in: %q (%+v)", state, outs)
	}
	if !m.havePlan || !m.plan.Feasible || m.plan.DeadlineMode {
		t.Fatalf("plan %+v", m.plan)
	}
	for _, s := range m.plan.Slots {
		if h := s.Start.In(sto).Hour(); h >= 17 && h < 21 {
			t.Fatalf("planned into the spike: %v", s.Start)
		}
	}
	// Charge exactly as planned (power held per quarter), reading grid too.
	delivered := 0.0
	for tt := t0; tt.Before(at(11, 6, 30)); tt = tt.Add(quarter) {
		kw := 0.0
		if s, ok := m.slotAt(tt); ok {
			kw = s.KW
		}
		feed(modules.StateUpdate{EntityID: "loaver_power", Time: tt, Value: kw, Available: true})
		feed(modules.StateUpdate{EntityID: "grid_meter_power", Time: tt, Value: kw*1000 + 600, Available: true})
		m.OnTick(tt.Add(time.Minute))
		delivered += kw * 0.25
	}
	feed(modules.StateUpdate{EntityID: "loaver_power", Time: at(11, 6, 30), Value: 0, Available: true})
	feed(modules.StateUpdate{EntityID: "loaver_status", Time: at(11, 6, 45), State: "disconnected", Available: false})
	if len(lines) != 1 {
		t.Fatalf("expected one session line, got %d", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatal(err)
	}
	if rec["delivered_kwh"].(float64) < 40 || rec["plan_finishes_by_departure"] != true {
		t.Fatalf("%v", rec)
	}
	// Followed the plan, so the saving against the plan's own cost is ~0
	// (greater than -1 kr and below +1 kr: the plan buys the same kWh).
	if s := rec["saving_kr"].(float64); s < -1 || s > 1 {
		t.Fatalf("plan-following session should score ~0, got %v (%v)", s, rec)
	}
	t.Logf("synthetic night: %.1f kWh, actual %.2f kr, plan %.2f kr", rec["delivered_kwh"], rec["actual_cost_kr"], rec["plan_cost_kr"])
	// An evening-spike naive charge (start 19:00 at max) would cost more.
	naive := 0.0
	for i, kwh := 0, 0.0; kwh < 44; i++ {
		tt := t0.Add(time.Duration(i) * quarter)
		naive += 5.52 * 0.25 * m.p.Price.AllInOre(spike(tt.In(sto).Hour())) / 100
		kwh += 5.52 * 0.25
	}
	if naive <= rec["actual_cost_kr"].(float64) {
		t.Fatalf("naive %.2f should exceed planned %.2f", naive, rec["actual_cost_kr"])
	}
}

func TestPersistRoundTripAndConfigDefaults(t *testing.T) {
	m := New()
	if err := m.Configure([]byte(`{"modules":["ev_control"],"ev_need_kwh":30,"ev_min_a":99,"ev_departure":"nonsense"}`)); err != nil {
		t.Fatal(err)
	}
	if m.p.NeedKWh != 30 || m.p.MinA != 6 || m.depHHMM != 390 {
		t.Fatalf("%+v %d", m.p, m.depHHMM)
	}
	m.totals.Sessions = 3
	b, err := m.Persist()
	if err != nil {
		t.Fatal(err)
	}
	n := New()
	if err := n.Init(b); err != nil || n.totals.Sessions != 3 {
		t.Fatalf("%v %+v", err, n.totals)
	}
	if err := New().Init([]byte("{garbage")); err != nil {
		t.Fatal("corrupt state must not fail Init")
	}
}
