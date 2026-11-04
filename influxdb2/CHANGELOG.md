# Changelog

## 0.1.2

- Fix: operator-token rotation (`admin_token_generation`) failed with
  `ERROR: failed to create a new operator token; old token kept` whenever
  it ran in a container other than the one that did first-run `influx
  setup` - which, per Home Assistant's own add-on update mechanism, is
  every container after the first one (an update replaces the container,
  it does not restart it). Root cause: the `influx` CLI's own saved
  config (`~/.influxdbv2`, holding the default org recorded by `influx
  setup`) lives in the container filesystem, not `/data`, so it is
  absent in any later container; `influx auth create --operator ...`
  then fails with `Error: must specify org ID or org name`, which the
  previous version hid behind `2>/dev/null`. `run.sh` now passes `--org`
  explicitly on every `influx auth create`/`auth list` call in the
  rotation path (never relying on the CLI's saved config), and captures
  each call's stderr so a failure logs a sanitised (token values
  stripped) first line instead of nothing. `ensure_ha_token` already
  passed `--org` on its own calls and is otherwise unaffected, but gets
  the same stderr capture/logging for consistency.
- Test harness: added a scenario in `tests/test.sh` that reproduces the
  real upgrade path - first-run setup in one container, then every
  subsequent step (two rotations and an `ha_token` recreation) in a
  brand-new container on the same `/data`, never a `docker restart` of
  the container that ran setup. Confirmed this scenario fails against
  0.1.1's `run.sh` and passes with the fix above.

## 0.1.1

- New `admin_token_generation` option (default `0`): raise it by one and
  restart to rotate the operator (all-access) token at
  `/data/secrets/admin_token`. On start, if the option is ahead of the
  generation last recorded under `/data/secrets/`, `run.sh` creates a new
  operator token for the admin user, verifies it works, writes it over
  `admin_token` (temp file + atomic `mv`, mode 0600), deletes the old
  authorization by ID, confirms the old token now gets `401`, then
  records the new generation - in that order, so a failure at any step
  before the old token is actually deleted leaves the previously-working
  token in place and just logs an error for the next start to retry.
  Idempotent: re-running with the same generation does nothing.
  `ha_token` is never touched by this. No token value is ever logged,
  only authorization IDs and the generation number. See DOCS.md
  "Rotating the operator token" for how to fetch the new token for the
  remote backup job afterwards.

## 0.1.0

- First release: InfluxDB 2.7.12 OSS, tuned for a memory-tight Pi, for
  Home Assistant Core's `influxdb` integration (`api_version: 2`).
  Published on the LAN at port 8086 (default, disable from the Network
  tab if not wanted) for a remote backup job; HA Core and every other
  add-on also reach it on the Supervisor's internal network at
  `9547a9e0-influxdb2:8086`. Never port-forward 8086 off the LAN/tailnet.
- First-run setup generates an org, a bucket (infinite retention by
  default) and an admin user under `/data/secrets/` (mode 0600, never
  in add-on options or logs): `admin_password`, an operator
  (all-access) `admin_token` for administration and for a remote backup
  job, and a `ha_token` scoped to write-only on the bucket, for Home
  Assistant Core. Idempotent on restart; `ha_token` is created on the
  next start if a prior install is missing it.
- Memory-limiting options (`storage-cache-max-memory-size`,
  `storage-cache-snapshot-memory-size`,
  `storage-max-concurrent-compactions`, `query-concurrency`,
  `query-queue-size`, `query-initial-memory-bytes`, `query-memory-bytes`,
  `query-max-memory-bytes`) and `GOMEMLIMIT`, with defaults chosen so an
  oversized query fails rather than growing the process - see DOCS.md.
- `/debug/pprof` always disabled; `/metrics` disabled by default
  (`metrics_enabled` option) - both are unauthenticated in InfluxDB
  itself.
- The add-on's own `/data` is excluded from Home Assistant's backups
  (`backup_exclude: ["**"]`); this add-on keeps no backup files of its
  own - a job on another LAN machine pulls `influx backup` over HTTP
  with the operator token instead (see DOCS.md for why: the Supervisor
  backs up `/addon_configs` as one unit with no per-add-on filtering).
- Watchdog on `/health`.
- Local test harness in `tests/test.sh`.
