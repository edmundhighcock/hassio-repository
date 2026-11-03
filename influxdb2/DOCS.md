# InfluxDB 2

InfluxDB 2.7 OSS, for storing Home Assistant's full state-change history
forever on a memory-tight Raspberry Pi. Written to by HA Core's own
`influxdb` integration (`api_version: 2`); no InfluxDB UI, no host port.

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
Docker network - reachable from HA Core and any other add-on, with no host
port published. Put the token (see "Token location" below) into
`secrets.yaml` as `influxdb_token: <value>`; never paste it into
`configuration.yaml` directly.

## Options and their defaults

| Option | Default | What it does |
| --- | --- | --- |
| `organization` | `household` | InfluxDB org created on first start. |
| `bucket` | `homeassistant` | Bucket created on first start. |
| `retention` | `` (empty) | Bucket retention. Empty means infinite - the brief for this add-on. |
| `admin_username` | `admin` | Not a secret; the password and token are generated, never configured. |
| `storage_cache_max_memory_size` | `268435456` (256 MiB) | Max size a shard's in-memory cache can reach before it rejects writes. |
| `storage_cache_snapshot_memory_size` | `16777216` (16 MiB) | Size at which the cache is snapshotted to a TSM file to free memory. |
| `storage_max_concurrent_compactions` | `1` | Max concurrent full/level compactions. |
| `query_concurrency` | `2` | Queries allowed to run at once. |
| `query_queue_size` | `10` | Queries allowed to wait; beyond this, new queries are rejected outright. |
| `query_memory_bytes` | `209715200` (200 MiB) | Max memory for a *single* query. Over this, the query fails. |
| `query_initial_memory_bytes` | `10485760` (10 MiB) | Memory a query starts with before it needs to grow. |
| `query_max_memory_bytes` | `419430400` (400 MiB) | Max memory for *all* queries running at once. |
| `go_memlimit_mib` | `450` | `GOMEMLIMIT` - the Go runtime's soft heap limit, in MiB. |
| `backup_keep` | `2` | How many nightly backup directories to keep under `addon_config`. |
| `backup_hour` | `3` | Local hour (0-23) the nightly backup runs. |

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
  waiting for Go's usual pacing, so steady-state RSS settles below the
  flags above would otherwise allow, with headroom under a ~700 MiB+
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

## Token location

On first start, this add-on generates, under `/data/secrets/` inside the
container (mode `0600`, root-owned, never logged, never in add-on
options):

- `admin_username` - the InfluxDB admin username (not secret; same value
  as the `admin_username` option).
- `admin_password` - a random 32-character admin password.
- `admin_token` - a random 64-character **all-access (operator) token**,
  passed to `influx setup --token` so it becomes both the InfluxDB admin
  token *and* the token to give Home Assistant.

An all-access token was chosen over a bucket-scoped one so the same token
also works for the nightly `influx backup` job (which needs the root/
operator token) without a second token to manage; the brief allows either.
In a single-household add-on this is an acceptable trade-off - note it in
case you want to issue HA a narrower, bucket-scoped token yourself later
(`influx auth create --org household --read-bucket homeassistant
--write-bucket homeassistant`) and use that one in `secrets.yaml` instead.

To copy the token out without displaying it on screen, from the Home
Assistant host (replace `<ha-user>` as appropriate):

```sh
docker exec addon_9547a9e0_influxdb2 cat /data/secrets/admin_token \
  | ssh <ha-user>@<some-other-machine> 'cat > influxdb_token.txt'
```

or use the Terminal & SSH add-on to run the same `docker exec` (or,
simpler, add a `!secret` line referencing a file you copy by hand from
`/addon_configs/9547a9e0_influxdb2/` - see below) - just avoid `cat`-ing
it straight into a terminal that gets logged.

These files are regenerated **only** when they do not already exist: the
upstream InfluxDB entry point itself skips `influx setup` whenever its
boltdb file is already present, so this add-on never overwrites a live
account's credentials on restart.

## Backups

**Home Assistant's own backups do not contain this add-on's data.**
`config.yaml` sets `backup_exclude: ["**"]`, which (per
<https://contributors.home-assistant.io/apps/reference/configuration/>
and the Supervisor's own filter in `supervisor/apps/app.py`, which matches
each backed-up file's *name* against every `backup_exclude` glob) excludes
every file under `/data` from a Home Assistant backup of this add-on. The
off-site copy is made separately (see below), by design, because InfluxDB
line-protocol history does not belong in the same backup/restore cycle as
the rest of Home Assistant.

**Limitation found while building this, not fixed by any add-on option:**
the nightly backup files below live under `/addon_configs/9547a9e0_influxdb2/`,
which Home Assistant backs up as part of the top-level "Add-on configs"
folder category - a wholesale tar of the entire `/addon_configs` tree,
selected per-backup by the user, with **no per-add-on filtering** (see
`supervisor/backups/backup.py`'s `_folder_save`, which only skips network
mount points, not `backup_exclude`). There is no documented add-on-level
way to keep just this add-on's subfolder out of that specific backup
folder. If you don't want the nightly InfluxDB backups duplicated into
Home Assistant's own backups as well as the off-site S3 copy, deselect
"Add-on configs" when creating a Home Assistant backup, or accept the
duplication.

Nightly, at `backup_hour` (default 03:00 local time), this add-on runs
`influx backup <dir> -t <admin token>` (a full, consistent backup: bolt +
SQL metadata + TSM data) into a new timestamped directory under
`/addon_configs/9547a9e0_influxdb2/backups/` (inside the container,
`/config/backups/`), then deletes all but the `backup_keep` (default 2)
most recent directories. A separate job on another machine is expected to
pull these over SSH (e.g. with the SSH & Terminal add-on, or any SSH
client reaching the host directly) and ship them to S3 Glacier - that job
is out of scope here.

To restore: copy a backup directory to a fresh InfluxDB instance, run
`influx setup --token <the same admin token the backup came from>` on
that instance (the restore process re-authenticates using it), then
`influx restore --full <path to the backup directory>`. See
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
  built and reached a healthy state with the expected flags in the logs
  (see `tests/test.sh` for the equivalent amd64 checks, run for real).
- Installing through the Supervisor itself, or anything involving the
  real `hassio` add-on store, ingress gateway, or backup UI - out of
  scope per the brief (no install on `hass`, no SSH to it).
- The exact behaviour of `backup_exclude: ["**"]` inside a real Supervisor
  backup (verified instead by reading the Supervisor source referenced
  above, since creating a real Home Assistant backup was not possible
  without installing on `hass`).
