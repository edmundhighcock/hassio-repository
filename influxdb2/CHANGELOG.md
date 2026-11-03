# Changelog

## 0.1.0

- First release: InfluxDB 2.7.12 OSS, tuned for a memory-tight Pi, for
  Home Assistant Core's `influxdb` integration (`api_version: 2`). No host
  port - reachable only on the Supervisor's internal network at
  `9547a9e0-influxdb2:8086`.
- First-run setup generates an org, a bucket (infinite retention by
  default), an admin user and an all-access token, all under
  `/data/secrets/` (mode 0600), never in add-on options or logs.
  Idempotent on restart.
- Memory-limiting options (`storage-cache-max-memory-size`,
  `storage-cache-snapshot-memory-size`,
  `storage-max-concurrent-compactions`, `query-concurrency`,
  `query-queue-size`, `query-initial-memory-bytes`, `query-memory-bytes`,
  `query-max-memory-bytes`) and `GOMEMLIMIT`, with defaults chosen so an
  oversized query fails rather than growing the process - see DOCS.md.
- Nightly full `influx backup` into `addon_config`, keeping the last
  `backup_keep` (default 2); the add-on's own `/data` is excluded from
  Home Assistant's backups (`backup_exclude: ["**"]`).
- Watchdog on `/health`.
- Local test harness in `tests/test.sh`.
