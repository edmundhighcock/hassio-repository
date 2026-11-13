package hotwater

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"virtual_sensors/internal/modules"
)

// The three fixtures in testdata/ are Home Assistant history recorded on
// 2026-11-13 (times UTC; local is UTC+1):
//
//	A  19:05-19:40Z  the missed reheat: EV started 19:24:20Z, the heater
//	                 19:24:42Z, the EV stopped/started 19:26:15 and 19:27:19.
//	B  05:15-05:50Z  the kettle draw at 05:32:33Z with the switch closed
//	                 (no kettle meter existed yet).
//	C  20:06-20:36Z  the metered kettle boils (20:11:56Z and 20:20:16Z) and
//	                 the dishwasher's +2.2 kW heater step, L3 only, 20:21:24Z.
type rowT struct {
	t     float64
	ent   string
	state string
	l     [3]float64
	hasL  bool
}

func readFixture(t *testing.T, name string) []rowT {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []rowT
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		p := strings.Split(line, ",")
		if len(p) != 6 {
			t.Fatalf("bad row %q", line)
		}
		ts, _ := strconv.ParseFloat(p[0], 64)
		r := rowT{t: ts, ent: p[1], state: p[2]}
		if p[3] != "" {
			r.hasL = true
			for i := 0; i < 3; i++ {
				r.l[i], _ = strconv.ParseFloat(p[3+i], 64)
			}
		}
		rows = append(rows, r)
	}
	return rows
}

type replayResult struct {
	opened []CycleOpened
	closed []CycleClosed
	powers []modules.OutputUpdate
	m      *Module
}

// replay feeds a fixture through a module configured with opts.
func replay(t *testing.T, name, opts string) *replayResult {
	t.Helper()
	m := New()
	if err := m.Configure([]byte(opts)); err != nil {
		t.Fatal(err)
	}
	if err := m.Init(nil); err != nil {
		t.Fatal(err)
	}
	res := &replayResult{m: m}
	m.SetSignalSink(func(s any) {
		switch v := s.(type) {
		case CycleOpened:
			res.opened = append(res.opened, v)
		case CycleClosed:
			res.closed = append(res.closed, v)
		}
	})
	last := 0.0
	for _, r := range readFixture(t, name) {
		ts := r.t
		if ts <= last { // distinct instants, as live events are
			ts = last + 0.001
		}
		last = ts
		u := modules.StateUpdate{EntityID: r.ent, Time: unixTime(ts).UTC(), State: r.state}
		if v, err := strconv.ParseFloat(r.state, 64); err == nil {
			u.Value, u.Available = v, true
		}
		if r.hasL {
			u.Available = true
			u.Attrs = map[string]json.RawMessage{}
			for i, k := range []string{"state_currentL1", "state_currentL2", "state_currentL3"} {
				b, _ := json.Marshal(r.l[i])
				u.Attrs[k] = b
			}
		}
		for _, o := range m.OnStateUpdate(u) {
			if o.ObjectID == OutPowerObjectID {
				res.powers = append(res.powers, o)
			}
		}
	}
	return res
}

func at(h, m, s int) time.Time { return time.Date(2026, 11, 13, h, m, s, 0, time.UTC) }

func (r *replayResult) openedBetween(a, b time.Time) bool {
	for _, o := range r.opened {
		if !o.OnTime.Before(a) && !o.OnTime.After(b) {
			return true
		}
	}
	return false
}

func lastPowerValue(r *replayResult) float64 {
	if len(r.powers) == 0 {
		return 0
	}
	return r.powers[len(r.powers)-1].Value
}

// Problem 1: the 19:24:42Z reheat was missed. The EV's start 22 s earlier
// chained with the heater's step into one event of about +7 kW (outside
// the step band, and vetoed by the moving EV load).
func TestReplay0613MissedReheat(t *testing.T) {
	old := replay(t, "20261113_A_reheat.csv", `{"hw_plateau_rescue": false, "hw_phase_gate": false, "hw_switch_gate": false}`)
	if old.openedBetween(at(19, 24, 0), at(19, 40, 0)) || lastPowerValue(old) != 0 {
		t.Fatalf("0.6.1 behaviour no longer reproduces: opened %+v power %v", old.opened, lastPowerValue(old))
	}
	// the earlier 19:07:36Z thermostat top-up IS found by the step logic
	if !old.openedBetween(at(19, 7, 30), at(19, 8, 0)) {
		t.Fatalf("19:07 top-up not found: %+v", old.opened)
	}
	got := replay(t, "20261113_A_reheat.csv", `{}`)
	if !got.openedBetween(at(19, 24, 40), at(19, 25, 30)) {
		t.Fatalf("reheat not detected near 19:24:42Z: %+v", got.opened)
	}
	if p := lastPowerValue(got); p < 2500 || p > 3600 {
		t.Fatalf("heater power at the end of the window %v", p)
	}
	if len(got.opened) != 2 {
		t.Fatalf("want exactly the top-up and the reheat, got %+v", got.opened)
	}
}

// Problem 2: the 05:32:33Z kettle draw (+2.5 kW for 3 min) was credited as
// the heater while the switch was CLOSED. The switch gate alone and the
// phase gate alone (the draw is on L1 only, +11.4 A) each stop it.
func TestReplay0613KettleNotCredited(t *testing.T) {
	old := replay(t, "20261113_B_kettle.csv", `{"hw_switch_gate": false, "hw_phase_gate": false, "hw_plateau_rescue": false}`)
	if !old.openedBetween(at(5, 32, 0), at(5, 33, 0)) {
		t.Fatalf("0.6.1 behaviour no longer reproduces: %+v", old.opened)
	}
	for name, opts := range map[string]string{
		"defaults":     `{}`,
		"switch gate":  `{"hw_phase_gate": false}`,
		"phase gate":   `{"hw_switch_gate": false}`,
		"switch+plat.": `{"hw_phase_gate": false, "hw_plateau_rescue": true}`,
	} {
		got := replay(t, "20261113_B_kettle.csv", opts)
		if len(got.opened) != 0 || got.m.energyKwh != 0 {
			t.Errorf("%s: kettle credited: opened %+v energy %v", name, got.opened, got.m.energyKwh)
		}
	}
}

// Item 2: the kettle and air fryer are metered known loads, in W.
func TestKnownLoadsIncludeKettleAndAirFryer(t *testing.T) {
	have := map[string]bool{}
	for _, e := range KnownLoadEntities {
		have[e] = true
	}
	for _, e := range []string{"kettle_power", "air_fryer_power", "loaver_power"} {
		if !have[e] {
			t.Errorf("%s missing from KnownLoadEntities", e)
		}
	}
	m := newTestModule(t)
	pushGrid(t, m, 1000, 600)
	for _, e := range KnownLoadEntities {
		pushLoad(t, m, e, 1001, 0)
	}
	pushLoad(t, m, "kettle_power", 1002, 2500) // W
	outs := pushGrid(t, m, 1003, 3100)
	if r, _, _ := lastResidual(outs); r != 600 {
		t.Fatalf("kettle (W) not subtracted: residual %v", r)
	}
	pushLoad(t, m, "loaver_power", 1004, 3.5) // kW
	outs = pushGrid(t, m, 1005, 6600)
	if r, _, _ := lastResidual(outs); r != 600 {
		t.Fatalf("loaver (kW) not scaled: residual %v", r)
	}
}

// Problem 1/3 fixture C: with the heater already on, the metered kettle
// boils and the dishwasher's L3-only heater step (+2.2 kW at 20:21:24Z) are
// not credited: one cycle, found by the plateau fallback, never re-opened.
func TestReplay0613DishwasherAndKettleNotCredited(t *testing.T) {
	got := replay(t, "20261113_C_dishwasher.csv", `{}`)
	if len(got.opened) != 1 || len(got.closed) != 0 {
		dump(t, got)
		t.Fatalf("want one open cycle and no close, got %d opens %d closes", len(got.opened), len(got.closed))
	}
	for _, p := range got.powers {
		if p.Time.After(at(20, 8, 0)) {
			t.Errorf("a power update after the cycle opened (a re-anchor or close): %+v", p)
		}
	}
	// And a cycle that is NOT already open does not open for the dishwasher:
	// start the replay after the heater's plateau by cutting the window.
}

func dump(t *testing.T, r *replayResult) {
	for _, o := range r.opened {
		t.Logf("opened %s %.0f", o.OnTime.Format("15:04:05"), o.Magnitude)
	}
	for _, c := range r.closed {
		t.Logf("closed %s-%s %s acc=%v kwh=%.3f", c.OnTime.Format("15:04:05"), c.OffTime.Format("15:04:05"), c.Reason, c.Accepted, c.Kwh)
	}
}
