package core

import (
	"math"
	"time"
)

// Move outcomes.
const (
	mvPending = iota
	mvOK
	mvRetreat
	mvStuck
	mvCmdFailed
)

// Move phases.
const (
	phSend = iota
	phAwait
	phMotor
	phSettle
	phDone
)

// move is one physical cover command (0 or 99), from the call_service
// send through the motor check (§5.2):
//   - send, timeout -> retry after CallRetryDelays; CallMaxFailures
//     consecutive failures -> mvCmdFailed. A move counts against the rate
//     limits only once HA accepts it.
//   - motor: within MotorWait of acceptance, motor_state must report a
//     stopped state (updated after the send) with the position within
//     PosTol of the target. Per scenario-suite D-3 the position echo is
//     immediate, so the position alone proves nothing; the stop report is
//     required. If motor_state has never been seen at all, the stop is
//     taken at acceptance + MotorStopFallback.
//   - settle: at stop+10 s and stop+15 s the HELD target must equal the
//     command and the held position be within PosTol (D-2: data are
//     change-only, so a "sample" is the held value at that instant).
//   - retreat: the target echoed the command, then changed away from it.
type move struct {
	target     int
	purpose    string
	corr       uint64
	failures   int
	phase      int
	outcome    int
	nextSend   time.Time
	sentAt     time.Time
	acceptedAt time.Time
	stopAt     time.Time
	echo       bool
	check1     bool
	lastErr    string
}

func (c *Controller) newMove(target int, purpose string) *move {
	return &move{target: target, purpose: purpose, phase: phSend, nextSend: c.now}
}

// startMove begins a physical move if the rate limits allow one more
// (checked at every physical move site, including retries). It returns
// false if the caps forbid it.
func (c *Controller) startMove(target int, purpose string) bool {
	if !c.movesAvailable(1) {
		c.logf("move %s to %d refused: move cap reached (%d/h, %d/day)", purpose, target, c.movesInLast(time.Hour), c.movesInLast(24*time.Hour))
		return false
	}
	c.mv = c.newMove(target, purpose)
	c.advanceMove()
	return true
}

func (c *Controller) posWithin(target int) bool {
	pos, ok := c.coverPosition()
	if !ok {
		return false
	}
	return math.Abs(pos-float64(target)) <= float64(c.cfg.PosTol)
}

// advanceMove progresses the in-flight move on every input.
func (c *Controller) advanceMove() {
	m := c.mv
	if m == nil || m.phase == phDone {
		return
	}
	switch m.phase {
	case phSend:
		if c.now.Before(m.nextSend) {
			return
		}
		c.corr++
		m.corr = c.corr
		m.sentAt = c.now
		m.phase = phAwait
		c.emit(Effect{Kind: EffCallService, Domain: "cover", Service: "set_cover_position",
			Data: map[string]any{"entity_id": c.coverEntity, "position": m.target}, CorrID: m.corr})
		c.logf("command cover -> %d (%s), attempt %d", m.target, m.purpose, m.failures+1)
	case phAwait:
		if c.now.Sub(m.sentAt) > c.cfg.CallResultTimeout {
			c.moveCallFailed(m, "no result")
		}
	case phMotor:
		h := c.in[RoleMotorState]
		if c.retreated(m) {
			c.finishMove(m, mvRetreat)
			return
		}
		stopped := false
		if h != nil && h.known {
			stopped = h.avail && c.isStoppedState(h.str) && !h.at.Before(m.sentAt) && c.posWithin(m.target)
		} else if c.now.Sub(m.acceptedAt) >= c.cfg.MotorStopFallback && c.posWithin(m.target) {
			stopped = true
		}
		if stopped {
			m.stopAt = c.now
			m.phase = phSettle
			return
		}
		if c.now.Sub(m.acceptedAt) > c.cfg.MotorWait {
			c.finishMove(m, mvStuck)
		}
	case phSettle:
		if c.retreated(m) {
			c.finishMove(m, mvRetreat)
			return
		}
		check := func() int {
			t, ok := c.num(RoleTarget)
			match := ok && targetMatches(t, m.target, c.cfg.CmdOn)
			if ok && !match && m.echo {
				return mvRetreat
			}
			if !match || !c.posWithin(m.target) {
				return mvStuck
			}
			return mvOK
		}
		if !m.check1 && c.now.Sub(m.stopAt) >= c.cfg.SettleCheck1 {
			if r := check(); r != mvOK {
				c.finishMove(m, r)
				return
			}
			m.check1 = true
		}
		if m.check1 && c.now.Sub(m.stopAt) >= c.cfg.SettleCheck2 {
			c.finishMove(m, check())
		}
	}
}

func (c *Controller) retreated(m *move) bool {
	t, ok := c.num(RoleTarget)
	if !ok {
		return false
	}
	if targetMatches(t, m.target, c.cfg.CmdOn) {
		m.echo = true // for an ON, 99 or 100 both echo the command
		return false
	}
	return m.echo
}

func (c *Controller) isStoppedState(s string) bool {
	for _, x := range c.cfg.MotorStoppedStates {
		if s == x {
			return true
		}
	}
	return false
}

func (c *Controller) onCommandResult(r CommandResult) {
	m := c.mv
	if m == nil || m.phase != phAwait || r.CorrID != m.corr {
		return
	}
	if !r.OK {
		c.moveCallFailed(m, r.Err)
		return
	}
	m.acceptedAt = c.now
	m.phase = phMotor
	m.echo = false
	c.P.Moves = append(c.P.Moves, c.now)
	c.P.LastCommand = LastCommand{Target: m.target, At: c.now}
	c.advanceMove()
}

func (c *Controller) moveCallFailed(m *move, err string) {
	m.failures++
	m.lastErr = err
	c.logf("cover command (%s) failed: %s (%d/%d)", m.purpose, err, m.failures, c.cfg.CallMaxFailures)
	if m.failures >= c.cfg.CallMaxFailures {
		c.finishMove(m, mvCmdFailed)
		return
	}
	d := c.cfg.CallRetryDelays[len(c.cfg.CallRetryDelays)-1]
	if m.failures-1 < len(c.cfg.CallRetryDelays) {
		d = c.cfg.CallRetryDelays[m.failures-1]
	}
	m.phase = phSend
	m.nextSend = c.now.Add(d)
}

func (c *Controller) finishMove(m *move, outcome int) {
	m.phase = phDone
	m.outcome = outcome
	v := map[int]string{mvOK: "motor_ok", mvRetreat: VRetreat, mvStuck: VStuck, mvCmdFailed: VCmdError}[outcome]
	if outcome != mvOK {
		c.setVerdict(v, map[string]any{"purpose": m.purpose, "target": m.target, "err": m.lastErr})
	}
	c.logf("move %s to %d: %s", m.purpose, m.target, v)
}

// takeMove returns the finished move and clears it (nil if none or still
// running).
func (c *Controller) takeMove() *move {
	if c.mv == nil || c.mv.phase != phDone {
		return nil
	}
	m := c.mv
	c.mv = nil
	return m
}

// ---- rate limits (§3) ----

func (c *Controller) movesInLast(d time.Duration) int {
	n := 0
	for _, t := range c.P.Moves {
		if c.now.Sub(t) < d {
			n++
		}
	}
	return n
}

// movesAvailable reports whether n more moves fit both rolling budgets.
func (c *Controller) movesAvailable(n int) bool {
	return c.movesInLast(time.Hour)+n <= c.cfg.MaxMovesHour && c.movesInLast(24*time.Hour)+n <= c.cfg.MaxMovesDay
}

// reserveAfter reports whether, after n moves, MoveReserve still remain
// in both budgets (the move reserve: an OFF can never strand the switch).
func (c *Controller) reserveAfter(n int) bool {
	return c.movesAvailable(n + c.cfg.MoveReserve)
}

func (c *Controller) lastMoveAt() time.Time {
	if len(c.P.Moves) == 0 {
		return time.Time{}
	}
	return c.P.Moves[len(c.P.Moves)-1]
}

func (c *Controller) pruneMoves() {
	i := 0
	for i < len(c.P.Moves) && c.now.Sub(c.P.Moves[i]) >= 24*time.Hour {
		i++
	}
	if i > 0 {
		c.P.Moves = append([]time.Time{}, c.P.Moves[i:]...)
	}
}
