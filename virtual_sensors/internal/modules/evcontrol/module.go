package evcontrol

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"virtual_sensors/internal/modules"
	"virtual_sensors/internal/modules/hotwater"
	"virtual_sensors/internal/modules/hwcontrol/planner"
)

// Name is the module's registry key.
const Name = "ev_control"

// Output object ids (sensor.<id>).
const (
	ObjState     = "ev_controller_state"
	ObjPlan      = "ev_plan"
	ObjWould     = "ev_would_command"
	ObjGuard     = "ev_guard_limit_a"
	ObjBreaches  = "ev_guard_breaches_tonight"
	ObjSaving    = "ev_shadow_saving"
	hwPlanEntity = "hot_water_controller_plan"
	gridEntity   = "grid_meter_power"
	hpEntity     = "heat_pump_power_power"
)

// SessionsDir is where the per-session evaluation lines go.
const SessionsDir = "/data/evcontrol"

func init() {
	modules.Register(Name, func() modules.Module { return New() })
}

type options struct {
	Prefix     *string  `json:"ev_entity_prefix"`
	Departure  *string  `json:"ev_departure"`
	NeedKWh    *float64 `json:"ev_need_kwh"`
	MinA       *int     `json:"ev_min_a"`
	MaxA       *int     `json:"ev_max_a"`
	BudgetA    *float64 `json:"ev_phase_budget_a"`
	HouseBaseA *float64 `json:"ev_house_base_a"`
	MinBlock   *int     `json:"ev_min_block_min"`
	HeaterA    *float64 `json:"ev_heater_phase_a"`
	HPA        *float64 `json:"ev_hp_phase_a"`
	PriceEnt   *string  `json:"hwc_price_entity"`
	AdderOre   *float64 `json:"hwc_price_adder_ore"`
	VAT        *float64 `json:"hwc_vat_factor"`
}

// persisted is the module's restart state.
type persisted struct {
	Session *session          `json:"session,omitempty"`
	Prices  map[int64]float64 `json:"prices,omitempty"` // quarter start (unix) -> SEK/kWh ex VAT
	Sum     savingTotals      `json:"totals"`
}

type savingTotals struct {
	Sessions int     `json:"sessions"`
	SavingKr float64 `json:"saving_kr"`
	ActualKr float64 `json:"actual_kr"`
	PlanKr   float64 `json:"plan_kr"`
}

// session is one plug-in to unplug.
type session struct {
	PlugIn       time.Time         `json:"plug_in"`
	Departure    time.Time         `json:"departure"`
	QuarterKW    map[int64]float64 `json:"quarter_kwh"` // quarter start -> kWh actually delivered
	Breaches     int               `json:"breaches"`
	FirstPlan    *planSummary      `json:"first_plan,omitempty"`
	DeliveredKWh float64           `json:"delivered_kwh"`
}

type planSummary struct {
	PlannedKWh float64 `json:"planned_kwh"`
	NeedKWh    float64 `json:"need_kwh"`
	Feasible   bool    `json:"feasible"`
	CostKr     float64 `json:"cost_kr"`
}

// Module is the ev_control module (shadow only).
type Module struct {
	p        Params
	prefix   string
	depHHMM  int // minutes after local midnight
	priceEnt string

	// readings
	status      string
	statusOK    bool
	evKW        float64
	evKWAt      time.Time
	sessionKWh  float64
	gridW, hpW  float64
	gridOK      bool
	residualW   float64
	hwOff       []period
	hwPlanKnown bool
	prices      map[int64]float64
	now         time.Time

	sess                 *session
	totals               savingTotals
	plan                 Result
	havePlan             bool
	lastPlanAt           time.Time
	guard                GuardOut
	haveGuard            bool
	inBreach             bool
	lastGuardPub         time.Time
	lastLimit            int
	lastWould, lastState string

	sessionsPath string
	appendLine   func(path string, b []byte) error
	lastSaving   *sessionResult
}

type period struct{ start, end time.Time }

// New returns a module with defaults applied.
func New() *Module {
	m := &Module{p: DefaultParams(), prefix: "loaver", depHHMM: 6*60 + 30,
		priceEnt: "nordpool_kwh_se3_sek_3_06_0", prices: map[int64]float64{},
		sessionsPath: filepath.Join(SessionsDir, "sessions.jsonl"), appendLine: appendFile}
	return m
}

func appendFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	return err
}

// Name implements modules.Module.
func (m *Module) Name() string { return Name }

func (m *Module) ent(s string) string { return m.prefix + "_" + s }

// Inputs implements modules.Module.
func (m *Module) Inputs() []modules.InputSpec {
	return []modules.InputSpec{
		{EntityID: m.ent("status")}, {EntityID: m.ent("power")}, {EntityID: m.ent("session_energy")},
		{EntityID: gridEntity}, {EntityID: hpEntity},
		{EntityID: hotwater.OutPowerObjectID},
		{EntityID: hwPlanEntity, Attributes: []string{"periods", "valid_until"}},
		{EntityID: m.priceEnt, Attributes: []string{"today", "tomorrow", "raw_today", "raw_tomorrow", "tomorrow_valid"}},
	}
}

// Outputs implements modules.Module.
func (m *Module) Outputs() []modules.OutputSpec {
	mk := func(obj, name, unit, class string) modules.OutputSpec {
		return modules.OutputSpec{ObjectID: obj, UniqueID: "virtual_sensors_" + obj, Name: name, Unit: unit, StateClass: class, HasAttributes: true}
	}
	return []modules.OutputSpec{
		mk(ObjState, "EV controller state", "", ""),
		mk(ObjPlan, "EV plan", "kWh", ""),
		mk(ObjWould, "EV would command", "", ""),
		mk(ObjGuard, "EV guard limit", "A", "measurement"),
		mk(ObjBreaches, "EV guard breaches tonight", "", "measurement"),
		mk(ObjSaving, "EV shadow saving", "kr", ""),
	}
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
	if o.Prefix != nil && *o.Prefix != "" {
		m.prefix = *o.Prefix
	}
	if o.Departure != nil {
		if v, ok := parseHHMM(*o.Departure); ok {
			m.depHHMM = v
		} else {
			msgs = append(msgs, "option ev_departure does not parse; default applied")
		}
	}
	if o.NeedKWh != nil && *o.NeedKWh > 0 && *o.NeedKWh <= 100 {
		p.NeedKWh = *o.NeedKWh
	}
	if o.MinA != nil && *o.MinA >= 6 && *o.MinA <= 32 {
		p.MinA = float64(*o.MinA)
	}
	if o.MaxA != nil && *o.MaxA >= 6 && *o.MaxA <= 32 {
		p.MaxA = float64(*o.MaxA)
	}
	if o.BudgetA != nil && *o.BudgetA > 0 && *o.BudgetA <= 63 {
		p.BudgetA = *o.BudgetA
	}
	if o.HouseBaseA != nil && *o.HouseBaseA >= 0 && *o.HouseBaseA <= 20 {
		p.HouseBaseA = *o.HouseBaseA
	}
	if o.MinBlock != nil && *o.MinBlock >= 0 && *o.MinBlock <= 240 {
		p.MinBlock = time.Duration(*o.MinBlock) * time.Minute
	}
	if o.HeaterA != nil && *o.HeaterA >= 0 && *o.HeaterA <= 20 {
		p.HeaterA = *o.HeaterA
	}
	if o.HPA != nil && *o.HPA >= 0 && *o.HPA <= 20 {
		p.HPA = *o.HPA
	}
	if o.PriceEnt != nil && *o.PriceEnt != "" {
		m.priceEnt = strings.TrimPrefix(*o.PriceEnt, "sensor.")
	}
	if o.AdderOre != nil {
		p.Price.AdderOre = *o.AdderOre
	}
	if o.VAT != nil && *o.VAT > 0 {
		p.Price.VAT = *o.VAT
	}
	if p.MaxA < p.MinA {
		msgs = append(msgs, "ev_max_a < ev_min_a; defaults applied")
		p.MinA, p.MaxA = 6, 16
	}
	for _, s := range msgs {
		log.Printf("%s: %s", Name, s)
	}
	m.p = p
	return nil
}

func parseHHMM(s string) (int, bool) {
	var h, mi int
	if n, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &mi); err != nil || n != 2 || h < 0 || h > 23 || mi < 0 || mi > 59 {
		return 0, false
	}
	return h*60 + mi, true
}

// Init implements modules.Module.
func (m *Module) Init(b []byte) error {
	if len(b) > 0 {
		var ps persisted
		if err := json.Unmarshal(b, &ps); err != nil {
			log.Printf("%s: persisted state unreadable (%v); starting fresh", Name, err)
		} else {
			m.sess, m.totals = ps.Session, ps.Sum
			if ps.Prices != nil {
				m.prices = ps.Prices
			}
		}
	}
	log.Printf("%s: SHADOW ONLY: reads Home Assistant, publishes sensors, never calls a service", Name)
	return nil
}

// Persist implements modules.Module.
func (m *Module) Persist() ([]byte, error) {
	return json.Marshal(persisted{Session: m.sess, Prices: m.prices, Sum: m.totals})
}

// InitialStates implements modules.InitialStater.
func (m *Module) InitialStates() []modules.OutputUpdate { return m.publishAll(m.now, true) }

func (m *Module) hwOn(t time.Time) bool {
	if !m.hwPlanKnown {
		return true
	}
	for _, p := range m.hwOff {
		if !t.Before(p.start) && t.Before(p.end) {
			return false
		}
	}
	return true
}

func (m *Module) priceList() []planner.Price {
	out := make([]planner.Price, 0, len(m.prices))
	for k, v := range m.prices {
		out = append(out, planner.Price{Start: time.Unix(k, 0), Dur: quarter, SEK: v})
	}
	return out
}

func (m *Module) plugged() bool {
	switch m.status {
	case "awaiting_start", "awaiting_authorization", "ready_to_charge", "charging", "completed":
		return true
	}
	return false
}

// nextDeparture is the first departure time strictly after t.
func (m *Module) nextDeparture(t time.Time) time.Time {
	l := t.In(m.p.Loc)
	d := time.Date(l.Year(), l.Month(), l.Day(), m.depHHMM/60, m.depHHMM%60, 0, 0, m.p.Loc)
	if !d.After(t) {
		d = time.Date(l.Year(), l.Month(), l.Day()+1, m.depHHMM/60, m.depHHMM%60, 0, 0, m.p.Loc)
	}
	return d
}

// OnStateUpdate implements modules.Module.
func (m *Module) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	if u.Time.After(m.now) {
		m.now = u.Time
	}
	var outs []modules.OutputUpdate
	replan := false
	switch u.EntityID {
	case m.ent("status"):
		was := m.plugged()
		m.integrate(u.Time)
		m.status, m.statusOK = u.State, u.Available || u.State != ""
		if u.State == "unavailable" || u.State == "unknown" {
			m.statusOK = false
			m.status = ""
		}
		now := m.plugged()
		switch {
		case now && !was:
			m.startSession(u.Time)
			replan = true
		case !now && was:
			outs = append(outs, m.endSession(u.Time)...)
		}
	case m.ent("power"):
		m.integrate(u.Time)
		if u.Available {
			m.evKW = u.Value
		} else {
			m.evKW = 0
		}
		m.evKWAt = u.Time
		outs = append(outs, m.runGuard(u.Time)...)
	case m.ent("session_energy"):
		if u.Available {
			m.sessionKWh = u.Value
			if m.sess != nil {
				m.sess.DeliveredKWh = u.Value
			}
		}
	case gridEntity:
		m.gridW, m.gridOK = u.Value, u.Available
		outs = append(outs, m.runGuard(u.Time)...)
	case hpEntity:
		if u.Available {
			m.hpW = u.Value
		}
	case hotwater.OutPowerObjectID:
		if u.Available {
			m.residualW = u.Value
		}
	case hwPlanEntity:
		if m.parseHWPlan(u) {
			replan = true
		}
	case m.priceEnt:
		if len(u.Attrs) > 0 {
			for _, pr := range planner.ParseNordpool(u.Attrs, u.Time, m.p.Loc) {
				for k := 0; k < int(pr.Dur/quarter); k++ {
					m.prices[pr.Start.Add(time.Duration(k)*quarter).Unix()] = pr.SEK
				}
			}
			m.prunePrices(u.Time)
			replan = true
		}
	default:
		return nil
	}
	if replan {
		m.replan(u.Time)
	}
	return append(outs, m.publishAll(u.Time, replan)...)
}

func (m *Module) prunePrices(now time.Time) {
	cut := now.Add(-72 * time.Hour).Unix()
	for k := range m.prices {
		if k < cut {
			delete(m.prices, k)
		}
	}
}

func (m *Module) parseHWPlan(u modules.StateUpdate) bool {
	raw, ok := u.Attrs["periods"]
	if !ok {
		return false
	}
	var rows []struct{ Start, End string }
	if json.Unmarshal(raw, &rows) != nil {
		return false
	}
	var off []period
	for _, r := range rows {
		s, e1 := time.Parse(time.RFC3339, r.Start)
		e, e2 := time.Parse(time.RFC3339, r.End)
		if e1 == nil && e2 == nil {
			off = append(off, period{s, e})
		}
	}
	changed := !m.hwPlanKnown || len(off) != len(m.hwOff)
	if !changed {
		for i := range off {
			if off[i] != m.hwOff[i] {
				changed = true
			}
		}
	}
	m.hwOff, m.hwPlanKnown = off, true
	return changed
}

func (m *Module) startSession(t time.Time) {
	if m.sess != nil && m.sess.Departure.After(t) {
		return // re-connected within the same night: keep the session
	}
	m.sess = &session{PlugIn: t, Departure: m.nextDeparture(t), QuarterKW: map[int64]float64{}}
	m.sess.DeliveredKWh = 0
}

// integrate attributes the held EV power since the last reading to
// quarter-hour slots of the open session (hold-last, capped at 30 min).
func (m *Module) integrate(t time.Time) {
	if m.sess != nil && !m.evKWAt.IsZero() && t.After(m.evKWAt) && m.evKW > 0 {
		from, to := m.evKWAt, t
		if to.Sub(from) > 30*time.Minute {
			to = from.Add(30 * time.Minute)
		}
		for s := from; s.Before(to); {
			q := s.Truncate(quarter)
			e := q.Add(quarter)
			if e.After(to) {
				e = to
			}
			m.sess.QuarterKW[q.Unix()] += m.evKW * e.Sub(s).Hours()
			s = e
		}
	}
	if t.After(m.evKWAt) {
		m.evKWAt = t
	}
}

type sessionResult struct {
	PlugIn, Departure time.Time
	DeliveredKWh      float64
	ActualKr, PlanKr  float64
	PlanFinished      bool
	Breaches          int
	SavingKr          float64
}

func (m *Module) endSession(t time.Time) []modules.OutputUpdate {
	s := m.sess
	if s == nil {
		return nil
	}
	kwh := 0.0
	actual := 0.0
	for q, e := range s.QuarterKW {
		kwh += e
		if sek, ok := m.prices[q]; ok {
			actual += e * m.p.Price.AllInOre(sek) / 100
		}
	}
	if kwh < 0.05 {
		m.sess = nil
		return nil
	}
	planKr, _ := CostOfKWh(kwh, s.PlugIn, s.Departure, m.priceList(), m.hwOn, m.p)
	fin := s.FirstPlan != nil && s.FirstPlan.Feasible
	r := sessionResult{PlugIn: s.PlugIn, Departure: s.Departure, DeliveredKWh: kwh, ActualKr: actual,
		PlanKr: planKr, PlanFinished: fin, Breaches: s.Breaches, SavingKr: actual - planKr}
	m.totals.Sessions++
	m.totals.SavingKr += r.SavingKr
	m.totals.ActualKr += actual
	m.totals.PlanKr += planKr
	m.lastSaving = &r
	line, _ := json.Marshal(map[string]any{
		"plug_in": r.PlugIn.Format(time.RFC3339), "departure": r.Departure.Format(time.RFC3339),
		"unplug": t.Format(time.RFC3339), "delivered_kwh": round(kwh, 2), "actual_cost_kr": round(actual, 2),
		"plan_cost_kr": round(planKr, 2), "saving_kr": round(r.SavingKr, 2),
		"plan_finishes_by_departure": fin, "guard_breaches": r.Breaches,
	})
	if err := m.appendLine(m.sessionsPath, append(line, '\n')); err != nil {
		log.Printf("%s: could not append session line: %v", Name, err)
	}
	m.sess = nil
	m.havePlan = false
	return nil
}

func round(v float64, n int) float64 { p := math.Pow10(n); return math.Round(v*p) / p }

func (m *Module) replan(now time.Time) {
	m.lastPlanAt = now
	if m.sess == nil || !m.plugged() || len(m.prices) == 0 {
		m.havePlan = false
		return
	}
	m.plan = Plan(Input{Now: now, Departure: m.sess.Departure, DeliveredKWh: m.sessionKWh,
		Prices: m.priceList(), HotWaterOn: m.hwOn}, m.p)
	m.havePlan = true
	if m.sess.FirstPlan == nil {
		// The plan at plug-in, for the "would it have finished" record,
		// is made for the FULL need from plug-in.
		full := Plan(Input{Now: m.sess.PlugIn, Departure: m.sess.Departure, Prices: m.priceList(), HotWaterOn: m.hwOn}, m.p)
		m.sess.FirstPlan = &planSummary{PlannedKWh: full.PlannedKWh, NeedKWh: full.NeedKWh, Feasible: full.Feasible, CostKr: full.CostKr}
	}
}

func (m *Module) runGuard(t time.Time) []modules.OutputUpdate {
	if !m.gridOK {
		return nil
	}
	g := Guard(GuardIn{GridW: m.gridW, EVkW: m.evKW, HPW: m.hpW, ImmersionOn: m.residualW >= 1500}, m.p)
	m.guard, m.haveGuard = g, true
	if g.Breach && !m.inBreach && m.sess != nil {
		m.sess.Breaches++
	}
	m.inBreach = g.Breach
	return nil
}

// OnTick implements modules.Ticker.
func (m *Module) OnTick(now time.Time) []modules.OutputUpdate {
	if now.After(m.now) {
		m.now = now
	}
	m.integrate(now)
	if m.sess != nil && !now.Before(m.sess.Departure) && m.status != "charging" {
		// Past departure with nothing charging: the shadow night is over.
	}
	re := m.lastPlanAt.IsZero() || now.Sub(m.lastPlanAt) >= 15*time.Minute
	if re {
		m.replan(now)
	}
	return m.publishAll(now, re)
}

// current plan slot at t.
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

// decide returns the controller state, the would-be command and the reason.
func (m *Module) decide(now time.Time) (state, cmd, reason string) {
	if !m.plugged() || m.sess == nil {
		return "idle", "none", "car not plugged in"
	}
	if !now.Before(m.sess.Departure) {
		return "idle", "none", "past departure"
	}
	if len(m.prices) == 0 || !m.havePlan {
		return "degraded", "none", "no prices"
	}
	if m.plan.NeedKWh <= 0 {
		return "idle", "none", "need met"
	}
	slot, in := m.slotAt(now)
	charging := m.status == "charging"
	cur := m.evKW * 1000 / (3 * Volts)
	if in {
		eff := slot.Amps
		why := fmt.Sprintf("planned slot at %.0f A", slot.Amps)
		if m.haveGuard && m.gridOK {
			if float64(m.guard.LimitA) < eff {
				eff = float64(m.guard.LimitA)
				why = fmt.Sprintf("guard limits to %d A", m.guard.LimitA)
			}
		}
		if eff < m.p.MinA {
			return "would_pause", "pause", "guard: no headroom (" + why + ")"
		}
		st := "would_charge"
		if m.plan.DeadlineMode {
			st = "deadline_mode"
		}
		if !charging {
			return st, fmt.Sprintf("resume at %.0f A", eff), why
		}
		if math.Abs(cur-eff) >= 1 {
			return st, fmt.Sprintf("limit %.0f A", eff), why
		}
		return st, "none", why
	}
	if charging {
		return "would_pause", "pause", "charging outside the planned slots"
	}
	return "planned", "none", "waiting for a planned slot"
}

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
		outs = append(outs, m.planOutput(now))
		outs = append(outs, m.savingOutput(now))
	}
	if m.haveGuard && (force || m.guard.LimitA != m.lastLimit || now.Sub(m.lastGuardPub) >= time.Minute) {
		m.lastLimit, m.lastGuardPub = m.guard.LimitA, now
		g := m.guard
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjGuard, Time: now, Value: float64(g.LimitA),
			AttributesJSON: jsonStr(map[string]any{
				"phase_a_a": round(g.PhaseA[0], 1), "phase_b_a": round(g.PhaseA[1], 1), "phase_c_a": round(g.PhaseA[2], 1),
				"house_a": round(g.HouseA[0], 1), "house_b": round(g.HouseA[1], 1), "house_c": round(g.HouseA[2], 1),
				"ev_a_per_phase": round(g.EVA, 1), "breach": g.Breach, "shadow": true})})
		n := 0
		if m.sess != nil {
			n = m.sess.Breaches
		}
		outs = append(outs, modules.OutputUpdate{ObjectID: ObjBreaches, Time: now, Value: float64(n),
			AttributesJSON: jsonStr(map[string]any{"budget_a": m.p.BudgetA})})
	}
	return outs
}

func (m *Module) planOutput(now time.Time) modules.OutputUpdate {
	slots := []any{}
	for _, s := range m.plan.Slots {
		if !m.havePlan {
			break
		}
		slots = append(slots, []any{s.Start.In(m.p.Loc).Format(time.RFC3339), s.Amps, round(s.KW, 2), round(s.PriceOre/100, 3)})
	}
	dep := ""
	if m.sess != nil {
		dep = m.sess.Departure.Format(time.RFC3339)
	}
	v := 0.0
	if m.havePlan {
		v = round(m.plan.PlannedKWh, 1)
	}
	return modules.OutputUpdate{ObjectID: ObjPlan, Time: now, Value: v, AttributesJSON: jsonStr(map[string]any{
		"slots": slots, "slot_fields": []string{"start", "amps", "kw", "price_kr_per_kwh"},
		"cost_kr": round(m.plan.CostKr, 2), "need_kwh": round(m.plan.NeedKWh, 1), "departure": dep,
		"prices_partial": m.plan.PricesPartial, "deadline_mode": m.plan.DeadlineMode, "feasible": m.plan.Feasible, "shadow": true})}
}

func (m *Module) savingOutput(now time.Time) modules.OutputUpdate {
	attrs := map[string]any{"sessions": m.totals.Sessions, "cumulative_saving_kr": round(m.totals.SavingKr, 2),
		"cumulative_actual_kr": round(m.totals.ActualKr, 2), "cumulative_plan_kr": round(m.totals.PlanKr, 2)}
	v := 0.0
	if r := m.lastSaving; r != nil {
		v = round(r.SavingKr, 2)
		attrs["last_actual_kr"], attrs["last_plan_kr"] = round(r.ActualKr, 2), round(r.PlanKr, 2)
		attrs["last_delivered_kwh"] = round(r.DeliveredKWh, 2)
		attrs["last_plan_finishes_by_departure"] = r.PlanFinished
	}
	return modules.OutputUpdate{ObjectID: ObjSaving, Time: now, Value: v, AttributesJSON: jsonStr(attrs)}
}

func jsonStr(v any) string { b, _ := json.Marshal(v); return string(b) }
