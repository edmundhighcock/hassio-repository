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

Both group under one MQTT device named "Virtual sensors" in Home
Assistant.

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

**Pairing (which on-step closes with which off-step) is deliberately
simplified live.** The batch algorithm (`detect.PairEvents`, used by
`backfill` and by the parity test) can look ahead across several
off-candidates after the on-step closes, and pick whichever one best
matches the on-step's magnitude (within a loose 900W tie-break) - see
the Python reference's own README "Pairing" section. A live stream
cannot look into the future without delaying every publication, so the
live engine (`internal/modules/hotwater/module.go`) closes on the
**first** qualifying off-candidate instead. In practice this rarely
differs (most cycles have exactly one off-candidate), but it means:

- **A provisional ON can later be rejected.** Live sets power to the
  heater's on-step magnitude as soon as a qualifying on-event is seen
  (before any pairing or floor check completes) - so Home Assistant
  shows power going up slightly ahead of the eventual accept/reject
  decision. If the floor check later fails (residual sags below
  `on_step - 600W` for more than 120s), live immediately republishes
  power = 0 and the cycle counts as rejected (no energy credited) - but
  a backfill re-run of the same period is the authority on whether that
  provisional ON should have counted at all.
- **Live never re-opens a closed cycle retroactively.** If the live
  no-lookahead pairing picks a different (always-first) off-candidate
  than the batch tie-break would have, the live energy credited for
  that one cycle can differ slightly from a backfill of the same
  period. This has not been observed in practice against this
  household's data (see "Parity" below) but is not proven impossible.
- **Energy is credited live ONLY on acceptance**, exactly like backfill:
  the energy total only increases when a cycle closes with the floor
  check passed, never at the provisional on-event. This is what keeps
  the live-accumulated energy total consistent with a backfill total
  over the same period, even though the *power* trace can show
  provisional detail backfill doesn't.

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

## MQTT discovery

Discovery configs are published retained to
`homeassistant/sensor/virtual_sensors/<object_id>/config`, with
`object_id` set explicitly in the payload (so the resulting entity id is
`sensor.<object_id>`, e.g. `sensor.hot_water_estimated_power`, not a
slug derived from the friendly name). State is published retained to
`virtual_sensors/<object_id>/state`. Availability is a single retained
LWT topic, `virtual_sensors/status` (`online`/`offline`), shared by
every output and referenced by every discovery config's
`availability_topic`. Discovery is re-published on every add-on start
AND whenever `homeassistant/status` reports `online` (an HA restart, or
an MQTT integration reload with "clean start" that would otherwise have
wiped the retained discovery configs).

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
10,962 grid samples, re-exported 2026-11-05 for the staged-reheat fix)
and compares against `tests/testdata/homeassistant_expected.json` (the
Python reference's own `detect_cycles_arrays()` output on the same
data). Result: **15/15 accepted cycles match exactly**, every stats
counter matches exactly, and total accepted-cycle kWh matches to 5
decimal places.

The much larger `homeassistant_legacy` (7,348,207 grid samples, ~3
years) and `homeassistant_2026` (796,688 grid samples, ~6 months)
buckets are too large to commit as fixtures; checked the same way
during development by running this add-on's own `backfill` CLI
(read-only) against InfluxDB directly and comparing its logged stats to
`build.py`'s `data/detection_summary.json` for the 2026-11-05
staged-reheat fix: **legacy 4,399/4,399 accepted cycles** (2,265
rejected-floor, 33 unmatched-timeout, 143 unmatched-data_gap, all
matching exactly), **homeassistant_2026 1,262/1,262** (18/0/240
likewise exact), on top of the committed 15/15 - 5,676/5,676 cycles
across the entire history. This exercise also found and fixed a
pre-existing bug in `pointsToArrays` (`cmd/virtual_sensors/
backfill_hotwater.go`): a duplicate timestamp in `fridge_power` (seen
only in the `homeassistant_2026` bucket) broke `sort.Search`'s
sortedness assumption in `MakeArrayRejectCheck`'s bisect and panicked;
`pointsToArrays` now sorts and de-duplicates (keeping the last of exact
ties), mirroring the Python reference's own `load_series`.

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
