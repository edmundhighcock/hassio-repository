# Changelog

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
