package core

import "time"

// Role is what an HA entity means to the controller. The adapter maps
// entity ids to roles, so the core never names an entity.
type Role int

// Roles (PLAN §2.4).
const (
	RoleCover      Role = iota + 1 // cover state + current_position attribute
	RoleTarget                     // motor target_position, 0-100
	RoleMotorState                 // motor state string
	RoleBattery                    // %
	RoleCharging                   // telemetry only
	RoleSelect                     // input_select.hot_water
	RoleArmed                      // input_boolean.hot_water_controller_armed
	RoleLatch                      // input_boolean.hot_water_watchdog_tripped
	RoleBoost                      // input_button.hot_water_boost (state = last press time)
	RoleConflict                   // automation.* of the old controllers
	RoleEV                         // loaver_power, already in W
	RoleTankTemp                   // logged only
	RoleTestMove                   // input_button.hot_water_test_move (R3 manual test OFF/ON)
	RoleTestProbe                  // input_button.hot_water_test_probe (R3 manual probe)
)

// Select options.
const (
	SelectAuto        = "Auto (Cost-saving)"
	SelectOverrideOn  = "Override On"
	SelectOverrideOff = "Override Off"
)

// Input is one event fed to Controller.Step. Every input carries its time.
type Input interface{ When() time.Time }

// EntityUpdate is one HA state (live or snapshot; Replay for warm-up).
type EntityUpdate struct {
	At         time.Time
	Role       Role
	Entity     string
	State      string
	Num        float64
	NumOK      bool
	Position   float64 // cover current_position
	PositionOK bool
	Replay     bool
}

// Sample is one residual evaluation from the hot_water hook.
type Sample struct {
	At        time.Time
	W         float64
	Available bool
	DiscSeq   int
	Replay    bool
}

// Discontinuity is one known-load loss/regain from the hook.
type Discontinuity struct {
	At     time.Time
	Entity string
	DeltaW float64
	Seq    int
	Replay bool
}

// CycleOpened / CycleClosed are detector cycles from the hook.
type CycleOpened struct {
	At        time.Time
	OnTime    time.Time
	Magnitude float64
	Replay    bool
}

// CycleClosed ends the cycle last opened.
type CycleClosed struct {
	At       time.Time
	OnTime   time.Time
	OffTime  time.Time
	Accepted bool
	Replay   bool
}

// Tick is the host's 5 s tick.
type Tick struct{ At time.Time }

// CommandResult is the outcome of a CallService effect.
type CommandResult struct {
	At     time.Time
	CorrID uint64
	OK     bool
	Err    string
}

// Link reports the HA WebSocket going up (states loaded) or down.
type Link struct {
	At time.Time
	Up bool
}

// Detector reports whether the in-process hook is connected at all.
type Detector struct {
	At      time.Time
	Present bool
}

// PlanUpdate replaces the current plan (nil: no plan).
type PlanUpdate struct {
	At   time.Time
	Plan *Plan
}

// Seed delivers cycles reconstructed from detector history (§4.4).
type Seed struct {
	At     time.Time
	Cycles []SeedCycle
}

// When implementations.
func (e EntityUpdate) When() time.Time  { return e.At }
func (e Sample) When() time.Time        { return e.At }
func (e Discontinuity) When() time.Time { return e.At }
func (e CycleOpened) When() time.Time   { return e.At }
func (e CycleClosed) When() time.Time   { return e.At }
func (e Tick) When() time.Time          { return e.At }
func (e CommandResult) When() time.Time { return e.At }
func (e Link) When() time.Time          { return e.At }
func (e Detector) When() time.Time      { return e.At }
func (e PlanUpdate) When() time.Time    { return e.At }
func (e Seed) When() time.Time          { return e.At }

// EffectKind enumerates effects.
type EffectKind int

// Effect kinds (PLAN §2.2).
const (
	EffCallService EffectKind = iota + 1
	EffPublish
	EffNotify
	EffPersistNow
	EffLog
)

// Effect is one outward act the shell must perform.
type Effect struct {
	Kind EffectKind
	// EffCallService
	Domain  string
	Service string
	Data    map[string]any
	CorrID  uint64
	// EffPublish: ObjectID is the telemetry entity's object id.
	ObjectID string
	State    string
	Attrs    map[string]any
	// EffNotify
	Code, Title, Text string
	// EffLog
	Msg string
}

// State is the controller state (§3).
type State string

// States.
const (
	StBoot      State = "BOOT"
	StIdleOn    State = "IDLE_ON"
	StCmdOff    State = "CMD_OFF"
	StVerifyOff State = "VERIFY_OFF"
	StIdleOff   State = "IDLE_OFF"
	StCmdOn     State = "CMD_ON"
	StVerifyOn  State = "VERIFY_ON"
	StRecycleOn State = "RECYCLE_ON"
	StProbe     State = "PROBE"
	StHoldOn    State = "HOLD_ON"
	StHoldOff   State = "HOLD_OFF"
	StDegraded  State = "DEGRADED"
)

// Hold reasons for HOLD_ON, in priority order.
const (
	HoldWatchdog = "watchdog"
	HoldConflict = "conflict"
	HoldStrikes  = "strikes"
	HoldBattery  = "battery"
	HoldOnFailed = "on_failed"
	HoldBoost    = "boost"
	HoldBackstop = "backstop"
	HoldOverride = "override"
)

// Verdicts (§2.5).
const (
	VOnConfirmed    = "on_confirmed"
	VOnPositionOnly = "on_position_only"
	VOnPending      = "on_pending"
	VSuspect        = "suspect"
	VOffConfirmed   = "off_confirmed"
	VOffUnverified  = "off_unverified"
	VOffFailed      = "off_failed"
	VRetreat        = "retreat"
	VStuck          = "stuck"
	VCmdError       = "cmd_error"
)

// Alert codes.
const (
	AlertOnFailed          = "ON_FAILED"
	AlertBackstop          = "BACKSTOP_18H"
	AlertSuspect           = "SUSPECT"
	AlertConflict          = "CONFLICT"
	AlertBatteryLow        = "BATTERY_LOW"
	AlertStrikes           = "STRIKES"
	AlertOverrideRefused   = "OVERRIDE_OFF_REFUSED"
	AlertOverrideExpired   = "OVERRIDE_OFF_EXPIRED"
	AlertNoDetector        = "NO_DETECTOR"
	AlertShadowDaily       = "SHADOW_SUMMARY"
	AlertOffAtCapUnhandled = "MOVE_CAP"
	AlertTestRefused       = "TEST_MOVE_REFUSED"
)

// Telemetry object ids (§2.5). Prefix hot_water_controller_, never hwc_.
const (
	ObjHeartbeat     = "hot_water_controller_heartbeat"
	ObjState         = "hot_water_controller_state"
	ObjDesired       = "hot_water_controller_desired"
	ObjVerdict       = "hot_water_controller_verdict"
	ObjOnHours       = "hot_water_controller_on_hours_since_genuine_start"
	ObjHoursSinceEnd = "hot_water_controller_hours_since_reheat_end"
	ObjMovesToday    = "hot_water_controller_moves_today"
	ObjPlan          = "hot_water_controller_plan"
	ObjShadowWould   = "hot_water_controller_shadow_would"
	ObjAlert         = "hot_water_controller_alert"
	ObjStrikes       = "hot_water_controller_strikes"
	// hot_water_controller_saving_month (PLAN §2.5) is not published: no
	// saving estimate is computed yet, and an entity that never gets a
	// value is worse than none. Add it with the estimate.
)

// TelemetryObjects lists every published object id.
var TelemetryObjects = []string{
	ObjHeartbeat, ObjState, ObjDesired, ObjVerdict, ObjOnHours, ObjHoursSinceEnd,
	ObjMovesToday, ObjPlan, ObjShadowWould, ObjAlert, ObjStrikes,
}
