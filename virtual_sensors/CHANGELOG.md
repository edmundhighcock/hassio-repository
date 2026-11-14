# Changelog

## 0.6.4

- Replan buttons: three MQTT-discovery `button` entities, `button.replan_hot_water`,
  `button.replan_ev` and `button.replan_heat_pump` (shown only for enabled modules).
  A press replans that module at once and logs `<module>: replan requested (button)`.
  Harmless with no prices or no session (the plan is just cleared, as a tick would).
- hot_water_control: replayed price updates now seed the prices when no live price
  has been seen, so a restart no longer leaves it with no plan (the boot logic gave
  up after 3 min). The next tick replans. `ev_control` and `hp_control` already
  consumed replayed prices and were unchanged.

## 0.6.3

- Own log history: every log line now also goes to `/data/logs/virtual_sensors.log`
  (rotated at 5 MiB, 5 backups `.1`-`.5`, about 30 MiB max), because the HAOS
  journal is size-capped and shared and loses everything older than a few hours.
  If the file cannot be opened a warning goes to stderr and logging continues
  there only. The `backfill` subcommand does not write the file.

## 0.6.2

Detector and learner fixes after 2026-11-13.

- hot_water: a reheat that began while the EV charger was starting was missed
  for 45 min (19:24Z: EV step 22 s before the heater; the two chained into one
  +7 kW event, outside the 2.5-3.6 kW step band and vetoed by the moving EV
  load). New residual plateau fallback (`hw_plateau_rescue`, on): residual at
  heater level for 120 s opens the cycle, back-dated; one without a seen off step
  is credited when the residual falls.
- hot_water: `kettle_power` and `air_fryer_power` (W) are now known loads.
- hot_water: switch gate (`hw_switch_gate`, on, uses `hwc_cover_entity`): no cycle
  opens while the cover is `closed`, and an open one is closed 3 min after it
  closed (the 06:32 kettle draw was credited with the switch closed).
- hot_water: phase gate (`hw_phase_gate`, on; `hw_phase_step_a` 7.3,
  `hw_phase_tol_a` 2.0): an ON step must show about +7.3 A on both L1 and L2 and
  nothing on L3 in the Equalizer (read from the attributes on every update);
  tested L1-L3 / L2-L3 in a wider band while the EV charges. Falls back to the
  step logic when the Equalizer is offline or stale (15 min). Phases found:
  heater L1+L2 (7.4/7.3 A), kettle L1 (10.6 A), dishwasher heater L3 (9.5 A),
  heat pump L2. See DOCS "Heater detection gates".
- hwcontrol planner v2 learner: reheats under 15 min are thermostat
  maintenance and feed no statistic; "full" now needs a tag, or 45 min of draw
  (30 min after a gap of 4 h or more). Before, five 5-10 min top-ups made
  "full p50 = 6 min" while the real full heat ran 96 min.
- hwcontrol: tank thermostat is now 80 C: the full-heat floor goes from 90 to
  180 min, cap 4 h to 6 h, `hwc_v2_full_default_min` default 150 to 180 (never below
  the floor). New options `hwc_v2_full_min_min` (default 180) and
  `hwc_v2_learn_from` (default `2026-11-13T20:00` local): older reheats are ignored
  by the learner, so the stats restart on the 80 C tank with no state-file edit.

## 0.6.1

- hwcontrol: BOOT no longer decides before its plan exists. It holds (no move)
  until the planner has a plan from fresh prices or `hwc_boot_plan_wait_s`
  (optional, default 180 s) passes; a plan saying OFF then takes effect at once
  (persisted OFF resume, v2 default-OFF), otherwise it fails ON as before.
  A boot counts as no reheat. Cause: `runBoot` settled to `IDLE_ON/boot_on` at
  once, before the plan arrived (2026-11-10 21:18, plan at 21:18:11), and the
  15 min settle then blocked any OFF. (2026-11-12 under v2 the OFF was also
  held by the move budget: 6 moves in 24 h plus the reserve of 3 exceeds 8.)
- hpcontrol: the minimum applies to each comfort sensor. The executor's
  below-minimum rule uses the coldest live sensor, and the plan's floor binds on
  the predicted coldest sensor (live offset, at least the bedroom offset).
  The desired temperature still uses the mean.

## 0.6.0

Hot-water planner v2 (hwcontrol): a 24 h planner, selectable and in shadow
by default.

- `hwc_planner: greedy_v1|v2`, default `greedy_v1` (nothing changes unless
  chosen). While `greedy_v1` drives, v2's plan is published in shadow on
  `sensor.hot_water_controller_plan_v2` (windows, learned durations, expected
  24 h cost against greedy_v1) and never reaches the controller.
- v2 plans heating WINDOWS (switch ON) and permits OFF everywhere else: one
  FULL heat in the cheapest contiguous night window (`hwc_v2_night_from/to`,
  22:00-07:00), a TOP-UP in the cheapest midday window (`hwc_v2_midday_from/to`,
  10:00-16:00, `hwc_v2_topup`), a CATCH-UP when a full heat produced no
  finished reheat or was cut short (alert `FULL_HEAT_INCOMPLETE`), an
  optional evening top-up (`hwc_v2_evening_topup`, default off), and an early
  end of a window once the tank is full (`hwc_v2_early_end_min`). A window
  that is still drawing at its end is extended; a window about to start is
  pinned (it does not slide to the next quarter).
- Durations are learned from the controller's own reheat log (persisted,
  60 entries): full = p85 clamped 90 min-4 h (default 150 min, 5 samples
  needed), top-up = p50 clamped 30-120 min (default 60 min).
- `HeaterSchedule()` reports v2's OFF stretches, clipped to what the tank
  clock would let the controller hold; in SHADOW it reports ON throughout.
- Safety rows (the core still decides every OFF through `MayGoOff`; the plan
  only permits): unchanged for the tank clock, evening rule, boost,
  override, backstops (still ON-time only), battery, detector and stale-input
  faults, watchdog latch, move budget and C10 restart. Changes: (1) under v2
  condition G (an expected step after a long OFF) waits until the tank
  clock has run out (`hwc_g_since_reheat_h` is raised to the clock limit),
  otherwise every window start would count as a missing step; (2) the core
  keeps a reheat log and the active plan's windows in its state file (absent
  in older files, read as empty). A missing price or window means no OFF.
- `hwc_v2_window_resets_clock` (default **false**, v2 only, NOT approved): a
  heating window in which the thermostat never called counts as a satisfied
  tank for the 14 h tank clock, on strict evidence (see DOCS.md). Off, a
  no-call midday window lets the clock run out. The evening rule is
  unchanged either way: it needs a real finished reheat on the same local
  day. Closed-loop replay (`hwcontrol/closedloop_test.go`, 48 h, modelled
  tank and winter prices): greedy_v1 255 ore/kWh paid, v2 with the reset 127,
  v2 without it 198 (house with morning/evening draws); on days with no call
  at midday, 313 / 122 / 213.
- config.yaml: new optional schema keys only, none under `options:`.

## 0.5.1

Heat-pump live executor (hpcontrol): fix a false manual override.

- On 2026-11-10 arming turned the Dyson off and hass-dyson reported it `on`
  again 150 ms later; the module took that for a hand change and held off
  for 2 h. A state change within `hp_command_grace_s` (new, optional,
  default 120 s, 10-900) of our own command to that device is no longer an
  override; this covers `climate.heat_pump` too (IR, no feedback).
- If the target is still not reached when the window ends the same command
  is sent again, at most twice; after that a notification and
  `hp_last_command` with `ok: false`. Never an override.
- Turning `input_boolean.hp_controller_armed` off now clears any active
  override, so off-then-on re-arms cleanly.
- The HA event context is not exposed by the WebSocket layer, so the grace
  window alone is used.

## 0.5.0

Integrates the three phase-3 branches and adds the house planner.

- Hot-water fixes (hwcontrol): `HeaterSchedule()` / `core.HeaterScheduleSource`,
  a read-only snapshot of the heater's planned ON/OFF windows for the next 24 h.
- EV live executor (evcontrol): `ev_mode: live` plus
  `input_boolean.ev_controller_armed` drives the Easee charger (dynamic charger
  and circuit limits with a time-to-live, pause and resume, fallbacks), with a
  guard that uses the Easee Equalizer's per-phase current. The Equalizer
  freshness window is now 15 min by default (it updates on change only), one
  rule shared with the house planner.
- Heat-pump live executor (hpcontrol): `hp_mode: live` plus
  `input_boolean.hp_controller_armed` drives `climate.heat_pump` and the Dyson
  fan; `haws.CallServiceResponse`; refitted model defaults. New accessor
  `PlannedPowerW()`.
- New module `house_planner` (package `internal/house` + `housebudget`): one
  19 A per-phase budget (manual, hot water, heat pump, EV), a 24 h headroom
  forecast the EV planner uses as its per-slot ceiling, and live overload
  handling (EV first, then the heat pump to hold then off; never the heater),
  acting only through the modules' own armed live executors. Publishes
  `house_budget_headroom_l1/l2/l3`, `house_budget_state`,
  `house_budget_heartbeat`. Options `house_*`, all optional schema keys.
  Needs `ev_control` enabled.

## 0.4.1

- hwcontrol: C10 revised. A restart or reconnect with the cover below 90 now
  RESUMES a valid OFF (`boot_resume_off`: IDLE_OFF, no motor move, OffSince and
  clocks kept) instead of commanding ON (`boot_below_90`). Resume only if the
  controller itself put the switch OFF (persisted `off_since` set and last
  command OFF), now is inside a period of the current plan (or a valid Override
  Off), and the normal running-OFF checks still pass (`offRevoked`: armed/live,
  tank clock, fresh inputs, no boost, no hold incl. latch/battery/strikes,
  selector) plus the evening rule. Otherwise ON as before. No new persisted
  field. Cover >= 90 at boot (`boot_on`) is unchanged.

## 0.4.0

- New module `hp_control` (Go package `hpcontrol`): SHADOW-ONLY heat-pump and
  Dyson planner, per ham `services/hp-planner/PLAN.md`. Dynamic programming over
  15-minute slots x 0.1 C house-mean grid x {off, hold, boost} against the
  all-in spot price, with a floor (`input_number.house_minimum_temperature`),
  desired average, evening penalty and night bedroom cap. Publishes
  `hp_controller_state`, `hp_plan`, `hp_would_command`, `hp_model_error`,
  `hp_shadow_cost`. It calls no service; there is no live mode. Not enabled
  unless `hp_control` is added to `modules`. Every `hp_*` option is optional
  with its default applied in Go. The thermal model defaults are PROVISIONAL
  until `services/house-model/RESULTS.md`. Missing helpers fall back to
  defaults (minimum 19.0, presence Home, auto off) and say so in `reason`.
  hwcontrol and evcontrol are unchanged.

## 0.3.0

- New module `ev_control` (Go package `evcontrol`): SHADOW-ONLY EV planner
  and guard for the Easee charger, per ham `services/ev-planner/PLAN.md`.
  Reads entities, plans the night against quarter-hour prices, publishes
  `ev_*` sensors and one evaluation line per session under
  `/data/evcontrol/`. It calls no service; there is no live mode. Not
  enabled unless `ev_control` is added to `modules`. Every `ev_*` option is
  optional with its default applied in Go. hwcontrol is unchanged.

## 0.2.0

- New module `hot_water_control`: the hot-water immersion controller of
  ham `services/hot-water-switch/go-plan/PLAN.md` (rev 2026-11-09). It
  ships DISARMED: `hwc_mode` defaults to `shadow`, and live also needs
  `input_boolean.hot_water_controller_armed` (two keys). Not enabled
  unless `hot_water_control` is added to `modules`. Every `hwc_*` option
  is optional with its default applied in Go.
- Host refactor: one serial event loop, 5 s ticks, effects with
  `call_service` results, string states, whitelisted attributes, a
  replay flag, a current-state snapshot at connect, atomic state files.
  The clock and the HA client are injectable (`internal/host`).
- hot_water: an in-process signal hook for the controller (observation
  only; parity and replay tests unchanged).
- `packages/hot_water_watchdog.yaml`: the HA watchdog package, for
  review; not deployed and not in the image.
- Interpretations where PLAN is silent or inconsistent (each chosen to
  keep the heater ON):
  - The 30-min "no update" staleness rule applies to the residual only.
    Event-driven entities (cover, target, select, battery...) are
    stale only when `unavailable`/`unknown` or the HA link is down.
    Residual, cover, select and target withhold the heartbeat; battery,
    motor state, EV, arming and latch only block or end an OFF.
  - Move verification (scenario suite D-2, D-3): needs a motor_state
    report in a stopped state after the send, with the position within
    ±5; the +10 s/+15 s checks read held values. A missing motor_state
    entity blocks every OFF (option `hwc_motor_state_entity: ""` to use
    an 8 s time fallback once M0 confirms there is none).
  - call_service: 3 attempts (0, +5 s, +20 s). An ON retry runs to 0
    first only if 2 moves remain in budget, so it never strands at 0.
  - Probes and recycles need the move reserve like an OFF; probes run
    under Override On and the backstop hold; an inconclusive probe
    counts as no rise. The 10-min gap applies to OFF/probe starts only.
  - Row 20's ON_FAILED hold clears when the cover reads ON with target
    99; the strikes hold clears by the 24 h decay.
  - Any discontinuity in an ON window gives `on_pending`. In an OFF
    window DeltaW is subtracted: a drop gives `off_unverified`, no drop
    gives `off_failed` (back ON).
  - Condition G counts as met if the last reheat is unknown.
  - Override Off is also refused on stale inputs, 3 strikes, low
    battery or no move reserve, and is not re-honoured after expiry or
    refusal until the select changes.
  - HA link lost mid-command or while OFF gives DEGRADED, not a strike.
- Review rulings (HAM, 2026-11-10):
  - Warm-up replay reconstructs the ON-time clock and restamps genuine
    starts from history; replay never actuates.
  - Any target or position >= 99 is ON (the switch rests at 100).
  - The OFF "heater drawing" test needs a step sustained 5 min; an
    unsustained step is idle (no spurious `off_failed`). A sustained step
    with an inconclusive window, or with a discontinuity near it, and no
    drop returns ON.
  - A started OFF period is frozen: a re-plan may only shorten it.
  - After an OFF is abandoned (rows 10, 11, 14, an ambiguous check, heat
    while OFF) the next OFF needs a new planned period.
  - No DEGRADED before the first snapshot; `""`/null persisted times read
    as unset.
  - R3 manual test moves (`input_button.hot_water_test_move`,
    `_test_probe`; options `hwc_test_moves_only`, `hwc_test_off_min`).
  - `saving_month` is not published (no estimate is computed yet).
- hot_water: `sonoff_1001721983_power` (the spa floor-heating Sonoff,
  which left Home Assistant in 2024) is no longer a known load of the
  live residual, so it stops sitting permanently in `missing_loads`. The
  backfill CLI still subtracts it, so re-runs over the years when it
  reported are unchanged.

## 0.1.7

- hot_water: a known load reporting `unavailable`/`unknown` is dropped
  from `sensor.hot_water_residual_power` immediately (no held value) and
  re-added on its next numeric value. Each loss/regain is published as a
  discontinuity in the sensor's retained JSON attributes
  (`missing_loads`, `discontinuity_seq`, `last_discontinuity`,
  `recent_discontinuities`) with the signed `delta_w` it caused.
- The detector uses the same residual and does not count a discontinuity
  as an on/off step (window rejection plus cycle-level correction).
- Known blind spot: a plug that freezes while still `available` is not
  detectable. See DOCS.md.

## 0.1.6

- hot_water: a third published sensor, `sensor.hot_water_residual_power`
  (W, `device_class: power`, `state_class: measurement`) - the live
  residual (grid minus every known load's held/LOCF value, same
  `knownSum` the detector itself uses) rounded to the nearest whole W,
  published on every fresh grid sample. Grouped under the same MQTT
  device as the other two sensors, with `default_entity_id:
  sensor.hot_water_residual_power` in its discovery config, same as
  they already have.
  - No change at all to the detector's own behaviour or its two
    existing outputs (`hot_water_estimated_power`/`_energy`): the new
    output is computed and published independently, before step
    detection/pairing runs, and a known load going unavailable is
    handled exactly as before - its LAST held value simply keeps being
    subtracted (see DOCS.md "Entities published").
  - When the GRID input itself is unavailable, this new sensor (only)
    goes `unavailable` in Home Assistant, so a consumer can fail safe
    rather than read a stale or fabricated number - via its own
    per-entity MQTT availability topic
    (`virtual_sensors/hot_water_residual_power/availability`), not the
    add-on's existing shared `virtual_sensors/status` topic (which
    would have taken the other two sensors down with it). See
    `modules.OutputSpec.PerEntityAvailability` and
    `modules.OutputUpdate.Unavailable`, new this release, and
    `mqttpub.Publisher.PublishState`.
  - New unit tests
    (`internal/modules/hotwater/module_test.go`): published on every
    fresh grid sample, the `loaver_power` kW-to-W conversion feeding
    into the residual, a known load going unavailable (held-value rule
    applies, no change), the grid itself going unavailable (residual
    only goes unavailable, detector untouched, recovers once grid
    reports available again), and rounding to the nearest whole W.

## 0.1.5

- hot_water: brought the LIVE path into line with 0.1.4's batch fixes,
  so the add-on stops cutting cycles short in production.
  - Gap handling: `onNewGridSample` now only hard-breaks its merge-run
    on a REAL gap (>= `RealGapMinS`/30min, or no other known load
    reported anything either via the new `hadActivity`), not any raw
    gap > `MaxGapS` (120s) - mirrors `FindGapMarkers`/`FindMergedEvents`
    exactly. Each known load's ring is now retained for
    `activityRetentionS` (45 min, bounded - the add-on still runs at a
    few MiB) instead of `RejectWindowS*2` (120s), so `hadActivity` can
    see back across the full span a gap might still be benign for.
  - Post-off dwell: `handleEvent` no longer commits an off-candidate
    immediately. It's held as `pendingOff` for `PostOffDwellS` (90s);
    `OnStateUpdate` resolves it every sample against the residual at/
    just before the on-step (`onBaseline`) + `FloorTol` - a bounce-back
    cancels it (cycle resumes, nothing published either way), a clean
    dwell commits it (backdated to the original off-event's time, so
    energy is credited for the real interval, not "now"). A fresh
    genuine on-step (re-anchor) or a `MaxOnDurationS` timeout also
    cancels/discards any pending off, so energy is never double-counted
    or dropped.
  - New unit tests (`internal/modules/hotwater/module_test.go`): a flat
    3kW stretch with 140s gaps (no premature close), a staged on
    (+2.08kW then +0.85kW, 4 min apart), the oscillating-load case (no
    premature off), and a clean on/off.
  - New replay test (`tests/replay_test.go`): feeds the real
    2026-11-05 19:20-21:40 and 2026-10-20 00:00-20:00 raw samples
    through the live module and compares against
    `detect.DetectCyclesArrays` over the same window. Both agree
    closely: the 2026-11-05 staged reheat matches on/off times exactly
    and kWh within 3.5% (4.638 vs 4.799); the 2026-10-20 Case B cycle
    matches within 5s and 0.3% kWh (3.486 vs 3.497), both ~68 minutes.

## 0.1.4

- hot_water: a raw-sample gap (> MaxGapS, 120s) no longer force-closes an
  open ON cycle just because the recorder had nothing new to log.
  Verified against this household's data that gaps > 120s have a median
  |delta| of 1-10W (i.e. flat, consistent with Home Assistant/InfluxDB
  only logging on change, not periodically) -- so a gap is now a REAL
  data-loss event (`FindGapMarkers`) only if it's long (>= RealGapMinS,
  30 min) or coincides with a true outage (no OTHER known load reported
  anything either, `MakeSampleActivityCheck`/`MakeArrayActivityCheck`).
  `FindMergedEvents` carries the same fix into step detection: a step
  that happens to follow a benign quiet gap is no longer silently
  dropped (found 118/283/3 such step-sized deltas across the three
  buckets that were previously invisible).
- hot_water: an off-candidate is now rejected unless the residual
  actually STAYS down afterward (`MakePostOffLookup`, a new post-off
  dwell check, 90s by default) -- fixes a regression where an unmetered
  oscillating load (2026-10-20 18:30-19:38, grid swinging
  ~3850<->6530W, no change in any metered entity) produced a dip that
  satisfied the existing PairTol tie-break and closed a real ~68-minute
  cycle at 21 minutes. The dwell check rejects that dip (it bounced back
  up within ~20s) and the pairer keeps looking for the real off-step.
- Together these fixes close tonight's (2026-11-05) staged reheat
  end-to-end: 19:53-21:27 (~1.55h, ~4.6kWh), previously left unmatched
  by a 140s benign gap at 20:13-20:15.
- Parity re-verified against the full history: 4,535/4,535 accepted
  cycles match the Python reference exactly across `homeassistant_legacy`
  (3,312), `homeassistant_2026` (1,207) and the committed `homeassistant`
  fixture (16).

## 0.1.3

- hot_water: recognise a STAGED reheat - the immersion heater switching
  on or off in 1-3 sub-steps seconds to minutes apart (e.g. +2.08kW then
  +0.85kW four minutes later, confirmed live 2026-11-05) - which
  previously read 0W throughout. `detect.FindMergedEvents` now builds a
  chain of consecutive same-sign deltas (catching a transition whose
  individual samples never reach the 800W seed threshold but whose sum
  does) and `detect.FindStagedEvents` bridges up to 3 such sub-threshold
  events across a stable plateau within a 450s window; the live path
  mirrors this with a small residual ring buffer (`admitMergedEvent`).
  The on/off band widened from 2500-3300W to 2500-3600W to absorb the
  modest shoulder the chain-based merge can legitimately pick up around
  a real step. A new `MinOnDurationS` (60s) guard rejects a brief
  reversal (a kettle/toaster-like blip) that the wider chain made
  easier to nudge into the band.
- Fixed a pre-existing `pointsToArrays` bug (`cmd/virtual_sensors/
  backfill_hotwater.go`): a duplicate timestamp in `fridge_power`
  (`homeassistant_2026` bucket) broke the bisect helpers' sortedness
  assumption and panicked; points are now sorted and de-duplicated
  (keeping the last of exact ties) before use, matching the Python
  reference's own `load_series`.
- Parity re-verified against the full history, not just the committed
  live-bucket fixture: 5,676/5,676 accepted cycles match the Python
  reference exactly across `homeassistant_legacy` (4,399),
  `homeassistant_2026` (1,262) and the committed `homeassistant` fixture
  (15) - see DOCS.md "Parity with the Python reference".

## 0.1.2

- MQTT discovery now sends `default_entity_id`, which current Home
  Assistant uses to pick the entity id (`object_id` alone produced
  `sensor.virtual_sensors_...`). Existing installs keep whatever id the
  entity registry already holds.
- Modules may publish initial states once MQTT is up; hot_water
  publishes its restored energy total and current power at start, so the
  energy sensor is never `unknown` after a restart.

## 0.1.1

- Fix live inputs never matching: Home Assistant events carry `sensor.`
  ids while modules name inputs without the domain (as the InfluxDB
  buckets do); the prefix is now stripped before routing.
- Fix the warm-up history request: one comma-separated
  `filter_entity_id` of full ids (was rejected with 400), and keep the
  entity id across a `minimal_response` series, where only the first
  entry carries it.
- hot_water: convert `loaver_power` (the EV charger, kW) to W live, as
  the backfill already does by InfluxDB measurement.

## 0.1.0

- First release: a small Go add-on hosting pluggable "virtual sensor"
  modules (`internal/modules`), published to Home Assistant via MQTT
  discovery, with a `backfill` CLI subcommand sharing the same detection
  code for historical re-runs against InfluxDB.
- First module: `hot_water`, a port of the validated Python
  hot-water-estimate detector (Taiga #237) -
  `sensor.hot_water_estimated_power` (W) and
  `sensor.hot_water_estimated_energy` (kWh). Energy is credited only on
  an accepted, finalised cycle, matching backfill exactly; see DOCS.md
  "Live vs backfill" for exactly where live's necessarily-simplified
  (no-lookahead) pairing can diverge from a backfill re-run.
- Live inputs over the Home Assistant WebSocket API with a 4h
  history warm-up on every (re)connect, so a restart mid-cycle isn't
  lost; state (energy total, open-cycle, step-merge run,
  processed-until watermark) persisted to `/data/hot_water-state.json`.
- MQTT credentials from the Supervisor services API with an explicit
  options override/fallback, since the Supervisor has been observed
  reporting `mqtt` unavailable while Mosquitto was running; a failure to
  get credentials from either path is logged loudly and retried, never
  silently idled.
- Parity test (`tests/parity_test.go`) against the Python reference's
  own cached data: exact match on accepted cycles, every stats counter,
  and total kWh, on both the committed `homeassistant` (live) bucket
  fixture and (checked during development, not committed) the full
  `homeassistant_2026` bucket - 847/847 cycles, see DOCS.md "Parity".
- `example_passthrough`, a trivial second module (off by default), to
  demonstrate the module interface is genuinely reusable.
- Local test harness in `tests/test.sh`.
