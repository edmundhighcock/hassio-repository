package hotwater

import "time"

// Signals (0.2.0) carry what the module already computes to an in-process
// consumer (the hot_water_control module), with no HA round trip. They are
// emitted only when a sink is set (SetSignalSink) and never change the
// module's own behaviour or outputs. Replay is true while the host is
// replaying warm-up history.

// ResidualSample is every evaluation of the published residual: one per
// fresh grid sample. Available=false when the grid input itself reported
// unavailable/unknown (the residual sensor goes unavailable too).
type ResidualSample struct {
	T                time.Time
	W                float64
	Available        bool
	MissingLoads     []string
	DiscontinuitySeq int
	Replay           bool
}

// Discontinuity is one known-load loss/regain (0.1.7), exact and
// immediate. Seq is the module's discontinuity_seq after this event.
type Discontinuity struct {
	T      time.Time
	Entity string
	Kind   string // "lost" | "regained"
	DeltaW float64
	Seq    int
	Replay bool
}

// MergedStep is one merged (and, where bridged, staged) step as handed to
// the pairing state machine. Rejected reports the coincident-load /
// discontinuity reject check for this step's window.
type MergedStep struct {
	TStart, TEnd time.Time
	Magnitude    float64
	Rejected     bool
	Replay       bool
}

// CycleOpened is a fresh cycle open (not a re-anchor of an open cycle).
type CycleOpened struct {
	OnTime    time.Time
	Magnitude float64
	Replay    bool
}

// CycleClosed is the end of the cycle last reported by CycleOpened.
// Accepted is true only for a dwell-confirmed close that credited energy;
// a floor violation or a max-duration timeout closes unaccepted. OnTime is
// the cycle's current anchor (a re-anchor moves it), so consumers should
// match on their own record of the open.
type CycleClosed struct {
	OnTime, OffTime time.Time
	Accepted        bool
	Kwh             float64
	Reason          string // "dwell_confirmed" | "short" | "floor" | "timeout"
	Replay          bool
}

// SetSignalSink implements modules.SignalSource.
func (m *Module) SetSignalSink(f func(any)) { m.sink = f }

func (m *Module) signal(s any) {
	if m.sink != nil {
		m.sink(s)
	}
}

func (m *Module) missingLoads() []string {
	missing := make([]string, 0, len(KnownLoadEntities))
	for _, name := range KnownLoadEntities {
		if _, ok := m.lastLoad[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}
