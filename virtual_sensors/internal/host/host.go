// Package host (0.2.0) is the add-on's imperative shell around its
// modules: it routes Home Assistant state events to modules, ticks them,
// wires hot_water's in-process signals to their consumer, executes the
// Effects modules return (service calls, persist-now), feeds service-call
// outcomes back, and persists module state atomically.
//
// Every method must be called from ONE goroutine (the event loop in
// cmd/virtual_sensors/main.go). The clock and the HA client are
// interfaces, so a test or simulator can drive a Host from recorded data
// with a virtual clock and a fake HA (see host_test.go).
package host

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"virtual_sensors/internal/haws"
	"virtual_sensors/internal/modules"
)

// Clock is the host's only source of time.
type Clock interface{ Now() time.Time }

// SystemClock is the real clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// HA is the part of the Home Assistant client the host acts through.
// CallService returns the WS id whose Result will later be passed to
// HandleResult.
type HA interface {
	CallService(domain, service string, data, target map[string]any) (int, error)
}

// ResponseCaller is optionally implemented by the HA client: a service call
// that asks HA to return response data (Effect.ReturnResponse).
type ResponseCaller interface {
	CallServiceResponse(domain, service string, data, target map[string]any) (int, error)
}

// Publisher publishes module outputs (mqttpub.Publisher in production).
type Publisher interface {
	PublishState(u modules.OutputUpdate) error
}

// CommandTimeout bounds how long a call_service may go unanswered.
const CommandTimeout = 10 * time.Second

// PersistEvery is the periodic persist interval.
const PersistEvery = time.Minute

type pendingCmd struct {
	mod      modules.Actor
	corrID   uint64
	deadline time.Time
}

// Host routes events to modules. Construct with New.
type Host struct {
	Clock    Clock
	Pub      Publisher
	StateDir string // where <module>-state.json files live ("" disables persistence)
	Logf     func(format string, args ...any)

	mods     []modules.Module
	exact    map[string][]modules.Module // key: module-convention id (sensor. stripped)
	prefixes []prefixRoute
	attrs    map[string][]string // full entity id -> attribute whitelist

	ha          HA
	pending     map[int]pendingCmd
	queued      []queuedResult
	lastPersist time.Time
	lastTick    time.Time
}

type prefixRoute struct {
	prefix string
	mod    modules.Module
	attrs  []string
}

type queuedResult struct {
	mod modules.Actor
	res modules.CommandResult
}

// New builds a Host for already-initialised modules and wires signal
// sources to sinks.
func New(mods []modules.Module, pub Publisher, clock Clock, stateDir string) *Host {
	h := &Host{
		Clock: clock, Pub: pub, StateDir: stateDir, Logf: log.Printf,
		mods: mods, exact: map[string][]modules.Module{}, attrs: map[string][]string{},
		pending: map[int]pendingCmd{},
	}
	for _, m := range mods {
		for _, in := range m.Inputs() {
			if in.Prefix {
				h.prefixes = append(h.prefixes, prefixRoute{prefix: in.EntityID, mod: m, attrs: in.Attributes})
				continue
			}
			h.exact[in.EntityID] = append(h.exact[in.EntityID], m)
			if len(in.Attributes) > 0 {
				full := FullID(in.EntityID)
				h.attrs[full] = appendUnique(h.attrs[full], in.Attributes...)
			}
		}
	}
	var sinks []modules.SignalSink
	for _, m := range mods {
		if s, ok := m.(modules.SignalSink); ok {
			sinks = append(sinks, s)
		}
	}
	if len(sinks) > 0 {
		for _, m := range mods {
			if src, ok := m.(modules.SignalSource); ok {
				src.SetSignalSink(func(sig any) {
					for _, s := range sinks {
						s.OnSignal(sig)
					}
				})
				for _, s := range sinks {
					s.SignalsConnected()
				}
				break
			}
		}
	}
	return h
}

func appendUnique(dst []string, add ...string) []string {
	for _, a := range add {
		found := false
		for _, d := range dst {
			if d == a {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, a)
		}
	}
	return dst
}

// FullID turns a module-convention id into a full HA entity id.
func FullID(id string) string {
	if strings.Contains(id, ".") {
		return id
	}
	return "sensor." + id
}

// moduleID is the inverse of FullID.
func moduleID(full string) string { return strings.TrimPrefix(full, "sensor.") }

// ExactEntityIDs lists every exactly-named input (module convention), for
// the history warm-up request.
func (h *Host) ExactEntityIDs() []string {
	ids := make([]string, 0, len(h.exact))
	for id := range h.exact {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Match reports whether a full entity id is an input of any module.
func (h *Host) Match(full string) bool {
	if _, ok := h.exact[moduleID(full)]; ok {
		return true
	}
	for _, p := range h.prefixes {
		if strings.HasPrefix(full, p.prefix) {
			return true
		}
	}
	return false
}

// AttrWhitelist returns the attributes to keep for a full entity id.
func (h *Host) AttrWhitelist(full string) []string {
	out := h.attrs[full]
	for _, p := range h.prefixes {
		if strings.HasPrefix(full, p.prefix) && len(p.attrs) > 0 {
			out = appendUnique(append([]string{}, out...), p.attrs...)
		}
	}
	return out
}

// HandleState dispatches one state event (live, snapshot or replayed).
func (h *Host) HandleState(ev haws.StateEvent, replay bool) {
	id := moduleID(ev.EntityID)
	targets := h.exact[id]
	for _, p := range h.prefixes {
		if strings.HasPrefix(ev.EntityID, p.prefix) {
			targets = append(targets, p.mod)
		}
	}
	if len(targets) == 0 {
		return
	}
	value, available := haws.ParseFloat(ev.State)
	u := modules.StateUpdate{
		EntityID: id, Time: ev.Time, Value: value, Available: available,
		State: ev.State, Attrs: ev.Attrs, Replay: replay,
	}
	for _, m := range targets {
		upd := u
		if h.prefixMatchOnly(m, id, ev.EntityID) {
			upd.EntityID = ev.EntityID
		}
		h.publish(m.OnStateUpdate(upd))
		h.afterCall()
	}
}

func (h *Host) prefixMatchOnly(m modules.Module, id, full string) bool {
	for _, em := range h.exact[id] {
		if em == m {
			return false
		}
	}
	return true
}

// HandleResult routes a WS command result to the module that sent it.
func (h *Host) HandleResult(r haws.Result) {
	p, ok := h.pending[r.ID]
	if !ok {
		return
	}
	delete(h.pending, r.ID)
	h.deliver(p.mod, modules.CommandResult{CorrID: p.corrID, OK: r.Success, Err: r.Err, At: h.Clock.Now(), Response: r.Response})
}

// Tick drives Ticker modules, times out unanswered commands and persists
// every PersistEvery. Call it every modules.TickInterval.
func (h *Host) Tick() {
	now := h.Clock.Now()
	h.lastTick = now
	for id, p := range h.pending {
		if now.After(p.deadline) {
			delete(h.pending, id)
			h.deliver(p.mod, modules.CommandResult{CorrID: p.corrID, OK: false, Err: "timeout", At: now})
		}
	}
	for _, m := range h.mods {
		if t, ok := m.(modules.Ticker); ok {
			h.publish(t.OnTick(now))
			h.afterCall()
		}
	}
	if h.lastPersist.IsZero() || now.Sub(h.lastPersist) >= PersistEvery {
		h.PersistAll()
		h.lastPersist = now
	}
}

// LinkUp tells the host (and LinkAware modules) that HA is reachable
// through ha: subscribed, and the current-state snapshot dispatched.
func (h *Host) LinkUp(ha HA) {
	h.ha = ha
	now := h.Clock.Now()
	for _, m := range h.mods {
		if l, ok := m.(modules.LinkAware); ok {
			h.publish(l.OnLink(true, now))
			h.afterCall()
		}
	}
}

// LinkDown fails every pending command and tells LinkAware modules.
func (h *Host) LinkDown() {
	h.ha = nil
	now := h.Clock.Now()
	for id, p := range h.pending {
		delete(h.pending, id)
		h.deliver(p.mod, modules.CommandResult{CorrID: p.corrID, OK: false, Err: "connection lost", At: now})
	}
	for _, m := range h.mods {
		if l, ok := m.(modules.LinkAware); ok {
			h.publish(l.OnLink(false, now))
			h.afterCall()
		}
	}
}

func (h *Host) deliver(a modules.Actor, r modules.CommandResult) {
	h.publish(a.OnCommandResult(r))
	h.afterCall()
}

// afterCall drains every module's effects and signal outputs, executing
// effects, until nothing more is produced (a failed send is fed back as a
// result, which may produce further effects).
func (h *Host) afterCall() {
	for guard := 0; guard < 100; guard++ {
		did := false
		for _, m := range h.mods {
			if s, ok := m.(modules.SignalSink); ok {
				if outs := s.DrainOutputs(); len(outs) > 0 {
					h.publish(outs)
				}
			}
			a, ok := m.(modules.Actor)
			if !ok {
				continue
			}
			for _, e := range a.Effects() {
				did = true
				h.execute(m, a, e)
			}
		}
		if len(h.queued) > 0 {
			q := h.queued
			h.queued = nil
			for _, qr := range q {
				did = true
				h.publish(qr.mod.OnCommandResult(qr.res))
			}
		}
		if !did {
			return
		}
	}
	h.Logf("host: ERROR effect loop did not settle after 100 rounds")
}

func (h *Host) execute(m modules.Module, a modules.Actor, e modules.Effect) {
	switch e.Kind {
	case modules.EffectPersistNow:
		h.Persist(m)
	case modules.EffectCallService:
		now := h.Clock.Now()
		if h.ha == nil {
			h.queued = append(h.queued, queuedResult{a, modules.CommandResult{CorrID: e.CorrID, Err: "not connected", At: now}})
			return
		}
		var target map[string]any
		data := map[string]any{}
		for k, v := range e.Data {
			if k == "entity_id" {
				target = map[string]any{"entity_id": v}
				continue
			}
			data[k] = v
		}
		var id int
		var err error
		if rc, ok := h.ha.(ResponseCaller); ok && e.ReturnResponse {
			id, err = rc.CallServiceResponse(e.Domain, e.Service, data, target)
		} else {
			id, err = h.ha.CallService(e.Domain, e.Service, data, target)
		}
		if err != nil {
			h.queued = append(h.queued, queuedResult{a, modules.CommandResult{CorrID: e.CorrID, Err: err.Error(), At: now}})
			return
		}
		h.pending[id] = pendingCmd{mod: a, corrID: e.CorrID, deadline: now.Add(CommandTimeout)}
	}
}

func (h *Host) publish(outs []modules.OutputUpdate) {
	if h.Pub == nil {
		return
	}
	for _, o := range outs {
		if err := h.Pub.PublishState(o); err != nil {
			h.Logf("ERROR: publishing %s: %v", o.ObjectID, err)
		}
	}
}

// PersistAll writes every module's state file.
func (h *Host) PersistAll() {
	for _, m := range h.mods {
		h.Persist(m)
	}
}

// Persist writes one module's state file atomically.
func (h *Host) Persist(m modules.Module) {
	if h.StateDir == "" {
		return
	}
	b, err := m.Persist()
	if err != nil {
		h.Logf("ERROR: %s: Persist failed: %v", m.Name(), err)
		return
	}
	if err := WriteFileAtomic(StatePath(h.StateDir, m.Name()), b); err != nil {
		h.Logf("ERROR: %s: could not write state file: %v", m.Name(), err)
	}
}

// StatePath is a module's state file path.
func StatePath(dir, name string) string { return filepath.Join(dir, name+"-state.json") }

// WriteFileAtomic writes b to path via a temp file in the same directory,
// fsync and rename, so a crash leaves either the old or the new file.
func WriteFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// Seeder (0.2.0) is optional: a module that wants a one-off history fetch
// after the first connect (the controller seeds its clocks from 24 h of
// detector output when its persisted clocks are unknown).
type Seeder interface {
	SeedRequest(now time.Time) (entityIDs []string, since time.Time, ok bool)
	OnSeed(updates []modules.StateUpdate)
}

// SeedRequests lists the modules wanting a seed and what they want.
func (h *Host) SeedRequests() []SeedReq {
	var out []SeedReq
	now := h.Clock.Now()
	for _, m := range h.mods {
		if s, ok := m.(Seeder); ok {
			if ids, since, want := s.SeedRequest(now); want {
				out = append(out, SeedReq{Mod: s, EntityIDs: ids, Since: since})
			}
		}
	}
	return out
}

// SeedReq is one module's seed request.
type SeedReq struct {
	Mod       Seeder
	EntityIDs []string
	Since     time.Time
}

// DeliverSeed hands fetched history to the requesting module.
func (h *Host) DeliverSeed(r SeedReq, evs []haws.StateEvent) {
	ups := make([]modules.StateUpdate, 0, len(evs))
	for _, ev := range evs {
		v, ok := haws.ParseFloat(ev.State)
		ups = append(ups, modules.StateUpdate{EntityID: moduleID(ev.EntityID), Time: ev.Time, Value: v, Available: ok, State: ev.State, Replay: true})
	}
	r.Mod.OnSeed(ups)
	h.afterCall()
}

// MarshalDebug is a tiny helper for logs.
func MarshalDebug(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
