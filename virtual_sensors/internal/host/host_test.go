package host

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"virtual_sensors/internal/haws"
	"virtual_sensors/internal/modules"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

type fakeHA struct {
	calls  []string
	nextID int
	fail   bool
}

func (f *fakeHA) CallService(domain, service string, data, target map[string]any) (int, error) {
	f.calls = append(f.calls, domain+"."+service)
	f.nextID++
	if f.fail {
		return 0, os.ErrClosed
	}
	return f.nextID, nil
}

type fakePub struct{ outs []modules.OutputUpdate }

func (p *fakePub) PublishState(u modules.OutputUpdate) error { p.outs = append(p.outs, u); return nil }

// actor is a minimal Actor/Ticker/SignalSink module.
type actor struct {
	updates []modules.StateUpdate
	ticks   int
	results []modules.CommandResult
	signals []any
	effects []modules.Effect
	links   []bool
	conn    bool
}

func (a *actor) Name() string { return "actor" }
func (a *actor) Inputs() []modules.InputSpec {
	return []modules.InputSpec{
		{EntityID: "cover.x", Attributes: []string{"current_position"}},
		{EntityID: "some_power"},
		{EntityID: "automation.hwc_", Prefix: true},
	}
}
func (a *actor) Outputs() []modules.OutputSpec { return nil }
func (a *actor) Init([]byte) error             { return nil }
func (a *actor) Persist() ([]byte, error)      { return []byte(`{"ok":true}`), nil }
func (a *actor) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	a.updates = append(a.updates, u)
	return nil
}
func (a *actor) OnTick(time.Time) []modules.OutputUpdate { a.ticks++; return nil }
func (a *actor) Effects() []modules.Effect {
	e := a.effects
	a.effects = nil
	return e
}
func (a *actor) OnCommandResult(r modules.CommandResult) []modules.OutputUpdate {
	a.results = append(a.results, r)
	return nil
}
func (a *actor) OnLink(up bool, _ time.Time) []modules.OutputUpdate {
	a.links = append(a.links, up)
	return nil
}
func (a *actor) OnSignal(s any)                       { a.signals = append(a.signals, s) }
func (a *actor) SignalsConnected()                    { a.conn = true }
func (a *actor) DrainOutputs() []modules.OutputUpdate { return nil }

type source struct {
	sink func(any)
}

func (s *source) Name() string                  { return "src" }
func (s *source) Inputs() []modules.InputSpec   { return []modules.InputSpec{{EntityID: "some_power"}} }
func (s *source) Outputs() []modules.OutputSpec { return nil }
func (s *source) Init([]byte) error             { return nil }
func (s *source) Persist() ([]byte, error)      { return nil, nil }
func (s *source) SetSignalSink(f func(any))     { s.sink = f }
func (s *source) OnStateUpdate(u modules.StateUpdate) []modules.OutputUpdate {
	s.sink(u.Value)
	return nil
}

func TestRoutingAttrsPrefixReplayAndSignals(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	a, src := &actor{}, &source{}
	h := New([]modules.Module{src, a}, &fakePub{}, clk, "")
	if !a.conn {
		t.Fatal("signal sink not connected")
	}
	if !h.Match("sensor.some_power") || !h.Match("automation.hwc_x") || h.Match("sensor.other") {
		t.Fatal("Match wrong")
	}
	if got := h.AttrWhitelist("cover.x"); len(got) != 1 || got[0] != "current_position" {
		t.Fatalf("attr whitelist %v", got)
	}
	h.HandleState(haws.StateEvent{EntityID: "sensor.some_power", Time: clk.t, State: "12.5"}, true)
	h.HandleState(haws.StateEvent{EntityID: "automation.hwc_x", Time: clk.t, State: "on"}, false)
	h.HandleState(haws.StateEvent{EntityID: "sensor.unrelated", Time: clk.t, State: "1"}, false)
	if len(a.updates) != 2 {
		t.Fatalf("updates %d", len(a.updates))
	}
	if u := a.updates[0]; u.EntityID != "some_power" || !u.Replay || u.Value != 12.5 || u.State != "12.5" {
		t.Fatalf("bad update %+v", u)
	}
	if u := a.updates[1]; u.EntityID != "automation.hwc_x" || u.State != "on" || u.Replay {
		t.Fatalf("prefix update %+v", u)
	}
	if len(a.signals) != 1 || a.signals[0].(float64) != 12.5 {
		t.Fatalf("signals %v", a.signals)
	}
}

func TestEffectsResultsTimeoutAndLink(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	a := &actor{}
	h := New([]modules.Module{a}, &fakePub{}, clk, "")
	ha := &fakeHA{}

	// Not connected: the call fails back as a result at once.
	a.effects = []modules.Effect{{Kind: modules.EffectCallService, Domain: "cover", Service: "set_cover_position", CorrID: 7}}
	h.Tick()
	if len(a.results) != 1 || a.results[0].OK || a.results[0].CorrID != 7 {
		t.Fatalf("expected not-connected failure, got %+v", a.results)
	}

	h.LinkUp(ha)
	if len(a.links) != 1 || !a.links[0] {
		t.Fatal("link up not delivered")
	}
	a.effects = []modules.Effect{{Kind: modules.EffectCallService, Domain: "cover", Service: "set_cover_position",
		Data: map[string]any{"entity_id": "cover.x", "position": 99}, CorrID: 8}}
	h.Tick()
	if len(ha.calls) != 1 {
		t.Fatalf("calls %v", ha.calls)
	}
	h.HandleResult(haws.Result{ID: 1, Success: true})
	if r := a.results[len(a.results)-1]; !r.OK || r.CorrID != 8 {
		t.Fatalf("result %+v", r)
	}

	// Timeout after CommandTimeout with no reply.
	a.effects = []modules.Effect{{Kind: modules.EffectCallService, Domain: "cover", Service: "x", CorrID: 9}}
	h.Tick()
	clk.t = clk.t.Add(CommandTimeout + time.Second)
	h.Tick()
	if r := a.results[len(a.results)-1]; r.OK || r.CorrID != 9 || r.Err != "timeout" {
		t.Fatalf("timeout result %+v", r)
	}

	// Link down fails pending commands.
	a.effects = []modules.Effect{{Kind: modules.EffectCallService, Domain: "cover", Service: "x", CorrID: 10}}
	h.Tick()
	h.LinkDown()
	if r := a.results[len(a.results)-1]; r.OK || r.CorrID != 10 || r.Err != "connection lost" {
		t.Fatalf("link-down result %+v", r)
	}
}

func TestAtomicPersist(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Unix(1000, 0)}
	a := &actor{}
	h := New([]modules.Module{a}, &fakePub{}, clk, dir)
	a.effects = []modules.Effect{{Kind: modules.EffectPersistNow}}
	h.Tick() // Tick also persists on its first call; PersistNow writes regardless
	b, err := os.ReadFile(filepath.Join(dir, "actor-state.json"))
	if err != nil || string(b) != `{"ok":true}` {
		t.Fatalf("state file %q %v", b, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temp files left behind: %v", ents)
	}
}
