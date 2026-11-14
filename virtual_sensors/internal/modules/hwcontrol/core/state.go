package core

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// held is the held value of one entity (§4.0).
type held struct {
	known bool
	avail bool
	at    time.Time
	str   string
	num   float64
	numOK bool
	pos   float64
	posOK bool
}

// Controller is the decision core. Construct with New; drive with Step.
type Controller struct {
	cfg         Config
	P           Persisted
	coverEntity string

	now     time.Time
	started bool

	in        map[Role]*held
	conflicts map[string]bool

	// residual (from the hook)
	res        heldSeries // raw held residual
	corr       uint64
	resKnown   bool
	resAvail   bool
	resAt      time.Time
	lastSeq    int
	seqKnown   bool
	cumDelta   float64
	corrected  heldSeries // residual - cumulative discontinuity DeltaW
	tracker    stepTracker
	discs      []discRec
	detTime    time.Time // latest residual sample time: how far the detector has seen
	linkUp     bool
	detPresent bool

	ev heldSeries

	plan *Plan

	mv  *move
	ver *verification

	// sequence bookkeeping
	onAttempts   int
	onRetryAt    time.Time
	onEndsOff    bool
	onOffDur     time.Duration
	subStep      int // RECYCLE_ON / PROBE / HOLD_OFF sub-steps
	recycleFrom  string
	holdOnFails  int
	holdOnRetry  time.Time
	conflictSent bool
	// probeUnderHold is the hold a backstop probe is running under (row
	// 29); the hold is re-entered when the probe ends.
	probeUnderHold string

	// planner v2 window-satisfied stamp (clock.go).
	winObs        map[int64]*winObs
	lastCycleOpen time.Time

	// populated: the first link-up (snapshot loaded) has happened.
	populated bool
	// histOn/histKnown: the switch state from replayed history, used for
	// the ON-time clock while HA is not linked.
	histOn, histKnown bool
	// shadowNextProbe is SHADOW's own would-probe schedule (ON seconds).
	shadowNextProbe float64

	// R3 manual test moves (PLAN §8).
	buttonPrev    map[Role]string
	testPending   string // "move" | "probe" | ""
	testPendingAt time.Time
	testOff       bool // the running OFF is a test move
	testProbe     bool // the running probe is a test probe

	hold      string
	bootSince time.Time
	// awaitPlanFrom is set (non-zero) while BOOT waits for the first plan
	// after a process start; planAt is when the last valid plan arrived;
	// settleWaived is the StateSince of an IDLE_ON entered from a boot that
	// waited for its plan (the plan has been seen, so no 15 min settle).
	awaitPlanFrom   time.Time
	planAt          time.Time
	settleWaived    time.Time
	healthySince    time.Time
	lastTick        time.Time
	live            bool
	exitingLive     bool
	overrideOffDone bool
	lastSelect      string
	boostPrev       string
	boostPrevKnown  bool
	heatOffCount    int
	heatOffUpAt     time.Time
	heatOffCounted  time.Time
	offPeriod       *OffPeriod // the period the running OFF belongs to
	lastHeartbeat   time.Time
	lastPub         map[string]string
	lastSig         string
	offGateVal      string
	offGateSince    time.Time
	offGatePlanOff  bool
	offGateAt       time.Time
	verdict         string
	verdictAttrs    map[string]any
	verdictAt       time.Time
	reason          string
	desired         string
	desiredReason   string
	shadowWould     string
	shadowReason    string
	shadowOff       *OffPeriod
	lastAlert       string
	lastAlertText   string
	lastAlertAt     time.Time
	lastSummaryDay  string
	shadowOffsToday int

	effects []Effect
}

// New builds a controller from config and persisted bytes (nil or corrupt
// means fresh state with every clock unknown). coverEntity is the cover's
// entity id, used only in the service-call data.
func New(cfg Config, persisted []byte, coverEntity string) *Controller {
	p, _ := LoadPersisted(persisted)
	c := &Controller{
		cfg: cfg, P: p, coverEntity: coverEntity,
		in: map[Role]*held{}, conflicts: map[string]bool{}, lastPub: map[string]string{},
		buttonPrev: map[Role]string{},
	}
	c.res.keep = 20 * time.Minute
	c.corrected.keep = 20 * time.Minute
	c.tracker.series.keep = 20 * time.Minute
	c.ev.keep = 10 * time.Minute
	if c.P.NextProbeOnS == 0 {
		c.P.NextProbeOnS = cfg.ProbeAfter.Seconds()
	}
	if c.P.NextBackstopAlertOnS == 0 {
		c.P.NextBackstopAlertOnS = cfg.AlertAfter.Seconds()
	}
	return c
}

// Config returns the controller's configuration.
func (c *Controller) Config() Config { return c.cfg }

// State returns the current state and hold reason.
func (c *Controller) State() (State, string) { return c.P.State, c.hold }

// Now returns the controller's notion of now.
func (c *Controller) Now() time.Time { return c.now }

// Live reports whether the controller is in LIVE mode (both keys).
func (c *Controller) Live() bool { return c.live || c.exitingLive }

// Persist returns the persisted bytes.
func (c *Controller) Persist() ([]byte, error) {
	c.P.Schema = SchemaVersion
	return c.P.Marshal()
}

// Facts returns the read-only facts offered to a planner (§4.2).
func (c *Controller) Facts() Facts {
	f := Facts{LastFinishedReheatEnd: c.P.LastFinishedReheatEnd, LastFinishedReheatStart: c.P.LastFinishedReheatStart,
		OffLimitSinceReheatEnd: c.cfg.OffLimitSinceReheatEnd}
	if te := c.tankEnd(); !te.IsZero() {
		if h := te.Add(c.cfg.OffLimitSinceReheatEnd).Sub(c.now); h > 0 {
			f.TankHeadroom = h
		}
	}
	if c.offPeriod != nil {
		op := *c.offPeriod
		f.CurrentOff = &op
	}
	f.Reheats = append([]ReheatRec(nil), c.P.Reheats...)
	f.Windows = append([]PlanWindow(nil), c.P.Windows...)
	if cy := c.P.Cycle; cy != nil {
		f.CycleOpen, f.CycleOpenSince = true, cy.OnTime
	}
	f.MovesLast24h = c.movesInLast(24 * time.Hour)
	f.MovesLastHour = c.movesInLast(time.Hour)
	f.Live = c.Live()
	return f
}

func (c *Controller) emit(e Effect) { c.effects = append(c.effects, e) }

func (c *Controller) logf(format string, args ...any) {
	c.emit(Effect{Kind: EffLog, Msg: fmt.Sprintf(format, args...)})
}

// Step consumes one input and returns the effects it causes.
func (c *Controller) Step(in Input) []Effect {
	if t := in.When(); t.After(c.now) {
		c.now = t
	}
	if !c.started {
		c.started = true
		c.P.BootCount++
		c.bootSince = c.now
		c.awaitPlanFrom = c.now
		c.lastTick = c.now
		c.setState(StBoot, "process start")
	}
	// The ON-time clock advances on data timestamps, replayed or live, so
	// warm-up replay reconstructs it exactly as live data would (row 2,
	// review fix 1). Ticks carry wall time and advance it only while HA
	// is linked (before the first link-up the replay has not arrived yet).
	switch v := in.(type) {
	case EntityUpdate, Sample, Discontinuity, CycleOpened, CycleClosed:
		c.advanceOnClock(in.When())
	case Tick:
		if c.linkUp {
			c.advanceOnClock(v.At)
		}
	case Link:
		if c.linkUp || v.Up {
			c.advanceOnClock(v.At)
		}
	}
	tick := false
	switch v := in.(type) {
	case EntityUpdate:
		c.onEntity(v)
	case Sample:
		c.onSample(v)
	case Discontinuity:
		c.onDiscontinuity(v)
	case CycleOpened:
		c.onCycleOpened(v)
	case CycleClosed:
		c.onCycleClosed(v)
	case Tick:
		tick = true
	case CommandResult:
		c.onCommandResult(v)
	case Link:
		c.onLink(v)
	case Detector:
		c.detPresent = v.Present
	case PlanUpdate:
		c.plan = v.Plan
		if v.Plan != nil && v.Plan.Valid(c.now) {
			c.planAt = c.now
		}
		if v.Plan != nil && v.Plan.DefaultOff {
			c.P.Windows = append([]PlanWindow(nil), v.Plan.Windows...)
		}
		for _, a := range v.Alerts {
			c.alert(a.Code, a.Text, a.Repeat)
		}
	case Seed:
		c.onSeed(v)
	}
	if tick {
		c.onTick()
	}
	if _, isSample := in.(Sample); !isSample {
		c.qualifyGenuine()
	}
	c.housekeeping()
	c.advanceMove()
	c.advanceVerify()
	for i := 0; i < 8; i++ {
		before := string(c.P.State) + "|" + c.hold
		c.evaluate()
		c.advanceMove()
		c.advanceVerify()
		if string(c.P.State)+"|"+c.hold == before {
			break
		}
	}
	c.publishOffGate()
	c.telemetry(tick)
	out := c.effects
	c.effects = nil
	return out
}

// ---- ingest ----

func (c *Controller) h(r Role) *held {
	x := c.in[r]
	if x == nil {
		x = &held{}
		c.in[r] = x
	}
	return x
}

func (c *Controller) onEntity(u EntityUpdate) {
	if u.Role == RoleConflict {
		if !u.Replay {
			c.conflicts[u.Entity] = u.State == "on"
		}
		return
	}
	if u.Replay {
		// Warm-up history carries no attributes and is older than the
		// connect-time snapshot; held values come from live/snapshot data.
		// It does tell the ON-time clock whether the switch was ON over
		// the replayed window: the cover state (closed = OFF; history has
		// no position attribute) and the numeric target.
		switch u.Role {
		case RoleCover:
			c.histKnown, c.histOn = true, u.State != "closed"
		case RoleTarget:
			if u.NumOK {
				c.histKnown, c.histOn = true, u.Num >= float64(c.cfg.OnPos)
			} else {
				c.histKnown, c.histOn = true, true // unknown counts as ON
			}
		}
		return
	}
	if u.Role == RoleBoost {
		c.onBoostState(u)
	}
	if u.Role == RoleTestMove || u.Role == RoleTestProbe {
		c.onTestButton(u)
		return
	}
	x := c.h(u.Role)
	x.known = true
	x.at = u.At
	x.str = u.State
	x.avail = u.State != "unavailable" && u.State != "unknown" && u.State != ""
	x.num, x.numOK = u.Num, u.NumOK
	if u.Role == RoleCover {
		x.pos, x.posOK = u.Position, u.PositionOK
		if !u.PositionOK {
			x.avail = false
		} else {
			c.P.LastKnownPosition = u.Position
		}
	}
	if u.Role == RoleTarget && u.NumOK {
		c.P.LastKnownTarget = u.Num
	}
	if u.Role == RoleEV && u.NumOK {
		c.ev.push(u.At, u.Num)
		if c.ver != nil && c.ver.baseEVOK && math.Abs(u.Num-c.ver.baseEV) > c.cfg.EVMoveW {
			c.ver.evMoved = true
		}
	}
	if u.Role == RoleBattery && u.NumOK {
		c.onBattery(u.Num)
	}
	if u.Role == RoleSelect && u.State != c.lastSelect {
		c.lastSelect = u.State
		c.overrideOffDone = false
	}
}

func (c *Controller) onBattery(v float64) {
	c.P.BatteryLast = v
	day := c.now.In(c.cfg.Loc).Format("2006-01-02")
	if c.P.BatteryDay != day {
		c.P.BatteryDay = day
		c.P.BatterySeries = append(c.P.BatterySeries, v)
		if len(c.P.BatterySeries) > 30 {
			c.P.BatterySeries = c.P.BatterySeries[len(c.P.BatterySeries)-30:]
		}
	} else if n := len(c.P.BatterySeries); n > 0 && v < c.P.BatterySeries[n-1] {
		c.P.BatterySeries[n-1] = v
	}
}

func (c *Controller) onBoostState(u EntityUpdate) {
	val := u.State
	valid := val != "unavailable" && val != "unknown" && val != ""
	if c.boostPrevKnown && valid && val != c.boostPrev {
		c.pressBoost()
	}
	if valid {
		c.boostPrev, c.boostPrevKnown = val, true
	}
}

// onTestButton records an R3 test-move or test-probe press (PLAN §8). A
// button's state is its last press time: a change to a new valid value is
// a press; the first value seen (the snapshot) is not.
func (c *Controller) onTestButton(u EntityUpdate) {
	val := u.State
	valid := val != "unavailable" && val != "unknown" && val != ""
	prev, known := c.buttonPrev[u.Role]
	if valid {
		c.buttonPrev[u.Role] = val
	}
	if !known || !valid || val == prev {
		return
	}
	kind := "move"
	if u.Role == RoleTestProbe {
		kind = "probe"
	}
	if !c.live {
		c.alert(AlertTestRefused, "Hot water: test "+kind+" refused: the controller is not LIVE (both keys needed).", 0)
		return
	}
	c.testPending, c.testPendingAt = kind, c.now
	c.logf("test %s requested", kind)
}

func (c *Controller) pressBoost() {
	open := c.P.Cycle != nil && c.P.Cycle.Magnitude >= c.cfg.GenuineMinW
	c.P.Boost = BoostState{Active: true, PressedAt: c.now, ReheatSeenStart: open}
	c.logf("boost pressed (reheat already running: %v)", open)
	c.persistNow()
}

func (c *Controller) onSample(s Sample) {
	// The detector's own clock: the time of the sample it last processed
	// (not the latest ever), so replayed history is judged on its own
	// timeline.
	c.detTime = s.At
	c.resKnown = true
	c.resAt = s.At
	c.resAvail = s.Available
	if c.ver != nil {
		if !s.Available {
			c.ver.stale = true
		}
		if c.ver.seqKnown && s.DiscSeq-c.ver.baseSeq > c.ver.discSince {
			c.ver.seqGap = true
		}
	}
	c.lastSeq, c.seqKnown = s.DiscSeq, true
	if !s.Available {
		return
	}
	c.res.push(s.At, s.W)
	cv := s.W - c.cumDelta
	c.corrected.push(s.At, cv)
	c.tracker.feed(&c.cfg, s.At, cv)
}

func (c *Controller) onDiscontinuity(d Discontinuity) {
	if d.DeltaW == 0 {
		return // startup-only bookkeeping, nothing jumped
	}
	c.cumDelta += d.DeltaW
	rec := discRec{T: d.At, Entity: d.Entity, DeltaW: d.DeltaW}
	c.discs = append(c.discs, rec)
	if c.ver != nil && !d.At.Before(c.ver.baseAt) {
		v := c.ver
		v.discSince++
		v.discSum += d.DeltaW
		v.discAny = true
		for _, o := range v.discTimes {
			if o.Entity != d.Entity && d.At.Sub(o.T) <= c.cfg.BurstWindow {
				v.burst = true
			}
		}
		v.discTimes = append(v.discTimes, rec)
	}
	cut := c.now.Add(-30 * time.Minute)
	i := 0
	for i < len(c.discs) && c.discs[i].T.Before(cut) {
		i++
	}
	c.discs = c.discs[i:]
}

func (c *Controller) onLink(l Link) {
	was := c.linkUp
	c.linkUp = l.Up
	if was && !l.Up {
		c.logf("HA connection lost: inputs stale")
		// Commands cannot be sent: abandon the in-flight move without
		// counting it as a motor failure (the link is not the motor).
		c.mv = nil
		c.ver = nil
		c.onRetryAt = time.Time{}
		c.histKnown = false // the next replay re-establishes it
	}
	if !was && l.Up {
		c.populated = true // inputs first populated: the boot timer may run (fix 6)
		// The boot timer runs from the link coming up, not from the
		// (older) warm-up replay times.
		c.bootSince = c.now
		if !c.plan.Valid(c.now) && c.awaitPlanFrom.IsZero() {
			c.awaitPlanFrom = c.now // no usable plan across a reconnect: wait for one
		}
		if c.P.State != StBoot && c.hold == "" {
			// §5.5: on reconnect go through BOOT; a valid OFF may be resumed (C10, runBoot).
			c.setState(StBoot, "reconnect")
		}
	}
}

func (c *Controller) onTick() {
	c.lastTick = c.now
}

// advanceOnClock accounts ON time from P.OnClockAt up to t (only ever
// forward), using the believed switch state over that stretch. A stretch
// with no data at all (the add-on down, before the replay window) counts
// as ON when the state is unknown: the probe then comes earlier, the safe
// direction (§4.4: unknown resolves to ON).
func (c *Controller) advanceOnClock(t time.Time) {
	if c.P.OnClockAt.IsZero() {
		c.P.OnClockAt = t
		return
	}
	if !t.After(c.P.OnClockAt) {
		return
	}
	if c.believedOn() {
		c.P.OnSSinceGenuineStart += t.Sub(c.P.OnClockAt).Seconds()
	}
	c.P.OnClockAt = t
}

// believedOn: the cover is believed ON (or unknown, which counts as ON so
// the probe comes earlier, the safe direction). While HA is not linked
// (warm-up replay) the replayed cover/target history decides.
func (c *Controller) believedOn() bool {
	if !c.linkUp && c.histKnown {
		return c.histOn
	}
	pos, ok := c.coverPosition()
	return !ok || pos >= float64(c.cfg.OnPos)
}

// ---- held-value accessors and freshness (§4.0) ----

func (c *Controller) entityFresh(r Role) bool {
	x := c.in[r]
	return c.linkUp && x != nil && x.known && x.avail
}

func (c *Controller) num(r Role) (float64, bool) {
	x := c.in[r]
	if x == nil || !x.known || !x.avail || !x.numOK {
		return 0, false
	}
	return x.num, true
}

func (c *Controller) str(r Role) string {
	x := c.in[r]
	if x == nil || !x.known {
		return ""
	}
	return x.str
}

func (c *Controller) coverPosition() (float64, bool) {
	x := c.in[RoleCover]
	if x == nil || !x.known || !x.posOK {
		return 0, false
	}
	return x.pos, true
}

func (c *Controller) coverOn() bool {
	p, ok := c.coverPosition()
	return ok && p >= float64(c.cfg.OnPos)
}

// targetIsOn: HAM's ruling (review fix 2): any target >= 99 means ON. The
// real switch rests at 100 when ON, so 100 must not read as a drift.
func (c *Controller) targetIsOn() bool {
	t, ok := c.num(RoleTarget)
	return ok && targetMatches(t, c.cfg.CmdOn, c.cfg.CmdOn)
}

// targetMatches: does a target reading satisfy a command? For the ON
// command any value >= cmdOn (99 or 100) matches; otherwise it must equal
// the command.
func targetMatches(t float64, cmd, cmdOn int) bool {
	ti := int(math.Round(t))
	if cmd == cmdOn {
		return ti >= cmdOn
	}
	return ti == cmd
}

// residualStale: unavailable/unknown, connection (WS or hook) lost, or no
// update for >= StaleAfter. Held values otherwise count (§4.0).
func (c *Controller) residualStale() bool {
	return !c.detPresent || !c.linkUp || !c.resKnown || !c.resAvail || c.now.Sub(c.resAt) >= c.cfg.StaleAfter
}

// criticalStale: the inputs whose staleness withholds the heartbeat
// (DEGRADED): residual, cover position, select, target. The 30-min age
// rule applies to the residual only; event-driven entities (cover,
// select, target, battery...) are stale only when unavailable/unknown or
// the connection is lost, because they are silent for days when nothing
// changes.
func (c *Controller) criticalStale() string {
	switch {
	case !c.linkUp:
		return "ha_connection"
	case c.residualStale():
		return "residual"
	case !c.entityFresh(RoleCover):
		return "cover"
	case !c.entityFresh(RoleSelect):
		return "select"
	case !c.entityFresh(RoleTarget):
		return "target"
	}
	return ""
}

// bootStale: row 3 needs residual, cover position and select known.
func (c *Controller) bootStale() string {
	switch {
	case !c.linkUp:
		return "ha_connection"
	case c.residualStale():
		return "residual"
	case !c.entityFresh(RoleCover):
		return "cover"
	case !c.entityFresh(RoleSelect):
		return "select"
	}
	return ""
}

// offInputsStale: rule 4's "none stale" for an OFF, and row 17's trigger
// while OFF: the critical inputs plus battery, motor state, EV, arming
// and latch.
func (c *Controller) offInputsStale() string {
	if s := c.criticalStale(); s != "" {
		return s
	}
	for _, r := range []struct {
		role Role
		name string
	}{{RoleBattery, "battery"}, {RoleEV, "ev"}, {RoleArmed, "armed"}, {RoleLatch, "latch"}} {
		if !c.entityFresh(r.role) {
			return r.name
		}
	}
	if c.cfg.RequireMotorState && !c.entityFresh(RoleMotorState) {
		return "motor_state"
	}
	return ""
}

// ---- housekeeping ----

func (c *Controller) housekeeping() {
	c.pruneMoves()
	if c.P.Strikes > 0 && !c.P.LastStrikeAt.IsZero() && c.now.Sub(c.P.LastStrikeAt) >= c.cfg.StrikeDecay {
		c.P.Strikes--
		c.P.LastStrikeAt = c.now
		c.logf("strike decayed, now %d", c.P.Strikes)
	}
	c.boostExits()
	c.trackWindows()
	if c.linkUp {
		c.backstopAlerts() // after a replay, alert only once HA is reachable
	}
	if c.testPending != "" && c.now.Sub(c.testPendingAt) > time.Minute {
		kind := c.testPending
		c.testPending = ""
		c.alert(AlertTestRefused, fmt.Sprintf("Hot water: test %s refused: controller busy in %s.", kind, c.stateLabel()), 0)
	}
	if b, ok := c.num(RoleBattery); ok && b < c.cfg.BatteryAlert {
		c.alert(AlertBatteryLow, fmt.Sprintf("Hot-water switch motor battery at %.0f%% (alert below %.0f%%).", b, c.cfg.BatteryAlert), 24*time.Hour)
	}
	c.shadowSummary()
}

func (c *Controller) strike(why string) {
	c.P.Strikes++
	c.P.LastStrikeAt = c.now
	c.logf("strike (%s): now %d", why, c.P.Strikes)
	if c.P.Strikes >= 3 {
		c.alert(AlertStrikes, fmt.Sprintf("Hot-water controller: %d motor-failure strikes (%s). Holding ON.", c.P.Strikes, why), 6*time.Hour)
	}
	c.persistNow()
}

func (c *Controller) persistNow() { c.emit(Effect{Kind: EffPersistNow}) }

// alert notifies (in LIVE; SHADOW only publishes the code) at most once per
// repeat for the same code.
func (c *Controller) alert(code, text string, repeat time.Duration) {
	if last, ok := c.P.AlertsSent[code]; ok && c.now.Sub(last) < repeat {
		return
	}
	c.P.AlertsSent[code] = c.now
	c.lastAlert, c.lastAlertText, c.lastAlertAt = code, text, c.now
	if c.Live() {
		c.emit(Effect{Kind: EffNotify, Code: code, Title: "Hot water controller", Text: text})
	}
	c.logf("ALERT %s: %s", code, text)
	c.persistNow()
}

func (c *Controller) setState(s State, reason string) {
	if c.P.State == s && c.reason == reason {
		return
	}
	if c.P.State != s {
		c.P.StateSince = c.now
	}
	c.P.State = s
	c.reason = reason
	c.logf("state -> %s (%s)", c.stateLabel(), reason)
	c.persistNow()
}

func (c *Controller) stateLabel() string {
	if c.P.State == StHoldOn && c.hold != "" {
		return fmt.Sprintf("HOLD_ON(%s)", c.hold)
	}
	return string(c.P.State)
}

func (c *Controller) setVerdict(v string, attrs map[string]any) {
	c.verdict = v
	c.verdictAt = c.now
	if attrs == nil {
		attrs = map[string]any{}
	}
	c.verdictAttrs = attrs
	c.P.LastCommand.Verdict = v
}

// ---- mode (two-key arming, D8; row 35) ----

func (c *Controller) armed() bool {
	// The held value, not entityFresh: losing the HA link is staleness
	// (DEGRADED, watchdog), not a disarm. Treating it as a disarm sent the
	// row-35 ON into a dead link and struck the motor for it.
	x := c.in[RoleArmed]
	return c.cfg.Live && c.detPresent && x != nil && x.known && x.avail && strings.EqualFold(x.str, "on")
}

func (c *Controller) updateMode() {
	want := c.armed()
	switch {
	case want && !c.live:
		c.live, c.exitingLive = true, false
		c.logf("mode -> LIVE")
		// Re-enter any hold seen while in SHADOW, so its entry actions
		// (I1's ON, the conflict alert) run now that the controller acts.
		c.hold = ""
		c.bootPath("armed")
	case !want && c.live:
		c.live = false
		// Row 35: ensure ON, then SHADOW.
		if !c.coverOn() || c.mv != nil {
			c.exitingLive = true
			c.logf("disarmed: ensuring ON before SHADOW")
			c.abandonOff()
			if c.mv == nil && c.hold == "" {
				c.startOn("disarm", false)
			}
		} else {
			c.logf("mode -> SHADOW")
			c.toShadowState()
		}
	case c.exitingLive && c.mv == nil && c.P.State != StCmdOn:
		c.exitingLive = false
		c.logf("mode -> SHADOW")
		c.toShadowState()
	}
}

func (c *Controller) toShadowState() {
	c.ver = nil
	c.offPeriod = nil
	c.P.CurrentOffPeriod = nil
	if c.hold != "" {
		return
	}
	c.bootPath("shadow")
}

// actuating: whether the controller may move the cover at all.
func (c *Controller) actuating() bool { return c.live || c.exitingLive }
