package core

import (
	"encoding/json"
	"time"
)

// SchemaVersion of the persisted file.
const SchemaVersion = 1

// Persisted is /data/hot_water_control-state.json (PLAN §2.6). Written on
// every state transition (PersistNow) and every minute by the host.
type Persisted struct {
	Schema               int       `json:"schema"`
	State                State     `json:"state"`
	StateSince           time.Time `json:"state_since"`
	LastGenuineStart     time.Time `json:"last_genuine_start"`
	LastGenuineQualifier string    `json:"last_genuine_qualifier"`
	// OnSSinceGenuineStart is the probe/alert clock: seconds of believed-ON
	// time since the last genuine start (§4.4).
	OnSSinceGenuineStart float64 `json:"on_s_since_genuine_start"`
	// OnClockAt is the time up to which ON time has been accounted, so a
	// restart's warm-up replay continues the clock from there.
	OnClockAt               time.Time   `json:"on_clock_at"`
	NextProbeOnS            float64     `json:"next_probe_on_s"`
	NextBackstopAlertOnS    float64     `json:"next_backstop_alert_on_s"`
	LastFinishedReheatStart time.Time   `json:"last_finished_reheat_start"`
	LastFinishedReheatEnd   time.Time   `json:"last_finished_reheat_end"`
	Boost                   BoostState  `json:"boost"`
	LastCommand             LastCommand `json:"last_command"`
	LastKnownPosition       float64     `json:"last_known_position"`
	LastKnownTarget         float64     `json:"last_known_target"`
	Moves                   []time.Time `json:"moves"`
	Strikes                 int         `json:"strikes"`
	LastStrikeAt            time.Time   `json:"last_strike_at"`
	Hold                    HoldState   `json:"hold"`
	CurrentOffPeriod        *OffPeriod  `json:"current_off_period,omitempty"`
	OffSince                time.Time   `json:"off_since"`
	// AbandonedUntil: the end of the last abandoned OFF period; the next
	// OFF needs a planned period starting at or after it (review fix 5).
	AbandonedUntil time.Time            `json:"abandoned_until"`
	AlertsSent     map[string]time.Time `json:"alerts_sent"`
	BatteryLast    float64              `json:"battery_last"`
	BatterySeries  []float64            `json:"battery_series"`
	BatteryDay     string               `json:"battery_day"`
	BootCount      int                  `json:"boot_count"`
	// Cycle is the detector cycle currently open, as the core saw it open.
	Cycle *CycleRec `json:"cycle,omitempty"`
	// Retreats counts drifts seen in IDLE_ON (telemetry only).
	Retreats int `json:"retreats"`
	// Reheats is the finished-reheat log (newest last, at most
	// MaxReheatLog) and Windows the active DefaultOff plan's windows, for
	// planner v2 (0.6.0). Absent in older files: empty.
	Reheats []ReheatRec `json:"reheats,omitempty"`
	// SatisfiedEnd: the end of the last heating window that counted as a
	// satisfied tank (clock.go); LastOffCmdAt: our last OFF command.
	SatisfiedEnd time.Time    `json:"satisfied_end,omitempty"`
	LastOffCmdAt time.Time    `json:"last_off_cmd_at,omitempty"`
	Windows      []PlanWindow `json:"windows,omitempty"`
}

// BoostState persists a boost so a restart mid-boost continues ON (§4.7).
type BoostState struct {
	Active          bool      `json:"active"`
	PressedAt       time.Time `json:"pressed_at"`
	ReheatSeenStart bool      `json:"reheat_seen_start"`
}

// LastCommand is the last cover command and its verdict.
type LastCommand struct {
	Target  int       `json:"target"`
	At      time.Time `json:"at"`
	Verdict string    `json:"verdict"`
}

// HoldState is the latched hold.
type HoldState struct {
	Latched bool      `json:"latched"`
	Reason  string    `json:"reason"`
	Since   time.Time `json:"since"`
}

// CycleRec is the core's record of an open detector cycle.
type CycleRec struct {
	OnTime    time.Time `json:"on_time"`
	Magnitude float64   `json:"magnitude"`
	Genuine   bool      `json:"genuine"`
}

func newPersisted() Persisted {
	return Persisted{Schema: SchemaVersion, State: StBoot, AlertsSent: map[string]time.Time{}}
}

// LoadPersisted parses the state file. A missing, corrupt or wrong-schema
// file gives fresh state with every clock unknown: fail-on, no OFF until a
// finished reheat is seen or seeded (§2.6). ok reports whether the file
// was used.
func LoadPersisted(b []byte) (Persisted, bool) {
	p := newPersisted()
	if len(b) == 0 {
		return p, false
	}
	// PLAN §2.6's own example writes "" for an unset time; null is the
	// other natural spelling. Both mean unset: drop them before decoding
	// (every field's zero value is "unset"), so they parse instead of
	// failing the whole file.
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return p, false
	}
	clean, err := json.Marshal(dropUnset(raw))
	if err != nil {
		return p, false
	}
	var q Persisted
	if err := json.Unmarshal(clean, &q); err != nil || q.Schema != SchemaVersion {
		return p, false
	}
	if q.AlertsSent == nil {
		q.AlertsSent = map[string]time.Time{}
	}
	if q.OnSSinceGenuineStart < 0 {
		q.OnSSinceGenuineStart = 0
	}
	return q, true
}

// dropUnset removes "" and null object members and array elements,
// recursively.
func dropUnset(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if e == nil || e == "" {
				delete(x, k)
				continue
			}
			x[k] = dropUnset(e)
		}
		return x
	case []any:
		out := x[:0]
		for _, e := range x {
			if e == nil || e == "" {
				continue
			}
			out = append(out, dropUnset(e))
		}
		return out
	}
	return v
}

// Marshal serialises the persisted state.
func (p Persisted) Marshal() ([]byte, error) { return json.Marshal(p) }
