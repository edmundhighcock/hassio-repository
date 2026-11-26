# Virtual Sensors

A small Go add-on that hosts "virtual sensor" modules: derived sensors
computed from other Home Assistant entities and published back to Home
Assistant as ordinary `sensor.*` entities, via MQTT discovery. It is
reusable - future modules plug in beside the first - and every module's
algorithm is shared, byte-for-byte, between the live path and a
`backfill` CLI subcommand that re-runs it over InfluxDB history, so a
live reading and a historical backfill always come from the same code.

The first (and, for now, only enabled-by-default) module is `hot_water`:
an estimate of the household's immersion-heater power and energy, ported
from the validated Python project at
`/home/edmundhighcock/Code/ham/services/hot-water-estimate/` (Taiga
#237) - see that project's own `README.md` for the full algorithm
rationale (step detection, pairing, floor check, staleness). This add-on
does not re-derive any of those thresholds; it ports them.

## Entities published

| Entity | Unit | device_class | state_class | Module |
| --- | --- | --- | --- | --- |
| `sensor.hot_water_estimated_power` | W | power | measurement | hot_water |
| `sensor.hot_water_estimated_energy` | kWh | energy | total_increasing | hot_water |
| `sensor.hot_water_residual_power` | W | power | measurement | hot_water |

All three group under one MQTT device named "Virtual sensors" in Home
Assistant.

`hot_water_residual_power` (0.1.6) is the live residual (grid minus the
LOCF-held sum of every `KnownLoadEntities` entry - the same `knownSum`
the detector itself uses) rounded to the nearest whole W, published on
every fresh grid sample. It has NO effect on, and is computed
independently of, the detector's own step detection/pairing and its two
outputs above - the parity/replay tests below cover only those and are
unaffected by this sensor's presence.

- **Known-load gaps (0.1.7)**: a known load whose HA state becomes
  `unavailable`/`unknown` is removed from the deduction IMMEDIATELY (no
  held value) and re-added when it next reports a numeric value. A load
  that is merely quiet but still `available` keeps the held-value rule
  (HA logs only on change). At add-on start any known load not restored
  or not yet seen is treated as missing (a synthetic `lost` event with
  `delta_w` 0 is recorded) until its first numeric value, which is then
  a normal `regained` event.
- **Discontinuity attributes**: every loss/regain is a discontinuity,
  published retained on the sensor's `json_attributes_topic`
  (`virtual_sensors/hot_water_residual_power/attributes`) with every
  state: `missing_loads` (excluded entity ids), `discontinuity_seq`
  (int, +1 per event), `last_discontinuity` and
  `recent_discontinuities` (last 10 min), each event being `{at (UTC
  ISO-8601), entity, kind: "lost"|"regained", delta_w}` where `delta_w`
  is the signed residual change caused by the event alone (+last value
  on loss, -new value on regain). A consumer should not treat a jump
  coincident with a new `discontinuity_seq` as a real load change.
- **Detector**: uses the same residual. A candidate on/off step with a
  non-zero-`delta_w` discontinuity within its reject window is rejected
  by `rejectOK` (same window as the load-movement check), and the
  floor/tentative-off comparisons subtract the discontinuities seen
  since the cycle was opened/re-anchored.
- **Leak while missing**: while a load is missing, any real change in
  its draw is invisible and shows up in the residual. This is expected.
- **Blind spot**: a plug that freezes while still reporting `available`
  (stale value, no unavailable state) cannot be detected; its last value
  keeps being deducted.
- **Grid unavailable**: this is the one case this sensor DOES react to
  on its own. When the grid input (`grid_meter_power`) itself reports
  `unavailable`/`unknown`, `hot_water_residual_power` (only) goes
  `unavailable` in Home Assistant, via its own dedicated MQTT
  availability topic (`virtual_sensors/hot_water_residual_power/
  availability`, online/offline) - NOT the add-on's shared
  `virtual_sensors/status` topic, which covers every sensor and would
  have taken the other two down with it. A consumer watching this
  entity can thus fail safe (not treat a stale/fabricated residual as
  current) exactly when the add-on itself no longer has a live grid
  reading, while `hot_water_estimated_power`/`_energy` keep publishing
  from their own, separately-tracked state exactly as before (see
  `hotwater.Module.OnStateUpdate`'s `!u.Available` branch for
  `GridEntity`). It recovers (`online`) as soon as the grid reports
  available again.

## Live vs backfill

**Step detection is identical** live and historical: both build a CHAIN
of consecutive same-sign deltas (any nonzero delta can start or extend
one, not just one that itself reaches 800W; a sub-seed delta only
extends within 30s of the chain's OWN start, while a delta that itself
reaches 800W can extend indefinitely as long as consecutive seeds keep
arriving within 30s of each other) and flushes it into a candidate
event once its net total reaches 800W, judged on its own against a
2500-3600W band (`detect.FindMergedEvents`). A STAGED reheat - the
heater switching on or off in 1-3 sub-steps seconds to minutes apart,
none of which alone reaches that band - is then bridged across a
stable intervening plateau into one combined event within a 450s
window: `detect.FindStagedEvents` in the batch path (a second pass over
the full event list); `admitMergedEvent` plus a short residual ring
buffer in the live path, since live can't re-query the raw series the
batch pass does. Fixed 2026-11-05 (Taiga #237 follow-up), after a live
staged on-step (+2.08kW then +0.85kW four minutes later) read 0W
throughout. A brief (<60s) on/off pair is rejected regardless
(`MinOnDurationS`) - the wider chain made a kettle/toaster-like blip
easier to nudge into the band by absorbing an adjacent small wobble;
every real heater cycle in this household's data is minutes long.
**As of 0.1.5, the gap-handling fix is also live**: `onNewGridSample`
only hard-breaks its chain on a REAL gap (>= `RealGapMinS`, or no other
known load reported anything either via `hadActivity`), not any raw
gap > `MaxGapS`, mirroring `FindGapMarkers`/`FindMergedEvents` exactly.
Each known load's ring (`loadRing`) is now retained for
`activityRetentionS` (45 min) rather than `RejectWindowS*2` (120s) so
`hadActivity` can answer "was anything else active" across the full
span a gap might still be benign for.

**Pairing (which on-step closes with which off-step) is deliberately
simplified live, but as of 0.1.5 it defers the one candidate it does
find.** The batch algorithm (`detect.PairEvents`, used by `backfill` and
by the parity test) can look ahead across several off-candidates after
the on-step closes, and pick whichever one best matches the on-step's
magnitude (within a loose 900W tie-break) - see the Python reference's
own README "Pairing" section. A live stream cannot look into the future
without delaying every publication, so the live engine
(`internal/modules/hotwater/module.go`) still only ever considers the
**first** qualifying off-candidate - but it no longer commits to it
immediately. `handleEvent` holds it as `pendingOff` for `PostOffDwellS`
(90s); `OnStateUpdate` resolves it on every subsequent sample against the
residual level at/just before the on-step (`onBaseline`) + `FloorTol`:
if the residual bounces back above that ceiling, the tentative off is
cancelled and the cycle resumes exactly as it was (no output published
either way); if the dwell passes clean, the close commits, backdated to
the original off-event's own time, crediting energy there (not "now").
This is the live equivalent of `MakePostOffLookup`/`PostOffDwellS` - see
`tests/replay_test.go` for live vs backfill agreement on both the
2026-11-05 staged reheat and the 2026-10-20 Case B regression this was
built to fix. In practice this still rarely differs from the batch
pairer (most cycles have exactly one off-candidate), but it means:

- **A provisional ON can later be rejected**, same as before. Live sets
  power to the heater's on-step magnitude as soon as a qualifying
  on-event is seen (before any pairing, floor, or post-off-dwell check
  completes) - so Home Assistant shows power going up slightly ahead of
  the eventual accept/reject decision. If the floor check later fails
  (residual sags below `on_step - 600W` for more than 120s - this only
  applies while NOT already resolving a pendingOff, since that has its
  own dedicated check), live immediately republishes power = 0 and the
  cycle counts as rejected (no energy credited).
- **A tentative OFF is never published until it resolves.** Unlike the
  on-step, the published power stays at the on-level throughout the
  `PostOffDwellS` confirmation window - there is no "provisional OFF" to
  retract, since nothing is published until the close actually commits.
- **Live never re-opens a closed (committed) cycle retroactively.** Once
  a close commits, it stands; only a STILL-PENDING off can be cancelled.
  If the live no-lookahead pairing's one candidate differs from what the
  batch tie-break across several candidates would have picked, the live
  energy credited for that cycle can still differ slightly from a
  backfill of the same period - not observed in practice (see "Parity")
  but not proven impossible.
- **Energy is credited live ONLY on acceptance**, exactly like backfill:
  the energy total only increases when a cycle closes with the floor and
  post-off dwell checks passed, never at the provisional on-event or
  during the pending-off window. This is what keeps the
  live-accumulated energy total consistent with a backfill total over
  the same period, even though the *power* trace can show provisional
  detail backfill doesn't.

## Restart semantics

`/data/hot_water-state.json` persists the energy total, whether a cycle
is currently open (and its on-step), the in-progress step-merge run, and
a `processed_until` high-water-mark timestamp. On restart:

1. `Init` loads this file (if present) and resumes exactly where live
   left off - no replay needed for a clean, recent restart.
2. The live loop also fetches the last ~4h of input history from Home
   Assistant (`GET .../history/period/<4h ago>`) and replays it through
   the same `OnStateUpdate` the live stream uses, so a restart mid-cycle
   (or a lost/corrupted state file) reconstructs correctly from HA's own
   history instead.
3. Replayed updates at or before `processed_until` are silently skipped
   (not double-counted) - this is what makes steps 1 and 2 safe to do
   together on every restart.

A restart exactly mid-dip (a brief below-floor run not yet past
`floor_dip_max_s`) is not perfectly preserved across a restart - the
dip-run timer itself isn't persisted, only the energy/open-cycle state.
This is a narrow edge case (requires a restart during a multi-second
window) and is called out here rather than silently accepted.

## Live inputs

Home Assistant WebSocket API at `ws://supervisor/core/websocket`
(`SUPERVISOR_TOKEN`, from `homeassistant_api: true`). Subscribes to
`state_changed`, filtered in the add-on's own dispatch loop (not at the
HA API level - HA's subscribe_events doesn't filter by entity) to each
enabled module's declared inputs. On every (re)connect, before
subscribing, fetches `GET
http://supervisor/core/api/history/period/<4h ago>?filter_entity_id=...&minimal_response&no_attributes`
and replays it - see "Restart semantics" above. Reconnects with
exponential backoff (1s doubling to 60s cap), logged at every attempt.

0.2.0: everything a module sees now goes through ONE event loop
(`internal/host`): warm-up history (marked `Replay`), then, after
subscribing, the current state of every input (`GET /api/states`,
stream-decoded so only matching entities are kept; this fills held
values for entities that have been quiet for days), then live events,
5 s ticks, `call_service` results (matched by WS id, 10 s timeout) and
link up/down. Attributes are passed through only for whitelisted
entities (the cover's `current_position`, the nordpool price arrays).
State files are written atomically (temp file, fsync, rename).

## Hot-water controller (`hot_water_control`, 0.2.0)

Design and every rule: `services/hot-water-switch/go-plan/PLAN.md` in the
ham repository (rev 2026-11-09). Enable it by adding `hot_water_control`
to `modules` beside `hot_water`; it reads the residual, its discontinuity
flags and the detector's cycles in-process from `hot_water` (no HA round
trip). Without `hot_water` it stays in shadow and reports
`degraded:no_detector`.

**Ships disarmed.** `hwc_mode` defaults to `shadow`: the whole state
machine and policy run and publish what they would do
(`sensor.hot_water_controller_shadow_would`), but the cover is never
commanded and the only notification is a daily summary at 09:00. Going
live needs two keys: `hwc_mode: live` AND
`input_boolean.hot_water_controller_armed` on. Disarming (either key)
commands ON first, then returns to shadow.

Layout: `internal/modules/hwcontrol/core` is the pure decision core
(state machine, policy, verification, backstop, persistence; no clock,
no I/O, no price). `.../planner` is the separate first planner (the only
code that reads prices; the core never imports it). `.../module.go` is
the adapter. The watchdog package is `packages/hot_water_watchdog.yaml`
(not deployed, not in the image).

Published (MQTT discovery, prefix `hot_water_controller_`, never
`hwc_`): `heartbeat` (Unix s, every 60 s only while healthy), `state`,
`desired`, `verdict`, `on_hours_since_genuine_start`,
`hours_since_reheat_end`, `moves_today`, `plan`, `shadow_would`, `alert`,
`strikes`, `off_gate`, `hot_water_overload` (unprefixed, 0.6.11) and `plan_v2` (the v2 shadow plan, only while `greedy_v1` drives). PLAN's `saving_month` is not published until a saving
estimate is computed. State persists in
`/data/hot_water_control-state.json`; a missing or corrupt file means
every clock is unknown, so no OFF until a finished reheat is seen live or
seeded from 24 h of detector history. `""` and `null` times in that file
read as unset.

**R3 test moves** (PLAN §8, supervised live actuation): with the
controller LIVE, pressing `input_button.hot_water_test_move` runs one OFF
for `hwc_test_off_min` (default 10 min) and then commands ON and
verifies; `input_button.hot_water_test_probe` runs one 0 → 99 probe
(without moving the backstop probe schedule). Both need select Auto, no
hold, fresh inputs, battery at or above the floor and the move reserve;
otherwise they are refused with the alert `TEST_MOVE_REFUSED`. They need
no plan period or tank clock. `hwc_test_moves_only: true` overrides the
planner (no planned OFF), so only test moves switch OFF.

Interpretations taken where PLAN is silent or inconsistent are listed in
the 0.2.0 CHANGELOG entry.

### Overload pause (0.6.11)

The two-phase heater draws ~7.3 A on L1 and L2. With 20 A main fuses, a kettle
(L1) plus the heat pump (L2) during a reheat can trip one. The controller reads
`sensor.equalizer_current` (`state_currentL1..L3`, and
`binary_sensor.equalizer_online`) and pauses the heater itself; house_planner
only plans budgets and never moves the cover.

- Trigger: heater drawing (cover ON and the step tracker or an open detector
  cycle of genuine magnitude) AND `max(L1, L2) > hwc_overload_amps` (default
  19.8) on two consecutive fresh Equalizer samples at least 30 s apart. Data
  older than 90 s, a gap over 90 s between the two samples, or an offline
  Equalizer never triggers. Not started if inputs are stale, HA is unlinked or
  the move reserve/caps would be violated (logged, heater stays ON).
- Action: `HOLD_OFF(overload)`, off_gate `overload_pause`, log
  `hot_water_control: overload pause: L1=..A L2=..A -> OFF`. The OFF is the
  normal verified OFF move (caps, reserve of 3, strikes).
- Resume: `max(L1, L2) + 7.4 <= threshold` (12.4 A) on fresh samples for 60 s,
  after a minimum hold of 3 min; or after 20 min whatever the currents. At most
  one pause per 30 min (protects the move budget). Then: an active Boost
  continues; if a plan OFF period still covers now it stays OFF (no move);
  otherwise ON is commanded by the normal ON path.
- Boost and overrides: a pause may interrupt a Boost (resume continues it; a
  Boost pressed during a pause waits for it). Override On is not interrupted
  (explicit user choice, fuse risk accepted). Override Off is already off;
  selecting it during a pause turns the pause into the Override Off. Every
  other hold (watchdog, conflict, strikes, battery, on_failed, backstop) keeps
  the heater ON and ends a pause. A restart during a pause goes through BOOT:
  the heater is switched back ON (or stays OFF if the plan says so).
- A reheat cut by a pause does not stamp the tank clock (logged as
  interrupted). The pause does not require the tank clock to be OK: it is
  bounded by 20 min.
- Output `sensor.hot_water_overload`: `off`/`paused`; attributes `enabled`,
  `threshold_a`, `last_pause_at`, `phase`, `amps`, `l1_a`, `l2_a`,
  `resumes_at` (latest), `earliest_resume_at`, `last_resume_at`.
- Options `hwc_overload_enabled` (default true) and `hwc_overload_amps`
  (default 19.8, 10..20).

### Override Off vs Boost (0.6.8)

**Override Off vs Boost**: Override Off is an explicit user intent and wins
over Boost. Selecting it while a boost is active ends the boost
(`boost ended: override_off`) and the controller returns to IDLE_ON, then the
normal Override Off checks run. If they refuse (tank clock, stale inputs,
strikes, battery, move reserve) an alert fires and the boost stays cancelled;
the heater stays ON. A boost pressed while Override Off is already selected
still wins (until the selector is changed again).

### Boot waits for the plan (0.6.1)

At process start the controller stays in BOOT, holding the switch where it
is and making no move, until the planner has produced a plan from fresh prices
or `hwc_boot_plan_wait_s` (default 180 s) has passed. With the plan in force it
then decides: OFF resumes a persisted OFF (C10) or applies v2's default-OFF
(the 15 min settle is not repeated, since the plan was seen); no plan period
now leaves it ON. Safety rules (boost, override, battery, strikes, tank clock,
stale inputs) still force ON. With no plan in time it fails ON as before (a
cover below 90 is commanded to 99; ON is settled for 15 min before any OFF).
A boot is not a reheat and resets no clock. A reconnect with a still-valid plan
does not wait. Note: an OFF can still be refused by the move budget (a 24 h cap
of `hwc_max_moves_day`, default 8, leaving a reserve of 3 after the OFF); the
state then stays `IDLE_ON`/`boot_on` until old moves age out.

### Planner v2 (0.6.0)

`hwc_planner: greedy_v1|v2` (default `greedy_v1`, unchanged behaviour)
selects the planner that drives the controller. v2 is a 24 h planner for
a switch that can only allow or block the 3 kW immersion heater (the tank
thermostat decides when it draws). It plans a few heating windows (switch
ON) and permits OFF everywhere else:

- FULL: one heat in the cheapest contiguous window of the learned full
  length inside the night band (`hwc_v2_night_from`/`_to`, 22:00-07:00).
- TOP-UP: the cheapest window of the learned top-up length inside the
  midday band (`hwc_v2_midday_from`/`_to`, 10:00-16:00; `hwc_v2_topup`).
- CATCH-UP: if the full heat produced no finished reheat (or it was cut
  short) a full-length window is planned before the tank clock runs out,
  and `FULL_HEAT_INCOMPLETE` is sent (LIVE only, once per 6 h). The tank
  may simply have been hot.
- EVENING (`hwc_v2_evening_topup`, default off): a short top-up before the
  night window after an evening draw-down, seen as the tank sensor falling
  `hwc_v2_evening_drop_c` (3 C) from its 6 h maximum. The proxy is not
  validated; leave it off unless needed.
- A window ends early once the heater has stayed off `hwc_v2_early_end_min`
  (10 min) after a finished reheat, if the move budget allows the OFF; it is
  extended while the heater is still drawing at its end.

Durations come from the controller's reheat log (kept in the state file,
60 entries, 45 days used). Since 0.6.2: a reheat shorter than 15 min is
thermostat maintenance (the tank was already near its set-point) and feeds
no statistic at all; a heat is "full" if the window tag says so, or untagged
and it drew 45 min or more, or 30 min or more after a gap of 4 h or more
(a tagged full that drew under 30 min is dropped too); the rest, 15 min up,
are top-ups. Full window = p85 of the full heats, clamped to
`hwc_v2_full_min_min` (default 180 min, floor) .. 6 h, default
`hwc_v2_full_default_min` (180, never below the floor) until 5 samples;
top-up = p50 clamped 30-120 min, default `hwc_v2_topup_default_min` (60).
Reheats cut short by the controller's own OFF are ignored. Reheats that ended
before `hwc_v2_learn_from` (default `2026-11-13T20:00` local, when the tank
thermostat went from about 60 to 80 C; "" = no cutoff) are ignored, so the
learned statistics start again from the 80 C tank: nothing is deleted from
the state file and old and mis-classified heats can never shrink a plan (the
floor also holds). Windows are quarter-hour aligned and 30 s longer than the
duration (the ON command leads the window), so a 180 min full heat is a
13-quarter (195 min) window.

**Shadow.** While `greedy_v1` runs, v2 is computed every re-plan and
published on `sensor.hot_water_controller_plan_v2` (state: number of
upcoming windows; attributes `windows`, `learned`, `expected_24h` with v2's
cost against greedy_v1's estimated cost for the same energy, `off_periods`,
`notes`). It never reaches the controller. With v2 selected the same
attributes appear on `sensor.hot_water_controller_plan` (re-planned every
minute) and the shadow sensor is not published.

**OFF gate (0.6.7).** `sensor.hot_water_controller_off_gate` is why the
controller is or is not switching the heater OFF. In IDLE_ON it is the first
failing condition of the OFF rule: `plan_period` (OFF allowed, about to be
commanded), `outside_plan_period` (the plan does not want OFF now),
`no_valid_plan`, or a blocker (`not_live`, `select_not_auto`, `boost`,
`period_abandoned`, `settling`, `strikes`, `hold`, `stale_<input>`,
`recent_discontinuity`, `cover_not_on`, `battery`, `move_reserve`,
`deliberate_gap`, `tank_limit`, `tank_clock_unknown`,
`evening_no_reheat_today`, `probe_due`, `ev_guard_time`, `ev_moved`). In other
states: `off` (IDLE_OFF, CMD_OFF, VERIFY_OFF), `degraded:<reason>`, `boot`,
else the lowercased state name. Attributes: `blocked_since` (when the value
began), `plan_off_now` (the plan has a permitted OFF period covering now),
`evaluated_at`. Published on change, and at least every 15 min.

**Learned durations (0.6.6).** `learned` holds `full_min`, `topup_min`,
`n_full`, `n_topup`, `n_full_cut`, `full_lower_bound_min` and more. A reheat
ended by our own OFF is never a sample (`n_full` ignores it), but a cut
full-type run lasting at least 30 min proves full heating takes at least that
long: `full_lower_bound_min` is the longest such run in the 60-day window,
`n_full_cut` their count, and `full_min` = max(normal estimate, lower bound),
clamped to the 6 h cap. The normal estimate is the p85 of uncut samples (5 or
more) or else the default; cuts can raise it, never lower it. An uncut reheat
is a sample only if the Equalizer showed L1 and L2 each dropping by 5 A or
more at its end (the heater's thermostat), or if there was no per-phase
reading for it (then it counts as before). The reheat log line reads
`reheat logged 50m cut=false satisfied=true`.

**Safety rows.** The core still decides every OFF through `MayGoOff`; a v2
plan only permits. Forced ON as before: tank clock (14 h), evening rule,
boost, override, backstops (ON-time only), battery and detector faults,
stale inputs, strikes, the watchdog latch, the move budget and the C10
restart resume. Differences under v2: condition G waits until the tank
clock has run out (otherwise every window start, after a long OFF, would
count as a missing step); the reheat log and the plan's windows are
persisted; the heater schedule for the house planner (`HeaterSchedule()`)
reports v2's OFF stretches clipped to what the tank clock would let the
controller hold, and ON throughout in SHADOW.

**`hwc_v2_window_resets_clock` (default false, not approved).** Off, a
heating window in which the thermostat never calls leaves the 14 h tank
clock running; on a day with no call at midday the clock trips and the
switch is forced ON, with the evening reheat at evening prices (see the
CHANGELOG and the closed-loop replay in
`internal/modules/hwcontrol/closedloop_test.go` for the measured cost). On,
the window counts as a satisfied tank and restarts the clock, but only if
the plan is a v2 plan, the controller is LIVE, it observed the whole window
(30 min or longer, no gap over 2 min), the cover and target were ON
throughout, no input was stale, no detector cycle opened, no OFF command of
ours fell inside it, and a real finished reheat ended within 28 h before
the window's end. The evening rule, condition G and the backstops still use
real reheats only (the evening rule needs a finished reheat on the same
local day whatever this option says).

## Heater detection gates (0.6.2)

`hot_water` now has three additions to the step detector (`gate.go`); the
detector parameters and the backfill/Python parity are unchanged (the
backfill CLI does not have them).

- **Known loads.** `kettle_power` and `air_fryer_power` (both W) join
  `KnownLoadEntities`; `loaver_power` is still kW (scaled x1000 in the
  module). History checked against the parity data is unaffected, as neither
  entity has history before 2026-11-13.
- **Switch gate** (`hw_switch_gate`, default true). While the cover
  (`hwc_cover_entity`, default `cover.hot_water_switch`) is `closed` - held at
  least 5 s, since it reports `closed` for a second at the close command -
  no ON step and no plateau opens a cycle, and an already-open cycle is closed
  (reason `switch_off`, energy credited to 15 s after the switch closed) once
  the switch has been closed for 3 min without the off step being seen. An
  unknown or unavailable cover never blocks.
- **Phase gate** (`hw_phase_gate`, default true). The immersion heater is a
  two-phase 400 V load: +7.4 A on L1 and +7.3 A on L2, 0 on L3 (36 steps
  2026-11-10..13: L1 7.37 A sd 0.1, L2 7.25 A sd 0.3, L3 0.0 A). The Equalizer
  (`ev_equalizer_entity`, `ev_equalizer_online_entity`; per-phase attributes
  read on every update, attribute-only changes included) confirms a step found
  on the 5 s grid meter: `hw_phase_step_a` (7.3) +- `hw_phase_tol_a` (2.0) on
  both L1 and L2 within 20 s of the step (the Equalizer reports 5-10 s after
  the meter, on a 10 s tick), L3 rising at most 2.5 A. The kettle (L1 only,
  +10.6 A), the dishwasher heater (L3 only, +9.5 A) and the heat-pump boost
  (L2 only, about +8 A) fail it. While the EV charges (`loaver_power` 500 W or
  more near the step) L1-L3 and L2-L3 are tested instead, in a wider band
  (3.5 to 10.8 A at the defaults): the EV's phases are not equal enough for the
  tight one (heater under an EV, 2026-11-13 19:24Z: L1 +4.9 A, L2 +7.7 A). The
  gate rejects only on positive evidence: with the Equalizer offline, not heard
  from in 15 min, without a reading before the step, or with no phase update at
  all in the window, the step logic stands unchanged (missing a real heater costs
  more than crediting a stray one). Measured: the EV's terminals T5 maps to L3
  cleanly; T3 to L1 and T4 to L2 only roughly, so the EV is not netted per phase.
  In 165 grid steps of 1.8 kW or more (2026-11-10..13) no non-heater event gave
  a balanced L1+L2 step (EV starts only looked balanced while attribute-only
  Equalizer changes were missing from the history API; ask it with
  `significant_changes_only=0`). The hob (2026-11-10 17:40Z) predates the
  Equalizer's data and is unchecked.
- **Residual plateau fallback** (`hw_plateau_rescue`, default true). If no
  cycle is open and the residual (grid minus known loads; a known load that went
  unavailable keeps its last value subtracted) stays at 2700 W or more for 120 s,
  with a median of 3900 W or less, the switch not known OFF and the phase gate not
  refusing, a cycle opens, back-dated to the start of the plateau, at the median
  less a 300 W base (clamped to the step band). This is for a heater step that
  coincides with another load's step: 2026-11-13 19:24Z the EV started 22 s
  before the heater, the two chained into one +7 kW event (outside the step
  band, and vetoed by the moving EV load) and the heater then ran 45 min
  unseen. A cycle opened this way that ends without a seen off step (the residual
  stays under the floor for 2 min) is credited up to the moment it fell.


## EV planner, guard and live executor (`ev_control`)

Design: `services/ev-planner/PLAN.md` in the ham repository. Enable it by
adding `ev_control` to `modules`. **It ships in shadow: it calls no Home
Assistant or Easee service until armed live** (see "Live executor" below).

It reads `sensor.loaver_status`, `_power` (kW), `_session_energy`,
`sensor.grid_meter_power`, `sensor.heat_pump_power_power`, hot water's
residual (`hot_water_estimated_power`), hot water's plan
(`sensor.hot_water_controller_plan`, read as Home Assistant attributes;
hwcontrol is unchanged) and the nordpool price sensor
(`hwc_price_entity`; parsed with hwcontrol's `planner.ParseNordpool`;
all-in price uses `hwc_price_adder_ore` and `hwc_vat_factor`).

Planner (`internal/modules/evcontrol/plan.go`, pure): assumes the car is
empty at plug-in; need = `ev_need_kwh` minus the session energy. **The
controller decides only WHEN to charge; the charger and the Easee Equalizer
decide HOW MUCH** (0.6.17). So the planner no longer sets a current ceiling:
per quarter-hour it estimates the energy the charger can DELIVER from the
forecast non-EV load per phase (L1, L2, L3), against the supply fuse
`ev_fuse_a` (default 20 A per phase):

- three phases: a3 = the smallest of (fuse minus load) over L1..L3, capped at
  `ev_max_a`, 0 below `ev_min_a`; kW = 3 x 230 x a3 / 1000;
- single phase on L3 (the Equalizer switches there by itself with phase mode
  auto): a1 = fuse minus the L3 load, capped, 0 below `ev_min_a`;
  kW = 230 x a1 / 1000;
- deliverable = the larger of the two, times `ev_derate` (default 0.9).

The load per phase comes from the house planner's published forecast
(`house_planner`, per-phase non-EV amps: base, heater and heat pump on the
phases its `house_phase_map` gives them; **set `house_phase_map` so the heat
pump is on L2 if that is where it is wired**, e.g.
`heater=L1+L2;hp=L2;ev=L1+L2+L3`). Without a forecast (none, older than 30 min,
or not covering the slot) the static assumptions apply: `ev_house_base_a` on
each phase, `ev_hp_phase_a` on L2 always, `ev_heater_phase_a` on L1 and L2
only in hot-water-ON slots between 16:00 and 23:00. The cheapest priced slots
are chosen until the summed deliverable kWh covers the need; gaps shorter than
`ev_min_block_min` between chosen runs are filled. If the priced slots cannot
meet the need, every usable slot to departure is planned (`deadline_mode`).
Without tomorrow's prices the plan is flagged `prices_partial`. It re-plans on
plug-in, a price update, a hot-water plan change and every 15 minutes.
`sensor.ev_plan` slots keep the order `[start, amps, kw, price_kr_per_kwh]`:
`amps` is the expected charger current (per phase on three phases, or the
single phase) and `kw` the deliverable power after the derate.

Guard (`guard.go`, pure): per-phase current is estimated from the grid
power, the EV power, the heat pump and the immersion heater (assumed
drawing when the hot-water residual is at least 1500 W); the would-be EV
limit is the floored minimum headroom against the budget. Breaches
(estimated phase above the budget) are counted per session on the rising
edge. Since 0.6.17 the guard only informs: it publishes `ev_guard_limit_a`,
counts breaches and feeds the house planner's measurement; it limits nothing
and raises no alert.

Published (prefix `ev_`): `sensor.ev_controller_state` (**`disarmed` whenever
the controller is not live and armed**, with the reason "disarmed: will not
start or pause the charger" and the plan's own state in the attributes
`would_state` / `would_reason`; when armed and live: idle, planned,
would_charge, would_pause, deadline_mode, degraded, charge_now, car_full;
attribute `reason`),
`ev_plan` (planned kWh; attributes slots, cost, need, departure,
prices_partial, deadline_mode), `ev_would_command` (resume, pause, none),
`ev_guard_limit_a` (per-phase attributes, informational only since 0.6.17),
`ev_guard_breaches_tonight`, `ev_shadow_saving` (last session actual cost
minus the plan's cost for the same kWh, plus cumulative).

Evaluation: at unplug one JSON line per session is appended to
`/data/evcontrol/sessions.jsonl` (plug-in, departure, kWh delivered,
actual and plan cost, whether the plan finishes by departure, breaches).
Restart state is `/data/ev_control-state.json`.

All options are optional with defaults in code (see config.yaml `ev_*`). New
in 0.6.17: `ev_fuse_a` (20) and `ev_derate` (0.9). `ev_limit_ttl_min` and
`ev_stale_after_min` are obsolete: still accepted in saved options, ignored.

## Heat-pump and Dyson planner (`hp_control`)

Design: `services/hp-planner/PLAN.md` in the ham repository. Enable it by
adding `hp_control` to `modules`. **It is SHADOW by default.** It drives
`climate.heat_pump` and `fan.dyson_nn2_eu_nfa8623a` only when BOTH
`hp_mode: live` AND `input_boolean.hp_controller_armed` is on (two keys, as
`hot_water_control`). With either key off it publishes what it would do and
sends no device command. The only service it calls in shadow is the
read-only `weather.get_forecasts`.

Reads: the comfort sensors (below), `sensor.outdoor_temperature_best`,
`sensor.bedroom_midheight_sensor_temperature`, `sensor.heat_pump_power_power`,
`input_number.house_target_temperature` (desired),
`input_number.house_minimum_temperature` (floor), `input_select.house_presence`,
`input_boolean.house_presence_auto`, `person.*`, `climate.heat_pump`,
`fan.dyson_nn2_eu_nfa8623a`, `timer.tv_quiet`, `input_boolean.tv_quiet_active`,
`input_boolean.hp_controller_armed` and the nordpool price sensor
(`hwc_price_entity`; all-in price from `hwc_price_adder_ore`, `hwc_vat_factor`).
Missing helpers do not break it: minimum falls back to `hp_min_default`
(19.0), desired to `hp_desired_default` (20.5), presence to Home; a missing
arming helper means not armed. **`input_boolean.hp_controller_armed` must
exist in Home Assistant (a package helper) before live can be armed.**

**Comfort sensor** (`hp_comfort_sensors`): the mean of
`sensor.kitchen_temperature_sensor_temperature` and
`sensor.bedroom_midheight_sensor_temperature`. `sensor.house_mean_temperature`
is deliberately not used: it includes the heat-pump intake sensor and the
Dyson's own sensor, both in the pump's airstream, and reads about 2.4 K per kW
high while the pump runs. These sensors report only on change, so one is
ignored after `hp_sensor_stale_min` (180 min). The night bedroom cap also uses
the bedroom sensor directly: the plan uses the measured bedroom-minus-comfort
offset, and a bedroom above `hp_bedroom_cap` at night turns the pump off.

**The minimum holds in the coldest comfort sensor (0.6.1).** The comfort
mean still sets the desired temperature, but `input_number.house_minimum_temperature`
applies to EACH comfort sensor. The executor's "below minimum -> heat at hold"
rule triggers on the minimum of the fresh comfort sensors (the reason names the
sensor). In the plan, the floor binds on the predicted coldest sensor: the mean
must stay above `minimum + hp_floor_margin_c` minus the cold offset, where the
cold offset is the coldest sensor minus the mean, measured live and never less
negative than `hp_model_bedroom_offset` (-0.3) while the bedroom is a comfort
sensor, bounded to [-1.5, 0]. (2026-11-10: the bedroom reached 18.77 C below
the 19.0 minimum for two hours while the mean stayed above it.)

**Model** (`model.go`): dT/dt = -u (T - Tout) + h(level, Tout) + gain; boost
h = max(0, boost_rate + slope (Tout - 5)), hold = hold_frac x boost. The
defaults are the 2026-11-10 fit (`services/house-model/RESULTS.md`,
kitchen+bedroom column, free-run, 3.7 days of data): `hp_model_u` 0.027 per
hour, `hp_model_boost_rate` 0.935 C/h (0.55 K/h per kW x 1.7 kW),
`hp_model_hold_frac` 0.41 (0.7 kW), `hp_model_gain` 0.05 C/h,
`hp_model_bedroom_offset` -0.3, **`hp_model_rate_slope` 0**: the COP slope is
not identifiable from the data yet. Because the plan's per-slot cost is price x
electrical energy and the heat delivered is the model's h(level, Tout), it
already plans on price / COP once the slope is non-zero. Options of these
names that were set earlier override the defaults: remove stale ones.

**Planner** (`plan.go`, pure): dynamic programming, backward, over slots x
0.1 C grid x {off, hold, boost} x previous level. Cost = price x electrical
energy + `hp_lambda` (T - desired)^2 per slot + `hp_evening_penalty_kr` per
running slot 17:00-21:00 + `hp_switch_penalty_kr` per level change (IR beeps) +
1000 kr per C below the floor (`minimum + hp_floor_margin_c`) or above the night
bedroom cap. **`hp_lambda` 0.05** was tuned by closed-loop replay of a winter
day with a 06:00-08:00 peak up to 4.8 kr/kWh (`TestReplayMorningPeakNoBoost`):
no heating in the morning or evening peaks while the house is above the
minimum, pre-heat to about 21.8 C by 15:00 and coast to about 19.4 C by
21:00, daily mean within 0.1 C of desired, never below the minimum. A higher
lambda (0.3 and up) pins the house to desired and ignores prices (a third
dearer); a much lower one rides the floor and the mean drifts. Horizon 24-36 h.

**Outdoor forecast**: every 30 minutes the module calls
`weather.get_forecasts` (type hourly) on `hp_forecast_entity`
(`weather.smhi_home`) and interpolates the hourly values per slot. The host
passes the service response back (`Effect.ReturnResponse`,
`CommandResult.Response`, `haws.CallServiceResponse`). A forecast older than
3 h, or none, falls back to the current `outdoor_temperature_best` held flat.

**Dyson**: runs whenever the heat pump heats, off otherwise; speed cap by time
and presence (`hp_dyson_*_pct`), in 10 % steps.

### Live executor

Heat pump (SmartIR over IR; every command BEEPS): commands are sent only when
the observed state differs from the target and never repeated for a state it is
already in; the same unmet command is not re-sent for 30 minutes (one
"command not applied" notification instead). Off: `climate.turn_off`. Hold:
heat at `hp_hold_setpoint_c` (22), fan high. Boost: heat at
`hp_boost_setpoint_c` (30), fan high. From off, one
`climate.set_temperature` (with `hvac_mode: heat`) carries mode and set-point;
the fan mode is sent only if it differs. Dyson: `fan.turn_on` with the
percentage, or `fan.turn_off`, only on change.

* **TV quiet**: while `timer.tv_quiet` is active (or `tv_quiet_active` is on),
  nothing is sent to either device, and the package's own pause is not an
  override. 90 s after it ends the plan resumes, after the package has restored
  its state.
* **Manual override**: a change of either device that the module did not
  cause (and is not what the plan wants anyway) holds the module off both
  devices for `hp_override_hold_h` (2 h), with a notification. A below-minimum
  house during the hold-off is notified (hourly) but not fought.
  Changes within `hp_command_grace_s` (120 s) of the module's own command to
  that device are never an override; an unreached target is re-sent twice at
  most, then reported. Disarming clears an active override.
* **Safety**: no fresh comfort reading, no input event for `hp_stale_after_min`
  (90), no price for the current slot, no plan or no outdoor value: heat at the
  hold set-point (state `safe_hold`, notification). Comfort below the minimum:
  at least hold (state `below_min`).
* **Notifications** (`hp_notify_service`, plus a persistent notification):
  armed, disarmed, override, command failure (retry after 5 min, 3 tries),
  safe hold, not applied. Disarming leaves both devices as they are.
* Nothing is sent before 30 s after the link comes up, on warm-up replay, or
  while the link is down.

Published (MQTT discovery): `sensor.hp_controller_state` (shadow: would_off,
would_hold, would_boost, floor_at_risk, degraded, idle; live adds safe_hold,
below_min, bedroom_cap, override, tv_quiet; attribute `reason`, `shadow`),
`hp_plan`, `hp_would_command`, `hp_model_error`, `hp_shadow_cost` (per-day
`days` no longer carries a `0001-01-01` key), **`hp_control_mode`** (shadow |
live), **`hp_control_armed`** (on | off, attribute `active` = both keys),
**`hp_last_command`**, **`hp_override`** (none | active, attributes `until`,
`reason`) and **`hp_heartbeat`** (an ISO timestamp refreshed every 30 s, for a
watchdog). Restart state is `/data/hp_control-state.json` (also the override
and whether it was armed, so a restart does not re-announce). All options are
optional (see config.yaml `hp_*`).

## House planner (`house_planner`, 0.5.0)

Design: ham `doc/energy-design/energy-design.tex`, "house planner". Enable it
by adding `house_planner` to `modules` **together with `ev_control`** (the EV
guard supplies the live per-phase current; without it the planner says
`no_data`). It reads no Home Assistant entity itself.

One budget, `house_budget_a` (19 A) per phase, priority manual (unmanaged)
loads, hot water, heat pump, EV. The modules share one process, so the code
is a leaf package `internal/house` (budget arithmetic, phase map, overload
state machine and a `Hub`) plus the small module `internal/modules/housebudget`.
hwcontrol registers its heater schedule (`HeaterSchedule()` via
`HouseHeaterPlan`), hpcontrol its planned power per slot
(`PlannedPowerW()` / `HousePlan`), evcontrol its measured per-phase current;
no module imports another.

Wiring (provisional, D32) is the option string `house_phase_map`, default
`heater=L1+L2;hp=L3;ev=L1+L2+L3`. Change it once the Equalizer confirms the
real phases. Amps per phase: one phase W/230, two phases W/400, three phases
W/(3x230).

Forecast (recomputed every minute, 24 h, 15-minute slots): per-phase headroom
= budget - base load (`house_base_a`, default 3 A) - the heater's ON windows
on its phases (no heater plan: ON throughout) - the heat pump's planned W per
slot on its phase (no heat-pump plan: `house_hp_max_w`). The EV planner uses
the minimum over its phases as its per-slot current ceiling (floored, capped
at `ev_max_a`, unusable below `ev_min_a`) instead of its static assumptions
(`ev_house_base_a`, `ev_heater_phase_a`, `ev_hp_phase_a`, which apply only
when there is no forecast or it is over 30 min old). The deadline backstop is
unchanged. `ev_phase_budget_a` still governs the EV guard's live limit; keep
it equal to `house_budget_a`.

Live overload: a measured phase above the budget for more than
`house_overload_delay_s` (30) first lowers the EV limit (down to pause; each
further 30 s of overload steps it down by the excess). Only with the EV at
zero, and the heat pump on an overloaded phase, the heat pump drops from
boost to hold, then to off, for `house_hp_cooldown_min` (15) after the last
overload. The EV cap is released 2 min after the overload ends (and only once
the heat pump is released). The hot-water heater is never curtailed: the
planner has no output for it. The caps go through the hub and are applied
ONLY by evcontrol's and hpcontrol's own live executors when armed live
(`ev_mode` live + `input_boolean.ev_controller_armed`; `hp_mode` live +
`input_boolean.hp_controller_armed`); in shadow they only show in
`ev_would_command` / `hp_would_command` and in the sensors below. A cap not
refreshed for 2 min lapses (a dead planner never leaves a load throttled).
With the Equalizer stale the guard's grid-power estimate is used and the
planner acts on it too (`source` attribute). While `timer.tv_quiet` is
active the heat-pump executor sends nothing, so an HP cap waits.

Published (MQTT discovery): `sensor.house_budget_headroom_l1/l2/l3` (live A,
budget minus the current; attribute `forecast` = [slot start, A] for the next
24 h, plus `ev_ceiling_forecast`), `sensor.house_budget_state` (ok,
curtailing_ev, curtailing_hp, no_data; attributes reason, source, caps, over
phases, whether each executor is armed live) and
`sensor.house_budget_heartbeat` (Unix seconds, every 30 s).

Options (all optional, defaults in Go): `house_budget_a`, `house_phase_map`,
`house_base_a`, `house_heater_w` (0 removes the heater from the budget),
`house_hp_max_w`, `house_overload_delay_s`, `house_hp_cooldown_min`,
`house_horizon_h`. The base load is a fixed option; a learned per-phase
median is not built.

## Price shed (`price_shed`, 0.6.9)

Keeps a simple on/off load ON except during the N most expensive clock hours
of each local day (Europe/Stockholm), with a manual override. One load for
now: the tool-shed lights, `switch.tool_shed_lights` (a Zigbee plug; the
lights are left on deliberately to keep the shed slightly warmer than outside
against moisture). Enable it by adding `price_shed` to the `modules` option.

**Plan.** From today's Nordpool prices (`hwc_price_entity`, parsed like the
hot-water controller, seeded from the warm-up replay) the mean price of each
local clock hour (23/25-hour DST days included) is ranked; the N most
expensive (`ps_off_hours`, default 4, ties go to the earlier hour) are the
OFF hours, all others ON. Hours need not be contiguous; an off period is a
maximal run of consecutive off hours. No prices for today: no off hours (the
load stays ON, state `no_prices`). Today's plan is persisted, so a restart
without prices keeps it.

**Acting.** On transitions only: the start of an off period commands
`switch.turn_off`; the start of an on period commands `switch.turn_on` and
clears any override. Also re-asserted (only if the switch is not already in
the wanted state) once the HA link is up and prices are known (or 60 s have
passed), and whenever the switch returns from `unavailable`/`unknown` (the
plug forgets its state after a power cut), except while an override is
active. A failed command is retried after 60 s. Warm-up replay never causes a
command.

**Override.** If the switch turns ON during an off period and it was not our
command, the override lasts until the end of that off period (persisted;
state `override`, attribute `override_until`). Our own commands are told
apart by time and target: any switch state change within 30 s of our command
is ignored, whatever it reports (a Dyson on this estate once reported a stale
state 156 ms after a command); only an OFF to ON change after that window
counts as a person. A return from `unavailable` is not a person. HA's event
context is not used (it is not passed through to modules). A manual change in
an on period is left alone until the next transition.

**Entities.** `sensor.price_shed_tool_shed_lights`: state `on` | `off_period` |
`override` | `no_prices`; attributes `off_hours` (list of "HH:00"),
`next_change` (local RFC3339), `override_until`, `last_command`,
`last_command_at`. `button.replan_price_shed` recomputes the plan now; it
switches nothing unless that changes whether now is an off hour.

**Options** (all optional): `ps_enabled` (bool, default true; false makes the
module inert), `ps_switch` (default `switch.tool_shed_lights`), `ps_off_hours`
(0..12, default 4). Logs: `price_shed: tool_shed_lights off_period start ->
turn_off`, `... on period start -> turn_on`, `... manual on during off period
-> override until HH:MM`, `... override ended`.

## Dahlia blanket controller (`blanket_control`, 0.7.0)

Keeps dahlia tubers in winter storage (an unheated tool shed) between a floor
and a HARD maximum with a ~70 W electric blanket, at minimum cost. Enable it
by adding `blanket_control` to `modules`. **SHADOW by default**: it commands
anything only with `bc_mode: live` AND `input_boolean.blanket_controller_armed`
on (two keys). With either off it publishes what it would do and sends no
command (the one service it calls then is the read-only
`weather.get_forecasts`; Start now pressed while not armed and live does
nothing and says so in a push and the Logbook). Alarms (below) push only
while armed and live.

**Temperatures** (guidance is trade practice, not a measurement of this box:
tubers keep at 4-10 C overall, aim 5-7 C, never warm; warmer is NOT safer,
above about 10 C they sprout and rot, which is worse than 3 C). The defaults
are lower here because the sensor, `sensor.dahlia_storage_temperature`,
sits in the air gap ON TOP of the tubers, the coolest part of the box, and
the tubers nearest the blanket can be several degrees warmer; it measures
air, not tubers. `bc_floor_c` 4.0 (planner floor), `bc_target_c` 4.5 (aim for
4-4.5; lower is cheaper and kinder to humidity), `bc_backstop_c` 3.0 (heat
regardless of price at or below it, latched until +0.5 C), `bc_max_c` 6.0,
`bc_alarm_high_c` 8.0. **The hard cap `bc_max_c`: never heat at or above
it.** It overrides the planner, the backstop (a latched backstop does not
heat above it), the fail-safe, the minimum on-block (the blanket is switched
off at once) and Start now.

**Optional hot-spot sensor** `bc_hot_sensor` (default empty): a second sensor
taped to the plastic where the blanket touches it, e.g.
`bc_hot_sensor: sensor.tool_shed_temperature` once that sensor sits inside
the box against the plastic opposite the blanket. When configured and fresh,
the cap and the high alarm also use it against their own limits
(`bc_hot_max_c` 8.0, `bc_hot_alarm_c` 10.0); the floor, the backstop and the
plan still use the storage sensor. Without it nothing changes. The module
never treats `sensor.tool_shed_temperature` as shed air.

### The plant and the actuation protocol (why)

The blanket has one push button that TOGGLES it; it starts OFF whenever its
power is restored; it switches itself OFF 3 h after being turned on; and it
has its own thermostat, so it can draw 0 W while logically on. A SwitchBot
Bot (`switch.dahlia_blanket_bot`; `switch.turn_on` = one press) presses the
button over Bluetooth at the edge of range: a press shows on the plug
(`switch.tool_shed_heater`, power `sensor.tool_shed_heater_power`) 8-125 s
later (median ~23 s), a press that does not get through is lost (not queued),
a delayed press can land late and toggle again, and the Bot's
`last_run_success` is unreliable and never used. Each press costs battery.

So every ON is a **start**: plug off, wait 10 s, plug on, wait 5 s, press
once, wait up to `bc_confirm_s` (150) for power above `bc_on_watts` (30),
read after the plug came back on. Starting from a power cycle means a press
can only turn the blanket ON, never toggle it off. A lost press is retried
by repeating the WHOLE start (cycle plus press), never by a bare second
press; at most `bc_start_tries` (3), then **FAILED**: one push plus a
Logbook entry, a full start (one try) every 10 min while heat is wanted, and
one push on recovery. OFF is just the plug off. While on, 0 W does NOT mean
off (thermostat), so it is ignored, with two exceptions: within 5 min of a
confirmed start a fall below the threshold means a delayed press landed and
toggled it, so the start is run again; and the plug going `off` or
`unavailable` (power loss, someone) means the blanket is off, so a start is
run if heat is wanted. The blanket is assumed on for 2 h 50 after a
confirmed start; before then (5 min earlier) a refresh start (cycle plus
press) runs if heat is still wanted, so its 3 h cut-off never bites. A start
that is a window longer is never counted twice. Commands are idempotent and
go through the host's normal service-call path with outcome feedback (a
plug command that fails or times out aborts the attempt and the whole start
is repeated after 30 s; a press whose call times out is not trusted either
way, power decides).

Minimising presses: `bc_min_on_min` (60) minimum on-block (a block is
extended over refreshes), `bc_max_starts_day` (12, refreshes and retries
included) planned starts per local day; the planner respects both, the frost
backstop and the sensor fail-safe may exceed the cap. `starts_today` counts
whole starts begun, `presses_today` the presses sent.

### Planner and model

A 15-minute slot dynamic programme over the price horizon (24-36 h, like
`hp_control`: its 1R1C `Model` and grid DP, `planner.Config.AllInOre`
prices, Nordpool parsing, the SMHI forecast fetched with
`weather.get_forecasts` through `hp_control`'s parser) over the storage
temperature: `dT/dt = -U (T - Tout) + H u + G`, `u` the draw as a fraction
of `bc_power_w`. States carry the on-run age, so the DP knows the minimum
on-block and the 2 h 45 refresh (a start with a cost); the per-day start
budget is met by raising the start penalty of an over-budget day until the
plan fits (flag `start_budget_binds` if the floor then suffers). Costs:
price x energy, `bc_start_penalty_kr` (0.3) per start so blocks are long,
a small penalty above the target, and 1000 kr per degC below the planning
floor (floor + margin). Heating is **never planned at or above `bc_max_c`**,
and its benefit is **capped at `bc_max_c`** (assumption: the blanket's
thermostat and the cap stop the temperature there; heating never lowers the
off result). The model is fitted online (windowed ridge least squares on
1 h differences of the last 5 days, toward the defaults U 0.30/h, H 1.5 K/h,
G 0; conservative: a leakier shed and weaker blanket than likely); with
fewer than 24 rows it stays at the defaults. `sensor.blanket_model` publishes
U, H, G, the 1 h-ahead error (MAE, bias, n) and the margin, which is
`max(0.5, 2 x MAE)` capped at 2 C, added to the floor.

Fail-safes: temperature at or below the backstop: heat regardless of price.
Sensor silent for more than 2 h, unavailable, or never heard (after 10 min):
heat, but only while the last known reading is below `bc_target_c`, and
never more than one on-window without a fresh reading. Without prices or an
outdoor temperature: hysteresis, on below floor + 1 C until floor + 1.5 C,
never off below the floor. A reading below the floor always heats.

### Entities

`sensor.blanket_controller_state`: `disarmed` (the armed switch is off),
`shadow`, `off`, `starting(try n)`, `on`, `failed`, `backstop`; attribute
`reason`, plus `would_state`, the temperatures, plug and power, alarms.
`sensor.blanket_plan` (planned kWh next 24 h; attribute `slots` as
`[start, on, price_kr_per_kwh, t_pred]`), `sensor.blanket_model`,
`sensor.blanket_presses_today`, `sensor.blanket_starts_today`,
`sensor.blanket_cost_today`, `sensor.blanket_cost_month` (measured power at
the all-in price), `sensor.blanket_last_start`, `sensor.blanket_humidity`,
`sensor.blanket_dew_point`; buttons `button.blanket_replan` and
`button.blanket_start_now` (a start through the full protocol, only while
armed and live, never at the cap; the block then follows the minimum on-block
and the plan).

Dead-man: while armed, live and healthy (link up, not FAILED) it calls
`timer.start` on `timer.blanket_deadman` with `00:31:00` at once on arm and
every 30 min (retry after 90 s on failure); the Home Assistant side owns the
fallback when it runs out. It stops while FAILED, deliberately.

### Humidity (monitor only)

Inputs `sensor.dahlia_storage_humidity` (RH %) with the storage temperature.
Published: dew point, absolute humidity (Magnus), and **RH at reference**,
the RH the same air would show at `bc_rh_ref_c` (5.0 C); raw RH falls about
5-6 points per degree of heating with no moisture lost, so alarms never use
it (it stays as an attribute). Each alarm pushes once per episode with a
Logbook entry and pushes again on recovery: **condensation risk** (the
cold-surface proxy, `bc_cold_surface_sensor`, default
`sensor.outdoor_temperature_best`, at or below the storage dew point + 0.5 C
for 1 h: moisture may condense on cold surfaces, check the lid and the cold
side for droplets), **wet** (RH at reference above `bc_rh_high` 95 for 6 h:
ventilate), **dry** (below `bc_rh_low` 60 for 6 h: mist the tubers). Also: a
push if the storage temperature stays above `bc_alarm_high_c` (or the hot spot
above `bc_hot_alarm_c`) for 30 min, and if it stays below the backstop for
30 min while heating (heating is failing to keep up).

**Options**: all optional, see `config.yaml` (`bc_*`; prices share
`hwc_price_entity`, `hwc_price_adder_ore`, `hwc_vat_factor`). Existing saved
options start unchanged.

## MQTT discovery

Discovery configs are published retained to
`homeassistant/sensor/virtual_sensors/<object_id>/config`, with
`object_id` set explicitly in the payload (so the resulting entity id is
`sensor.<object_id>`, e.g. `sensor.hot_water_estimated_power`, not a
slug derived from the friendly name). State is published retained to
`virtual_sensors/<object_id>/state`. Availability is, BY DEFAULT, a
single retained LWT topic, `virtual_sensors/status` (`online`/
`offline`), shared by every output and referenced by every discovery
config's `availability_topic` - this still covers both detector
outputs unchanged. An output can opt out of that sharing with
`OutputSpec.PerEntityAvailability` (0.1.6): its discovery config then
gets its OWN retained topic, `virtual_sensors/<object_id>/
availability`, defaulted to `online` as soon as discovery is
(re-)published, and flipped by `OutputUpdate.Unavailable` on the module's
own say-so (see `mqttpub.Publisher.PublishState`) - used by
`hot_water_residual_power` so a grid outage marks only that one sensor
unavailable, not the whole device. Discovery is re-published on every
add-on start AND whenever `homeassistant/status` reports `online` (an HA
restart, or an MQTT integration reload with "clean start" that would
otherwise have wiped the retained discovery configs).

### Replan buttons (0.6.4)

Each planner module publishes one `button` entity (discovery topic
`homeassistant/button/virtual_sensors/<object_id>/config`, command topic
`virtual_sensors/<object_id>/press`, payload `PRESS`, shared availability):
`button.replan_hot_water` (hot_water_control), `button.replan_ev`
(ev_control), `button.replan_heat_pump` (hp_control), `button.replan_price_shed`
(price_shed, 0.6.9), `button.blanket_replan` and `button.blanket_start_now` (blanket_control, 0.7.0). Pressing one calls that
module's replan immediately instead of waiting for the 1/15 min timer and logs
`hot_water_control: replan requested (button)` (likewise `ev_control`,
`hp_control`). No helper YAML is needed. A press with no prices, no session or
a disabled module is harmless (a disabled module has no button).

### MQTT credentials

Resolved from the Supervisor's services API (`GET
http://supervisor/services/mqtt`, enabled by `services: ["mqtt:want"]` in
config.yaml - `want`, not `need`, so a wrong "unavailable" from the
registry cannot stop the add-on starting) first. **This has been observed on this system to report
`mqtt` unavailable while the Mosquitto add-on was in fact running** - if
that happens, the explicit `mqtt_host`/`mqtt_port`/`mqtt_username`/
`mqtt_password` options are tried as a fallback/override. If NEITHER
path yields a host, the add-on logs a loud, specific error and retries
with backoff - it never idles silently waiting for a broker that may
never come. See `cmd/virtual_sensors/main.go`'s
`resolveMQTTCredentials`.

## Backfill mode

```
virtual_sensors backfill --module hot_water \
  --bucket homeassistant_2026 \
  --from 2026-05-08T00:00:00Z --to 2026-11-04T00:00:00Z \
  --influx-url http://192.168.0.36:8086 --org household \
  --token-file ~/.config/influxdb2-hass/admin_token \
  --out-dir ./out
```

Reads `grid_meter_power` and every known-load entity (same list as the
Python reference's `KNOWN_LOAD_ENTITIES`) from the given bucket over the
given range via **read-only** Flux queries (`internal/influxread`),
applies the same staleness fix, LOCF merge and residual calculation as
the Python `build.py`, runs `detect.DetectCyclesArrays`, and writes:

- `hot_water_power_<bucket>.lp` - InfluxDB line protocol matching the
  Python reference's own `write_lp` format exactly (same tags, same
  `_str` field convention), for a human to load elsewhere if wanted.
  **This add-on never writes it to InfluxDB itself** - nothing in this
  task issues an InfluxDB write.
- `hot_water_energy_hourly_<bucket>.csv` - hourly kWh and a running sum,
  same columns as the Python reference's own CSV.

Run once per bucket (`homeassistant_legacy`, `homeassistant_2026`,
`homeassistant`), same as the Python pipeline. The token is read from
`--token-file` and never logged, echoed, or put on a command line
anywhere in this add-on.

## Parity with the Python reference (acceptance gate)

`tests/parity_test.go` runs this add-on's `detect`/`backfill` Go
packages over `tests/testdata/homeassistant__*.csv` (exported from the
Python reference's own cached `homeassistant` [live] bucket parquet -
10,962 grid samples) and compares against
`tests/testdata/homeassistant_expected.json` (the Python reference's
own `detect_cycles_arrays()` output on the same data). Result (0.1.4,
after the gap-handling and post-off dwell fixes): **16/16 accepted
cycles match exactly**, every stats counter matches exactly, and total
accepted-cycle kWh matches to 5 decimal places.

The much larger `homeassistant_legacy` (7,348,207 grid samples, ~3
years) and `homeassistant_2026` (796,688 grid samples, ~6 months)
buckets are too large to commit as fixtures; checked the same way
during development (0.1.3 and 0.1.4) by running this add-on's own
`backfill` CLI (read-only) against InfluxDB directly and comparing its
logged stats to `build.py`'s `data/detection_summary.json`. 0.1.4:
**legacy 3,312/3,312 accepted cycles** (2,073 rejected-floor, 82
unmatched-timeout, 239 unmatched-data_gap, all matching exactly),
**homeassistant_2026 1,207/1,207** (19/3/256 likewise exact), on top of
the committed 16/16 - **4,535/4,535 cycles across the entire history**.
(0.1.3's own full-history check, before the gap/post-off fixes, was
5,676/5,676 on different cycle boundaries - the counts move between
versions as real cycles get recovered, split or re-paired differently;
what's invariant is Python and Go always agreeing with EACH OTHER.)
0.1.3 also found and fixed a pre-existing bug in `pointsToArrays`
(`cmd/virtual_sensors/backfill_hotwater.go`): a duplicate timestamp in
`fridge_power` (seen only in the `homeassistant_2026` bucket) broke
`sort.Search`'s sortedness assumption in `MakeArrayRejectCheck`'s
bisect and panicked; `pointsToArrays` now sorts and de-duplicates
(keeping the last of exact ties), mirroring the Python reference's own
`load_series`.

**Live vs backfill agreement (0.1.5):** `tests/replay_test.go` feeds the
same raw samples through the LIVE module (`hotwater.Module.OnStateUpdate`,
one sample at a time, exactly as the live dispatch loop would) and
through `detect.DetectCyclesArrays`, over two narrow fixtures
(`tests/testdata/replay/{1105,1020}_*.csv`): the 2026-11-05 staged
reheat and the 2026-10-20 Case B regression. Both agree closely: 1105
on/off times exact to the second, kWh 4.638 (backfill) vs 4.799 (live,
+3.5%); 1020 on/off times within 5s, kWh 3.486 vs 3.497 (+0.3%), both
showing the ~68-minute cycle intact. This is NOT a byte-exact parity
gate like the one above (live's documented simplifications - no
lookahead among several off-candidates - mean exact agreement isn't
guaranteed even in principle) but confirms the gap-handling and
post-off dwell fixes bring live into line on the two cases this
iteration exists to fix.

## Memory

Target: under 30MB resident on a 4GB Raspberry Pi 5 (aarch64).
`go_memlimit_mib` (default 24) sets `GOMEMLIMIT` via
`runtime/debug.SetMemoryLimit` at startup - see this add-on's hand-off
report for the measured RSS of an idle container (no real Supervisor/
MQTT/HA connections to carry any load).

## Adding a module

Implement `modules.Module` (`internal/modules/module.go`): declare
`Inputs()`/`Outputs()`, consume `StateUpdate`s in time order from
`OnStateUpdate`, return `OutputUpdate`s to publish, and persist/restore
state as opaque bytes. Register it with `modules.Register("name", ...)`
in your package's `init()`, import the package for its side effect from
`cmd/virtual_sensors/main.go`, and add `"name"` to config.yaml's
`modules` schema enum. `internal/modules/example/module.go` is a
deliberately trivial second module (off by default) that exists only to
prove this interface isn't secretly hot_water-shaped.

### Live executor (phase 3)

Two keys, like hot water: option `ev_mode: live` AND
`input_boolean.ev_controller_armed` on (create the helper; absent or
unavailable counts as off). In shadow, or live without the key, no
service is called (a pause this module still holds from an earlier live period
is resumed), `sensor.ev_controller_state` reads `disarmed`, and **Charge now
does nothing**: it does not activate the override, it writes a Logbook entry
and a phone push ("EV charge now not started") saying the controller is
disarmed and nothing was started.

**Before arming, disable the `Car Start Charging` automation** (22:00,
`start` then `resume`): it fights the planner. The module publishes
`sensor.ev_controller_armed` (`on` only when live AND armed; attributes
`mode`, `armed_key`) for a condition or a check, and `sensor.ev_controller_mode`
(shadow/live).

Services (Easee integration v2026.11.0), targeted with `charger_id`, the
`id` attribute of `sensor.loaver_status` (override: `ev_charger_id`).
**While armed and live the only charger commands are `easee.action_command`
`pause`, `resume`, and `start` before `resume` when the status is
awaiting_start/awaiting_authorization**, sent at planned slot boundaries (and
for Charge now and the fallbacks). The controller never writes a dynamic
limit, a phase mode, or a guard-breach alert: the charger and the Equalizer
protect every phase the car uses (they back off, and with phase mode auto the
charger goes single-phase by itself). The one exception: when the module
starts or is armed it sends ONE release, `easee.set_charger_dynamic_limit` and
`easee.set_circuit_dynamic_limit` at 16 A with a 1 minute TTL, so a limit left
by an older version or a manual action does not linger. A failed release is
retried up to three times; a successful one is never repeated until the next
start or arming. An overloaded phase the car is NOT using is the hot-water
module's overload pause, not this module's concern; the house planner's EV cap
is no longer acted on.

Safety rules: (1) deadline backstop: if the plan cannot finish by
departure, charge in every usable slot to departure. (2) Stale data (link
down for more than 2 min, no price for the current quarter, tomorrow's prices
still missing at 14:00, no session plan) falls back to charge now. A stale grid
or Equalizer reading no longer matters: the controller uses no meter to decide.
(3) Disarming (or leaving live) while the controller holds a pause resumes the
charger; the disarm push says a resume was sent only if one was.

**Dead-man postponement.** Home Assistant has `timer.ev_deadman` (30 min); if it
runs out, an HA automation starts a waiting car itself, assuming the controller
is dead or disabled. While armed AND live AND healthy (link up, charger id
known) the controller calls `timer.start` on `timer.ev_deadman` with duration
`00:31:00` immediately on start/arm and then every 30 minutes (a failed call is
retried after 90 s). It never calls it while disarmed or not live.

**Pause has no TTL.** If the add-on dies while the car is paused, nothing
resumes it. Mitigation: `sensor.ev_controller_heartbeat` (Unix seconds,
refreshed every 30 s, attribute `paused_by_us`). An HA-side watchdog
automation should resume the charger if the heartbeat is older than a few
minutes while the car is connected and paused.

Notifications (`notify.mobile_app_edmund15max`, `ev_notify_service`, plus
a Logbook entry): armed, disarmed, Charge now refused while disarmed,
deadline or stale fallback (once per session), command failure (each rate
limited to once per 15 min).

Published by the executor: `ev_controller_mode`, `ev_controller_armed`,
`ev_last_command` (state: the command; attributes result, error, at),
`ev_limit_in_force` (kept so dashboards do not break: since 0.6.17 always 0 A,
as the controller writes no limit; attributes paused, limits_written=false,
release_sent, desired, reason, fallback, deadman_postponed_at),
`ev_controller_heartbeat`.

### Guard inputs: Equalizer

When `binary_sensor.equalizer_online` is on and `sensor.equalizer_current`
was updated within `ev_equalizer_fresh_min` (default 15 min), the guard uses its real
per-phase currents (`state_currentL1..L3`, at the house fuse). Other load per
phase is that reading minus the charger's own current: the smaller of
`loaver_power`/(3x230) and `sensor.loaver_current` (whose per-phase mapping
is not documented, so the smaller value is subtracted, never overstating the
charger and so never understating other load). Otherwise it falls back to the
grid-power estimate above. Entities are options `ev_equalizer_entity` and
`ev_equalizer_online_entity`.

**Freshness rule (one rule, guard and house planner).** The Equalizer's
attributes update ON CHANGE only, so a steady house is silent for minutes;
a 2 min window made the guard read "stale" most of the time. A reading is
fresh when the Equalizer is online AND `sensor.equalizer_current` was last
updated less than `ev_equalizer_fresh_min` ago (default 15, was 2). The
house planner does not re-judge it: it uses the guard's measurement
(`evcontrol.EqualizerFresh`), so both always agree. Home Assistant's
`last_reported` would be better (it advances on unchanged reports), but the
add-on subscribes to `state_changed` only, which does not fire on unchanged
reports; using it needs a second `state_reported` subscription in
`internal/haws`, which is not built. A steady reading older than the window
falls back to the grid-power estimate until the next change.

## Log file

Besides stdout (HAOS journal, short-lived), the add-on writes its log to
`/data/logs/virtual_sensors.log` (rotated at 5 MiB, backups `.1` to `.5`).
Read it with:

    docker exec app_9547a9e0_virtual_sensors tail -n 200 /data/logs/virtual_sensors.log
