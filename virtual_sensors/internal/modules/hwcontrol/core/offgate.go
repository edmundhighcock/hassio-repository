package core

import (
	"strings"
	"time"
)

// offGateRefresh bounds how long an unchanged OFF-gate value goes
// unpublished.
const offGateRefresh = 15 * time.Minute

// OffGate is why the controller is (not) switching the heater OFF, as a
// short string. In IDLE_ON it is MayGoOff(false)'s first failing reason
// ("plan_period" = OFF allowed and about to be commanded). MayGoOff only
// reads controller state (no writes, no emits), so calling it here is safe.
// Other states give a state-derived value.
func (c *Controller) OffGate() string {
	switch c.P.State {
	case StIdleOn:
		_, why := c.MayGoOff(false)
		return why
	case StIdleOff, StCmdOff, StVerifyOff:
		return "off"
	case StDegraded:
		return "degraded:" + c.reason
	case StBoot:
		return "boot"
	}
	return strings.ToLower(string(c.P.State))
}

// publishOffGate publishes sensor.hot_water_controller_off_gate when its
// value or plan_off_now changes, and at most every offGateRefresh.
func (c *Controller) publishOffGate() {
	v := c.OffGate()
	_, planOff := c.plan.PeriodAt(c.now)
	changed := v != c.offGateVal || planOff != c.offGatePlanOff
	if !changed && !c.offGateAt.IsZero() && c.now.Sub(c.offGateAt) < offGateRefresh {
		return
	}
	if v != c.offGateVal || c.offGateSince.IsZero() {
		if v != c.offGateVal {
			c.logf("off_gate -> %s", v)
		}
		c.offGateSince = c.now
	}
	c.offGateVal, c.offGatePlanOff, c.offGateAt = v, planOff, c.now
	loc := c.cfg.Loc
	if loc == nil {
		loc = time.UTC
	}
	c.emit(Effect{Kind: EffPublish, ObjectID: ObjOffGate, State: v, Attrs: map[string]any{
		"blocked_since": c.offGateSince.In(loc).Format(time.RFC3339),
		"plan_off_now":  planOff,
		"evaluated_at":  c.now.In(loc).Format(time.RFC3339),
	}})
}
