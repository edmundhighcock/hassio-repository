// Package example is a trivial second module, included only to prove the
// modules.Module interface is actually reusable and not hot_water-shaped
// in disguise. It is NOT in config.yaml's default `modules` list - see
// DOCS.md "Adding a module". It republishes one input entity's value,
// unchanged, as a differently-named output sensor.
package example

import (
	"encoding/json"

	"virtual_sensors/internal/modules"
)

const (
	InputEntity = "example_passthrough_input"
	OutputID    = "example_passthrough_output"
)

func init() {
	modules.Register("example_passthrough", func() modules.Module { return &Module{} })
}

type Module struct {
	last float64
}

func (m *Module) Name() string { return "example_passthrough" }

func (m *Module) Inputs() []modules.InputSpec {
	return []modules.InputSpec{{EntityID: InputEntity}}
}

func (m *Module) Outputs() []modules.OutputSpec {
	return []modules.OutputSpec{{
		ObjectID: OutputID, UniqueID: "virtual_sensors_" + OutputID,
		Name: "Example passthrough output", StateClass: "measurement",
	}}
}

func (m *Module) Init(persisted []byte) error {
	if len(persisted) == 0 {
		return nil
	}
	var v struct {
		Last float64 `json:"last"`
	}
	if err := json.Unmarshal(persisted, &v); err == nil {
		m.last = v.Last
	}
	return nil
}

func (m *Module) Persist() ([]byte, error) {
	return json.Marshal(struct {
		Last float64 `json:"last"`
	}{m.last})
}

func (m *Module) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	if !u.Available || u.EntityID != InputEntity {
		return nil
	}
	m.last = u.Value
	return []modules.OutputUpdate{{ObjectID: OutputID, Time: u.Time, Value: u.Value}}
}
