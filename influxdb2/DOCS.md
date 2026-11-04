# InfluxDB 2

InfluxDB 2.7 OSS, for storing Home Assistant's full state-change history
forever on a memory-tight Raspberry Pi. Written to by HA Core's own
`influxdb` integration (`api_version: 2`); no InfluxDB UI.

Published on the LAN at port 8086 (see "Network" below) so a backup job
on another LAN machine can pull `influx backup` over HTTP - **this port
must never be port-forwarded off the LAN or tailnet.** Edmund treats the
LAN and tailnet as trusted; nothing here is designed to be exposed to the
internet.

## Connecting Home Assistant Core

Add to `configuration.yaml`:

```yaml
influxdb:
  api_version: 2
  host: 9547a9e0-influxdb2
  port: 8086
  ssl: false
  organization: household
  bucket: homeassistant
  token: !secret influxdb_token
```

`9547a9e0-influxdb2` is this add-on's hostname on the Supervisor's internal
Docker network - reachable from HA Core and any other add-on regardless
of the LAN port below. Put the **Home Assistant token** (see "Token
locations" below, not the operator token) into `secrets.yaml` as
`influxdb_token: <value>`; never paste it into `configuration.yaml`
directly.

## Network

`ports: {"8086/tcp": 8086}` publishes InfluxDB's API on the host's LAN
interfaces at port 8086 by default, so a backup job running on another
machine on the LAN (or tailnet) can reach it directly - see "Backups"
below. It can be disabled or remapped from the add-on's **Network** tab
in the Supervisor UI if that job isn't wanted. Home Assistant Core's own
connection (above) does not depend on this port at all; it always uses
the Supervisor-internal hostname.

**Do not port-forward 8086 to the internet.** The InfluxDB write token
(see below) is bearer-token auth over plain HTTP by default; this is
acceptable on a trusted LAN/tailnet, not acceptable on the open internet.

## Options and their defaults

| Option | Default | What it does |
| --- | --- | --- |
| `organization` | `household` | InfluxDB org created on first start. |
| `bucket` | `homeassistant` | Bucket created on first start. |
| `retention` | `` (empty) | Bucket retention. Empty means infinite - the brief for this add-on. |
| `admin_username` | `admin` | Not a secret; the password and tokens are generated, never configured. |
| `storage_cache_max_memory_size` | `268435456` (256 MiB) | Max size a shard's in-memory cache can reach before it rejects writes. |
| `storage_cache_snapshot_memory_size` | `16777216` (16 MiB) | Size at which the cache is snapshotted to a TSM file to free memory. |
| `storage_max_concurrent_compactions` | `1` | Max concurrent full/level compactions. |
| `query_concurrency` | `2` | Queries allowed to run at once. |
| `query_queue_size` | `10` | Queries allowed to wait; beyond this, new queries are rejected outright. |
| `query_memory_bytes` | `209715200` (200 MiB) | Max memory for a *single* query. Over this, the query fails. |
| `query_initial_memory_bytes` | `10485760` (10 MiB) | Memory a query starts with before it needs to grow. |
| `query_max_memory_bytes` | `419430400` (400 MiB) | Max memory for *all* queries running at once. |
| `go_memlimit_mib` | `450` | `GOMEMLIMIT` - the Go runtime's soft heap limit, in MiB. |
| `metrics_enabled` | `false` | Turn the `/metrics` endpoint back on (e.g. for a LAN Prometheus scraper). |
| `admin_token_generation` | `0` | Raise by one and restart to rotate the operator token - see "Rotating the operator token" below. |

## Memory defaults and why

A trial of this setup with ~57M points loaded showed steady RSS of
370-470 MiB with no limits, and a raw one-year string-field query alone
pushing RSS to near 1 GiB - enough to kill the process under a 600 MiB
container cap after a write burst. The goal here is that an oversized
query **fails on its own terms** (HTTP error, logged, retried by the
caller) rather than growing the process until something else (the kernel
OOM killer, or Docker's own cgroup limit) kills it.

- `storage-cache-max-memory-size` (256 MiB) and
  `storage-cache-snapshot-memory-size` (16 MiB) bound the write-side
  in-memory cache. The snapshot size is lower than InfluxDB's own default
  (25 MiB) so the cache is flushed to disk sooner, trading a little extra
  I/O for a lower memory ceiling - sensible on an SD card doing mostly
  sequential small writes.
- `storage-max-concurrent-compactions=1` (required by the brief) keeps
  background compaction from competing with queries/writes for memory and
  CPU on a Pi's few cores.
- `query-concurrency=2` and `query-queue-size=10`: this instance has one
  real client (HA Core) plus occasional ad hoc queries; two concurrent
  queries is already generous, and a queue of 10 gives a little slack
  before new queries are rejected outright rather than piling up memory.
- `query-memory-bytes=200 MiB` per query, `query-max-memory-bytes=400 MiB`
  total (`= query-concurrency × query-memory-bytes`, InfluxDB's own
  default formula, made explicit here). A single-shard dashboard query
  easily fits; the one-year raw string-field query from the trial is
  **meant** to be rejected by this cap rather than pushing the container
  toward 1 GiB - export that kind of query with `influx query` run
  outside the add-on, or downsample first.
- `GOMEMLIMIT=450MiB`: a *soft* limit to the Go garbage collector - it
  makes the GC work harder to stay under 450 MiB of heap instead of
  waiting for Go's usual pacing, so steady-state RSS settles below what
  the flags above would otherwise allow, with headroom under a ~700 MiB+
  container memory cap. It does not by itself stop a single huge
  allocation; the query-memory flags above are the actual hard stop for
  that. See
  <https://pkg.go.dev/runtime/debug#SetMemoryLimit> and
  <https://pkg.go.dev/runtime#hdr-Environment_Variables>.

All flag names and semantics were checked against
<https://docs.influxdata.com/influxdb/v2/reference/config-options/>
(InfluxDB honours, in order: `influxd` flags, then environment variables,
then the config file - this add-on uses environment variables, which the
official entry point itself also uses for `bolt-path`/`engine-path`).

**No add-on-level hard memory cap:** Home Assistant's add-on config schema
has no field for a container memory limit (cgroup `--memory`) - see
<https://contributors.home-assistant.io/apps/reference/configuration/>.
The defaults above are the actual defence; if you want a hard cgroup cap
as well, it has to be set outside the add-on's own configuration (not
attempted here, since there is no documented way to do it from
`config.yaml`).

## Disabled endpoints

Both are unauthenticated by design in InfluxDB itself, so both are off by
default here:

- `--pprof-disabled` (always on, not an option): `/debug/pprof/*` serves
  Go runtime profiling data to anyone who can reach the port, with no
  token check at all.
- `--metrics-disabled` (on by default, `metrics_enabled: true` turns it
  off): `/metrics` exposes internal InfluxDB counters, also with no token
  check.

Both flags verified against
<https://docs.influxdata.com/influxdb/v2/reference/config-options/>
(`pprof-disabled` / `INFLUXD_PPROF_DISABLED`, `metrics-disabled` /
`INFLUXD_METRICS_DISABLED`). With both disabled, InfluxDB answers
requests to either path with `403`, confirmed in `tests/test.sh`.

## Token locations

On first start, this add-on generates, under `/data/secrets/` inside the
container (mode `0600`, root-owned, never logged, never in add-on
options):

- `/data/secrets/admin_username`, `/data/secrets/admin_password` - the
  InfluxDB admin login (username not secret; same value as the
  `admin_username` option).
- `/data/secrets/admin_token` - a random 64-character **operator
  (all-access) token**, passed to `influx setup --token`. **Used for
  administration and for the remote backup job** (see "Backups") - it is
  never given to Home Assistant.
- `/data/secrets/ha_token` - a token scoped to **write-only on the
  `bucket` option** (`homeassistant` by default), created once the server
  is actually answering on :8086, via `influx auth create --write-bucket
  <id>`. **This is the token to put in Home Assistant's `secrets.yaml`.**
  It cannot read any bucket, write any other bucket, or administer
  anything (create buckets/tokens/users, etc.) - verified in
  `tests/test.sh` against the running server (write to its own bucket:
  `204`; query/read its own bucket: `404`; write another bucket: `403`;
  create a bucket: `401`; its own bucket listing: empty).

  Write-only, not read+write, because Home Assistant Core's `influxdb`
  integration never needs read access for API v2: `get_influx_connection`
  in `homeassistant/components/influxdb/__init__.py` only calls with
  `test_read=True` from a code path that is in practice never taken for
  v2 (the only caller, `config_flow.py`, always passes `test_write=True`
  and leaves `test_read` at its default of `False`) - checked against the
  `dev` branch of `home-assistant/core`. If a future HA version needs
  read access too, regenerate `ha_token` with `--read-bucket <id>` added
  (delete the file and restart; see "idempotent" below).

  This file is created once the server is healthy, which also covers an
  add-on that was already set up by an earlier version of this add-on
  that predates this token: on the next start, if `ha_token` is missing,
  it is created then.

Both secret files are regenerated **only** when missing: the upstream
InfluxDB entry point itself skips `influx setup` whenever its boltdb file
already exists, so this add-on never overwrites a live account's
credentials on restart; `ha_token` is created once and left alone once it
exists.

To copy a token out without displaying it on screen, from the Home
Assistant host (replace `<ha-user>` as appropriate):

```sh
docker exec addon_9547a9e0_influxdb2 cat /data/secrets/ha_token \
  | ssh <ha-user>@<some-other-machine> 'cat > influxdb_token.txt'
```

or use the Terminal & SSH add-on to run the same `docker exec` - just
avoid `cat`-ing it straight into a terminal that gets logged.

## Rotating the operator token

Raise the `admin_token_generation` option by one (e.g. `0` -> `1`) and
restart the add-on to rotate `/data/secrets/admin_token`:

1. On the next start, once `influxd` is healthy, `run.sh` compares the
   option against the generation it last recorded at
   `/data/secrets/admin_token_generation` (a missing file counts as `0`).
   If the option is now higher, it creates a new operator (all-access,
   instance-wide) token for the admin user, using the current operator
   token to authenticate.
2. The new token is verified (a bucket listing) before anything on disk
   changes.
3. `/data/secrets/admin_token` is replaced with the new token (written to
   a temp file, then `mv`'d into place, so nothing ever reads a
   half-written file).
4. The *old* authorization is deleted by its ID (found by matching it
   against the token that was on disk, never printed), and the add-on
   confirms the old token now gets `401` before it records the new
   generation.
5. The new generation number is written last, only once all of the above
   has actually succeeded.

Every `influx` CLI call in this process passes `--org` explicitly - the
CLI's own saved config (which would otherwise supply a default org) is
written inside the container's filesystem by first-run `influx setup`,
not under `/data`, so it is absent in any container created after that
one. An add-on **update** replaces the container (it is not a `docker
restart` of the one that ran setup), so this matters on every update,
not just the first rotation - fixed in 0.1.2 (see CHANGELOG.md).

If any step fails, the generation file is left untouched (so the same
rotation is retried on the next start) and the token currently in
`admin_token` - old or new, whichever is last known to work - is left in
place; the add-on logs a clear `ERROR` line but is never left without a
working operator token. Restarting again with the same
`admin_token_generation` value, once rotation has already succeeded, does
nothing (no new token is created). Nothing here touches `ha_token` - it
keeps working throughout, unmodified.

Logs name only authorization IDs and the generation number; no token
value is ever written to a log.

**After rotating, refresh any copy of the operator token kept on another
machine** (e.g. the "Backups" job below) - the old copy will start
getting `401` as soon as the old authorization is deleted. Fetch the new
one the same way as the original (see "Token locations" above):

```sh
docker exec addon_9547a9e0_influxdb2 cat /data/secrets/admin_token \
  | ssh <ha-user>@<some-other-machine> 'cat > admin_token.txt'
```

## Backups

**Home Assistant's own backups do not contain this add-on's data.**
`config.yaml` sets `backup_exclude: ["**"]`, which (per
<https://contributors.home-assistant.io/apps/reference/configuration/>
and the Supervisor's own filter in `supervisor/apps/app.py`, which matches
each backed-up file's *name* against every `backup_exclude` glob) excludes
every file under `/data` from a Home Assistant backup of this add-on.
InfluxDB line-protocol history does not belong in the same backup/restore
cycle as the rest of Home Assistant - the off-site copy is made
separately, by a job on another LAN machine, pulling over HTTP.

This add-on does not run its own backup job or keep any backup files on
disk (an earlier revision did, writing into `addon_config`; that was
dropped because the Supervisor backs up `/addon_configs` as a single
unit with no per-add-on filtering - see `supervisor/backups/backup.py`'s
`_folder_save`, which only skips network mount points - so anything
written there would have ridden inside every Home Assistant backup,
including ones synced to Google Drive, which is explicitly not wanted).

Instead, from another machine on the LAN or tailnet, with the operator
token from `/data/secrets/admin_token`:

```sh
influx backup /path/to/backup_$(date +%Y-%m-%d_%H-%M) \
  --host http://<hass-lan-ip>:8086 \
  -t <operator token>
```

This is a full, consistent backup (bolt + SQL metadata + TSM data). That
job, and shipping its output to S3 Glacier, run elsewhere and are out of
scope for this add-on.

To restore (on a fresh InfluxDB instance): run `influx setup --token
<the same operator token the backup came from>` first (the restore
process re-authenticates using it), then `influx restore --full
<path to the backup directory>`. See
<https://docs.influxdata.com/influxdb/v2/admin/backup-restore/restore/>.

## Health

The add-on declares `watchdog: "http://[HOST]:8086/health"`, matching
InfluxDB's own unauthenticated `/health` endpoint (`{"status":"pass",...}`
once ready for queries and writes). The Supervisor restarts the add-on if
this stops responding.

## Not tested

- Running on real Raspberry Pi hardware - only `docker buildx build
  --platform linux/arm64 --load` plus a plain `docker run --platform
  linux/arm64` under the host's QEMU emulation were tried locally; both
  built and reached a healthy state with the expected flags in the logs.
- Installing through the Supervisor itself, or anything involving the
  real `hassio` add-on store, ingress gateway, Network tab, or backup UI -
  out of scope per the brief (no install on `hass`, no SSH to it).
- The exact behaviour of `backup_exclude: ["**"]` inside a real Supervisor
  backup (verified instead by reading the Supervisor source referenced
  above, since creating a real Home Assistant backup was not possible
  without installing on `hass`).
- Home Assistant Core's `influxdb` integration itself talking to this
  add-on (verified instead by reading its source, and by exercising the
  HA token's exact permission set directly against the API in
  `tests/test.sh`).
