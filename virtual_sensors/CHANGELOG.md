# Changelog

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
