package hpcontrol

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hwcontrol/planner"
)

// Name is the module's registry key.
const Name = "hp_control"

// Output object ids (sensor.<id>).
const (
	ObjState    = "hp_controller_state"
	ObjPlan     = "hp_plan"
	ObjWould    = "hp_would_command"
	ObjModelErr = "hp_model_error"
	ObjCost     = "hp_shadow_cost"
)

// Input entities (sensor. domain omitted, as everywhere in this add-on).
const (
	houseEnt    = "house_mean_temperature"
	outdoorEnt  = "outdoor_temperature_best"
	bedroomEnt  = "bedroom_midheight_sensor_temperature"
	hpPowerEnt  = "heat_pump_power_power"
	desiredEnt  = "input_number.house_target_temperature"
	minimumEnt  = "input_number.house_minimum_temperature"
	presenceEnt = "input_select.house_presence"
	autoEnt     = "input_boolean.house_presence_auto"
	personPfx   = "person."
	climateEnt  = "climate.heat_pump"
	dysonEnt    = "fan.dyson_nn2_eu_nfa8623a"
)

func init() {
	modules.Register(Name, func() modules.Module { return New() })
}

type options struct {
	U          *float64 `json:"hp_model_u"`
	Boost5     *float64 `json:"hp_model_boost_rate"`
	Slope      *float64 `json:"hp_model_rate_slope"`
	HoldFrac   *float64 `json:"hp_model_hold_frac"`
	Gain       *float64 `json:"hp_model_gain"`
	BedOffset  *float64 `json:"hp_model_bedroom_offset"`
	PHold      *float64 `json:"hp_p_hold_w"`
	PBoost     *float64 `json:"hp_p_boost_w"`
	MinDefault *float64 `json:"hp_min_default"`
	DesiredDef *float64 `json:"hp_desired_default"`
	BedCap     *float64 `json:"hp_bedroom_cap"`
	NightStart *string  `json:"hp_night_start"`
	NightEnd   *string  `json:"hp_night_end"`
	EveStart   *string  `json:"hp_evening_start"`
	EveEnd     *string  `json:"hp_evening_end"`
	DysonNight *int     `json:"hp_dyson_night_pct"`
	DysonDay   *int     `json:"hp_dyson_day_home_pct"`
	DysonEve   *int     `json:"hp_dyson_evening_pct"`
	DysonAway  *int     `json:"hp_dyson_away_pct"`
	Lambda     *float64 `json:"hp_lambda"`
	EvePen     *float64 `json:"hp_evening_penalty_kr"`
	SwitchPen  *float64 `json:"hp_switch_penalty_kr"`
	PriceEnt   *string  `json:"hwc_price_entity"`
	AdderOre   *float64 `json:"hwc_price_adder_ore"`
	VAT        *float64 `json:"hwc_vat_factor"`
}

type errPoint struct {
	T   int64   `json:"t"`
	Err float64 `json:"err"` // actual minus predicted, deg C
}

type dayCost struct {
	PlanKr    float64 `json:"plan_kr"`
	ActualKr  float64 `json:"actual_kr"`
	PlanKWh   float64 `json:"plan_kwh"`
	ActualKWh float64 `json:"actual_kwh"`
}

type persisted struct {
	Prices map[int64]float64   `json:"prices,omitempty"` // quarter start -> SEK/kWh ex VAT
	Errs   []errPoint          `json:"errs,omitempty"`
	Days   map[string]*dayCost `json:"days,omitempty"`
}

type reading struct {
	v  float64
	ok bool
	at time.Time
}

// Module is the hp_control module (shadow only).
type Module struct {
	p        Params
	priceEnt string

	house, outdoor, bedroom reading
	hpW                     float64
	desired, minimum        reading
	presSel                 string
	presOK                  bool
	autoOn, autoOK          bool
	persons                 map[string]bool
	hpState                 string // climate.heat_pump state
	hpSet                   float64
	hpSetOK                 bool
	hpFan                   string
	dysonOn                 string
	dysonPct                float64
	dysonPctOK              bool
	prices                  map[int64]float64
	now                     time.Time

	plan       Result
	havePlan   bool
	lastPlanAt time.Time
	planNotes  []string

	pending map[int64]float64 // target quarter (unix) -> predicted house mean
	errs    []errPoint
	days    map[string]*dayCost
	lastInt time.Time

	lastState, lastWould string
	lastBench            time.Duration
}

// New returns a module with defaults applied.
func New() *Module {
	return &Module{p: DefaultParams(), priceEnt: "nordpool_kwh_se3_sek_3_06_0", prices: map[int64]float64{},
		persons: map[string]bool{}, pending: map[int64]float64{}, days: map[string]*dayCost{}}
}

// Name implements modules.Module.
func (m *Module) Name() string { return Name }

// Inputs implements modules.Module.
func (m *Module) Inputs() []modules.InputSpec {
	return []modules.InputSpec{
		{EntityID: houseEnt}, {EntityID: outdoorEnt}, {EntityID: bedroomEnt}, {EntityID: hpPowerEnt},
		{EntityID: desiredEnt}, {EntityID: minimumEnt}, {EntityID: presenceEnt}, {EntityID: autoEnt},
		{EntityID: personPfx, Prefix: true},
		{EntityID: climateEnt, Attributes: []string{"temperature", "fan_mode"}},
		{EntityID: dysonEnt, Attributes: []string{"percentage"}},
		{EntityID: m.priceEnt, Attributes: []string{"today", "tomorrow", "raw_today", "raw_tomorrow", "tomorrow_valid"}},
	}
}

// Outputs implements modules.Module.
func (m *Module) Outputs() []modules.OutputSpec {
	mk := func(obj, name, unit, class string) modules.OutputSpec {
		return modules.OutputSpec{ObjectID: obj, UniqueID: "virtual_sensors_" + obj, Name: name, Unit: unit, StateClass: class, HasAttributes: true}
	}
	return []modules.OutputSpec{
		mk(ObjState, "HP controller state", "", ""),
		mk(ObjPlan, "HP plan", "kWh", ""),
		mk(ObjWould, "HP would command", "", ""),
		withAvail(mk(ObjModelErr, "HP model error", "°C", "measurement")),
		mk(ObjCost, "HP shadow cost", "kr", ""),
	}
}

func withAvail(o modules.OutputSpec) modules.OutputSpec { o.PerEntityAvailability = true; return o }

func parseHHMM(s string) (int, bool) {
	var h, mi int
	if n, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &mi); err != nil || n != 2 || h < 0 || h > 23 || mi < 0 || mi > 59 {
		return 0, false
	}
	return h*60 + mi, true
}

// Configure implements modules.Configurable. Every key optional; out of
// range falls back to the default (logged).
func (m *Module) Configure(raw []byte) error {
	var o options
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
	}
	p := DefaultParams()
	var msgs []string
	rng := func(v *float64, lo, hi float64, dst *float64, key string) {
		if v == nil {
			return
		}
		if *v >= lo && *v <= hi {
			*dst = *v
		} else {
			msgs = append(msgs, key+" out of range; default applied")
		}
	}
	rng(o.U, 0.001, 1, &p.Model.U, "hp_model_u")
	rng(o.Boost5, 0, 10, &p.Model.Boost5, "hp_model_boost_rate")
	rng(o.Slope, 0, 1, &p.Model.Slope, "hp_model_rate_slope")
	rng(o.HoldFrac, 0, 1, &p.Model.HoldFrac, "hp_model_hold_frac")
	rng(o.Gain, -1, 2, &p.Model.Gain, "hp_model_gain")
	rng(o.BedOffset, -10, 10, &p.Model.BedroomOffset, "hp_model_bedroom_offset")
	rng(o.PHold, 1, 10000, &p.PHoldW, "hp_p_hold_w")
	rng(o.PBoost, 1, 10000, &p.PBoostW, "hp_p_boost_w")
	rng(o.MinDefault, 10, 25, &p.MinDefault, "hp_min_default")
	rng(o.DesiredDef, 12, 26, &p.DesiredDef, "hp_desired_default")
	rng(o.BedCap, 15, 35, &p.BedroomCap, "hp_bedroom_cap")
	rng(o.Lambda, 0, 100, &p.Lambda, "hp_lambda")
	rng(o.EvePen, 0, 1000, &p.EveningPenKr, "hp_evening_penalty_kr")
	rng(o.SwitchPen, 0, 100, &p.SwitchPenaltyKr, "hp_switch_penalty_kr")
	hh := func(s *string, dst *int, key string) {
		if s == nil {
			return
		}
		if v, ok := parseHHMM(*s); ok {
			*dst = v
		} else {
			msgs = append(msgs, key+" does not parse; default applied")
		}
	}
	hh(o.NightStart, &p.NightStart, "hp_night_start")
	hh(o.NightEnd, &p.NightEnd, "hp_night_end")
	hh(o.EveStart, &p.EveStart, "hp_evening_start")
	hh(o.EveEnd, &p.EveEnd, "hp_evening_end")
	pct := func(v *int, dst *int, key string) {
		if v == nil {
			return
		}
		if *v >= 0 && *v <= 100 {
			*dst = *v
		} else {
			msgs = append(msgs, key+" out of range; default applied")
		}
	}
	pct(o.DysonNight, &p.Dyson.Night, "hp_dyson_night_pct")
	pct(o.DysonDay, &p.Dyson.DayHome, "hp_dyson_day_home_pct")
	pct(o.DysonEve, &p.Dyson.Evening, "hp_dyson_evening_pct")
	pct(o.DysonAway, &p.Dyson.Away, "hp_dyson_away_pct")
	if o.PriceEnt != nil && *o.PriceEnt != "" {
		m.priceEnt = strings.TrimPrefix(*o.PriceEnt, "sensor.")
	}
	if o.AdderOre != nil {
		p.Price.AdderOre = *o.AdderOre
	}
	if o.VAT != nil && *o.VAT > 0 {
		p.Price.VAT = *o.VAT
	}
	for _, s := range msgs {
		log.Printf("%s: %s", Name, s)
	}
	m.p = p
	return nil
}

// Init implements modules.Module.
func (m *Module) Init(b []byte) error {
	if len(b) > 0 {
		var ps persisted
		if err := json.Unmarshal(b, &ps); err != nil {
			log.Printf("%s: persisted state unreadable (%v); starting fresh", Name, err)
		} else {
			if ps.Prices != nil {
				m.prices = ps.Prices
			}
			m.errs = ps.Errs
			if ps.Days != nil {
				m.days = ps.Days
			}
		}
	}
	log.Printf("%s: SHADOW ONLY: reads Home Assistant, publishes sensors, never calls a service", Name)
	return nil
}

// Persist implements modules.Module.
func (m *Module) Persist() ([]byte, error) {
	return json.Marshal(persisted{Prices: m.prices, Errs: m.errs, Days: m.days})
}

// InitialStates implements modules.InitialStater.
func (m *Module) InitialStates() []modules.OutputUpdate { return m.publishAll(m.now, true) }

func num(u modules.StateUpdate) reading {
	return reading{v: u.Value, ok: u.Available && !math.IsNaN(u.Value), at: u.Time}
}

func attrFloat(u modules.StateUpdate, k string) (float64, bool) {
	b, ok := u.Attrs[k]
	if !ok {
		return 0, false
	}
	var v float64
	if json.Unmarshal(b, &v) != nil {
		return 0, false
	}
	return v, true
}

func attrStr(u modules.StateUpdate, k string) string {
	var s string
	if b, ok := u.Attrs[k]; ok {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// OnStateUpdate implements modules.Module.
func (m *Module) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	if u.Time.After(m.now) {
		m.now = u.Time
	}
	replan := false
	switch {
	case u.EntityID == houseEnt:
		first := !m.house.ok
		m.integrate(u.Time)
		m.house = num(u)
		replan = first && m.house.ok
	case u.EntityID == outdoorEnt:
		first := !m.outdoor.ok
		m.outdoor = num(u)
		replan = first && m.outdoor.ok
	case u.EntityID == bedroomEnt:
		m.bedroom = num(u)
	case u.EntityID == hpPowerEnt:
		m.integrate(u.Time)
		if u.Available {
			m.hpW = u.Value
		} else {
			m.hpW = 0
		}
	case u.EntityID == desiredEnt:
		old := m.desired
		m.desired = num(u)
		replan = old.v != m.desired.v || old.ok != m.desired.ok
	case u.EntityID == minimumEnt:
		old := m.minimum
		m.minimum = num(u)
		replan = old.v != m.minimum.v || old.ok != m.minimum.ok
	case u.EntityID == presenceEnt:
		old := m.presenceNow()
		m.presSel, m.presOK = u.State, u.State != "" && u.State != "unavailable" && u.State != "unknown"
		replan = old != m.presenceNow()
	case u.EntityID == autoEnt:
		old := m.presenceNow()
		m.autoOn, m.autoOK = u.State == "on", u.State == "on" || u.State == "off"
		replan = old != m.presenceNow()
	case strings.HasPrefix(u.EntityID, personPfx):
		old := m.presenceNow()
		m.persons[u.EntityID] = u.State == "home"
		replan = old != m.presenceNow()
	case u.EntityID == climateEnt:
		m.hpState = u.State
		if v, ok := attrFloat(u, "temperature"); ok {
			m.hpSet, m.hpSetOK = v, true
		} else {
			m.hpSetOK = false
		}
		m.hpFan = attrStr(u, "fan_mode")
	case u.EntityID == dysonEnt:
		m.dysonOn = u.State
		m.dysonPct, m.dysonPctOK = attrFloat(u, "percentage")
	case u.EntityID == m.priceEnt:
		if len(u.Attrs) > 0 {
			for _, pr := range planner.ParseNordpool(u.Attrs, u.Time, m.p.Loc) {
				for k := 0; k < int(pr.Dur/quarter); k++ {
					m.prices[pr.Start.Add(time.Duration(k)*quarter).Unix()] = pr.SEK
				}
			}
			cut := u.Time.Add(-72 * time.Hour).Unix()
			for k := range m.prices {
				if k < cut {
					delete(m.prices, k)
				}
			}
			replan = true
		}
	default:
		return nil
	}
	if replan {
		m.replan(u.Time)
	}
	return m.publishAll(u.Time, replan)
}

// presenceNow resolves the household presence (PLAN "Presence", D33):
// input_select Home/Out/Away decides; Auto (or no helper) falls to the
// auto boolean + person entities when the boolean is on, else Home.
func (m *Module) presenceNow() Presence {
	switch strings.ToLower(m.presSel) {
	case "out":
		if m.presOK {
			return Out
		}
	case "away":
		if m.presOK {
			return Away
		}
	case "home":
		if m.presOK {
			return Home
		}
	case "auto":
		if m.presOK && m.autoOK && m.autoOn && len(m.persons) > 0 {
			for _, h := range m.persons {
				if h {
					return Home
				}
			}
			return Away
		}
	}
	return Home
}

func (m *Module) presenceNote() string {
	switch {
	case !m.presOK:
		return "presence helper missing: Home"
	case strings.EqualFold(m.presSel, "auto") && !(m.autoOK && m.autoOn):
		return "presence Auto with auto switch off: Home"
	}
	return ""
}

func (m *Module) minNow() (float64, string) {
	if m.minimum.ok {
		return m.minimum.v, ""
	}
	return m.p.MinDefault, fmt.Sprintf("minimum helper missing: using %.1f", m.p.MinDefault)
}

func (m *Module) desiredNow() (float64, string) {
	if m.desired.ok {
		return m.desired.v, ""
	}
	return m.p.DesiredDef, fmt.Sprintf("desired helper missing: using %.1f", m.p.DesiredDef)
}

func (m *Module) priceKr(sek float64) float64 { return m.p.Price.AllInOre(sek) / 100 }

func (m *Module) inputPlan(now time.Time) Input {
	mn, _ := m.minNow()
	de, _ := m.desiredNow()
	pr := make(map[int64]float64, len(m.prices))
	for k, v := range m.prices {
		pr[k] = m.priceKr(v)
	}
	prev, _ := m.observedLevel()
	return Input{Now: now, T0: m.house.v, Tout: m.outdoor.v, Min: mn, Desired: de, Prices: pr, Prev: prev}
}

func (m *Module) replan(now time.Time) {
	m.lastPlanAt = now
	if !m.house.ok || !m.outdoor.ok || len(m.prices) == 0 {
		m.havePlan = false
		return
	}
	t0 := time.Now()
	m.plan = Plan(m.inputPlan(now), m.p)
	m.lastBench = time.Since(t0)
	m.havePlan = !m.plan.NoPrices && len(m.plan.Slots) > 0
	if m.havePlan && len(m.plan.Slots) >= 4 {
		tgt := m.plan.Slots[0].Start.Add(time.Hour).Unix()
		if _, ok := m.pending[tgt]; !ok {
			m.pending[tgt] = m.plan.Slots[3].TEnd
		}
	}
}

func (m *Module) slotAt(t time.Time) (Slot, bool) {
	if !m.havePlan {
		return Slot{}, false
	}
	q := t.Truncate(quarter)
	for _, s := range m.plan.Slots {
		if s.Start.Equal(q) {
			return s, true
		}
	}
	return Slot{}, false
}

// integrate attributes the held heat-pump power since the last call to
// quarter-hour slots: actual cost at the all-in price, plan cost at the
// currently planned level (hold-last, capped at 30 min).
func (m *Module) integrate(t time.Time) {
	from := m.lastInt
	if t.After(m.lastInt) {
		m.lastInt = t
	}
	if from.IsZero() || !t.After(from) {
		return
	}
	to := t
	if to.Sub(from) > 30*time.Minute {
		to = from.Add(30 * time.Minute)
	}
	for s := from; s.Before(to); {
		q := s.Truncate(quarter)
		e := q.Add(quarter)
		if e.After(to) {
			e = to
		}
		h := e.Sub(s).Hours()
		if sek, ok := m.prices[q.Unix()]; ok {
			d := m.day(s)
			pk := m.priceKr(sek)
			if m.hpW > 0 {
				d.ActualKWh += m.hpW / 1000 * h
				d.ActualKr += m.hpW / 1000 * h * pk
			}
			if sl, ok := m.slotAt(q); ok {
				w := m.p.PowerW(sl.Level) / 1000 * h
				d.PlanKWh += w
				d.PlanKr += w * pk
			}
		}
		s = e
	}
}

func (m *Module) day(t time.Time) *dayCost {
	k := t.In(m.p.Loc).Format("2006-01-02")
	d := m.days[k]
	if d == nil {
		d = &dayCost{}
		m.days[k] = d
		if len(m.days) > 14 {
			oldest := ""
			for kk := range m.days {
				if oldest == "" || kk < oldest {
					oldest = kk
				}
			}
			delete(m.days, oldest)
		}
	}
	return d
}

// resolve scores 1 h-ahead predictions whose target time has passed.
func (m *Module) resolve(now time.Time) {
	for tgt, pred := range m.pending {
		if now.Unix() < tgt {
			continue
		}
		delete(m.pending, tgt)
		if !m.house.ok || now.Sub(m.house.at) > 30*time.Minute || now.Unix()-tgt > 20*60 {
			continue
		}
		m.errs = append(m.errs, errPoint{T: tgt, Err: m.house.v - pred})
	}
	cut := now.Add(-24 * time.Hour).Unix()
	i := 0
	for i < len(m.errs) && m.errs[i].T < cut {
		i++
	}
	m.errs = m.errs[i:]
}

// OnTick implements modules.Ticker.
func (m *Module) OnTick(now time.Time) []modules.OutputUpdate {
	if now.After(m.now) {
		m.now = now
	}
	m.integrate(now)
	m.resolve(now)
	re := m.lastPlanAt.IsZero() || now.Sub(m.lastPlanAt) >= 15*time.Minute
	if re {
		m.replan(now)
	}
	return m.publishAll(now, re)
}

func (m *Module) observedLevel() (Level, bool) {
	switch m.hpState {
	case "off":
		return Off, true
	case "heat":
		if m.hpSetOK && m.hpSet >= 30 {
			return Boost, true
		}
		return Hold, true
	}
	return Off, false
}

// decide returns the controller state, the would-be command and the reason.
func (m *Module) decide(now time.Time) (state, cmd, reason string) {
	var notes []string
	if _, n := m.minNow(); n != "" {
		notes = append(notes, n)
	}
	if _, n := m.desiredNow(); n != "" {
		notes = append(notes, n)
	}
	if n := m.presenceNote(); n != "" {
		notes = append(notes, n)
	}
	tail := ""
	if len(notes) > 0 {
		tail = " [" + strings.Join(notes, "; ") + "]"
	}
	switch {
	case m.now.IsZero() || (!m.house.ok && !m.outdoor.ok && len(m.prices) == 0):
		return "idle", "none", "no readings yet" + tail
	case !m.house.ok:
		return "degraded", "none", "no house mean temperature" + tail
	case !m.outdoor.ok:
		return "degraded", "none", "no outdoor temperature" + tail
	case len(m.prices) == 0 || !m.havePlan:
		return "degraded", "none", "no prices" + tail
	}
	sl, in := m.slotAt(now)
	if !in {
		return "degraded", "none", "no plan slot for now" + tail
	}
	pr := m.presenceNow()
	pct := m.p.DysonPct(now, sl.Level, pr)
	state = "would_" + sl.Level.String()
	reason = fmt.Sprintf("planned %s; house %.1f C, predicted %.1f C at slot end", sl.Level, m.house.v, sl.TEnd)
	if m.plan.FloorAtRisk {
		state = "floor_at_risk"
		reason += fmt.Sprintf("; floor at risk: plan min %.1f C", m.plan.MinT)
	}
	if m.plan.Extrapolated {
		reason += "; prices beyond the published ones extrapolated"
	}
	var cs []string
	cur, known := m.observedLevel()
	if !known || cur != sl.Level {
		desired, _ := m.desiredNow()
		switch sl.Level {
		case Off:
			cs = append(cs, "heat pump off")
		case Hold:
			cs = append(cs, fmt.Sprintf("heat pump heat %.1f C fan auto", desired))
		case Boost:
			cs = append(cs, "heat pump heat 30 C fan high")
		}
		if !known {
			reason += "; heat pump state unknown"
		}
	}
	dcur := 0
	if m.dysonOn == "on" && m.dysonPctOK {
		dcur = int(m.dysonPct)
	}
	if m.dysonOn == "" || m.dysonOn == "unavailable" || m.dysonOn == "unknown" || dcur != pct {
		if pct == 0 {
			if dcur != 0 || m.dysonOn == "" {
				cs = append(cs, "dyson off")
			}
		} else {
			cs = append(cs, fmt.Sprintf("dyson %d%%", pct))
		}
	}
	cmd = "none"
	if len(cs) > 0 {
		cmd = strings.Join(cs, "; ")
	}
	return state, cmd, reason + tail
}

func round(v float64, n int) float64 { p := math.Pow10(n); return math.Round(v*p) / p }

func jsonStr(v any) string { b, _ := json.Marshal(v); return string(b) }

func (m *Module) publishAll(now time.Time, force bool) []modules.OutputUpdate {
	var outs []modules.OutputUpdate
	state, cmd, reason := m.decide(now)
	if force || state != m.lastState {
		m.lastState = state
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjState, Time: now, StateString: state,
			AttributesJSON: jsonStr(map[string]any{"reason": reason, "shadow": true})})
	}
	if force || cmd != m.lastWould {
		m.lastWould = cmd
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjWould, Time: now, StateString: cmd,
			AttributesJSON: jsonStr(map[string]any{"reason": reason, "shadow": true})})
	}
	if force {
		outs = append(outs, m.planOutput(now), m.errOutput(now), m.costOutput(now))
	}
	return outs
}

func (m *Module) planOutput(now time.Time) modules.OutputUpdate {
	slots := []any{}
	pr := m.presenceNow()
	if m.havePlan {
		for _, s := range m.plan.Slots {
			slots = append(slots, []any{s.Start.In(m.p.Loc).Format(time.RFC3339), s.Level.String(),
				m.p.DysonPct(s.Start, s.Level, pr), round(s.TEnd, 2), round(s.PriceKr, 3)})
		}
	}
	v := 0.0
	attrs := map[string]any{"slots": slots, "slot_fields": []string{"start", "level", "dyson_pct", "t_pred_end", "price_kr_per_kwh"}, "shadow": true}
	if m.havePlan {
		// Planned kWh today (local date of now).
		dk := now.In(m.p.Loc).Format("2006-01-02")
		for _, s := range m.plan.Slots {
			if s.Start.In(m.p.Loc).Format("2006-01-02") == dk {
				v += s.KWh
			}
		}
		attrs["cost_kr"] = round(m.plan.CostKr, 2)
		attrs["predicted_daily_mean_c"] = round(m.plan.MeanT, 2)
		attrs["predicted_min_c"] = round(m.plan.MinT, 2)
		attrs["floor_at_risk"] = m.plan.FloorAtRisk
		attrs["prices_extrapolated"] = m.plan.Extrapolated
		attrs["plan_ms"] = m.lastBench.Milliseconds()
	}
	return modules.OutputUpdate{ObjectID: ObjPlan, Time: now, Value: round(v, 2), AttributesJSON: jsonStr(attrs)}
}

func (m *Module) errOutput(now time.Time) modules.OutputUpdate {
	var abs, sum float64
	for _, e := range m.errs {
		abs += math.Abs(e.Err)
		sum += e.Err
	}
	n := float64(len(m.errs))
	if n == 0 {
		return modules.OutputUpdate{ObjectID: ObjModelErr, Time: now, Unavailable: true,
			AttributesJSON: jsonStr(map[string]any{"n": 0, "horizon_h": 1, "shadow": true})}
	}
	return modules.OutputUpdate{ObjectID: ObjModelErr, Time: now, Value: round(abs/n, 3),
		AttributesJSON: jsonStr(map[string]any{"mae_c": round(abs/n, 3), "bias_c": round(sum/n, 3), "n": len(m.errs), "horizon_h": 1, "window_h": 24, "shadow": true})}
}

func (m *Module) costOutput(now time.Time) modules.OutputUpdate {
	d := m.day(now)
	days := map[string]any{}
	for k, v := range m.days {
		days[k] = map[string]any{"plan_kr": round(v.PlanKr, 2), "actual_kr": round(v.ActualKr, 2),
			"plan_kwh": round(v.PlanKWh, 2), "actual_kwh": round(v.ActualKWh, 2)}
	}
	return modules.OutputUpdate{ObjectID: ObjCost, Time: now, Value: round(d.PlanKr, 2),
		AttributesJSON: jsonStr(map[string]any{"plan_kr_today": round(d.PlanKr, 2), "actual_kr_today": round(d.ActualKr, 2),
			"plan_kwh_today": round(d.PlanKWh, 2), "actual_kwh_today": round(d.ActualKWh, 2), "days": days, "shadow": true})}
}
