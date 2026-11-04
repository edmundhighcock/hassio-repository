// Package modules defines the pluggable "virtual sensor" interface this
// add-on hosts, and a registry mapping a module name (the add-on's
// `modules` option) to a constructor. A module declares the HA entities
// it reads and the MQTT-discovery sensors it publishes, and consumes
// state updates in time order - the same interface is driven both live
// (internal/haws, one update at a time) and from backfill (cmd's
// `backfill` subcommand, replayed from InfluxDB in bulk). See DOCS.md
// "Adding a module".
package modules

import "time"

// InputSpec describes one Home Assistant entity a module reads.
type InputSpec struct {
	EntityID string
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
}

// StateUpdate is one Home Assistant state_changed event (or one history
// point replayed during warm-up), always delivered to a module in
// non-decreasing time order.
type StateUpdate struct {
	EntityID  string
	Time      time.Time
	Value     float64 // parsed numeric state
	Available bool    // false if state was "unavailable"/"unknown"/non-numeric
}

// OutputUpdate is one output value a module wants published right now.
type OutputUpdate struct {
	ObjectID string
	Time     time.Time
	Value    float64
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
