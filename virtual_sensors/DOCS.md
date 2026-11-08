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
