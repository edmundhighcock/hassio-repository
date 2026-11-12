package hpcontrol

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"virtual_sensors/internal/house"
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
	// Live-executor outputs.
	ObjMode     = "hp_control_mode"  // shadow | live
	ObjArmed    = "hp_control_armed" // on | off (the arming helper); attribute active
	ObjLastCmd  = "hp_last_command"  // what was last sent
	ObjOverride = "hp_override"      // none | active (manual override hold-off)
	// ObjHeartbeat is an ISO timestamp refreshed at least every 60 s (every
	// 30 s in practice), for an HA-side watchdog.
	ObjHeartbeat = "hp_heartbeat"
)

// Input entities (sensor. domain omitted, as everywhere in this add-on).
const (
	outdoorEnt  = "outdoor_temperature_best"
	bedroomEnt  = "bedroom_midheight_sensor_temperature"
	kitchenEnt  = "kitchen_temperature_sensor_temperature"
	hpPowerEnt  = "heat_pump_power_power"
	desiredEnt  = "input_number.house_target_temperature"
	minimumEnt  = "input_number.house_minimum_temperature"
	presenceEnt = "input_select.house_presence"
	autoEnt     = "input_boolean.house_presence_auto"
	personPfx   = "person."
	climateEnt  = "climate.heat_pump"
	dysonEnt    = "fan.dyson_nn2_eu_nfa8623a"
	armedEnt    = "input_boolean.hp_controller_armed"
	tvTimerEnt  = "timer.tv_quiet"
	tvActiveEnt = "input_boolean.tv_quiet_active"
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
	// Comfort sensors, forecast, live executor.
	Comfort       *string  `json:"hp_comfort_sensors"`
	ForecastEnt   *string  `json:"hp_forecast_entity"`
	FloorMargin   *float64 `json:"hp_floor_margin_c"`
	Mode          *string  `json:"hp_mode"`
	NotifyService *string  `json:"hp_notify_service"`
	HoldSet       *float64 `json:"hp_hold_setpoint_c"`
	BoostSet      *float64 `json:"hp_boost_setpoint_c"`
	OverrideH     *float64 `json:"hp_override_hold_h"`
	GraceS        *int     `json:"hp_command_grace_s"`
	StaleMin      *int     `json:"hp_stale_after_min"`
	SensorStale   *int     `json:"hp_sensor_stale_min"`
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
	// Live executor: survive a restart without a spurious arm notification
	// and without forgetting a manual override.
	WasActive     bool      `json:"was_active,omitempty"`
	OverrideUntil time.Time `json:"override_until,omitempty"`
}

type reading struct {
	v  float64
	ok bool
	at time.Time
}

const zeroDayKey = "0001-01-01"

// Module is the hp_control module. Shadow by default; with hp_mode live AND
// input_boolean.hp_controller_armed on it also drives the heat pump and the
// Dyson fan (exec.go).
type Module struct {
	p        Params
	priceEnt string

	// Configuration beyond Params.
	comfortIDs  []string // entity ids, "sensor." prefix stripped
	forecastEnt string
	floorMargin float64
	live        bool // hp_mode: live
	notifySvc   string
	holdSet     float64
	boostSet    float64
	overrideFor time.Duration
	cmdGrace    time.Duration
	staleAfter  time.Duration
	sensorStale time.Duration

	comfort          map[string]reading
	outdoor, bedroom reading
	hpW              float64
	desired, minimum reading
	presSel          string
	presOK           bool
	autoOn, autoOK   bool
	persons          map[string]bool
	hpState          string // climate.heat_pump state
	hpSet            float64
	hpSetOK          bool
	hpFan            string
	dysonOn          string
	dysonPct         float64
	dysonPctOK       bool
	prices           map[int64]float64
	now              time.Time
	lastHeard        time.Time // latest event time from any input

	forecast      map[int64]float64
	forecastAt    time.Time // when the forecast was fetched
	forecastReqAt time.Time
	forecastBusy  bool

	plan       Result
	havePlan   bool
	lastPlanAt time.Time
	planNotes  []string

	pending map[int64]float64 // target quarter (unix) -> predicted comfort temperature
	errs    []errPoint
	days    map[string]*dayCost
	lastInt time.Time

	lastState, lastWould string
	lastBench            time.Duration

	ex executor

	// House planner (internal/house): the plan snapshot it reads, and the hub.
	hub    *house.Hub
	snapMu sync.RWMutex
	snap   map[int64]float64 // slot start (unix) -> planned W; nil: no plan
}

// New returns a module with defaults applied.
func New() *Module {
	m := &Module{p: DefaultParams(), priceEnt: "nordpool_kwh_se3_sek_3_06_0", prices: map[int64]float64{},
		persons: map[string]bool{}, pending: map[int64]float64{}, days: map[string]*dayCost{},
		comfort: map[string]reading{}, forecast: map[int64]float64{}, hub: house.Default}
	m.applyExecDefaults()
	return m
}

// Name implements modules.Module.
func (m *Module) Name() string { return Name }

// Inputs implements modules.Module.
func (m *Module) Inputs() []modules.InputSpec {
	in := []modules.InputSpec{
		{EntityID: outdoorEnt}, {EntityID: bedroomEnt}, {EntityID: hpPowerEnt},
		{EntityID: desiredEnt}, {EntityID: minimumEnt}, {EntityID: presenceEnt}, {EntityID: autoEnt},
		{EntityID: armedEnt}, {EntityID: tvTimerEnt}, {EntityID: tvActiveEnt},
		{EntityID: personPfx, Prefix: true},
		{EntityID: climateEnt, Attributes: []string{"temperature", "fan_mode"}},
		{EntityID: dysonEnt, Attributes: []string{"percentage"}},
		{EntityID: m.priceEnt, Attributes: []string{"today", "tomorrow", "raw_today", "raw_tomorrow", "tomorrow_valid"}},
	}
	seen := map[string]bool{outdoorEnt: true, bedroomEnt: true}
	for _, id := range m.comfortIDs {
		if !seen[id] {
			seen[id] = true
			in = append(in, modules.InputSpec{EntityID: id})
		}
	}
	return in
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
		mk(ObjMode, "HP control mode", "", ""),
		mk(ObjArmed, "HP control armed", "", ""),
		mk(ObjLastCmd, "HP last command", "", ""),
		mk(ObjOverride, "HP override", "", ""),
		{ObjectID: ObjHeartbeat, UniqueID: "virtual_sensors_" + ObjHeartbeat, Name: "HP controller heartbeat", DeviceClass: "timestamp"},
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
	msgs = append(msgs, m.configureExec(o)...)
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
			m.ex.wasActive = ps.WasActive
			m.ex.overrideUntil = ps.OverrideUntil
		}
	}
	m.hub.SetHP(m.HousePlan) // read-only for the house planner
	// An older build could persist a zero-time day key; drop it.
	delete(m.days, zeroDayKey)
	if m.live {
		log.Printf("%s: mode LIVE: drives climate.heat_pump and the Dyson fan only while %s is on", Name, armedEnt)
	} else {
		log.Printf("%s: SHADOW: reads Home Assistant, publishes sensors; device commands need hp_mode live and %s on", Name, armedEnt)
	}
	return nil
}

// Persist implements modules.Module.
func (m *Module) Persist() ([]byte, error) {
	return json.Marshal(persisted{Prices: m.prices, Errs: m.errs, Days: m.days,
		WasActive: m.ex.wasActive, OverrideUntil: m.ex.overrideUntil})
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

// isComfort reports whether id is one of the comfort sensors.
func (m *Module) isComfort(id string) bool {
	for _, c := range m.comfortIDs {
		if c == id {
			return true
		}
	}
	return false
}

// OnStateUpdate implements modules.Module.
func (m *Module) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	if u.Time.After(m.now) {
		m.now = u.Time
	}
	// Liveness is judged on the inputs the module does not cause: the echo
	// of its own commands must not hide a silent Home Assistant.
	if !u.Replay && u.Time.After(m.lastHeard) && u.EntityID != climateEnt && u.EntityID != dysonEnt {
		m.lastHeard = u.Time
	}
	replan := false
	switch {
	case m.isComfort(u.EntityID):
		_, was := m.comfortNow(m.now)
		m.integrate(u.Time)
		m.comfort[u.EntityID] = num(u)
		if u.EntityID == bedroomEnt {
			m.bedroom = num(u)
		}
		_, is := m.comfortNow(m.now)
		replan = !was && is
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
	case u.EntityID == armedEnt:
		m.ex.armed, m.ex.armedOK = u.State == "on", u.State == "on" || u.State == "off"
		if u.State == "off" {
			// Disarming clears the override so off-then-on re-arms cleanly.
			m.ex.overrideUntil, m.ex.overrideWhy = time.Time{}, ""
			m.ex.sent = map[string]*sentRec{}
			m.ex.grace = map[string]*graceRec{}
		}
	case u.EntityID == tvTimerEnt:
		m.ex.onTimer(u.State == "active", m.now)
	case u.EntityID == tvActiveEnt:
		m.ex.onTVBool(u.State == "on", m.now)
	case u.EntityID == climateEnt:
		m.hpState = u.State
		if v, ok := attrFloat(u, "temperature"); ok {
			m.hpSet, m.hpSetOK = v, true
		} else {
			m.hpSetOK = false
		}
		m.hpFan = attrStr(u, "fan_mode")
		m.onDeviceChange(u.Replay, m.now, "heat pump")
	case u.EntityID == dysonEnt:
		m.dysonOn = u.State
		m.dysonPct, m.dysonPctOK = attrFloat(u, "percentage")
		m.onDeviceChange(u.Replay, m.now, "Dyson fan")
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
		m.replan(m.now)
	}
	if !u.Replay {
		m.act(m.now)
	}
	return m.publishAll(m.now, replan)
}

// comfortNow is the mean of the fresh comfort sensors and how many of them
// were fresh. A sensor is fresh when available and its last update is no
// older than hp_sensor_stale_min (these sensors report only on change, every
// 20-30 min or so).
func (m *Module) comfortNow(now time.Time) (float64, bool) {
	var sum float64
	n := 0
	for _, id := range m.comfortIDs {
		r, ok := m.comfort[id]
		if !ok || !r.ok || (!r.at.IsZero() && now.Sub(r.at) > m.sensorStale) {
			continue
		}
		sum += r.v
		n++
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// comfortMinNow is the coldest fresh comfort sensor and its entity id.
func (m *Module) comfortMinNow(now time.Time) (float64, string, bool) {
	best, who, found := 0.0, "", false
	for _, id := range m.comfortIDs {
		r, ok := m.comfort[id]
		if !ok || !r.ok || (!r.at.IsZero() && now.Sub(r.at) > m.sensorStale) {
			continue
		}
		if !found || r.v < best {
			best, who, found = r.v, id, true
		}
	}
	return best, who, found
}

// bedroomNow is the bedroom reading if fresh.
func (m *Module) bedroomNow(now time.Time) (float64, bool) {
	r := m.bedroom
	if !r.ok || (!r.at.IsZero() && now.Sub(r.at) > m.sensorStale) {
		return 0, false
	}
	return r.v, true
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

// forecastFresh reports whether the outdoor forecast is usable at now.
func (m *Module) forecastFresh(now time.Time) bool {
	return len(m.forecast) > 0 && !m.forecastAt.IsZero() && now.Sub(m.forecastAt) <= 3*time.Hour
}

// toutNow is the outdoor temperature for the plan's first slot: the
// reading, else the forecast.
func (m *Module) toutNow(now time.Time) (float64, bool) {
	if m.outdoor.ok {
		return m.outdoor.v, true
	}
	if m.forecastFresh(now) {
		return Input{Forecast: m.forecast}.outdoorAt(now), true
	}
	return 0, false
}

func (m *Module) inputPlan(now time.Time) Input {
	mn, _ := m.minNow()
	de, _ := m.desiredNow()
	pr := make(map[int64]float64, len(m.prices))
	for k, v := range m.prices {
		pr[k] = m.priceKr(v)
	}
	prev, _ := m.observedLevel()
	t0, _ := m.comfortNow(now)
	tout, _ := m.toutNow(now)
	in := Input{Now: now, T0: t0, Tout: tout, Min: mn + m.floorMargin, Desired: de, Prices: pr, Prev: prev}
	if m.forecastFresh(now) {
		in.Forecast = m.forecast
	}
	if b, ok := m.bedroomNow(now); ok {
		// The measured bedroom-minus-comfort offset, bounded so one odd
		// reading cannot disable the night cap.
		in.BedOffset, in.BedOffsetOK = math.Max(-1.5, math.Min(0.5, b-t0)), true
	}
	in.ColdOffset = m.coldOffset(now, t0)
	return in
}

// coldOffset is the coldest comfort sensor minus the comfort mean, <= 0,
// bounded to [-1.5, 0]. With two or more fresh sensors it is measured live;
// otherwise it falls back to the model's bedroom offset when the bedroom is
// a comfort sensor. The floor binds on the coldest sensor, not the mean.
func (m *Module) coldOffset(now time.Time, mean float64) float64 {
	cold, n := math.Inf(1), 0
	for _, id := range m.comfortIDs {
		r, ok := m.comfort[id]
		if !ok || !r.ok || (!r.at.IsZero() && now.Sub(r.at) > m.sensorStale) {
			continue
		}
		n++
		cold = math.Min(cold, r.v)
	}
	off := 0.0
	if n >= 2 {
		off = cold - mean
	}
	if m.isComfort(bedroomEnt) {
		off = math.Min(off, m.p.Model.BedroomOffset) // the bedroom's usual lag, even if the kitchen is colder now
	}
	return math.Max(-1.5, math.Min(0, off))
}

func (m *Module) replan(now time.Time) {
	m.lastPlanAt = now
	defer m.snapshotPlan()
	if _, ok := m.comfortNow(now); !ok {
		m.havePlan = false
		return
	}
	if _, ok := m.toutNow(now); !ok || len(m.prices) == 0 {
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

// snapshotPlan copies the current plan's planned power per slot for
// PlannedPowerW (called after every replan).
func (m *Module) snapshotPlan() {
	var snap map[int64]float64
	if m.havePlan {
		snap = make(map[int64]float64, len(m.plan.Slots))
		for _, s := range m.plan.Slots {
			snap[s.Start.Unix()] = m.p.PowerW(s.Level)
		}
	}
	m.snapMu.Lock()
	m.snap = snap
	m.snapMu.Unlock()
}

// PlannedPowerW is the heat pump's current plan as slot start (unix) ->
// planned electrical W, nil when there is no valid plan. Read-only and safe
// to call from another goroutine; the house planner uses it for its budget.
func (m *Module) PlannedPowerW() map[int64]float64 {
	m.snapMu.RLock()
	defer m.snapMu.RUnlock()
	if m.snap == nil {
		return nil
	}
	out := make(map[int64]float64, len(m.snap))
	for k, v := range m.snap {
		out[k] = v
	}
	return out
}

// HousePlan adapts PlannedPowerW for internal/house.
func (m *Module) HousePlan() house.HPPlan {
	w := m.PlannedPowerW()
	return house.HPPlan{Valid: w != nil, W: w, MaxW: m.p.PBoostW}
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
		c, ok := m.comfortNow(now)
		if !ok || now.Unix()-tgt > 20*60 {
			continue
		}
		m.errs = append(m.errs, errPoint{T: tgt, Err: c - pred})
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
	m.requestForecast(now)
	re := m.lastPlanAt.IsZero() || now.Sub(m.lastPlanAt) >= 15*time.Minute
	if re {
		m.replan(now)
	}
	live := m.ex.active(m)
	m.hub.SetArmed(nil, &live)
	m.act(now)
	return m.publishAll(now, re)
}

func (m *Module) observedLevel() (Level, bool) {
	switch m.hpState {
	case "off":
		return Off, true
	case "heat":
		if m.hpSetOK && m.hpSet >= m.boostSet {
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
	if m.ex.active(m) && !m.now.IsZero() {
		if tg := m.target(now); tg.Kind == "safe_hold" {
			state, reason = "safe_hold", "safe hold: "+tg.Reason+tail
			switch {
			case m.ex.quiet(now):
				state, reason = "tv_quiet", "TV quiet: nothing is sent"+tail
			case now.Before(m.ex.overrideUntil):
				state, reason = "override", "manual override until "+m.ex.overrideUntil.In(m.p.Loc).Format("15:04")+tail
			}
			cmd = "none"
			if cs := m.describeCalls(tg); len(cs) > 0 {
				cmd = strings.Join(cs, "; ")
			}
			return state, cmd, reason
		}
	}
	_, cok := m.comfortNow(now)
	_, tok := m.toutNow(now)
	switch {
	case m.now.IsZero() || (!cok && !m.outdoor.ok && len(m.prices) == 0):
		return "idle", "none", "no readings yet" + tail
	case !cok:
		return "degraded", "none", "no fresh comfort temperature" + tail
	case !tok:
		return "degraded", "none", "no outdoor temperature" + tail
	case len(m.prices) == 0 || !m.havePlan:
		return "degraded", "none", "no prices" + tail
	}
	sl, in := m.slotAt(now)
	if !in {
		return "degraded", "none", "no plan slot for now" + tail
	}
	tg := m.target(now)
	state = "would_" + tg.Level.String()
	c, _ := m.comfortNow(now)
	reason = fmt.Sprintf("planned %s; comfort %.1f C, predicted %.1f C at slot end", sl.Level, c, sl.TEnd)
	if tg.Kind != "plan" {
		state = tg.Kind
		reason += fmt.Sprintf("; %s: %s -> %s", tg.Kind, tg.Reason, tg.Level)
	}
	if m.plan.FloorAtRisk {
		state = "floor_at_risk"
		reason += fmt.Sprintf("; floor at risk: plan min %.1f C", m.plan.MinT)
	}
	if m.plan.Extrapolated {
		reason += "; prices beyond the published ones extrapolated"
	}
	if m.ex.active(m) {
		switch {
		case m.ex.quiet(now):
			state, reason = "tv_quiet", "TV quiet: nothing is sent"+tail
		case now.Before(m.ex.overrideUntil):
			state, reason = "override", "manual override until "+m.ex.overrideUntil.In(m.p.Loc).Format("15:04")+tail
		}
	}
	cs := m.describeCalls(tg)
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
	shadow := !m.ex.active(m)
	if force || state != m.lastState {
		m.lastState = state
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjState, Time: now, StateString: state,
			AttributesJSON: jsonStr(map[string]any{"reason": reason, "shadow": shadow})})
	}
	if force || cmd != m.lastWould {
		m.lastWould = cmd
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjWould, Time: now, StateString: cmd,
			AttributesJSON: jsonStr(map[string]any{"reason": reason, "shadow": shadow})})
	}
	if force {
		outs = append(outs, m.planOutput(now), m.errOutput(now), m.costOutput(now))
	}
	outs = append(outs, m.ex.liveOutputs(m, now, force)...)
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
	attrs := map[string]any{"slots": slots, "slot_fields": []string{"start", "level", "dyson_pct", "t_pred_end", "price_kr_per_kwh"}, "shadow": !m.ex.active(m)}
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
	var d dayCost
	if !now.IsZero() { // a zero time would create the spurious 0001-01-01 day key
		d = *m.day(now)
	}
	days := map[string]any{}
	for k, v := range m.days {
		if k == zeroDayKey {
			continue
		}
		days[k] = map[string]any{"plan_kr": round(v.PlanKr, 2), "actual_kr": round(v.ActualKr, 2),
			"plan_kwh": round(v.PlanKWh, 2), "actual_kwh": round(v.ActualKWh, 2)}
	}
	return modules.OutputUpdate{ObjectID: ObjCost, Time: now, Value: round(d.PlanKr, 2),
		AttributesJSON: jsonStr(map[string]any{"plan_kr_today": round(d.PlanKr, 2), "actual_kr_today": round(d.ActualKr, 2),
			"plan_kwh_today": round(d.PlanKWh, 2), "actual_kwh_today": round(d.ActualKWh, 2), "days": days, "shadow": !m.ex.active(m)})}
}
