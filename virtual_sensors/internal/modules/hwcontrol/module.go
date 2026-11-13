// Package hwcontrol is the "hot_water_control" module: the adapter
// between the add-on host and the pure controller core (PLAN §2.2). It
// maps HA entities to core roles, hot_water's in-process signals to core
// inputs, core effects to host effects (notifications go to the phone and
// to a persistent notification), and wires the planner to the core. The
// planner is the only place prices are read.
package hwcontrol

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"virtual_sensors/internal/house"
	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hotwater"
	"virtual_sensors/internal/modules/hwcontrol/core"
	"virtual_sensors/internal/modules/hwcontrol/planner"
)

// Name is the module's registry key.
const Name = "hot_water_control"

func init() {
	modules.Register(Name, func() modules.Module { return New() })
}

// Entities are the controller's HA inputs (PLAN §2.4). Sensor ids use the
// add-on's convention (no "sensor." prefix).
type Entities struct {
	Cover, Target, MotorState, Battery, Charging string
	Select, Armed, Latch, Boost                  string
	TestMove, TestProbe                          string // R3 manual test buttons (PLAN §8)
	EV, TankTemp, Price                          string
	Conflicts                                    []string
	ConflictPrefix                               string
	NotifyService                                string
}

func entitiesFor(cover, price, notify string) Entities {
	obj := strings.TrimPrefix(cover, "cover.")
	return Entities{
		Cover: cover, Target: obj + "_target_position", MotorState: obj + "_motor_state",
		Battery: obj + "_battery", Charging: "binary_sensor." + obj + "_charging",
		Select: "input_select.hot_water", Armed: "input_boolean.hot_water_controller_armed",
		Latch: "input_boolean.hot_water_watchdog_tripped", Boost: "input_button.hot_water_boost",
		TestMove: "input_button.hot_water_test_move", TestProbe: "input_button.hot_water_test_probe",
		EV: "loaver_power", TankTemp: "hot_water_tank_sensor_device_temperature",
		Price:          strings.TrimPrefix(price, "sensor."),
		Conflicts:      []string{"automation.hot_water", "automation.hot_water_on", "automation.hot_water_on_2"},
		ConflictPrefix: "automation.hwc_",
		NotifyService:  notify,
	}
}

// Module is the hot_water_control module.
type Module struct {
	cfg     core.Config
	pcfg    planner.Config
	ent     Entities
	roles   map[string]core.Role
	c       *core.Controller
	signals bool
	started bool

	prices     []planner.Price
	lastPlanAt time.Time

	lastShadowSig string
	lastShadowAt  time.Time

	// planner v2 (0.6.0)
	v2         bool // hwc_planner: v2 drives the controller
	v2cfg      planner.V2Config
	shadowWin  []core.PlanWindow // the shadow v2 plan's own window memory (greedy_v1 active)
	tankTemps  []tempSample
	eveningDrp float64 // hwc_v2_evening_drop_c
	testOnly   bool    // hwc_test_moves_only: R3 planner override, never a plan
	now        time.Time

	schedMu sync.RWMutex
	sched   core.HeaterSchedule

	outs    []modules.OutputUpdate
	effects []modules.Effect
	notifyC uint64
}

type tempSample struct {
	t time.Time
	v float64
}

// ObjPlanV2 is the shadow comparison sensor (sensor.hot_water_controller_plan_v2).
const ObjPlanV2 = "hot_water_controller_plan_v2"

// New returns an unconfigured module (defaults apply until Configure).
func New() *Module {
	m := &Module{cfg: core.Defaults(), pcfg: planner.DefaultConfig(), v2cfg: planner.DefaultV2Config(), eveningDrp: 3}
	m.ent = entitiesFor("cover.hot_water_switch", "sensor.nordpool_kwh_se3_sek_3_06_0", "notify.mobile_app_edmund15max")
	m.buildRoles()
	return m
}

func (m *Module) buildRoles() {
	e := m.ent
	m.roles = map[string]core.Role{
		e.Cover: core.RoleCover, e.Target: core.RoleTarget, e.MotorState: core.RoleMotorState,
		e.Battery: core.RoleBattery, e.Charging: core.RoleCharging, e.Select: core.RoleSelect,
		e.Armed: core.RoleArmed, e.Latch: core.RoleLatch, e.Boost: core.RoleBoost,
		e.EV: core.RoleEV, e.TankTemp: core.RoleTankTemp,
		e.TestMove: core.RoleTestMove, e.TestProbe: core.RoleTestProbe,
	}
	for _, a := range e.Conflicts {
		m.roles[a] = core.RoleConflict
	}
}

// Name implements modules.Module.
func (m *Module) Name() string { return Name }

// Inputs implements modules.Module.
func (m *Module) Inputs() []modules.InputSpec {
	e := m.ent
	in := []modules.InputSpec{
		{EntityID: e.Cover, Attributes: []string{"current_position"}},
		{EntityID: e.Target}, {EntityID: e.MotorState}, {EntityID: e.Battery}, {EntityID: e.Charging},
		{EntityID: e.Select}, {EntityID: e.Armed}, {EntityID: e.Latch}, {EntityID: e.Boost},
		{EntityID: e.TestMove}, {EntityID: e.TestProbe},
		{EntityID: e.EV}, {EntityID: e.TankTemp},
		{EntityID: e.Price, Attributes: []string{"today", "tomorrow", "raw_today", "raw_tomorrow", "tomorrow_valid"}},
		{EntityID: e.ConflictPrefix, Prefix: true},
	}
	for _, a := range e.Conflicts {
		in = append(in, modules.InputSpec{EntityID: a})
	}
	out := in[:0]
	for _, s := range in {
		if s.EntityID != "__none__" {
			out = append(out, s)
		}
	}
	return out
}

// Outputs implements modules.Module (PLAN §2.5; prefix
// hot_water_controller_, never hwc_).
func (m *Module) Outputs() []modules.OutputSpec {
	names := map[string]struct{ name, unit, class string }{
		core.ObjHeartbeat:     {"Hot water controller heartbeat", "s", ""},
		core.ObjState:         {"Hot water controller state", "", ""},
		core.ObjDesired:       {"Hot water controller desired", "", ""},
		core.ObjVerdict:       {"Hot water controller verdict", "", ""},
		core.ObjOnHours:       {"Hot water controller ON hours since genuine start", "h", "measurement"},
		core.ObjHoursSinceEnd: {"Hot water controller hours since reheat end", "h", ""},
		core.ObjMovesToday:    {"Hot water controller moves today", "", "measurement"},
		core.ObjPlan:          {"Hot water controller plan", "", ""},
		core.ObjShadowWould:   {"Hot water controller shadow would", "", ""},
		core.ObjAlert:         {"Hot water controller alert", "", ""},
		core.ObjStrikes:       {"Hot water controller strikes", "", "measurement"},
	}
	var out []modules.OutputSpec
	for _, obj := range core.TelemetryObjects {
		n := names[obj]
		out = append(out, modules.OutputSpec{ObjectID: obj, UniqueID: "virtual_sensors_" + obj, Name: n.name,
			Unit: n.unit, StateClass: n.class, HasAttributes: true})
	}
	out = append(out, modules.OutputSpec{ObjectID: ObjPlanV2, UniqueID: "virtual_sensors_" + ObjPlanV2,
		Name: "Hot water controller plan v2 (shadow)", HasAttributes: true})
	return out
}

// options is the hwc_* subset of /data/options.json (PLAN §9). Every key
// is optional; absent means default.
type options struct {
	Mode               *string  `json:"hwc_mode"`
	NotifyService      *string  `json:"hwc_notify_service"`
	CoverEntity        *string  `json:"hwc_cover_entity"`
	PriceEntity        *string  `json:"hwc_price_entity"`
	PriceAdderOre      *float64 `json:"hwc_price_adder_ore"`
	VATFactor          *float64 `json:"hwc_vat_factor"`
	OffLimitH          *float64 `json:"hwc_off_limit_since_reheat_end_h"`
	EveningOffFrom     *string  `json:"hwc_evening_off_from"`
	EveningRule        *bool    `json:"hwc_evening_off_requires_finished_reheat_today"`
	StaleAfterMin      *int     `json:"hwc_stale_after_min"`
	BoostNoStartMin    *int     `json:"hwc_boost_no_start_timeout_min"`
	BoostMaxH          *float64 `json:"hwc_boost_max_h"`
	PlannerMaxBlockMin *int     `json:"hwc_planner_max_block_min"`
	MaxBlocksPerPeriod *int     `json:"hwc_max_blocks_per_period"`
	MinSavingOre       *float64 `json:"hwc_min_saving_ore"`
	ProbeH             *float64 `json:"hwc_backstop_probe_h"`
	AlertH             *float64 `json:"hwc_backstop_alert_h"`
	GenuineMinW        *int     `json:"hwc_genuine_min_w"`
	GenuineMinS        *int     `json:"hwc_genuine_min_s"`
	GOffH              *float64 `json:"hwc_g_off_h"`
	GSinceReheatH      *float64 `json:"hwc_g_since_reheat_h"`
	MaxMovesHour       *int     `json:"hwc_max_moves_hour"`
	MaxMovesDay        *int     `json:"hwc_max_moves_day"`
	BatteryFloor       *int     `json:"hwc_battery_floor_pct"`
	BatteryAlert       *int     `json:"hwc_battery_alert_pct"`
	HeartbeatS         *int     `json:"hwc_heartbeat_s"`
	EVGuardMinutes     *string  `json:"hwc_ev_guard_minutes"`
	MotorStateEntity   *string  `json:"hwc_motor_state_entity"`
	MotorStoppedStates *string  `json:"hwc_motor_stopped_states"`
	TestMovesOnly      *bool    `json:"hwc_test_moves_only"` // R3: no planned OFF, only manual test moves
	TestOffMin         *int     `json:"hwc_test_off_min"`
	BootPlanWaitS      *int     `json:"hwc_boot_plan_wait_s"` // 0.6.1

	// planner v2 (0.6.0)
	Planner         *string  `json:"hwc_planner"`
	V2NightFrom     *string  `json:"hwc_v2_night_from"`
	V2NightTo       *string  `json:"hwc_v2_night_to"`
	V2MiddayFrom    *string  `json:"hwc_v2_midday_from"`
	V2MiddayTo      *string  `json:"hwc_v2_midday_to"`
	V2Topup         *bool    `json:"hwc_v2_topup"`
	V2FullDefaultM  *int     `json:"hwc_v2_full_default_min"`
	V2TopupDefaultM *int     `json:"hwc_v2_topup_default_min"`
	V2FullMinM      *int     `json:"hwc_v2_full_min_min"` // 0.6.2
	V2LearnFrom     *string  `json:"hwc_v2_learn_from"`   // 0.6.2
	V2EarlyEndMin   *int     `json:"hwc_v2_early_end_min"`
	V2EveningTopup  *bool    `json:"hwc_v2_evening_topup"`
	V2EveningGapH   *float64 `json:"hwc_v2_evening_gap_h"`
	V2EveningDropC  *float64 `json:"hwc_v2_evening_drop_c"`
	V2WindowClock   *bool    `json:"hwc_v2_window_resets_clock"`
}

// parseLocalStamp parses "2006-01-02" or "2006-01-02T15:04" in loc.
func parseLocalStamp(s string, loc *time.Location) (time.Time, bool) {
	for _, f := range []string{"2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(f, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func hours(v float64) time.Duration { return time.Duration(v * float64(time.Hour)) }

// Configure implements modules.Configurable: maps hwc_* options onto the
// core and planner configs, then clamps (PLAN §9: out-of-range values
// fall back to the default and are logged; never refuse to start).
func (m *Module) Configure(raw []byte) error {
	var o options
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
	}
	c, p := core.Defaults(), planner.DefaultConfig()
	if o.Mode != nil {
		c.Live = *o.Mode == "live"
	}
	cover, price, notify := "cover.hot_water_switch", "sensor.nordpool_kwh_se3_sek_3_06_0", "notify.mobile_app_edmund15max"
	if o.CoverEntity != nil && strings.HasPrefix(*o.CoverEntity, "cover.") {
		cover = *o.CoverEntity
	}
	if o.PriceEntity != nil && *o.PriceEntity != "" {
		price = *o.PriceEntity
	}
	if o.NotifyService != nil && strings.HasPrefix(*o.NotifyService, "notify.") {
		notify = *o.NotifyService
	}
	m.ent = entitiesFor(cover, price, notify)
	if o.MotorStateEntity != nil {
		if *o.MotorStateEntity == "" {
			c.RequireMotorState = false // M0 found no motor_state entity
			m.ent.MotorState = "__none__"
		} else {
			m.ent.MotorState = strings.TrimPrefix(*o.MotorStateEntity, "sensor.")
		}
	}
	if o.MotorStoppedStates != nil {
		var states []string
		for _, s := range strings.Split(*o.MotorStoppedStates, ",") {
			if s = strings.TrimSpace(s); s != "" {
				states = append(states, s)
			}
		}
		if len(states) > 0 {
			c.MotorStoppedStates = states
		}
	}
	m.buildRoles()
	setH := func(dst *time.Duration, v *float64) {
		if v != nil {
			*dst = hours(*v)
		}
	}
	setMin := func(dst *time.Duration, v *int) {
		if v != nil {
			*dst = time.Duration(*v) * time.Minute
		}
	}
	setH(&c.OffLimitSinceReheatEnd, o.OffLimitH)
	setMin(&c.StaleAfter, o.StaleAfterMin)
	setMin(&c.BoostNoStartTimeout, o.BoostNoStartMin)
	setH(&c.BoostMax, o.BoostMaxH)
	setH(&c.ProbeAfter, o.ProbeH)
	setH(&c.AlertAfter, o.AlertH)
	setH(&c.GOff, o.GOffH)
	setH(&c.GSinceReheat, o.GSinceReheatH)
	if o.EveningRule != nil {
		c.EveningRule = *o.EveningRule
	}
	var msgs []string
	if o.EveningOffFrom != nil {
		if v, ok := core.ParseHHMM(*o.EveningOffFrom); ok {
			c.EveningOffFromMin = v
		} else {
			msgs = append(msgs, "option hwc_evening_off_from does not parse; default applied")
		}
	}
	if o.EVGuardMinutes != nil {
		if v, ok := core.ParseHHMMList(*o.EVGuardMinutes); ok {
			c.EVGuardMinutes = v
		} else {
			msgs = append(msgs, "option hwc_ev_guard_minutes does not parse; default applied")
		}
	}
	if o.GenuineMinW != nil {
		c.GenuineMinW = float64(*o.GenuineMinW)
	}
	if o.GenuineMinS != nil {
		c.GenuineMinDur = time.Duration(*o.GenuineMinS) * time.Second
	}
	if o.MaxMovesHour != nil {
		c.MaxMovesHour = *o.MaxMovesHour
	}
	if o.MaxMovesDay != nil {
		c.MaxMovesDay = *o.MaxMovesDay
	}
	if o.BatteryFloor != nil {
		c.BatteryFloor = float64(*o.BatteryFloor)
	}
	if o.BatteryAlert != nil {
		c.BatteryAlert = float64(*o.BatteryAlert)
	}
	if o.HeartbeatS != nil {
		c.Heartbeat = time.Duration(*o.HeartbeatS) * time.Second
	}
	if o.TestOffMin != nil {
		c.TestOffDuration = time.Duration(*o.TestOffMin) * time.Minute
	}
	m.testOnly = o.TestMovesOnly != nil && *o.TestMovesOnly
	if m.testOnly {
		msgs = append(msgs, "hwc_test_moves_only: the planner is overridden, no planned OFF (R3 manual test moves only)")
	}
	v := planner.DefaultV2Config()
	m.v2 = false
	if o.Planner != nil {
		switch *o.Planner {
		case "v2":
			m.v2 = true
		case "greedy_v1", "":
		default:
			msgs = append(msgs, "option hwc_planner must be greedy_v1 or v2; greedy_v1 applied")
		}
	}
	band := func(dst *int, s *string, name string) {
		if s == nil {
			return
		}
		if x, ok := core.ParseHHMM(*s); ok {
			*dst = x
		} else {
			msgs = append(msgs, "option "+name+" does not parse; default applied")
		}
	}
	band(&v.NightFrom, o.V2NightFrom, "hwc_v2_night_from")
	band(&v.NightTo, o.V2NightTo, "hwc_v2_night_to")
	band(&v.MiddayFrom, o.V2MiddayFrom, "hwc_v2_midday_from")
	band(&v.MiddayTo, o.V2MiddayTo, "hwc_v2_midday_to")
	if v.NightFrom == v.NightTo || v.MiddayFrom >= v.MiddayTo {
		msgs = append(msgs, "hwc_v2 bands are empty or inverted; defaults applied")
		d := planner.DefaultV2Config()
		v.NightFrom, v.NightTo, v.MiddayFrom, v.MiddayTo = d.NightFrom, d.NightTo, d.MiddayFrom, d.MiddayTo
	}
	if o.V2Topup != nil {
		v.Topup = *o.V2Topup
	}
	if o.V2FullDefaultM != nil && *o.V2FullDefaultM >= 60 && *o.V2FullDefaultM <= 300 {
		v.FullDefault = time.Duration(*o.V2FullDefaultM) * time.Minute
	}
	if o.V2FullMinM != nil {
		if *o.V2FullMinM >= 60 && *o.V2FullMinM <= 360 {
			v.FullFloor = time.Duration(*o.V2FullMinM) * time.Minute
		} else {
			msgs = append(msgs, "option hwc_v2_full_min_min out of range 60..360; default applied")
		}
	}
	if o.V2LearnFrom != nil {
		if *o.V2LearnFrom == "" {
			v.LearnFrom = time.Time{}
		} else if t, ok := parseLocalStamp(*o.V2LearnFrom, v.Loc); ok {
			v.LearnFrom = t
		} else {
			msgs = append(msgs, "option hwc_v2_learn_from does not parse (YYYY-MM-DD or YYYY-MM-DDTHH:MM local); default applied")
		}
	}
	if v.FullDefault < v.FullFloor {
		v.FullDefault = v.FullFloor
	}
	if o.V2TopupDefaultM != nil && *o.V2TopupDefaultM >= 15 && *o.V2TopupDefaultM <= 180 {
		v.TopupDefault = time.Duration(*o.V2TopupDefaultM) * time.Minute
	}
	if o.V2EarlyEndMin != nil && *o.V2EarlyEndMin >= 2 && *o.V2EarlyEndMin <= 60 {
		v.EarlyEnd = time.Duration(*o.V2EarlyEndMin) * time.Minute
	}
	if o.V2EveningTopup != nil {
		v.EveningTopup = *o.V2EveningTopup
	}
	if o.V2EveningGapH != nil && *o.V2EveningGapH >= 0 && *o.V2EveningGapH <= 12 {
		v.EveningGap = hours(*o.V2EveningGapH)
	}
	m.eveningDrp = 3
	if o.V2EveningDropC != nil && *o.V2EveningDropC > 0 && *o.V2EveningDropC <= 20 {
		m.eveningDrp = *o.V2EveningDropC
	}
	if m.v2 && c.GSinceReheat < c.OffLimitSinceReheatEnd {
		// Under v2 every window opens after a long OFF, so condition G
		// (a step is expected after >= 2 h OFF and >= 4 h since the last
		// reheat) would hold at every window start, and each satisfied
		// tank would cost an extra recycle (two moves) and a `suspect`.
		// G therefore waits until the tank clock itself has run out.
		c.GSinceReheat = c.OffLimitSinceReheatEnd
	}
	// A heating window with no call for heat restarts the tank clock, on
	// strict evidence (core/clock.go). v2 only; NOT approved yet: default off.
	if o.BootPlanWaitS != nil {
		c.BootPlanWait = time.Duration(*o.BootPlanWaitS) * time.Second
	}
	c.WindowStamp = m.v2 && o.V2WindowClock != nil && *o.V2WindowClock
	msgs = append(msgs, c.Clamp()...)
	if m.v2 && c.GSinceReheat < c.OffLimitSinceReheatEnd {
		c.GSinceReheat = c.OffLimitSinceReheatEnd
	}
	v.Loc, v.MaxMovesDay, v.MaxMovesHour, v.MoveReserve = c.Loc, c.MaxMovesDay, c.MaxMovesHour, c.MoveReserve

	if o.PriceAdderOre != nil {
		p.AdderOre = *o.PriceAdderOre
	}
	if o.VATFactor != nil && *o.VATFactor > 0 {
		p.VAT = *o.VATFactor
	}
	if o.PlannerMaxBlockMin != nil && *o.PlannerMaxBlockMin >= 30 && *o.PlannerMaxBlockMin <= 720 {
		p.MaxBlock = time.Duration(*o.PlannerMaxBlockMin) * time.Minute
	}
	if o.MaxBlocksPerPeriod != nil && *o.MaxBlocksPerPeriod >= 0 && *o.MaxBlocksPerPeriod <= 4 {
		p.MaxBlocksPer24h = *o.MaxBlocksPerPeriod
	}
	if o.MinSavingOre != nil {
		p.MinSavingOre = *o.MinSavingOre
	}
	for _, s := range msgs {
		log.Printf("%s: %s", Name, s)
	}
	v.Price = p
	m.cfg, m.pcfg, m.v2cfg = c, p, v
	return nil
}

// Init implements modules.Module.
func (m *Module) Init(persisted []byte) error {
	m.c = core.New(m.cfg, persisted, m.ent.Cover)
	if _, ok := core.LoadPersisted(persisted); !ok && len(persisted) > 0 {
		log.Printf("%s: WARNING persisted state unreadable; clocks unknown, no OFF until a finished reheat is seen or seeded", Name)
	}
	mode := "shadow"
	if m.cfg.Live {
		mode = "live (needs the HA arming boolean too)"
	}
	log.Printf("%s: mode %s", Name, mode)
	house.Default.SetHeater(m.HouseHeaterPlan) // read-only: the house planner never commands the heater
	return nil
}

// HouseHeaterPlan adapts HeaterSchedule for the house planner's shared
// budget (internal/house). Valid only once the controller has stepped.
func (m *Module) HouseHeaterPlan() house.HeaterPlan {
	s := m.HeaterSchedule()
	p := house.HeaterPlan{Valid: !s.At.IsZero(), NominalW: float64(s.NominalW)}
	for _, w := range s.Windows {
		p.Windows = append(p.Windows, house.HeaterWindow{Start: w.Start, End: w.End, On: w.On})
	}
	return p
}

// Persist implements modules.Module.
func (m *Module) Persist() ([]byte, error) { return m.c.Persist() }

// Controller exposes the core (tests, simulator).
func (m *Module) Controller() *core.Controller { return m.c }

func (m *Module) step(in core.Input) {
	if t := in.When(); t.After(m.now) {
		m.now = t
	}
	if !m.started {
		m.started = true
		m.apply(m.c.Step(core.Detector{At: in.When(), Present: m.signals}))
		if !m.signals {
			log.Printf("%s: hot_water is not enabled: staying in SHADOW (degraded:no_detector)", Name)
		}
	}
	m.apply(m.c.Step(in))
	s := m.c.Schedule()
	m.schedMu.Lock()
	m.sched = s
	m.schedMu.Unlock()
}

// HeaterSchedule implements core.HeaterScheduleSource: the next 24 h of
// planned heater ON/OFF windows and the heater's current state, as of the
// last step. Safe to call from another goroutine or module.
func (m *Module) HeaterSchedule() core.HeaterSchedule {
	m.schedMu.RLock()
	defer m.schedMu.RUnlock()
	s := m.sched
	s.Windows = append([]core.HeaterWindow(nil), s.Windows...)
	return s
}

var _ core.HeaterScheduleSource = (*Module)(nil)

func (m *Module) apply(effs []core.Effect) {
	for _, e := range effs {
		switch e.Kind {
		case core.EffCallService:
			m.effects = append(m.effects, modules.Effect{Kind: modules.EffectCallService, Domain: e.Domain, Service: e.Service, Data: e.Data, CorrID: e.CorrID})
		case core.EffPersistNow:
			m.effects = append(m.effects, modules.Effect{Kind: modules.EffectPersistNow})
		case core.EffNotify:
			m.notify(e)
		case core.EffPublish:
			attrs := ""
			if e.Attrs != nil {
				if b, err := json.Marshal(e.Attrs); err == nil {
					attrs = string(b)
				}
			}
			st := e.State
			if st == "" {
				st = "none"
			}
			m.outs = append(m.outs, modules.OutputUpdate{ObjectID: e.ObjectID, Time: m.now, StateString: st, AttributesJSON: attrs})
		case core.EffLog:
			log.Printf("%s: %s", Name, e.Msg)
		}
	}
}

// notify sends an alert to the phone and as a persistent notification
// (PLAN §2.5: a second channel). Their results are not fed to the core.
func (m *Module) notify(e core.Effect) {
	dom, svc, _ := strings.Cut(m.ent.NotifyService, ".")
	m.notifyC++
	m.effects = append(m.effects, modules.Effect{Kind: modules.EffectCallService, Domain: dom, Service: svc,
		Data: map[string]any{"title": e.Title, "message": e.Text}, CorrID: 1<<40 + m.notifyC})
	m.notifyC++
	m.effects = append(m.effects, modules.Effect{Kind: modules.EffectCallService, Domain: "persistent_notification", Service: "create",
		Data: map[string]any{"title": e.Title, "message": e.Text, "notification_id": "hot_water_controller_" + strings.ToLower(e.Code)}, CorrID: 1<<40 + m.notifyC})
}

func (m *Module) drainOuts() []modules.OutputUpdate {
	o := m.outs
	m.outs = nil
	return o
}

// OnStateUpdate implements modules.Module.
func (m *Module) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	if u.EntityID == m.ent.Price {
		if !u.Replay && len(u.Attrs) > 0 {
			m.prices = planner.ParseNordpool(u.Attrs, u.Time, m.cfg.Loc)
			m.replan(u.Time)
		}
		return m.drainOuts()
	}
	if u.EntityID == m.ent.TankTemp {
		m.recordTemp(u)
	}
	role, ok := m.roles[u.EntityID]
	if !ok && strings.HasPrefix(u.EntityID, m.ent.ConflictPrefix) {
		role, ok = core.RoleConflict, true
	}
	if !ok {
		return nil
	}
	in := core.EntityUpdate{At: u.Time, Role: role, Entity: u.EntityID, State: u.State, Num: u.Value, NumOK: u.Available, Replay: u.Replay}
	if role == core.RoleEV && u.Available {
		in.Num = u.Value * 1000 // loaver_power reports kW
	}
	if role == core.RoleCover {
		if raw, ok := u.Attrs["current_position"]; ok {
			var pos float64
			if json.Unmarshal(raw, &pos) == nil {
				in.Position, in.PositionOK = pos, true
			}
		}
	}
	m.step(in)
	return m.drainOuts()
}

// v2ReplanEvery is how often planner v2 re-plans when it drives the
// controller: it ends windows early and extends them while the heater
// draws, so it needs minute-scale freshness.
const v2ReplanEvery = time.Minute

// eveningDemand is the (unvalidated) evening draw-down proxy: while the
// heater is blocked its demand is invisible on the grid, so the only
// signal is the tank sensor's temperature falling by hwc_v2_evening_drop_c
// from its maximum over the previous 6 h.
func (m *Module) eveningDemand(now time.Time) bool {
	if !m.v2cfg.EveningTopup || len(m.tankTemps) == 0 {
		return false
	}
	last := m.tankTemps[len(m.tankTemps)-1]
	if now.Sub(last.t) > 45*time.Minute {
		return false
	}
	max := last.v
	for _, s := range m.tankTemps {
		if now.Sub(s.t) <= 6*time.Hour && s.v > max {
			max = s.v
		}
	}
	return max-last.v >= m.eveningDrp
}

func (m *Module) recordTemp(u modules.StateUpdate) {
	if !u.Available || u.Replay {
		return
	}
	m.tankTemps = append(m.tankTemps, tempSample{u.Time, u.Value})
	for len(m.tankTemps) > 0 && u.Time.Sub(m.tankTemps[0].t) > 8*time.Hour {
		m.tankTemps = m.tankTemps[1:]
	}
}

// replan runs the planner(s) on the known prices and hands the core the
// plan. greedy_v1 is always computed (it is the baseline of v2's cost
// comparison). With hwc_planner v2 the core gets v2's plan; otherwise it
// gets greedy_v1's and v2 runs as a SHADOW, published on
// sensor.hot_water_controller_plan_v2 only.
func (m *Module) replan(now time.Time) {
	m.lastPlanAt = now
	if len(m.prices) == 0 || m.testOnly {
		m.step(core.PlanUpdate{At: now, Plan: nil})
		return
	}
	facts := m.c.Facts()
	greedy := planner.Plan(now, m.prices, m.pcfg, facts)
	in := planner.V2Input{Now: now, Prices: m.prices, Facts: facts, EveningDemand: m.eveningDemand(now),
		GreedyOff: greedy.OffPeriods, TankHeadroom: facts.TankHeadroom}
	if m.v2 {
		res := planner.PlanV2(in, m.v2cfg)
		var alerts []core.PlanAlert
		if facts.Live {
			alerts = res.Alerts
		}
		m.step(core.PlanUpdate{At: now, Plan: res.Plan, Alerts: alerts})
		return
	}
	m.step(core.PlanUpdate{At: now, Plan: greedy})
	// Shadow: v2 with its own window memory, never given to the core.
	in.Facts.Windows = m.shadowWin
	res := planner.PlanV2(in, m.v2cfg)
	m.shadowWin = res.Plan.Windows
	m.publishShadow(now, res, greedy)
}

// publishShadow publishes v2's would-be plan on the comparison sensor.
func (m *Module) publishShadow(now time.Time, res planner.V2Result, greedy *core.Plan) {
	attrs := map[string]any{"planner": planner.V2ID, "shadow": true, "active_planner": greedy.PlannerID,
		"generated_at": now.In(m.cfg.Loc).Format(time.RFC3339)}
	for k, v := range res.Plan.Extra {
		attrs[k] = v
	}
	var offs []map[string]any
	for _, p := range res.Plan.OffPeriods {
		if len(offs) >= 6 {
			break
		}
		offs = append(offs, map[string]any{"start": p.Start.In(m.cfg.Loc).Format(time.RFC3339), "end": p.End.In(m.cfg.Loc).Format(time.RFC3339)})
	}
	attrs["off_periods"] = offs
	b, err := json.Marshal(attrs)
	if err != nil || len(b) > 15000 {
		return
	}
	n := 0
	for _, w := range res.Plan.Windows {
		if w.End.After(now) {
			n++
		}
	}
	state := fmt.Sprintf("%d", n)
	sig := state + "|" + stripGenerated(string(b))
	if sig == m.lastShadowSig && now.Sub(m.lastShadowAt) < 15*time.Minute {
		return
	}
	m.lastShadowSig, m.lastShadowAt = sig, now
	m.outs = append(m.outs, modules.OutputUpdate{ObjectID: ObjPlanV2, Time: now, StateString: state, AttributesJSON: string(b)})
}

// stripGenerated removes the generated_at stamp so an unchanged plan is not
// republished every minute.
func stripGenerated(s string) string {
	i := strings.Index(s, `"generated_at":"`)
	if i < 0 {
		return s
	}
	j := strings.Index(s[i+16:], `"`)
	if j < 0 {
		return s
	}
	return s[:i] + s[i+16+j+1:]
}

// OnTick implements modules.Ticker.
func (m *Module) OnTick(now time.Time) []modules.OutputUpdate {
	every := 15 * time.Minute
	if m.v2 {
		every = v2ReplanEvery
	}
	if m.lastPlanAt.IsZero() || now.Sub(m.lastPlanAt) >= every {
		m.replan(now)
	}
	m.step(core.Tick{At: now})
	return m.drainOuts()
}

// Effects implements modules.Actor.
func (m *Module) Effects() []modules.Effect {
	e := m.effects
	m.effects = nil
	return e
}

// OnCommandResult implements modules.Actor.
func (m *Module) OnCommandResult(r modules.CommandResult) []modules.OutputUpdate {
	if r.CorrID >= 1<<40 {
		if !r.OK {
			log.Printf("%s: notification call failed: %s", Name, r.Err)
		}
		return nil
	}
	m.step(core.CommandResult{At: r.At, CorrID: r.CorrID, OK: r.OK, Err: r.Err})
	return m.drainOuts()
}

// OnLink implements modules.LinkAware.
func (m *Module) OnLink(up bool, now time.Time) []modules.OutputUpdate {
	m.step(core.Link{At: now, Up: up})
	return m.drainOuts()
}

// SignalsConnected implements modules.SignalSink.
func (m *Module) SignalsConnected() { m.signals = true }

// DrainOutputs implements modules.SignalSink.
func (m *Module) DrainOutputs() []modules.OutputUpdate { return m.drainOuts() }

// OnSignal implements modules.SignalSink: hot_water's in-process signals.
func (m *Module) OnSignal(s any) {
	switch v := s.(type) {
	case hotwater.ResidualSample:
		m.step(core.Sample{At: v.T, W: v.W, Available: v.Available, DiscSeq: v.DiscontinuitySeq, Replay: v.Replay})
	case hotwater.Discontinuity:
		m.step(core.Discontinuity{At: v.T, Entity: v.Entity, DeltaW: v.DeltaW, Seq: v.Seq, Replay: v.Replay})
	case hotwater.CycleOpened:
		m.step(core.CycleOpened{At: v.OnTime, OnTime: v.OnTime, Magnitude: v.Magnitude, Replay: v.Replay})
	case hotwater.CycleClosed:
		m.step(core.CycleClosed{At: v.OffTime, OnTime: v.OnTime, OffTime: v.OffTime, Accepted: v.Accepted, Replay: v.Replay})
		if m.v2 && !v.Replay && !m.lastPlanAt.IsZero() {
			m.replan(m.now) // a reheat just finished: the window may end early
		}
	case hotwater.MergedStep:
		// Logged only: verification uses the controller's own step test.
	}
}

// SeedRequest implements host.Seeder: 24 h of detector output when the
// persisted clocks are unknown (PLAN §4.4).
func (m *Module) SeedRequest(now time.Time) ([]string, time.Time, bool) {
	if !m.c.SeedWanted() {
		return nil, time.Time{}, false
	}
	return []string{hotwater.OutPowerObjectID, hotwater.OutEnergyObjectID}, now.Add(-24 * time.Hour), true
}

// OnSeed implements host.Seeder.
func (m *Module) OnSeed(ups []modules.StateUpdate) {
	var pw, en []core.SeedPoint
	at := m.now
	for _, u := range ups {
		pt := core.SeedPoint{T: u.Time, V: u.Value, OK: u.Available}
		switch u.EntityID {
		case hotwater.OutPowerObjectID:
			pw = append(pw, pt)
		case hotwater.OutEnergyObjectID:
			en = append(en, pt)
		}
		if u.Time.After(at) {
			at = u.Time
		}
	}
	cycles := core.SeedFromHistory(pw, en)
	log.Printf("%s: seed: %d detector cycles in the last 24 h", Name, len(cycles))
	m.step(core.Seed{At: at, Cycles: cycles})
}

// String is for logs.
func (m *Module) String() string { return fmt.Sprintf("%s(%+v)", Name, m.ent) }
