package hpcontrol

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"virtual_sensors/internal/modules"
)

var sto = DefaultParams().Loc

func at(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, sto) }

// testParams uses a model consistent with 20.5 C at 3 C outdoor (the
// PROVISIONAL defaults cannot hold that: see TestProvisionalDefaultsAtThreeC).
func testParams() Params {
	p := DefaultParams()
	p.Model = Model{U: 0.04, Boost5: 1.0, Slope: 0.03, HoldFrac: 0.4, Gain: 0.1, BedroomOffset: -2.0}
	return p
}

func priceMap(from time.Time, slots int, f func(h int) float64) map[int64]float64 {
	m := map[int64]float64{}
	for k := 0; k < slots; k++ {
		t := from.Add(time.Duration(k) * quarter)
		m[t.Unix()] = f(t.In(sto).Hour())
	}
	return m
}

func dayPrices(h int) float64 {
	switch {
	case h >= 17 && h < 21:
		return 4.0
	case h >= 22 || h < 6:
		return 0.5
	case h >= 11 && h < 14:
		return 0.8
	}
	return 1.5
}

func run(t *testing.T, p Params, in Input) Result {
	t.Helper()
	r := Plan(in, p)
	if len(r.Slots) == 0 {
		t.Fatalf("empty plan: %+v", r)
	}
	return r
}

func synth() Input {
	start := at(10, 0, 0)
	return Input{Now: start, T0: 20.5, Tout: 3, Min: 19, Desired: 20.5, Prices: priceMap(start, 144, dayPrices)}
}

func TestSynthetic36hDay(t *testing.T) {
	p := testParams()
	in := synth()
	t0 := time.Now()
	r := run(t, p, in)
	el := time.Since(t0)
	if len(r.Slots) != 144 {
		t.Fatalf("slots %d", len(r.Slots))
	}
	if r.FloorAtRisk || r.MinT < in.Min-1e-6 {
		t.Fatalf("floor breached: min %.2f", r.MinT)
	}
	var eve, pre int
	var tAt17 float64
	var sum float64
	for i, s := range r.Slots {
		h := s.Start.In(sto).Hour()
		if h >= 17 && h < 21 && s.Level != Off {
			eve++
		}
		if h >= 14 && h < 17 && s.Level != Off {
			pre++
		}
		if s.Start.Equal(at(10, 17, 0)) {
			tAt17 = s.T
		}
		if i < 96 {
			sum += s.TEnd
		}
		if p.IsNight(s.Start) && s.BedroomT > 23+1e-6 {
			t.Fatalf("bedroom %.2f at night %v", s.BedroomT, s.Start)
		}
	}
	if eve > 0 {
		t.Fatalf("running in the evening: %d slots", eve)
	}
	if tAt17 < in.Desired+0.2 {
		t.Fatalf("no pre-heat: T(17:00)=%.2f", tAt17)
	}
	if mean := sum / 96; math.Abs(mean-in.Desired) > 0.3 {
		t.Fatalf("24 h mean %.2f vs desired %.2f", mean, in.Desired)
	}
	t.Logf("36 h plan: %v, pre-heat slots 14-17 %d, T(17)=%.2f, 24h mean %.2f, min %.2f, cost %.1f kr, %.1f kWh",
		el, pre, tAt17, r.MeanT, r.MinT, r.CostKr, r.KWh)
	if el > time.Second {
		t.Fatalf("DP too slow: %v", el)
	}
}

func BenchmarkPlan36h(b *testing.B) {
	p := testParams()
	in := synth()
	for i := 0; i < b.N; i++ {
		Plan(in, p)
	}
}

func TestFlatPricesSteadyHolding(t *testing.T) {
	p := testParams()
	in := synth()
	in.Prices = priceMap(in.Now, 144, func(int) float64 { return 1.0 })
	// Nothing to arbitrage; the plan should be mostly hold, few level changes
	// (the test model needs some boost: hold alone cannot offset the loss).
	p.EveningPenKr = 0
	r := run(t, p, in)
	switches, holds := 0, 0
	for i, s := range r.Slots {
		if s.Level == Hold {
			holds++
		}
		if i > 0 && s.Level != r.Slots[i-1].Level {
			switches++
		}
	}
	if float64(holds) < 0.5*float64(len(r.Slots)) || switches > 12 {
		t.Fatalf("holds %d/%d switches %d", holds, len(r.Slots), switches)
	}
	if math.Abs(r.MeanT-in.Desired) > 0.3 {
		t.Fatalf("mean %.2f", r.MeanT)
	}
}

func TestColdNightForcesBoost(t *testing.T) {
	p := testParams()
	in := synth()
	in.Tout = -2
	in.Prices = priceMap(in.Now, 144, func(int) float64 { return 1.0 })
	in.T0 = 19.1
	r := run(t, p, in)
	boosts := 0
	for _, s := range r.Slots[:16] {
		if s.Level == Boost {
			boosts++
		}
	}
	if boosts == 0 {
		t.Fatal("expected boosts at -2 C starting near the floor")
	}
	if r.MinT < in.Min-1e-6 {
		t.Fatalf("floor breached %.2f", r.MinT)
	}
}

func TestEveningRunsOnlyToProtectFloor(t *testing.T) {
	p := testParams()
	in := synth()
	in.Now = at(10, 16, 0)
	in.T0 = 19.1 // about to hit the floor during the spike
	in.Prices = priceMap(in.Now, 96, dayPrices)
	r := run(t, p, in)
	eve := 0
	for _, s := range r.Slots {
		if p.IsEvening(s.Start) && s.Level != Off {
			eve++
		}
	}
	if eve == 0 {
		t.Fatal("floor protection should run in the evening when otherwise breached")
	}
	if r.MinT < in.Min-1e-6 {
		t.Fatalf("floor breached %.2f", r.MinT)
	}
}

func TestBedroomCapBites(t *testing.T) {
	p := testParams()
	p.Model.BedroomOffset = 1.5 // bedroom warmer than the mean
	in := synth()
	in.Desired = 22.5
	in.T0 = 21.5
	r := run(t, p, in)
	for _, s := range r.Slots {
		if p.IsNight(s.Start) && s.BedroomT > 23+0.1 {
			t.Fatalf("bedroom %.2f at %v", s.BedroomT, s.Start)
		}
	}
}

func TestProvisionalDefaultsAtThreeC(t *testing.T) {
	// The PROVISIONAL model cannot hold 19 C at 3 C outdoor: it must flag
	// floor_at_risk and boost wherever needed rather than fail.
	p := DefaultParams()
	in := synth()
	in.T0 = 19.5
	r := run(t, p, in)
	if !r.FloorAtRisk {
		t.Fatalf("expected floor_at_risk, min %.2f", r.MinT)
	}
	if r.Slots[0].Level != Boost {
		t.Fatalf("first slot %v", r.Slots[0].Level)
	}
}

func TestMissingPricesExtrapolated(t *testing.T) {
	p := testParams()
	in := synth()
	in.Prices = priceMap(in.Now, 40, dayPrices) // 10 h known
	r := run(t, p, in)
	if len(r.Slots) != MinHorizonSlots || !r.Extrapolated {
		t.Fatalf("%d %v", len(r.Slots), r.Extrapolated)
	}
	if r := Plan(Input{Now: in.Now, T0: 20, Tout: 3, Min: 19, Desired: 20.5}, p); !r.NoPrices {
		t.Fatal("no prices must be flagged")
	}
}

func TestModelStepNumbers(t *testing.T) {
	m := DefaultModel()
	// PROVISIONAL numbers: u 0.08, 0.9 C/h at 5 C, -0.03 per C colder, hold 40 %, gain 0.1.
	if got := m.Rate(Boost, 5); math.Abs(got-0.9) > 1e-12 {
		t.Fatalf("%v", got)
	}
	if got := m.Rate(Boost, -5); math.Abs(got-0.6) > 1e-12 {
		t.Fatalf("%v", got)
	}
	if got := m.Rate(Hold, 5); math.Abs(got-0.36) > 1e-12 {
		t.Fatalf("%v", got)
	}
	// Off at 20 C / 0 C outdoor over 15 min: 20 + 0.25*(-1.6+0.1) = 19.625.
	if got := m.Step(20, Off, 0); math.Abs(got-19.625) > 1e-9 {
		t.Fatalf("%v", got)
	}
	if got := m.Rate(Boost, -40); got != 0 {
		t.Fatalf("rate must not go negative: %v", got)
	}
}

func TestDysonCaps(t *testing.T) {
	p := DefaultParams()
	cases := []struct {
		t    time.Time
		l    Level
		pr   Presence
		want int
	}{
		{at(10, 3, 0), Boost, Home, 100},
		{at(10, 23, 0), Hold, Home, 100},
		{at(10, 12, 0), Hold, Home, 60},
		{at(10, 12, 0), Hold, Out, 100},
		{at(10, 12, 0), Hold, Away, 100},
		{at(10, 18, 0), Boost, Home, 50},
		{at(10, 18, 0), Boost, Out, 100},
		{at(10, 8, 0), Boost, Home, 100},
		{at(10, 12, 0), Off, Home, 0},
	}
	for i, c := range cases {
		if got := p.DysonPct(c.t, c.l, c.pr); got != c.want {
			t.Fatalf("case %d: %d want %d", i, got, c.want)
		}
	}
	p.Dyson.DayHome = 65
	if got := p.DysonPct(at(10, 12, 0), Hold, Home); got != 60 {
		t.Fatalf("10 %% steps: %d", got)
	}
}

// --- module ---

func raw(t *testing.T, from time.Time, slots int, f func(h int) float64) map[string]json.RawMessage {
	type row struct {
		Start string  `json:"start"`
		End   string  `json:"end"`
		Value float64 `json:"value"`
	}
	var today, tomorrow []row
	for k := 0; k < slots; k++ {
		s := from.Add(time.Duration(k) * quarter)
		r := row{s.Format(time.RFC3339), s.Add(quarter).Format(time.RFC3339), f(s.In(sto).Hour())}
		if s.In(sto).Day() == from.In(sto).Day() {
			today = append(today, r)
		} else {
			tomorrow = append(tomorrow, r)
		}
	}
	a, _ := json.Marshal(today)
	b, _ := json.Marshal(tomorrow)
	return map[string]json.RawMessage{"raw_today": a, "raw_tomorrow": b}
}

func stateOf(outs []modules.OutputUpdate, obj string) (string, string) {
	for _, o := range outs {
		if o.ObjectID == obj {
			return o.StateString, o.AttributesJSON
		}
	}
	return "", ""
}

func feed(m *Module, t time.Time, ent string, v float64) []modules.OutputUpdate {
	return m.OnStateUpdate(modules.StateUpdate{EntityID: ent, Time: t, Value: v, Available: true})
}

func TestModuleMissingHelpersFallBack(t *testing.T) {
	m := New()
	_ = m.Init(nil)
	t0 := at(10, 12, 0)
	m.OnStateUpdate(modules.StateUpdate{EntityID: m.priceEnt, Time: t0, Available: true, Attrs: raw(t, at(10, 0, 0), 192, dayPrices)})
	feed(m, t0, outdoorEnt, 3)
	outs := feed(m, t0, houseEnt, 20.5)
	st, attrs := stateOf(outs, ObjState)
	if !strings.HasPrefix(st, "would_") && st != "floor_at_risk" {
		t.Fatalf("state %q %s", st, attrs)
	}
	for _, want := range []string{"minimum helper missing: using 19.0", "presence helper missing: Home", "desired helper missing"} {
		if !strings.Contains(attrs, want) {
			t.Fatalf("reason lacks %q: %s", want, attrs)
		}
	}
	if m.presenceNow() != Home {
		t.Fatal("presence default Home")
	}
	// The helper arrives: its note drops.
	m.OnStateUpdate(modules.StateUpdate{EntityID: minimumEnt, Time: t0, Value: 18, Available: true})
	_, attrs = stateOf(m.publishAll(t0, true), ObjState)
	if strings.Contains(attrs, "minimum helper missing") {
		t.Fatal(attrs)
	}
}

func TestModulePresenceAndOptions(t *testing.T) {
	m := New()
	_ = m.Init(nil)
	m.OnStateUpdate(modules.StateUpdate{EntityID: presenceEnt, Time: at(10, 1, 0), State: "Out"})
	if m.presenceNow() != Out {
		t.Fatal("Out")
	}
	m.OnStateUpdate(modules.StateUpdate{EntityID: presenceEnt, Time: at(10, 1, 0), State: "Auto"})
	if m.presenceNow() != Home {
		t.Fatal("Auto with switch off is Home")
	}
	m.OnStateUpdate(modules.StateUpdate{EntityID: autoEnt, Time: at(10, 1, 0), State: "on"})
	m.OnStateUpdate(modules.StateUpdate{EntityID: "person.a", Time: at(10, 1, 0), State: "not_home"})
	if m.presenceNow() != Away {
		t.Fatal("nobody home")
	}
	m.OnStateUpdate(modules.StateUpdate{EntityID: "person.b", Time: at(10, 1, 0), State: "home"})
	if m.presenceNow() != Home {
		t.Fatal("any home")
	}
	n := New()
	if err := n.Configure([]byte(`{"hp_model_u":0.05,"hp_p_boost_w":99999,"hp_night_start":"nonsense","hp_dyson_evening_pct":40}`)); err != nil {
		t.Fatal(err)
	}
	if n.p.Model.U != 0.05 || n.p.PBoostW != 1700 || n.p.NightStart != 22*60 || n.p.Dyson.Evening != 40 {
		t.Fatalf("%+v", n.p)
	}
	if err := New().Configure(nil); err != nil {
		t.Fatal(err)
	}
}

func TestModuleErrorTrackingPersistAndNoEffects(t *testing.T) {
	m := New()
	_ = m.Init(nil)
	t0 := at(10, 12, 0)
	m.OnStateUpdate(modules.StateUpdate{EntityID: m.priceEnt, Time: t0, Available: true, Attrs: raw(t, at(10, 0, 0), 192, dayPrices)})
	feed(m, t0, outdoorEnt, 3)
	feed(m, t0, houseEnt, 20.5)
	pred := m.plan.Slots[3].TEnd
	feed(m, t0.Add(time.Hour), houseEnt, pred+0.4)
	m.OnTick(t0.Add(time.Hour + 5*time.Second))
	if len(m.errs) != 1 || math.Abs(m.errs[0].Err-0.4) > 1e-9 {
		t.Fatalf("%+v", m.errs)
	}
	_, a := stateOf(m.publishAll(t0.Add(time.Hour), true), ObjModelErr)
	if !strings.Contains(a, `"mae_c":0.4`) || !strings.Contains(a, `"bias_c":0.4`) {
		t.Fatal(a)
	}
	b, err := m.Persist()
	if err != nil {
		t.Fatal(err)
	}
	n := New()
	if err := n.Init(b); err != nil || len(n.errs) != 1 || len(n.prices) == 0 {
		t.Fatalf("%v", err)
	}
	if err := New().Init([]byte("{garbage")); err != nil {
		t.Fatal("corrupt state must not fail Init")
	}
	// Shadow only: the module does not implement modules.Actor.
	var x any = m
	if _, ok := x.(modules.Actor); ok {
		t.Fatal("hp_control must not be an Actor")
	}
}

func TestModuleWouldCommandAndFloorState(t *testing.T) {
	m := New() // provisional model: at 3 C cannot hold the floor
	_ = m.Init(nil)
	t0 := at(10, 12, 0)
	m.OnStateUpdate(modules.StateUpdate{EntityID: m.priceEnt, Time: t0, Available: true, Attrs: raw(t, at(10, 0, 0), 192, dayPrices)})
	feed(m, t0, outdoorEnt, 3)
	feed(m, t0, houseEnt, 19.2)
	m.OnStateUpdate(modules.StateUpdate{EntityID: climateEnt, Time: t0, State: "off"})
	outs := m.OnTick(t0.Add(15 * time.Minute))
	st, _ := stateOf(outs, ObjState)
	cmd, _ := stateOf(outs, ObjWould)
	if st != "floor_at_risk" || !strings.Contains(cmd, "heat pump heat 30 C fan high") || !strings.Contains(cmd, "dyson") {
		t.Fatalf("%q %q", st, cmd)
	}
	// Once the heat pump already boosts at 30 and Dyson runs at the cap: none.
	m.OnStateUpdate(modules.StateUpdate{EntityID: climateEnt, Time: t0, State: "heat",
		Attrs: map[string]json.RawMessage{"temperature": json.RawMessage("30")}})
	m.OnStateUpdate(modules.StateUpdate{EntityID: dysonEnt, Time: t0, State: "on",
		Attrs: map[string]json.RawMessage{"percentage": json.RawMessage("60")}})
	if _, c := m.decideCmd(t0.Add(15 * time.Minute)); c != "none" {
		t.Fatalf("cmd %q", c)
	}
}

func (m *Module) decideCmd(now time.Time) (string, string) {
	s, c, _ := m.decide(now)
	return s, c
}
