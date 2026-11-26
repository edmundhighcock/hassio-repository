package blanketcontrol

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hpcontrol"
)

// Name is the registry key (the add-on's `modules` option).
const Name = "blanket_control"

// Buttons (button.<id>).
const (
	ButtonReplan   = "blanket_replan"
	ButtonStartNow = "blanket_start_now"
)

// Output object ids (sensor.<id>).
const (
	ObjState    = "blanket_controller_state"
	ObjPlan     = "blanket_plan"
	ObjModel    = "blanket_model"
	ObjPresses  = "blanket_presses_today"
	ObjStarts   = "blanket_starts_today"
	ObjCostDay  = "blanket_cost_today"
	ObjCostMon  = "blanket_cost_month"
	ObjLast     = "blanket_last_start"
	ObjHumidity = "blanket_humidity"
	ObjDew      = "blanket_dew_point"
)

// Entities.
const (
	ArmedEntity    = "input_boolean.blanket_controller_armed"
	DeadmanEntity  = "timer.blanket_deadman"
	defaultTemp    = "dahlia_storage_temperature"
	defaultShed    = "tool_shed_temperature"
	defaultOutdoor = "outdoor_temperature_best"
	defaultHum     = "dahlia_storage_humidity"
	defaultPower   = "tool_shed_heater_power"
	defaultPlug    = "switch.tool_shed_heater"
	defaultBot     = "switch.dahlia_blanket_bot"
	defaultPrice   = "nordpool_kwh_se3_sek_3_06_0"
	defaultFcast   = "weather.smhi_home"
	defaultNotify  = "notify.mobile_app_edmund15max"

	silentAfter  = 2 * time.Hour // temperature sensor silent this long: fail-safe
	bootGrace    = 10 * time.Minute
	forecastCorr = uint64(2) << 40
	notifyCorr   = uint64(1) << 40
)

func init() { modules.Register(Name, func() modules.Module { return New() }) }

type options struct {
	Mode      *string  `json:"bc_mode"`
	Floor     *float64 `json:"bc_floor_c"`
	Backstop  *float64 `json:"bc_backstop_c"`
	Target    *float64 `json:"bc_target_c"`
	Max       *float64 `json:"bc_max_c"`
	AlarmHigh *float64 `json:"bc_alarm_high_c"`
	ConfirmS  *int     `json:"bc_confirm_s"`
	OnWatts   *float64 `json:"bc_on_watts"`
	Tries     *int     `json:"bc_start_tries"`
	MinOn     *int     `json:"bc_min_on_min"`
	MaxStarts *int     `json:"bc_max_starts_day"`
	Notify    *string  `json:"bc_notify_service"`
	PowerW    *float64 `json:"bc_power_w"`
	StartPen  *float64 `json:"bc_start_penalty_kr"`
	ModelU    *float64 `json:"bc_model_u"`
	ModelH    *float64 `json:"bc_model_h"`
	ModelG    *float64 `json:"bc_model_g"`
	Forecast  *string  `json:"bc_forecast_entity"`
	TempEnt   *string  `json:"bc_temp_entity"`
	ShedEnt   *string  `json:"bc_shed_temp_entity"`
	HumEnt    *string  `json:"bc_humidity_entity"`
	OutEnt    *string  `json:"bc_outdoor_entity"`
	PowerEnt  *string  `json:"bc_power_entity"`
	PlugEnt   *string  `json:"bc_plug_entity"`
	BotEnt    *string  `json:"bc_bot_entity"`
	HotEnt    *string  `json:"bc_hot_sensor"`
	HotMax    *float64 `json:"bc_hot_max_c"`
	HotAlarm  *float64 `json:"bc_hot_alarm_c"`
	RHRef     *float64 `json:"bc_rh_ref_c"`
	RHLow     *float64 `json:"bc_rh_low"`
	RHHigh    *float64 `json:"bc_rh_high"`
	PriceEnt  *string  `json:"hwc_price_entity"`
	AdderOre  *float64 `json:"hwc_price_adder_ore"`
	VAT       *float64 `json:"hwc_vat_factor"`
}

type reading struct {
	v    float64
	ok   bool // last state was numeric
	at   time.Time
	seen bool
}

type dayRec struct {
	Kr      float64 `json:"kr"`
	KWh     float64 `json:"kwh"`
	Starts  int     `json:"starts"`
	Presses int     `json:"presses"`
}

type persisted struct {
	Prices  map[int64]float64  `json:"prices,omitempty"` // quarter start (unix) -> SEK/kWh ex VAT
	Recs    []Rec              `json:"recs,omitempty"`
	Days    map[string]*dayRec `json:"days,omitempty"`
	LastAdv time.Time          `json:"last_adv,omitempty"`
	LastT   *float64           `json:"last_t,omitempty"`
	LastTAt time.Time          `json:"last_t_at,omitempty"`
	Exec    execPersist        `json:"exec"`
	Alarms  map[string]*alarm  `json:"alarms,omitempty"`
}

// Module is the blanket_control module.
type Module struct {
	p        Params
	defModel hpcontrol.Model
	live     bool
	notify   string
	onW      float64
	confirm  time.Duration
	tries    int

	alarmHigh          float64
	hotMax, hotAlarm   float64
	hotEnt             string
	hot                reading
	backstop           float64
	rhRef, rhLow, rhHi float64

	tempEnt, shedEnt, outEnt, humEnt, powerEnt, plugEnt, botEnt, priceEnt, fcastEnt string

	t, shed, out, hum, pw reading
	plugState             string
	plugAt                time.Time
	armedKey              bool

	prices map[int64]float64
	now    time.Time
	bootAt time.Time

	forecast      map[int64]float64
	forecastAt    time.Time
	forecastReqAt time.Time
	forecastBusy  bool

	plan       Result
	havePlan   bool
	planAt     time.Time
	lastPlanAt time.Time
	fit        Fit
	recs       []Rec
	hystOn     bool

	// integration / history
	lastAdv     time.Time
	slotStart   time.Time
	uSecs, secs float64
	days        map[string]*dayRec

	alarms map[string]*alarm
	x      exec
	sig    map[string]string
}

// New returns a module with defaults.
func New() *Module {
	m := &Module{p: DefaultParams(), defModel: DefaultModel(), notify: defaultNotify, onW: 30, confirm: 150 * time.Second, tries: 3,
		alarmHigh: 8, backstop: 3.0, hotMax: 8, hotAlarm: 10, rhRef: 5, rhLow: 60, rhHi: 95,
		tempEnt: defaultTemp, shedEnt: defaultShed, outEnt: defaultOutdoor, humEnt: defaultHum, powerEnt: defaultPower,
		plugEnt: defaultPlug, botEnt: defaultBot, priceEnt: defaultPrice, fcastEnt: defaultFcast,
		prices: map[int64]float64{}, forecast: map[int64]float64{}, days: map[string]*dayRec{},
		alarms: map[string]*alarm{}, sig: map[string]string{}, x: newExec()}
	m.fit = Fit{U: m.defModel.U, H: m.defModel.Boost5, G: m.defModel.Gain}
	return m
}

// Name implements modules.Module.
func (m *Module) Name() string { return Name }

func trimSensor(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "sensor.") }

// Configure implements modules.Configurable. Every key optional; an invalid
// value is replaced by the default (logged).
func (m *Module) Configure(raw []byte) error {
	var o options
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
	}
	p := DefaultParams()
	bad := func(k string, def any) { log.Printf("%s: option %s out of range; default %v applied", Name, k, def) }
	m.live = o.Mode != nil && *o.Mode == "live"
	if o.Notify != nil && strings.HasPrefix(*o.Notify, "notify.") {
		m.notify = *o.Notify
	}
	if o.Floor != nil {
		if *o.Floor >= 0 && *o.Floor <= 12 {
			p.FloorC = *o.Floor
		} else {
			bad("bc_floor_c", p.FloorC)
		}
	}
	m.backstop = math.Min(3.0, p.FloorC-0.5)
	if o.Backstop != nil {
		if *o.Backstop >= -5 && *o.Backstop < p.FloorC {
			m.backstop = *o.Backstop
		} else {
			bad("bc_backstop_c", m.backstop)
		}
	}
	p.TargetC = math.Max(4.5, p.FloorC+0.5)
	if o.Target != nil {
		if *o.Target >= p.FloorC && *o.Target <= 12 {
			p.TargetC = *o.Target
		} else {
			bad("bc_target_c", p.TargetC)
		}
	}
	p.MaxC = math.Max(6, p.TargetC+1)
	if o.Max != nil {
		if *o.Max > p.TargetC && *o.Max <= 15 {
			p.MaxC = *o.Max
		} else {
			bad("bc_max_c", p.MaxC)
		}
	}
	m.alarmHigh = math.Max(8, p.MaxC+1)
	if o.AlarmHigh != nil {
		if *o.AlarmHigh > p.MaxC && *o.AlarmHigh <= 25 {
			m.alarmHigh = *o.AlarmHigh
		} else {
			bad("bc_alarm_high_c", m.alarmHigh)
		}
	}
	if o.ConfirmS != nil {
		if *o.ConfirmS >= 30 && *o.ConfirmS <= 600 {
			m.confirm = time.Duration(*o.ConfirmS) * time.Second
		} else {
			bad("bc_confirm_s", 150)
		}
	}
	if o.OnWatts != nil {
		if *o.OnWatts >= 1 && *o.OnWatts <= 500 {
			m.onW = *o.OnWatts
		} else {
			bad("bc_on_watts", 30)
		}
	}
	if o.Tries != nil {
		if *o.Tries >= 1 && *o.Tries <= 10 {
			m.tries = *o.Tries
		} else {
			bad("bc_start_tries", 3)
		}
	}
	if o.MinOn != nil {
		if *o.MinOn >= 0 && *o.MinOn <= 165 {
			p.MinOnSlots = (*o.MinOn + 14) / 15
		} else {
			bad("bc_min_on_min", 60)
		}
	}
	if o.MaxStarts != nil {
		if *o.MaxStarts >= 1 && *o.MaxStarts <= 48 {
			p.MaxStartsDay = *o.MaxStarts
		} else {
			bad("bc_max_starts_day", 12)
		}
	}
	if o.PowerW != nil {
		if *o.PowerW >= 10 && *o.PowerW <= 2000 {
			p.PowerW = *o.PowerW
		} else {
			bad("bc_power_w", 70)
		}
	}
	if o.StartPen != nil && *o.StartPen >= 0 && *o.StartPen <= 100 {
		p.StartPenaltyKr = *o.StartPen
	}
	if o.ModelU != nil && *o.ModelU > 0 && *o.ModelU <= 2 {
		p.Model.U = *o.ModelU
	}
	if o.ModelH != nil && *o.ModelH > 0 && *o.ModelH <= 10 {
		p.Model.Boost5 = *o.ModelH
	}
	if o.ModelG != nil && *o.ModelG >= -0.3 && *o.ModelG <= 0.3 {
		p.Model.Gain = *o.ModelG
	}
	if o.HotEnt != nil {
		m.hotEnt = trimSensor(*o.HotEnt) // "" = no hot-spot sensor
	}
	if o.HotMax != nil {
		if *o.HotMax > 0 && *o.HotMax <= 30 {
			m.hotMax = *o.HotMax
		} else {
			bad("bc_hot_max_c", 8)
		}
	}
	if o.HotAlarm != nil {
		if *o.HotAlarm > 0 && *o.HotAlarm <= 40 {
			m.hotAlarm = *o.HotAlarm
		} else {
			bad("bc_hot_alarm_c", 10)
		}
	}
	if o.RHRef != nil && *o.RHRef >= -5 && *o.RHRef <= 20 {
		m.rhRef = *o.RHRef
	}
	if o.RHLow != nil && *o.RHLow > 0 && *o.RHLow < 100 {
		m.rhLow = *o.RHLow
	}
	if o.RHHigh != nil && *o.RHHigh > m.rhLow && *o.RHHigh <= 150 {
		m.rhHi = *o.RHHigh
	}
	if o.AdderOre != nil {
		p.Price.AdderOre = *o.AdderOre
	}
	if o.VAT != nil && *o.VAT > 0 {
		p.Price.VAT = *o.VAT
	}
	set := func(dst *string, src *string, strip bool) {
		if src != nil && strings.TrimSpace(*src) != "" {
			if strip {
				*dst = trimSensor(*src)
			} else {
				*dst = strings.TrimSpace(*src)
			}
		}
	}
	set(&m.tempEnt, o.TempEnt, true)
	set(&m.shedEnt, o.ShedEnt, true)
	set(&m.humEnt, o.HumEnt, true)
	set(&m.outEnt, o.OutEnt, true)
	set(&m.powerEnt, o.PowerEnt, true)
	set(&m.priceEnt, o.PriceEnt, true)
	set(&m.plugEnt, o.PlugEnt, false)
	set(&m.botEnt, o.BotEnt, false)
	if o.Forecast != nil {
		m.fcastEnt = strings.TrimSpace(*o.Forecast) // "" disables
	}
	m.defModel = p.Model
	m.p = p
	m.fit = Fit{U: p.Model.U, H: p.Model.Boost5, G: p.Model.Gain}
	return nil
}

// Inputs implements modules.Module.
func (m *Module) Inputs() []modules.InputSpec {
	in := []modules.InputSpec{
		{EntityID: m.tempEnt}, {EntityID: m.shedEnt}, {EntityID: m.outEnt}, {EntityID: m.humEnt},
		{EntityID: m.powerEnt}, {EntityID: m.plugEnt}, {EntityID: ArmedEntity},
		{EntityID: m.priceEnt, Attributes: []string{"today", "tomorrow", "raw_today", "raw_tomorrow", "tomorrow_valid"}},
	}
	if m.hotEnt != "" {
		in = append(in, modules.InputSpec{EntityID: m.hotEnt})
	}
	return in
}

// Outputs implements modules.Module.
func (m *Module) Outputs() []modules.OutputSpec {
	mk := func(obj, name, unit, class string) modules.OutputSpec {
		return modules.OutputSpec{ObjectID: obj, UniqueID: "virtual_sensors_" + obj, Name: name, Unit: unit, StateClass: class, HasAttributes: true}
	}
	av := func(o modules.OutputSpec) modules.OutputSpec { o.PerEntityAvailability = true; return o }
	last := mk(ObjLast, "Blanket last start", "", "")
	last.DeviceClass = "timestamp"
	return []modules.OutputSpec{
		mk(ObjState, "Blanket controller state", "", ""),
		mk(ObjPlan, "Blanket plan", "kWh", ""),
		av(mk(ObjModel, "Blanket model error", "°C", "measurement")),
		mk(ObjPresses, "Blanket presses today", "", "measurement"),
		mk(ObjStarts, "Blanket starts today", "", "measurement"),
		mk(ObjCostDay, "Blanket cost today", "kr", ""),
		mk(ObjCostMon, "Blanket cost this month", "kr", ""),
		av(last),
		av(mk(ObjHumidity, "Blanket storage RH at reference", "%", "measurement")),
		av(mk(ObjDew, "Blanket storage dew point", "°C", "measurement")),
	}
}

// Buttons implements modules.Buttoned.
func (m *Module) Buttons() []modules.ButtonSpec {
	return []modules.ButtonSpec{
		{ObjectID: ButtonReplan, UniqueID: "virtual_sensors_" + ButtonReplan, Name: "Replan blanket"},
		{ObjectID: ButtonStartNow, UniqueID: "virtual_sensors_" + ButtonStartNow, Name: "Blanket start now"},
	}
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
			m.recs = ps.Recs
			if ps.Days != nil {
				m.days = ps.Days
			}
			m.lastAdv = ps.LastAdv
			if !m.lastAdv.IsZero() {
				m.slotStart = m.lastAdv.Truncate(quarter)
			}
			if ps.LastT != nil {
				m.t = reading{v: *ps.LastT, ok: false, at: ps.LastTAt, seen: true}
			}
			m.x.st = ps.Exec
			m.restoreExec()
			if ps.Alarms != nil {
				m.alarms = ps.Alarms
			}
			m.refit()
		}
	}
	if m.live {
		log.Printf("%s: mode live (needs %s on too)", Name, ArmedEntity)
	} else {
		log.Printf("%s: mode shadow: reads Home Assistant, publishes sensors, calls no service", Name)
	}
	return nil
}

// Persist implements modules.Module.
func (m *Module) Persist() ([]byte, error) {
	ps := persisted{Prices: m.prices, Recs: m.recs, Days: m.days, LastAdv: m.lastAdv, Exec: m.persistExec(), Alarms: m.alarms}
	if m.t.seen {
		v := m.t.v
		ps.LastT, ps.LastTAt = &v, m.t.at
	}
	return json.Marshal(ps)
}

// InitialStates implements modules.InitialStater.
func (m *Module) InitialStates() []modules.OutputUpdate { return m.publishAll(m.now, true) }

func (m *Module) armedLive() bool { return m.live && m.armedKey }

func (m *Module) priceKr(sek float64) float64 { return m.p.Price.AllInOre(sek) / 100 }

func (m *Module) dayKey(t time.Time) string { return t.In(m.p.Loc).Format("2006-01-02") }

func (m *Module) day(t time.Time) *dayRec {
	k := m.dayKey(t)
	d := m.days[k]
	if d == nil {
		d = &dayRec{}
		m.days[k] = d
		if len(m.days) > 70 {
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

// ---- readings ----

func (m *Module) tempFresh(now time.Time) bool {
	return m.t.ok && !m.t.at.IsZero() && now.Sub(m.t.at) <= silentAfter
}

// hotFresh: a hot-spot sensor is configured and fresh.
func (m *Module) hotFresh(now time.Time) bool {
	return m.hotEnt != "" && m.hot.ok && now.Sub(m.hot.at) <= silentAfter
}

func (m *Module) outdoorNow(now time.Time) (float64, bool) {
	if m.out.ok && now.Sub(m.out.at) <= 3*time.Hour {
		return m.out.v, true
	}
	if m.forecastFresh(now) {
		return hpcontrol.OutdoorAt(m.forecast, 0, now), true
	}
	return 0, false
}

func (m *Module) forecastFresh(now time.Time) bool {
	return len(m.forecast) > 0 && !m.forecastAt.IsZero() && now.Sub(m.forecastAt) <= 3*time.Hour
}

func (m *Module) powerW() float64 {
	if !m.pw.ok || m.plugState == "off" || m.plugState == "unavailable" || m.plugState == "unknown" {
		return 0
	}
	return math.Max(0, m.pw.v)
}

// OnStateUpdate implements modules.Module.
func (m *Module) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	if u.Time.After(m.now) {
		m.now = u.Time
	}
	if m.bootAt.IsZero() {
		m.bootAt = u.Time
	}
	r := reading{v: u.Value, ok: u.Available, at: u.Time, seen: true}
	switch u.EntityID {
	case m.tempEnt:
		m.advance(u.Time)
		if u.Available {
			m.x.silentStarts = 0
			m.t = r
		} else {
			m.t.ok, m.t.at, m.t.seen = false, u.Time, true
		}
	case m.shedEnt:
		m.shed = r
	case m.hotEnt:
		m.hot = r
	case m.outEnt:
		m.advance(u.Time)
		m.out = r
	case m.humEnt:
		m.hum = r
	case m.powerEnt:
		m.advance(u.Time)
		m.pw = r
		m.onPower(u)
	case m.plugEnt:
		m.advance(u.Time)
		prev := m.plugState
		m.plugState, m.plugAt = u.State, u.Time
		m.onPlug(prev, u)
	case ArmedEntity:
		m.armedKey = u.State == "on"
	case m.priceEnt:
		if len(u.Attrs) > 0 {
			for _, pr := range plannerParse(u) {
				m.prices[pr.k] = pr.v
			}
			m.prunePrices(u.Time)
			if !u.Replay {
				m.lastPlanAt = time.Time{}
			}
		}
	default:
		return nil
	}
	if u.Replay {
		return nil
	}
	return m.publishAll(u.Time, false)
}

func (m *Module) prunePrices(now time.Time) {
	cut := now.Add(-72 * time.Hour).Unix()
	for k := range m.prices {
		if k < cut {
			delete(m.prices, k)
		}
	}
}

// advance integrates power into cost and 15-minute history records up to t
// (hold-last values), never going backwards and never across a >30 min gap.
func (m *Module) advance(t time.Time) {
	if m.lastAdv.IsZero() || t.Sub(m.lastAdv) > 30*time.Minute {
		m.lastAdv, m.slotStart, m.uSecs, m.secs = t, t.Truncate(quarter), 0, 0
		return
	}
	for m.lastAdv.Before(t) {
		end := m.slotStart.Add(quarter)
		if t.Before(end) {
			end = t
		}
		dt := end.Sub(m.lastAdv)
		w := m.powerW()
		u := math.Min(1, w/m.p.PowerW)
		m.uSecs += u * dt.Seconds()
		m.secs += dt.Seconds()
		if sek, ok := m.prices[m.lastAdv.Truncate(quarter).Unix()]; ok && w > 0 {
			kwh := w / 1000 * dt.Hours()
			d := m.day(m.lastAdv)
			d.KWh += kwh
			d.Kr += kwh * m.priceKr(sek)
		}
		m.lastAdv = end
		if !end.Before(m.slotStart.Add(quarter)) {
			m.closeSlot(end)
			m.slotStart = end
		}
	}
}

func (m *Module) closeSlot(end time.Time) {
	secs, us := m.secs, m.uSecs
	m.secs, m.uSecs = 0, 0
	start := end.Add(-quarter)
	if secs < 0.8*quarter.Seconds() {
		return
	}
	to, ok := m.outdoorNow(end)
	if !ok {
		return
	}
	tm := Missing
	if m.t.ok && end.Sub(m.t.at) <= 30*time.Minute {
		tm = m.t.v
	}
	if n := len(m.recs); n > 0 && m.recs[n-1].T >= start.Unix() {
		return
	}
	m.recs = append(m.recs, Rec{T: start.Unix(), Tm: tm, To: to, U: us / secs})
	if len(m.recs) > MaxRecs {
		m.recs = m.recs[len(m.recs)-MaxRecs:]
	}
	m.refit()
}

func (m *Module) refit() {
	m.fit = FitModel(m.recs, m.defModel)
	m.p.Model.U, m.p.Model.Boost5, m.p.Model.Gain = m.fit.U, m.fit.H, m.fit.G
	m.p.MarginC = 0.5
	if m.fit.Fitted {
		m.p.MarginC = math.Max(0.5, math.Min(2, 2*m.fit.MAE))
	}
}

// ---- planning ----

func (m *Module) replan(now time.Time) {
	m.lastPlanAt = now
	m.havePlan = false
	if !m.tempFresh(now) || len(m.prices) == 0 {
		return
	}
	tout, ok := m.outdoorNow(now)
	if !ok {
		return
	}
	pr := make(map[int64]float64, len(m.prices))
	for k, v := range m.prices {
		pr[k] = m.priceKr(v)
	}
	in := Input{Now: now, T0: m.t.v, Tout: tout, Prices: pr, StartsToday: m.day(now).Starts}
	if m.forecastFresh(now) {
		in.Forecast = m.forecast
	}
	in.InitAge, in.InitMature = m.initAge(now)
	m.plan = Plan(in, m.p)
	m.havePlan = !m.plan.NoPrices && len(m.plan.Slots) > 0
	m.planAt = now
}

func (m *Module) initAge(now time.Time) (int, bool) {
	x := &m.x
	switch {
	case x.phase != phIdle:
		return 1, false
	case x.on && now.Before(x.onSince.Add(windowLen)):
		a := int(math.Ceil(now.Sub(x.onSince).Seconds() / quarter.Seconds()))
		if a < 1 {
			a = 1
		}
		if a > WindowSlots {
			a = WindowSlots
		}
		return a, !now.Before(x.minOnEnd)
	}
	return 0, false
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

// ---- forecast ----

func (m *Module) requestForecast(now time.Time) {
	if m.fcastEnt == "" || !m.x.linkUp {
		return
	}
	if m.forecastBusy && now.Sub(m.forecastReqAt) < 5*time.Minute {
		return
	}
	if !m.forecastReqAt.IsZero() && now.Sub(m.forecastReqAt) < 30*time.Minute {
		return
	}
	m.forecastBusy, m.forecastReqAt = true, now
	m.x.fcorr++
	m.x.effects = append(m.x.effects, modules.Effect{Kind: modules.EffectCallService, Domain: "weather",
		Service: "get_forecasts", Data: map[string]any{"entity_id": m.fcastEnt, "type": "hourly"},
		CorrID: forecastCorr + m.x.fcorr, ReturnResponse: true})
}

// ---- buttons, tick ----

// OnButton implements modules.Buttoned.
func (m *Module) OnButton(objectID string, now time.Time) []modules.OutputUpdate {
	if now.After(m.now) {
		m.now = now
	}
	switch objectID {
	case ButtonReplan:
		log.Printf("%s: replan requested (button)", Name)
		m.replan(now)
		return m.publishAll(now, true)
	case ButtonStartNow:
		m.startNow(now)
		return m.publishAll(now, true)
	}
	return nil
}

// OnTick implements modules.Ticker.
func (m *Module) OnTick(now time.Time) []modules.OutputUpdate {
	if now.After(m.now) {
		m.now = now
	}
	if m.bootAt.IsZero() {
		m.bootAt = now
	}
	m.advance(now)
	m.requestForecast(now)
	m.alarmsTick(now)
	re := m.lastPlanAt.IsZero() || now.Truncate(quarter) != m.lastPlanAt.Truncate(quarter)
	if re {
		m.replan(now)
	}
	m.control(now)
	return m.publishAll(now, re)
}

// ---- outputs ----

func jsonStr(v any) string { b, _ := json.Marshal(v); return string(b) }
func round(v float64, n int) float64 {
	p := math.Pow10(n)
	return math.Round(v*p) / p
}

func (m *Module) emit(outs *[]modules.OutputUpdate, force bool, u modules.OutputUpdate) {
	key := fmt.Sprintf("%v|%v|%s|%s", u.Value, u.Unavailable, u.StateString, u.AttributesJSON)
	if !force && m.sig[u.ObjectID] == key {
		return
	}
	m.sig[u.ObjectID] = key
	*outs = append(*outs, u)
}

func (m *Module) publishAll(now time.Time, force bool) []modules.OutputUpdate {
	if now.IsZero() {
		return nil
	}
	var outs []modules.OutputUpdate
	st, reason, would := m.stateOf(now)
	attrs := map[string]any{"reason": reason, "mode": m.modeString(), "armed_key": m.armedKey, "armed_live": m.armedLive(),
		"would_state": would, "plug": m.plugState, "power_w": round(m.powerW(), 1), "believed_on": m.x.on,
		"on_window_ends": m.fmtT(m.x.windowEnd), "min_on_until": m.fmtT(m.x.minOnEnd), "try": m.x.tries, "failed": m.x.failed,
		"temp_c": m.tempAttr(), "shed_temp_c": m.shedAttr(), "silent": !m.tempFresh(now), "desire_kind": m.x.desired.Kind,
		"last_command": m.x.last.Cmd, "last_command_result": m.x.last.Result, "last_error": m.x.last.Err,
		"max_c": m.p.MaxC, "target_c": m.p.TargetC, "floor_c": m.p.FloorC, "backstop_c": m.backstop,
		"deadman_postponed_at": m.fmtT(m.x.deadmanOK), "hot_spot_c": m.hotAttr(now), "alarms": m.activeAlarms()}
	m.emit(&outs, force, modules.OutputUpdate{ObjectID: ObjState, Time: now, StateString: st, AttributesJSON: jsonStr(attrs)})
	d := m.day(now)
	m.emit(&outs, force, modules.OutputUpdate{ObjectID: ObjPresses, Time: now, Value: float64(d.Presses)})
	m.emit(&outs, force, modules.OutputUpdate{ObjectID: ObjStarts, Time: now, Value: float64(d.Starts),
		AttributesJSON: jsonStr(map[string]any{"cap": m.p.MaxStartsDay, "last_kind": m.x.st.LastKind})})
	mon := 0.0
	mkwh := 0.0
	pfx := now.In(m.p.Loc).Format("2006-01")
	for k, v := range m.days {
		if strings.HasPrefix(k, pfx) {
			mon += v.Kr
			mkwh += v.KWh
		}
	}
	m.emit(&outs, force, modules.OutputUpdate{ObjectID: ObjCostDay, Time: now, Value: round(d.Kr, 3),
		AttributesJSON: jsonStr(map[string]any{"kwh_today": round(d.KWh, 3)})})
	m.emit(&outs, force, modules.OutputUpdate{ObjectID: ObjCostMon, Time: now, Value: round(mon, 2),
		AttributesJSON: jsonStr(map[string]any{"kwh_month": round(mkwh, 2)})})
	if m.x.st.LastStart.IsZero() {
		m.emit(&outs, force, modules.OutputUpdate{ObjectID: ObjLast, Time: now, Unavailable: true})
	} else {
		m.emit(&outs, force, modules.OutputUpdate{ObjectID: ObjLast, Time: now, StateString: m.x.st.LastStart.UTC().Format(time.RFC3339),
			AttributesJSON: jsonStr(map[string]any{"kind": m.x.st.LastKind})})
	}
	m.emit(&outs, force, m.modelOutput(now))
	m.emit(&outs, force, m.planOutput(now))
	h, dew := m.humidityOutputs(now)
	m.emit(&outs, force, h)
	m.emit(&outs, force, dew)
	return outs
}

func (m *Module) modeString() string {
	if m.live {
		return "live"
	}
	return "shadow"
}

func (m *Module) fmtT(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(m.p.Loc).Format(time.RFC3339)
}

func (m *Module) tempAttr() any {
	if !m.t.seen || m.t.at.IsZero() {
		return nil
	}
	return round(m.t.v, 2)
}

func (m *Module) hotAttr(now time.Time) any {
	if !m.hotFresh(now) {
		return nil
	}
	return round(m.hot.v, 2)
}

func (m *Module) activeAlarms() []string {
	out := []string{}
	for _, k := range []string{"cond", "high", "low", "wet", "dry"} {
		if a := m.alarms[k]; a != nil && a.Active {
			out = append(out, k)
		}
	}
	return out
}

func (m *Module) shedAttr() any {
	if !m.shed.ok {
		return nil
	}
	return round(m.shed.v, 2)
}

func (m *Module) modelOutput(now time.Time) modules.OutputUpdate {
	f := m.fit
	attrs := jsonStr(map[string]any{"u_per_h": round(f.U, 4), "h_k_per_h": round(f.H, 3), "g_k_per_h": round(f.G, 4), "n": f.N,
		"fitted": f.Fitted, "mae_c": round(f.MAE, 3), "bias_c": round(f.Bias, 3), "margin_c": round(m.p.MarginC, 2),
		"defaults":  map[string]float64{"u": m.defModel.U, "h": m.defModel.Boost5, "g": m.defModel.Gain},
		"horizon_h": 1, "saturation_c": m.p.MaxC,
		"note": "heating benefit is capped at bc_max_c (the blanket thermostat and the hard cap)"})
	if !f.Fitted {
		return modules.OutputUpdate{ObjectID: ObjModel, Time: now, Unavailable: true, AttributesJSON: attrs}
	}
	return modules.OutputUpdate{ObjectID: ObjModel, Time: now, Value: round(f.MAE, 3), AttributesJSON: attrs}
}

func (m *Module) planOutput(now time.Time) modules.OutputUpdate {
	slots := []any{}
	v := 0.0
	attrs := map[string]any{"slot_fields": []string{"start", "on", "price_kr_per_kwh", "t_pred"}, "shadow": !m.armedLive()}
	if m.havePlan {
		for _, s := range m.plan.Slots {
			on := 0
			if s.On {
				on = 1
			}
			slots = append(slots, []any{s.Start.In(m.p.Loc).Format(time.RFC3339), on, round(s.PriceKr, 3), round(s.TEnd, 2)})
			if s.Start.Before(m.planAt.Add(24 * time.Hour)) {
				v += s.KWh
			}
		}
		attrs["cost_kr"] = round(m.plan.CostKr, 3)
		attrs["predicted_min_c"] = round(m.plan.MinT, 2)
		attrs["predicted_max_c"] = round(m.plan.MaxT, 2)
		attrs["floor_at_risk"] = m.plan.FloorAtRisk
		attrs["start_budget_binds"] = m.plan.CapBound
		attrs["planned_starts"] = m.plan.Starts
		attrs["prices_extrapolated"] = m.plan.Extrapolated
		attrs["plan_floor_c"] = round(m.plan.PlanFloor, 2)
	}
	attrs["slots"] = slots
	return modules.OutputUpdate{ObjectID: ObjPlan, Time: now, Value: round(v, 3), AttributesJSON: jsonStr(attrs)}
}

// humidityReady: the storage RH and temperature are both fresh enough.
func (m *Module) humidityReady(now time.Time) bool {
	return m.hum.ok && m.t.ok && now.Sub(m.hum.at) <= 3*time.Hour && now.Sub(m.t.at) <= 3*time.Hour
}

func (m *Module) humidityOutputs(now time.Time) (modules.OutputUpdate, modules.OutputUpdate) {
	if !m.humidityReady(now) {
		return modules.OutputUpdate{ObjectID: ObjHumidity, Time: now, Unavailable: true},
			modules.OutputUpdate{ObjectID: ObjDew, Time: now, Unavailable: true}
	}
	T, rh := m.t.v, m.hum.v
	dew := DewPoint(T, rh)
	ref := RHAt(T, rh, m.rhRef)
	attrs := map[string]any{"rh_raw_percent": round(rh, 1), "rh_at_reference_percent": round(ref, 1), "reference_c": m.rhRef,
		"dew_point_c": round(dew, 2), "abs_humidity_g_m3": round(AbsHumidity(T, rh), 2), "storage_temp_c": round(T, 2),
		"shed_temp_c": m.shedAttr(), "alarm_dry": m.alarms["dry"] != nil && m.alarms["dry"].Active,
		"alarm_wet": m.alarms["wet"] != nil && m.alarms["wet"].Active, "alarm_condensation": m.alarms["cond"] != nil && m.alarms["cond"].Active,
		"low_percent": m.rhLow, "high_percent": m.rhHi,
		"note": "RH at reference is the RH this air would show at the reference temperature; raw RH is only the display value"}
	return modules.OutputUpdate{ObjectID: ObjHumidity, Time: now, Value: round(ref, 1), AttributesJSON: jsonStr(attrs)},
		modules.OutputUpdate{ObjectID: ObjDew, Time: now, Value: round(dew, 2),
			AttributesJSON: jsonStr(map[string]any{"abs_humidity_g_m3": round(AbsHumidity(T, rh), 2)})}
}

// alarmsTick runs the temperature and humidity alarms. State is tracked in
// every mode (and shown in attributes); pushes go out only when armed and live.
func (m *Module) alarmsTick(now time.Time) {
	get := func(k string) *alarm {
		a := m.alarms[k]
		if a == nil {
			a = &alarm{}
			m.alarms[k] = a
		}
		return a
	}
	fresh := m.tempFresh(now)
	T := m.t.v
	heating := m.x.on || m.x.phase != phIdle
	fire := func(key string, f, c bool, title, text, rec string) {
		if f {
			m.alert(key, title, text)
		}
		if c {
			m.alert(key+"_ok", title+": recovered", rec)
		}
	}
	hotHigh := m.hotFresh(now) && m.hot.v > m.hotAlarm
	f, c := get("high").step(fresh && T > m.alarmHigh || hotHigh, now, 30*time.Minute)
	fire("high", f, c, "Dahlia storage too warm", fmt.Sprintf("The storage temperature (%.1f C, limit %.1f C) or the blanket hot spot (limit %.1f C) has been too high for 30 min. Above about 8-10 C the tubers sprout and rot: check the shed and the blanket.", T, m.alarmHigh, m.hotAlarm), fmt.Sprintf("The storage temperature is back to %.1f C.", T))
	f, c = get("low").step(fresh && T < m.backstop && heating, now, 30*time.Minute)
	fire("low", f, c, "Dahlia storage too cold", fmt.Sprintf("The storage temperature has stayed below %.1f C for 30 min while the blanket is heating (now %.1f C): heating is failing to keep up.", m.backstop, T), fmt.Sprintf("The storage temperature is back to %.1f C.", T))
	ready := m.humidityReady(now)
	ref := 0.0
	if ready {
		ref = RHAt(m.t.v, m.hum.v, m.rhRef)
	}
	f, c = get("dry").step(ready && ref < m.rhLow, now, 6*time.Hour)
	fire("dry", f, c, "Dahlia tubers may be too dry", fmt.Sprintf("Humidity at %.0f C reference has been below %.0f %% for 6 h (now %.0f %%, raw %.0f %%): mist the tubers.", m.rhRef, m.rhLow, ref, m.hum.v), fmt.Sprintf("Humidity at reference is back to %.0f %%.", ref))
	f, c = get("wet").step(ready && ref > m.rhHi, now, 6*time.Hour)
	fire("wet", f, c, "Dahlia storage too humid", fmt.Sprintf("Humidity at %.0f C reference has been above %.0f %% for 6 h (now %.0f %%, raw %.0f %%): ventilate.", m.rhRef, m.rhHi, ref, m.hum.v), fmt.Sprintf("Humidity at reference is back to %.0f %%.", ref))
	cond, dew := false, 0.0
	if ready && m.shed.ok && now.Sub(m.shed.at) <= 3*time.Hour {
		dew = DewPoint(m.t.v, m.hum.v)
		cond = m.shed.v <= dew+0.5
	}
	f, c = get("cond").step(cond, now, time.Hour)
	fire("cond", f, c, "Dahlia storage: condensation risk", fmt.Sprintf("The shed air (%.1f C) has been within 0.5 C of the storage dew point (%.1f C) for 1 h: moisture may condense on cold surfaces. Check the lid and the cold side for droplets.", m.shed.v, dew), "The shed air is clear of the storage dew point again.")
}

// alert pushes and logs, only while armed and live.
func (m *Module) alert(kind, title, text string) {
	log.Printf("%s: alarm %s: %s", Name, kind, text)
	if m.armedLive() {
		m.notifyNow(kind, title, text)
	}
}

var (
	_ modules.Module        = (*Module)(nil)
	_ modules.Ticker        = (*Module)(nil)
	_ modules.Actor         = (*Module)(nil)
	_ modules.Buttoned      = (*Module)(nil)
	_ modules.LinkAware     = (*Module)(nil)
	_ modules.Configurable  = (*Module)(nil)
	_ modules.InitialStater = (*Module)(nil)
)
