// Package modules defines the pluggable "virtual sensor" interface this
// add-on hosts, and a registry mapping a module name (the add-on's
// `modules` option) to a constructor. A module declares the HA entities
// it reads and the MQTT-discovery sensors it publishes, and consumes
// state updates in time order - the same interface is driven both live
// (internal/haws, one update at a time) and from backfill (cmd's
// `backfill` subcommand, replayed from InfluxDB in bulk). See DOCS.md
// "Adding a module".
package modules

import (
	"encoding/json"
	"time"
)

// InputSpec describes one Home Assistant entity a module reads.
//
// EntityID follows the existing convention: a `sensor.` entity is named
// without its domain (as the InfluxDB buckets name it), any other domain
// is named in full (`cover.hot_water_switch`). Prefix (0.2.0), when true,
// matches every entity whose full id starts with EntityID (used for the
// `automation.hwc_` conflict interlock). Attributes (0.2.0) is the
// whitelist of state attributes passed through to the module in
// StateUpdate.Attrs; every other attribute is dropped undecoded.
type InputSpec struct {
	EntityID   string
	Prefix     bool
	Attributes []string
}

// OutputSpec describes one sensor a module publishes via MQTT discovery.
// ObjectID becomes the entity id `sensor.<ObjectID>` in Home Assistant
// (HA builds entity_id from the discovery topic's object_id, not from
// Name) - see DOCS.md "Entities published".
type OutputSpec struct {
	ObjectID    string
	UniqueID    string
	Name        string
	DeviceClass string // HA device_class, e.g. "power", "energy"
	StateClass  string // HA state_class, e.g. "measurement", "total_increasing"
	Unit        string // HA unit_of_measurement, e.g. "W", "kWh"
	// PerEntityAvailability: if true, this sensor is published with its
	// OWN retained availability topic (online/offline) instead of the
	// add-on's shared device-wide one, so an OutputUpdate with
	// Unavailable=true for this ObjectID marks only this entity
	// unavailable in Home Assistant, not every sensor this add-on
	// publishes. See mqttpub.Publisher.PublishState.
	PerEntityAvailability bool
	// HasAttributes: if true, this sensor's discovery config also gets a
	// `json_attributes_topic` (0.1.7), and an OutputUpdate for this
	// ObjectID with AttributesJSON set has that JSON published retained
	// to it. Used for structured, consumer-readable metadata (e.g. hot
	// water's residual sensor: which known loads are currently excluded
	// and the discontinuities that caused it) that doesn't belong in the
	// numeric state itself.
	HasAttributes bool
}

// StateUpdate is one Home Assistant state_changed event (or one history
// point replayed during warm-up), always delivered to a module in
// non-decreasing time order.
type StateUpdate struct {
	EntityID  string
	Time      time.Time
	Value     float64 // parsed numeric state
	Available bool    // false if state was "unavailable"/"unknown"/non-numeric
	// State (0.2.0) is the raw state string, for string-valued entities
	// (input_select, motor_state, automation on/off). Always set by the
	// live host; empty from the backfill.
	State string
	// Attrs (0.2.0) holds the whitelisted attributes (InputSpec.Attributes)
	// as raw JSON, nil when none were requested or present.
	Attrs map[string]json.RawMessage
	// Replay (0.2.0) is true for warm-up history replayed at (re)connect.
	// A module that acts on the world (the controller) must never actuate
	// on a Replay update.
	Replay bool
}

// Effect (0.2.0) is something a module asks the host to do outside the
// process. Modules stay free of I/O: they return effects and the host
// executes them, feeding each service call's outcome back as a
// CommandResult with the same CorrID.
type Effect struct {
	Kind EffectKind
	// For EffectCallService.
	Domain  string
	Service string
	Data    map[string]any
	CorrID  uint64
}

// EffectKind enumerates Effect kinds.
type EffectKind int

const (
	// EffectCallService calls an HA service over the WebSocket.
	EffectCallService EffectKind = iota + 1
	// EffectPersistNow asks the host to write the module's state file now
	// (atomically), rather than waiting for the minute timer.
	EffectPersistNow
)

// CommandResult (0.2.0) is the outcome of one EffectCallService.
type CommandResult struct {
	CorrID uint64
	OK     bool
	Err    string
	At     time.Time
}

// Ticker (0.2.0) is optional: the host calls OnTick every TickInterval
// with its (injectable) clock's time.
type Ticker interface {
	OnTick(now time.Time) []OutputUpdate
}

// TickInterval is the host's tick period for Ticker modules.
const TickInterval = 5 * time.Second

// Actor (0.2.0) is optional: a module that asks the host to act. The host
// drains Effects after every call into the module and after every signal
// dispatch, executes them in order, and feeds service-call outcomes back
// through OnCommandResult.
type Actor interface {
	Effects() []Effect
	OnCommandResult(r CommandResult) []OutputUpdate
}

// LinkAware (0.2.0) is optional: told when the HA WebSocket connection
// comes up (subscribed and current states loaded) or goes down.
type LinkAware interface {
	OnLink(up bool, now time.Time) []OutputUpdate
}

// Configurable (0.2.0) is optional: handed the raw add-on options JSON
// before Init, so a module can read its own keys.
type Configurable interface {
	Configure(rawOptions []byte) error
}

// SignalSource (0.2.0) is optional: a module that emits in-process
// signals (hot_water's residual samples, discontinuities and cycles).
type SignalSource interface {
	SetSignalSink(func(any))
}

// SignalSink (0.2.0) is optional: a module that consumes another module's
// signals. The host calls SignalsConnected once at start if a source was
// wired to it. Outputs produced while handling a signal are collected by
// DrainOutputs, which the host calls after every dispatch.
type SignalSink interface {
	OnSignal(s any)
	SignalsConnected()
	DrainOutputs() []OutputUpdate
}

// OutputUpdate is one output value a module wants published right now.
type OutputUpdate struct {
	ObjectID string
	Time     time.Time
	Value    float64
	// Unavailable, when true, means Value is not meaningful: the
	// publisher should mark this ObjectID's entity unavailable (via its
	// PerEntityAvailability topic, see OutputSpec) rather than publish
	// Value as a state. Only meaningful for outputs with
	// PerEntityAvailability set; ignored otherwise.
	Unavailable bool
	// AttributesJSON, if non-empty, is published retained to this
	// ObjectID's json_attributes_topic (see OutputSpec.HasAttributes).
	// Already-marshalled JSON, not a Go value, so mqttpub stays ignorant
	// of any module's attribute schema.
	AttributesJSON string
	// AttributesOnly, if true, means this update carries ONLY an
	// attributes change (AttributesJSON) with nothing new to say about
	// Value/Unavailable - e.g. a module reacting to something other than
	// its own primary input tick. The publisher must publish the
	// attributes topic and otherwise leave the state/availability topics
	// untouched.
	AttributesOnly bool
	// StateString (0.2.0), if non-empty, is published as the state
	// instead of Value (for enum/text sensors).
	StateString string
}

// InitialStater is optional: a module implementing it has its current
// outputs published once MQTT is up, so restored totals appear at start
// rather than "unknown" until the next change.
type InitialStater interface {
	InitialStates() []OutputUpdate
}

// Module is the interface every virtual-sensor module implements.
type Module interface {
	// Name is the registry key - must match what's registered with Register.
	Name() string
	Inputs() []InputSpec
	Outputs() []OutputSpec

	// Init is called exactly once at startup, before any OnStateUpdate,
	// with the module's own previously-persisted bytes (nil/empty if
	// there were none - first run, or a module new to this install).
	Init(persisted []byte) error

	// OnStateUpdate consumes one input update, in time order (including
	// updates replayed from HA history during startup warm-up), and
	// returns zero or more OutputUpdate values to publish. Must not
	// block or do network I/O - the caller (live loop or backfill
	// replay) owns publishing.
	OnStateUpdate(u StateUpdate) []OutputUpdate

	// Persist returns the module's current state, written to
	// /data/<name>-state.json by the caller after every OnStateUpdate
	// batch and on clean shutdown, and handed back to Init on the next
	// start - see DOCS.md "Restart semantics".
	Persist() ([]byte, error)
}

// Constructor builds a fresh, unconfigured instance of a module.
type Constructor func() Module

var registry = map[string]Constructor{}

// Register adds a module constructor to the registry. Called from each
// module package's init(), so importing the package for its side effect
// (see cmd/virtual_sensors/main.go) is enough to make it available.
func Register(name string, c Constructor) {
	if _, exists := registry[name]; exists {
		panic("modules: duplicate registration for " + name)
	}
	registry[name] = c
}

// Get returns the constructor registered under name, if any.
func Get(name string) (Constructor, bool) {
	c, ok := registry[name]
	return c, ok
}

// Names returns every registered module name.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
