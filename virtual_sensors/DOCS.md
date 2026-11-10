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
`strikes`. PLAN's `saving_month` is not published until a saving
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
empty at plug-in; need = `ev_need_kwh` minus the session energy. Per
quarter-hour the EV ceiling is the smallest of (`ev_phase_budget_a` minus
the forecast non-EV amps) over phases A, B, C, floored to whole amps,
clamped to `ev_max_a`, unusable below `ev_min_a`. The forecast is a base
`ev_house_base_a` on every phase, `ev_heater_phase_a` on A and B in
hot-water-ON slots between 16:00 and 23:00, and `ev_hp_phase_a` on C
always. The cheapest usable priced slots are chosen until the need is met;
gaps shorter than `ev_min_block_min` between chosen runs are filled. If
the priced slots cannot meet the need, every usable slot to departure is
planned at its ceiling (`deadline_mode`). Without tomorrow's prices the
plan is flagged `prices_partial`. It re-plans on plug-in, a price update,
a hot-water plan change and every 15 minutes.

Guard (`guard.go`, pure): per-phase current is estimated from the grid
power, the EV power, the heat pump and the immersion heater (assumed
drawing when the hot-water residual is at least 1500 W); the would-be EV
limit is the floored minimum headroom against the budget. Breaches
(estimated phase above the budget) are counted per session on the rising
edge.

Published (prefix `ev_`): `sensor.ev_controller_state` (idle, planned,
would_charge, would_pause, deadline_mode, degraded; attribute `reason`),
`ev_plan` (planned kWh; attributes slots, cost, need, departure,
prices_partial, deadline_mode), `ev_would_command` (resume at N A, pause,
limit N A, none), `ev_guard_limit_a` (per-phase attributes),
`ev_guard_breaches_tonight`, `ev_shadow_saving` (last session actual cost
minus the plan's cost for the same kWh, plus cumulative).

Evaluation: at unplug one JSON line per session is appended to
`/data/evcontrol/sessions.jsonl` (plug-in, departure, kWh delivered,
actual and plan cost, whether the plan finishes by departure, breaches).
Restart state is `/data/ev_control-state.json`.

All options are optional with defaults in code (see config.yaml `ev_*`).

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
service is called , except that a pause or
limit this module still holds from an earlier live period is released.

**Before arming, disable the `Car Start Charging` automation** (22:00,
`start` then `resume`): it fights the planner. The module publishes
`sensor.ev_controller_armed` (`on` only when live AND armed; attributes
`mode`, `armed_key`) for a condition or a check, and `sensor.ev_controller_mode`
(shadow/live).

Services (Easee integration v2026.11.0), targeted with `charger_id`, the
`id` attribute of `sensor.loaver_status` (override: `ev_charger_id`):
`easee.action_command` (`action_command`: `pause`, `resume`, and `start`
before `resume` when the status is awaiting_start/awaiting_authorization),
`easee.set_charger_dynamic_limit` (`current`, `time_to_live` in minutes)
and `easee.set_circuit_dynamic_limit` (`current_p1..p3`, `time_to_live`).
Limits are written only on change plus a refresh every `ev_limit_ttl_min`/3
(TTL default 15 min), so a dead add-on lets them lapse. The circuit limit is
per phase: the budget less the estimated other load on that phase. The
charger limit is the smaller of the plan's amps and the guard's limit;
below `ev_min_a` the charger is paused. Resume is sent only after fresh
limits are written, except in a fallback where limit writes keep failing.

Safety rules: (1) deadline backstop: if the plan cannot finish by
departure, charge continuously at the guard's maximum allowed. (2) Stale
data (no grid or Equalizer reading for `ev_stale_after_min`, link down for
more than 2 min, no price for the current quarter, tomorrow's prices still
missing at 14:00, no session plan) falls back to charge now at the guard
limit (the forecast ceiling when the guard is stale). (3) The phase budget
`ev_phase_budget_a` less other load is never exceeded by the limits sent; if
there is no headroom for `ev_min_a` the charger pauses. (4) Disarming (or
leaving live) while paused resumes the charger, then writes a 32 A
limit with a 1 minute TTL on charger and circuit, handing control back to
Easee's defaults.

**Pause has no TTL.** If the add-on dies while the car is paused, nothing
resumes it. Mitigation: `sensor.ev_controller_heartbeat` (Unix seconds,
refreshed every 30 s, attribute `paused_by_us`). An HA-side watchdog
automation should resume the charger if the heartbeat is older than a few
minutes while the car is connected and paused.

Notifications (`notify.mobile_app_edmund15max`, `ev_notify_service`, plus
a persistent notification): armed, disarmed, deadline or stale fallback
(once per session), guard breach, command failure (each rate limited to
once per 15 min).

Published by the executor: `ev_controller_mode`, `ev_controller_armed`,
`ev_last_command` (state: the command; attributes result, error, at),
`ev_limit_in_force` (charger A last written; attributes circuit_a, paused,
ttl_min, desired, reason, fallback), `ev_controller_heartbeat`.

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
