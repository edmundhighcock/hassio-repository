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
	OnSSinceGenuineStart    float64              `json:"on_s_since_genuine_start"`
	NextProbeOnS            float64              `json:"next_probe_on_s"`
	NextBackstopAlertOnS    float64              `json:"next_backstop_alert_on_s"`
	LastFinishedReheatStart time.Time            `json:"last_finished_reheat_start"`
	LastFinishedReheatEnd   time.Time            `json:"last_finished_reheat_end"`
	Boost                   BoostState           `json:"boost"`
	LastCommand             LastCommand          `json:"last_command"`
	LastKnownPosition       float64              `json:"last_known_position"`
	LastKnownTarget         float64              `json:"last_known_target"`
	Moves                   []time.Time          `json:"moves"`
	Strikes                 int                  `json:"strikes"`
	LastStrikeAt            time.Time            `json:"last_strike_at"`
	Hold                    HoldState            `json:"hold"`
	CurrentOffPeriod        *OffPeriod           `json:"current_off_period,omitempty"`
	OffSince                time.Time            `json:"off_since"`
	AlertsSent              map[string]time.Time `json:"alerts_sent"`
	BatteryLast             float64              `json:"battery_last"`
	BatterySeries           []float64            `json:"battery_series"`
	BatteryDay              string               `json:"battery_day"`
	BootCount               int                  `json:"boot_count"`
	// Cycle is the detector cycle currently open, as the core saw it open.
	Cycle *CycleRec `json:"cycle,omitempty"`
	// Retreats counts drifts seen in IDLE_ON (telemetry only).
	Retreats int `json:"retreats"`
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
	var q Persisted
	if err := json.Unmarshal(b, &q); err != nil || q.Schema != SchemaVersion {
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

// Marshal serialises the persisted state.
func (p Persisted) Marshal() ([]byte, error) { return json.Marshal(p) }
