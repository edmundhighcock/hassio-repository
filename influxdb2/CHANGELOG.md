# Changelog

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
