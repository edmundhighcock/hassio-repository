// Package core is the hot-water controller's decision core (PLAN.md
// §2.2): the state machine (§3), the decision policy (§4), actuation and
// electrical verification (§5) and the persisted state (§2.6).
//
// It is PURE: no time.Now, no I/O, no goroutines. Every input carries its
// own time, and every outward act is returned as an Effect. It never reads
// a price and never imports the planner; the only planning type it knows
// is Plan (plan.go).
package core

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config holds every number the controller uses. Defaults() gives the
// decided values (PLAN §9, D1-D20); the add-on maps hwc_* options onto it
// and calls Clamp.
type Config struct {
	// Live is the add-on key of two-key arming (hwc_mode: live). LIVE also
	// needs the HA arming boolean (D8). Default false: shadow.
	Live bool
	Loc  *time.Location

	// Safety (§4.1, §4.4, §4.7).
	OffLimitSinceReheatEnd time.Duration // tank clock, 14 h (hwc_off_limit_since_reheat_end_h)
	EveningOffFromMin      int           // minutes after local midnight, 18:00
	EveningRule            bool          // hwc_evening_off_requires_finished_reheat_today
	StaleAfter             time.Duration // §4.0, 30 min
	BoostNoStartTimeout    time.Duration // 30 min
	BoostMax               time.Duration // 2 h
	ProbeAfter             time.Duration // 10 h of ON time
	ProbeRepeat            time.Duration // 4 h of ON time
	AlertAfter             time.Duration // 18 h of ON time
	AlertRepeat            time.Duration // 6 h of ON time
	GenuineMinW            float64       // 2700 W
	GenuineMinDur          time.Duration // 300 s
	GenuineDiscGuard       time.Duration // ±60 s around the on-step
	GOff                   time.Duration // condition G: off >= 2 h
	GSinceReheat           time.Duration // condition G: >= 4 h since the last reheat ended
	MaxMovesHour           int           // 6
	MaxMovesDay            int           // 8 (D7)
	BatteryFloor           float64       // 30 %
	BatteryAlert           float64       // 25 %
	Heartbeat              time.Duration // 60 s
	EVGuardMinutes         []int         // 22:00, 23:00 local
	EVGuardHalfWidth       time.Duration // ±10 min
	EVGuardWindow          time.Duration // EV must not have moved > EVMoveW in the last 2 min
	EVMoveW                float64       // 1000 W

	// State machine timings (§3).
	BootTimeout     time.Duration // 3 min
	DegradedRecover time.Duration // 2 min
	Settle          time.Duration // 15 min
	DeliberateGap   time.Duration // 10 min between deliberate changes
	MoveReserve     int           // 3 moves left after an OFF
	DiscGuard       time.Duration // no discontinuity in the last 5 min (rule 4)
	StrikeDecay     time.Duration // 24 h

	// Actuation (§5.1, §5.2).
	CmdOn, CmdOff      int // 99, 0
	OnPos, OffPos      int // >= 90 is ON, <= 15 is OFF
	PosTol             int // ±5
	MotorWait          time.Duration
	SettleCheck1       time.Duration // stop + 10 s
	SettleCheck2       time.Duration // stop + 15 s
	MotorStopFallback  time.Duration // used only when no motor_state is ever seen
	MotorStoppedStates []string
	RequireMotorState  bool // motor_state must be fresh for an OFF (rule 4)
	CallRetryDelays    []time.Duration
	CallMaxFailures    int
	CallResultTimeout  time.Duration
	OnRetryGap         time.Duration // 30 s between ON retries
	OnMaxAttempts      int           // 3

	// Electrical verification (§5.3, §4.6).
	ConfirmW        float64 // 1800 W
	DrawingW        float64 // 2500 W up-step
	DrawingLevelTol float64 // 600 W
	StagedWindow    time.Duration
	MergeWindow     time.Duration
	HeatWhileOffW   float64       // 2700 W
	HeatWhileOffFor time.Duration // 3 min
	OnWindow        time.Duration // 90 s
	OnHold          time.Duration // 15 s
	OffWindow       time.Duration // 60 s
	BaselineWindow  time.Duration // 30 s
	MedianWindow    time.Duration // 20 s
	BurstWindow     time.Duration // >= 2 loads within 10 s: inconclusive

	// Daily shadow summary, local hour (notifications are otherwise
	// suppressed in SHADOW, §3).
	ShadowSummaryHour int

	// R3 manual test moves (PLAN §8): how long a test OFF lasts.
	TestOffDuration time.Duration // 10 min; clamped to 2-60 min

	// Version is published with the heartbeat.
	Version string
}

// Defaults returns the decided configuration (PLAN §9, rev 2026-11-09).
func Defaults() Config {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		loc = time.UTC
	}
	return Config{
		Live: false, Loc: loc,
		OffLimitSinceReheatEnd: 14 * time.Hour,
		EveningOffFromMin:      18 * 60,
		EveningRule:            true,
		StaleAfter:             30 * time.Minute,
		BoostNoStartTimeout:    30 * time.Minute,
		BoostMax:               2 * time.Hour,
		ProbeAfter:             10 * time.Hour,
		ProbeRepeat:            4 * time.Hour,
		AlertAfter:             18 * time.Hour,
		AlertRepeat:            6 * time.Hour,
		GenuineMinW:            2700,
		GenuineMinDur:          300 * time.Second,
		GenuineDiscGuard:       60 * time.Second,
		GOff:                   2 * time.Hour,
		GSinceReheat:           4 * time.Hour,
		MaxMovesHour:           6,
		MaxMovesDay:            8,
		BatteryFloor:           30,
		BatteryAlert:           25,
		Heartbeat:              60 * time.Second,
		EVGuardMinutes:         []int{22 * 60, 23 * 60},
		EVGuardHalfWidth:       10 * time.Minute,
		EVGuardWindow:          2 * time.Minute,
		EVMoveW:                1000,

		BootTimeout:     3 * time.Minute,
		DegradedRecover: 2 * time.Minute,
		Settle:          15 * time.Minute,
		DeliberateGap:   10 * time.Minute,
		MoveReserve:     3,
		DiscGuard:       5 * time.Minute,
		StrikeDecay:     24 * time.Hour,

		CmdOn: 99, CmdOff: 0, OnPos: 90, OffPos: 15, PosTol: 5,
		MotorWait:          30 * time.Second,
		SettleCheck1:       10 * time.Second,
		SettleCheck2:       15 * time.Second,
		MotorStopFallback:  8 * time.Second,
		MotorStoppedStates: []string{"stopped", "stop", "idle"},
		RequireMotorState:  true,
		CallRetryDelays:    []time.Duration{5 * time.Second, 20 * time.Second},
		CallMaxFailures:    3,
		CallResultTimeout:  15 * time.Second,
		OnRetryGap:         30 * time.Second,
		OnMaxAttempts:      3,

		ConfirmW:        1800,
		DrawingW:        2500,
		DrawingLevelTol: 600,
		StagedWindow:    450 * time.Second,
		MergeWindow:     30 * time.Second,
		HeatWhileOffW:   2700,
		HeatWhileOffFor: 3 * time.Minute,
		OnWindow:        90 * time.Second,
		OnHold:          15 * time.Second,
		OffWindow:       60 * time.Second,
		BaselineWindow:  30 * time.Second,
		MedianWindow:    20 * time.Second,
		BurstWindow:     10 * time.Second,

		ShadowSummaryHour: 9,
		TestOffDuration:   10 * time.Minute,
		Version:           "0.2.0",
	}
}

// Clamp enforces the safety bounds of PLAN §9 whatever the options say,
// replacing out-of-range values with the default. It returns one message
// per replacement, for the log. The controller never refuses to start over
// options.
func (c *Config) Clamp() []string {
	d := Defaults()
	var msgs []string
	fix := func(bad bool, name string, set func()) {
		if bad {
			set()
			msgs = append(msgs, fmt.Sprintf("option %s out of range; default applied", name))
		}
	}
	if c.Loc == nil {
		c.Loc = d.Loc
	}
	fix(c.AlertAfter <= 0 || c.AlertAfter > 24*time.Hour, "hwc_backstop_alert_h", func() { c.AlertAfter = d.AlertAfter })
	fix(c.ProbeAfter <= 0 || c.ProbeAfter >= c.AlertAfter, "hwc_backstop_probe_h", func() { c.ProbeAfter = d.ProbeAfter })
	fix(c.OffLimitSinceReheatEnd <= 0 || c.OffLimitSinceReheatEnd > 16*time.Hour, "hwc_off_limit_since_reheat_end_h", func() { c.OffLimitSinceReheatEnd = d.OffLimitSinceReheatEnd })
	fix(c.StaleAfter < 5*time.Minute || c.StaleAfter > 4*time.Hour, "hwc_stale_after_min", func() { c.StaleAfter = d.StaleAfter })
	fix(c.BoostMax <= 0 || c.BoostMax > 4*time.Hour, "hwc_boost_max_h", func() { c.BoostMax = d.BoostMax })
	fix(c.BoostNoStartTimeout < 5*time.Minute || c.BoostNoStartTimeout > 2*time.Hour, "hwc_boost_no_start_timeout_min", func() { c.BoostNoStartTimeout = d.BoostNoStartTimeout })
	fix(c.EveningOffFromMin < 0 || c.EveningOffFromMin >= 24*60, "hwc_evening_off_from", func() { c.EveningOffFromMin = d.EveningOffFromMin })
	fix(c.GenuineMinW < 2500 || c.GenuineMinW > 3600, "hwc_genuine_min_w", func() { c.GenuineMinW = d.GenuineMinW })
	fix(c.GenuineMinDur < 60*time.Second || c.GenuineMinDur > 30*time.Minute, "hwc_genuine_min_s", func() { c.GenuineMinDur = d.GenuineMinDur })
	fix(c.GOff <= 0, "hwc_g_off_h", func() { c.GOff = d.GOff })
	fix(c.GSinceReheat < 0, "hwc_g_since_reheat_h", func() { c.GSinceReheat = d.GSinceReheat })
	fix(c.MaxMovesHour < 2 || c.MaxMovesHour > 6, "hwc_max_moves_hour", func() { c.MaxMovesHour = d.MaxMovesHour })
	fix(c.MaxMovesDay < 2 || c.MaxMovesDay > 24, "hwc_max_moves_day", func() { c.MaxMovesDay = d.MaxMovesDay })
	fix(c.BatteryFloor < 0 || c.BatteryFloor > 100, "hwc_battery_floor_pct", func() { c.BatteryFloor = d.BatteryFloor })
	fix(c.BatteryAlert < 0 || c.BatteryAlert > 100, "hwc_battery_alert_pct", func() { c.BatteryAlert = d.BatteryAlert })
	fix(c.Heartbeat < 15*time.Second || c.Heartbeat > 300*time.Second, "hwc_heartbeat_s", func() { c.Heartbeat = d.Heartbeat })
	fix(c.TestOffDuration < 2*time.Minute || c.TestOffDuration > time.Hour, "hwc_test_off_min", func() { c.TestOffDuration = d.TestOffDuration })
	return msgs
}

// ParseHHMM parses "18:00" to minutes after midnight.
func ParseHHMM(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// ParseHHMMList parses "22:00,23:00".
func ParseHHMMList(s string) ([]int, bool) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		v, ok := ParseHHMM(p)
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}
