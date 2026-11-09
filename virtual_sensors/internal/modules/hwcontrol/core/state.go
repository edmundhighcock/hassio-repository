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

	hold            string
	bootSince       time.Time
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
	if !c.P.LastFinishedReheatEnd.IsZero() {
		if h := c.P.LastFinishedReheatEnd.Add(c.cfg.OffLimitSinceReheatEnd).Sub(c.now); h > 0 {
			f.TankHeadroom = h
		}
	}
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
		c.lastTick = c.now
		c.setState(StBoot, "process start")
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
		return
	}
	if u.Role == RoleBoost {
		c.onBoostState(u)
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
	}
	if !was && l.Up {
		// The boot timer runs from the link coming up, not from the
		// (older) warm-up replay times.
		c.bootSince = c.now
		if c.P.State != StBoot && c.hold == "" {
			// §5.5: on reconnect go through BOOT; an OFF is not resumed (C10).
			c.setState(StBoot, "reconnect")
		}
	}
}

func (c *Controller) onTick() {
	dt := c.now.Sub(c.lastTick)
	c.lastTick = c.now
	if dt < 0 {
		dt = 0
	}
	if dt > 5*time.Minute {
		dt = 5 * time.Minute // a gap in ticks is not ON time we observed
	}
	if c.believedOn() {
		c.P.OnSSinceGenuineStart += dt.Seconds()
	}
}

// believedOn: the cover is believed ON (or unknown, which counts as ON so
// the probe comes earlier, the safe direction).
func (c *Controller) believedOn() bool {
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

func (c *Controller) targetIsOn() bool {
	t, ok := c.num(RoleTarget)
	return ok && int(math.Round(t)) == c.cfg.CmdOn
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
	c.backstopAlerts()
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
	return c.cfg.Live && c.detPresent && c.entityFresh(RoleArmed) && strings.EqualFold(c.str(RoleArmed), "on")
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
