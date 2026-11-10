// Package housebudget is the "house_planner" module: one electrical budget
// per phase shared by the controllers (ham energy design, "house planner").
// Priority: manual loads, then hot water, then heat pump, then EV.
//
// It reads nothing from Home Assistant itself. The modules share one
// process, so it meets them through internal/house's Hub: the heater plan
// (hwcontrol), the heat-pump plan (hpcontrol) and the measured per-phase
// current (the EV guard, which already prefers the Easee Equalizer and
// falls back to the grid-power estimate). It publishes the headroom
// forecast (read back by evcontrol as its per-slot current ceiling) and,
// on a sustained overload, caps the EV and then the heat pump. The caps
// are applied only by those modules' own live executors, and only when
// armed live; in shadow they only show in the would-command sensors and
// here. It has no output for the hot-water heater: it never touches it.
package housebudget

import (
	"encoding/json"
	"log"
	"math"
	"time"

	"virtual_sensors/internal/house"
	"virtual_sensors/internal/modules"
)

// Name is the registry key.
const Name = "house_planner"

// Output object ids.
const (
	ObjHeadroomL1 = "house_budget_headroom_l1"
	ObjHeadroomL2 = "house_budget_headroom_l2"
	ObjHeadroomL3 = "house_budget_headroom_l3"
	ObjState      = "house_budget_state"
	ObjHeartbeat  = "house_budget_heartbeat"
)

var headroomIDs = [3]string{ObjHeadroomL1, ObjHeadroomL2, ObjHeadroomL3}

const (
	forecastEvery  = time.Minute
	heartbeatEvery = 30 * time.Second
	valueEvery     = time.Minute
)

func init() {
	modules.Register(Name, func() modules.Module { return New() })
}

type options struct {
	BudgetA   *float64 `json:"house_budget_a"`
	PhaseMap  *string  `json:"house_phase_map"`
	BaseA     *float64 `json:"house_base_a"`
	HeaterW   *float64 `json:"house_heater_w"`
	HPMaxW    *float64 `json:"house_hp_max_w"`
	DelayS    *int     `json:"house_overload_delay_s"`
	CooldownM *int     `json:"house_hp_cooldown_min"`
	HorizonH  *int     `json:"house_horizon_h"`
}

// Module is the house planner.
type Module struct {
	hub      *house.Hub
	budgetA  float64
	pmap     house.PhaseMap
	pmapStr  string
	baseA    float64
	heaterW  float64 // -1: the schedule's own nominal watts; 0: heater not in the budget
	hpMaxW   float64
	delay    time.Duration
	cooldown time.Duration
	horizon  time.Duration

	ov        *house.Overload
	fc        house.Forecast
	haveFc    bool
	lastFc    time.Time
	lastFcKey [3]string
	lastFcPub [3]time.Time
	dec       house.Decision
	haveDec   bool
	meas      house.Measure
	haveMeas  bool
	now       time.Time

	lastState    string
	lastStateKey string
	lastVal      [3]float64
	lastValOK    [3]bool
	lastValAt    [3]time.Time
	lastHB       time.Time
	loc          *time.Location
}

// New returns a module with defaults, on the process-wide hub.
func New() *Module {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		loc = time.UTC
	}
	m := &Module{hub: house.Default, budgetA: 19, pmap: house.DefaultPhaseMap(), pmapStr: house.DefaultPhaseMapString,
		baseA: 3, heaterW: -1, hpMaxW: 1700, delay: house.DefaultDelay, cooldown: house.DefaultCooldown, horizon: 24 * time.Hour, loc: loc}
	m.ov = m.newOverload()
	return m
}

// WithHub points the module at a private hub (tests).
func (m *Module) WithHub(h *house.Hub) *Module { m.hub = h; return m }

func (m *Module) newOverload() *house.Overload {
	return house.NewOverload(house.OverloadCfg{BudgetA: m.budgetA, Delay: m.delay, Cooldown: m.cooldown, Map: m.pmap})
}

// Name implements modules.Module.
func (m *Module) Name() string { return Name }

// Inputs implements modules.Module: none; it reads the Hub.
func (m *Module) Inputs() []modules.InputSpec { return nil }

// Outputs implements modules.Module.
func (m *Module) Outputs() []modules.OutputSpec {
	var o []modules.OutputSpec
	for i, id := range headroomIDs {
		o = append(o, modules.OutputSpec{ObjectID: id, UniqueID: "virtual_sensors_" + id,
			Name: "House budget headroom L" + string(rune('1'+i)), DeviceClass: "current", StateClass: "measurement", Unit: "A",
			PerEntityAvailability: true, HasAttributes: true})
	}
	o = append(o,
		modules.OutputSpec{ObjectID: ObjState, UniqueID: "virtual_sensors_" + ObjState, Name: "House budget state", HasAttributes: true},
		modules.OutputSpec{ObjectID: ObjHeartbeat, UniqueID: "virtual_sensors_" + ObjHeartbeat, Name: "House budget heartbeat", Unit: "s", HasAttributes: true})
	return o
}

// Configure implements modules.Configurable. Every key optional; out of
// range or unparseable falls back to the default (logged).
func (m *Module) Configure(raw []byte) error {
	var o options
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
	}
	if o.BudgetA != nil {
		if *o.BudgetA > 0 && *o.BudgetA <= 63 {
			m.budgetA = *o.BudgetA
		} else {
			log.Printf("%s: house_budget_a out of range 1..63; default 19 applied", Name)
		}
	}
	if o.PhaseMap != nil && *o.PhaseMap != "" {
		if pm, err := house.ParsePhaseMap(*o.PhaseMap); err == nil {
			m.pmap, m.pmapStr = pm, *o.PhaseMap
		} else {
			log.Printf("%s: house_phase_map: %v; default %q applied", Name, err, house.DefaultPhaseMapString)
		}
	}
	if o.BaseA != nil {
		if *o.BaseA >= 0 && *o.BaseA <= 20 {
			m.baseA = *o.BaseA
		} else {
			log.Printf("%s: house_base_a out of range 0..20; default 3 applied", Name)
		}
	}
	if o.HeaterW != nil {
		if *o.HeaterW >= 0 && *o.HeaterW <= 12000 {
			m.heaterW = *o.HeaterW
		} else {
			log.Printf("%s: house_heater_w out of range 0..12000; the heater schedule's own watts apply", Name)
		}
	}
	if o.HPMaxW != nil {
		if *o.HPMaxW >= 0 && *o.HPMaxW <= 12000 {
			m.hpMaxW = *o.HPMaxW
		} else {
			log.Printf("%s: house_hp_max_w out of range 0..12000; default 1700 applied", Name)
		}
	}
	if o.DelayS != nil {
		if *o.DelayS >= 5 && *o.DelayS <= 600 {
			m.delay = time.Duration(*o.DelayS) * time.Second
		} else {
			log.Printf("%s: house_overload_delay_s out of range 5..600; default 30 applied", Name)
		}
	}
	if o.CooldownM != nil {
		if *o.CooldownM >= 1 && *o.CooldownM <= 240 {
			m.cooldown = time.Duration(*o.CooldownM) * time.Minute
		} else {
			log.Printf("%s: house_hp_cooldown_min out of range 1..240; default 15 applied", Name)
		}
	}
	if o.HorizonH != nil {
		if *o.HorizonH >= 1 && *o.HorizonH <= 48 {
			m.horizon = time.Duration(*o.HorizonH) * time.Hour
		} else {
			log.Printf("%s: house_horizon_h out of range 1..48; default 24 applied", Name)
		}
	}
	m.ov = m.newOverload()
	return nil
}

// Init implements modules.Module. The module keeps no persistent state: a
// restart starts with no caps, which is the safe direction.
func (m *Module) Init([]byte) error {
	log.Printf("%s: budget %.0f A per phase, wiring %s, base %.1f A, overload delay %s, heat-pump cool-down %s",
		Name, m.budgetA, m.pmapStr, m.baseA, m.delay, m.cooldown)
	return nil
}

// Persist implements modules.Module.
func (m *Module) Persist() ([]byte, error) { return []byte("{}"), nil }

// OnStateUpdate implements modules.Module (never called: no inputs).
func (m *Module) OnStateUpdate(modules.StateUpdate) []modules.OutputUpdate { return nil }

// InitialStates implements modules.InitialStater.
func (m *Module) InitialStates() []modules.OutputUpdate {
	return []modules.OutputUpdate{{ObjectID: ObjState, StateString: house.StateNoData,
		AttributesJSON: jsonStr(map[string]any{"reason": "starting"})}}
}

func (m *Module) heaterPlan() house.HeaterPlan {
	h := m.hub.Heater()
	switch {
	case m.heaterW >= 0: // option set: it wins (0 switches the heater out of the budget)
		h.NominalW = m.heaterW
	case h.NominalW <= 0: // no provider: worst case 3 kW ON throughout
		h.NominalW = 3000
	}
	return h
}

func (m *Module) hpPlan() house.HPPlan {
	p, ok := m.hub.HP()
	if !ok {
		p = house.HPPlan{}
	}
	p.MaxW = m.hpMaxW
	return p
}

// recompute rebuilds the forecast and hands it to the Hub.
func (m *Module) recompute(now time.Time) {
	m.fc = house.ComputeForecast(house.ForecastIn{Now: now, Horizon: m.horizon, BudgetA: m.budgetA,
		BaseA: [3]float64{m.baseA, m.baseA, m.baseA}, Map: m.pmap, Heater: m.heaterPlan(), HP: m.hpPlan()})
	m.haveFc, m.lastFc = true, now
	m.hub.SetForecast(m.fc)
}

// OnTick implements modules.Ticker.
func (m *Module) OnTick(now time.Time) []modules.OutputUpdate {
	m.now = now
	if !m.haveFc || now.Sub(m.lastFc) >= forecastEvery {
		m.recompute(now)
	}
	meas, ok := m.hub.Measure()
	m.meas, m.haveMeas = meas, ok
	m.dec = m.ov.Step(now, meas, ok)
	m.haveDec = true
	m.hub.SetCaps(now, m.dec.EVCapA, m.dec.HPCap)
	if m.dec.Changed {
		log.Printf("%s: %s (EV cap %d, heat-pump cap %d)", Name, m.dec.Reason, m.dec.EVCapA, m.dec.HPCap)
	}
	return m.publish(now)
}

func (m *Module) source() string {
	switch {
	case !m.haveMeas || m.dec.NoData:
		return "none"
	case m.meas.Measured:
		return "equalizer"
	}
	return "grid_estimate"
}

func (m *Module) publish(now time.Time) []modules.OutputUpdate {
	var outs []modules.OutputUpdate
	evA, hpA := m.hub.Armed()
	// state
	key := jsonStr(map[string]any{"s": m.dec.State, "e": m.dec.EVCapA, "h": m.dec.HPCap, "o": m.dec.Over, "src": m.source(), "r": m.dec.Reason, "ea": evA, "ha": hpA})
	if m.lastStateKey != key {
		m.lastStateKey = key
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjState, Time: now, StateString: m.dec.State,
			AttributesJSON: jsonStr(map[string]any{
				"reason": m.dec.Reason, "source": m.source(), "budget_a": m.budgetA, "phase_map": m.pmapStr,
				"over_l1": m.dec.Over[0], "over_l2": m.dec.Over[1], "over_l3": m.dec.Over[2],
				"ev_cap_a": capOrNull(m.dec.EVCapA), "hp_cap": hpCapName(m.dec.HPCap),
				"ev_armed_live": evA, "hp_armed_live": hpA,
				"would_act_only": !(evA || hpA),
				"note":           "caps are applied only by the EV / heat-pump modules' own live executors when armed; hot water is never curtailed"})})
	}
	// live headroom per phase, plus the forecast attribute
	for i, id := range headroomIDs {
		if !m.haveMeas || m.dec.NoData {
			if m.lastValOK[i] || m.lastValAt[i].IsZero() {
				outs = append(outs, modules.OutputUpdate{ObjectID: id, Time: now, Unavailable: true})
				m.lastValOK[i], m.lastValAt[i] = false, now
			}
		} else {
			v := math.Round((m.budgetA-m.meas.PhaseA[i])*10) / 10
			if !m.lastValOK[i] || v != m.lastVal[i] || now.Sub(m.lastValAt[i]) >= valueEvery {
				outs = append(outs, modules.OutputUpdate{ObjectID: id, Time: now, Value: v})
				m.lastVal[i], m.lastValOK[i], m.lastValAt[i] = v, true, now
			}
		}
		if m.haveFc {
			if k := m.forecastKey(i); k != m.lastFcKey[i] || now.Sub(m.lastFcPub[i]) >= 10*time.Minute {
				m.lastFcKey[i], m.lastFcPub[i] = k, now
				outs = append(outs, modules.OutputUpdate{ObjectID: id, Time: now, AttributesOnly: true, AttributesJSON: m.forecastAttrs(i)})
			}
		}
	}
	if m.lastHB.IsZero() || now.Sub(m.lastHB) >= heartbeatEvery {
		m.lastHB = now
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjHeartbeat, Time: now, Value: float64(now.Unix()),
			AttributesJSON: jsonStr(map[string]any{"state": m.dec.State})})
	}
	return outs
}

func capOrNull(v int) any {
	if v < 0 {
		return nil
	}
	return v
}

func hpCapName(l int) any {
	switch l {
	case house.HPOff:
		return "off"
	case house.HPHold:
		return "hold"
	}
	return nil
}

// forecastRows is the phase's headroom per slot: [start, amps].
func (m *Module) forecastRows(p int) [][2]any {
	rows := make([][2]any, 0, len(m.fc.Slots))
	for _, s := range m.fc.Slots {
		rows = append(rows, [2]any{s.Start.In(m.loc).Format(time.RFC3339), math.Round(s.HeadA[p]*10) / 10})
	}
	return rows
}

func (m *Module) forecastKey(p int) string {
	b, _ := json.Marshal(m.forecastRows(p))
	return string(b)
}

func (m *Module) forecastAttrs(p int) string {
	ev := make([][2]any, 0, len(m.fc.Slots))
	for _, s := range m.fc.Slots {
		if v, ok := m.fc.EVHeadroom(s.Start); ok {
			ev = append(ev, [2]any{s.Start.In(m.loc).Format(time.RFC3339), math.Round(v*10) / 10})
		}
	}
	return jsonStr(map[string]any{
		"forecast": m.forecastRows(p), "forecast_fields": []string{"start", "headroom_a"},
		"ev_ceiling_forecast": ev, "budget_a": m.budgetA, "base_a": m.baseA,
		"heater_plan_known": m.fc.HeaterKnown, "hp_plan_known": m.fc.HPKnown, "phase_map": m.pmapStr,
		"computed": m.fc.At.In(m.loc).Format(time.RFC3339)})
}

func jsonStr(v any) string { b, _ := json.Marshal(v); return string(b) }
