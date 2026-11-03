#!/bin/bash
# Entry point for the influxdb2 add-on.
#
#  1. Reads add-on options (JSON written by the Supervisor to
#     /data/options.json) with jq - there is no bashio in this image,
#     because the base is the official influxdb image, not a
#     home-assistant/base image.
#  2. On the very first start, generates random admin credentials and an
#     admin (all-access) token under /data/secrets/, mode 0600, and never
#     repeats this once they exist - see DOCS.md "Token location".
#  3. Exports the memory-limiting influxd options (DOCS.md "Memory
#     defaults") and GOMEMLIMIT.
#  4. Starts the nightly backup loop in the background.
#  5. Hands off to the image's own /entrypoint.sh (unmodified), which does
#     `influx setup` (first start only - it already skips setup when the
#     boltdb file is found, so restarts are idempotent) and then runs
#     influxd in the foreground as PID 1.
set -eu

OPTIONS=/data/options.json
DATA_DIR=/data/influxdb2
SECRETS_DIR=/data/secrets
BACKUP_ROOT=/config/backups

log() { echo "[influxdb2] $*"; }

opt() {
    # opt <key> <default>
    jq -r --arg d "$2" '.[$ARGS.named.k] // $d | tostring' --arg k "$1" "$OPTIONS" \
        2>/dev/null || echo "$2"
}

ORG="$(opt organization household)"
BUCKET="$(opt bucket homeassistant)"
RETENTION="$(opt retention '')"
ADMIN_USERNAME="$(opt admin_username admin)"
STORAGE_CACHE_MAX_MEMORY_SIZE="$(opt storage_cache_max_memory_size 268435456)"
STORAGE_CACHE_SNAPSHOT_MEMORY_SIZE="$(opt storage_cache_snapshot_memory_size 16777216)"
STORAGE_MAX_CONCURRENT_COMPACTIONS="$(opt storage_max_concurrent_compactions 1)"
QUERY_CONCURRENCY="$(opt query_concurrency 2)"
QUERY_QUEUE_SIZE="$(opt query_queue_size 10)"
QUERY_INITIAL_MEMORY_BYTES="$(opt query_initial_memory_bytes 10485760)"
QUERY_MEMORY_BYTES="$(opt query_memory_bytes 209715200)"
QUERY_MAX_MEMORY_BYTES="$(opt query_max_memory_bytes 419430400)"
GO_MEMLIMIT_MIB="$(opt go_memlimit_mib 450)"
BACKUP_KEEP="$(opt backup_keep 2)"
BACKUP_HOUR="$(opt backup_hour 3)"

mkdir -p "$DATA_DIR/engine" "$SECRETS_DIR" "$BACKUP_ROOT"
chmod 700 "$SECRETS_DIR"

# --- First-run setup: generate random secrets, once. ------------------
# influxd's own entrypoint skips `influx setup` whenever the boltdb file
# already exists, so re-generating these on every restart would silently
# desync the files from the real, already-provisioned account. Generate
# only when the directory is still empty.
random_token() { tr -dc 'A-Za-z0-9' </dev/urandom | head -c "$1"; }

if [ ! -s "$SECRETS_DIR/admin_password" ]; then
    log "first run: generating admin credentials and token under $SECRETS_DIR"
    umask 077
    random_token 32 >"$SECRETS_DIR/admin_password"
    random_token 64 >"$SECRETS_DIR/admin_token"
    echo "$ADMIN_USERNAME" >"$SECRETS_DIR/admin_username"
    chmod 600 "$SECRETS_DIR"/admin_password "$SECRETS_DIR"/admin_token "$SECRETS_DIR"/admin_username
fi

# --- influxd setup/env (read by the upstream /entrypoint.sh) ----------
export DOCKER_INFLUXDB_INIT_MODE=setup
export DOCKER_INFLUXDB_INIT_USERNAME="$ADMIN_USERNAME"
export DOCKER_INFLUXDB_INIT_PASSWORD_FILE="$SECRETS_DIR/admin_password"
export DOCKER_INFLUXDB_INIT_ADMIN_TOKEN_FILE="$SECRETS_DIR/admin_token"
export DOCKER_INFLUXDB_INIT_ORG="$ORG"
export DOCKER_INFLUXDB_INIT_BUCKET="$BUCKET"
if [ -n "$RETENTION" ]; then
    export DOCKER_INFLUXDB_INIT_RETENTION="$RETENTION"
fi

export INFLUXD_BOLT_PATH="$DATA_DIR/influxd.bolt"
export INFLUXD_ENGINE_PATH="$DATA_DIR/engine"

# --- Memory limits (DOCS.md "Memory defaults and rationale") ----------
export INFLUXD_STORAGE_CACHE_MAX_MEMORY_SIZE="$STORAGE_CACHE_MAX_MEMORY_SIZE"
export INFLUXD_STORAGE_CACHE_SNAPSHOT_MEMORY_SIZE="$STORAGE_CACHE_SNAPSHOT_MEMORY_SIZE"
export INFLUXD_STORAGE_MAX_CONCURRENT_COMPACTIONS="$STORAGE_MAX_CONCURRENT_COMPACTIONS"
export INFLUXD_QUERY_CONCURRENCY="$QUERY_CONCURRENCY"
export INFLUXD_QUERY_QUEUE_SIZE="$QUERY_QUEUE_SIZE"
export INFLUXD_QUERY_INITIAL_MEMORY_BYTES="$QUERY_INITIAL_MEMORY_BYTES"
export INFLUXD_QUERY_MEMORY_BYTES="$QUERY_MEMORY_BYTES"
export INFLUXD_QUERY_MAX_MEMORY_BYTES="$QUERY_MAX_MEMORY_BYTES"
export GOMEMLIMIT="${GO_MEMLIMIT_MIB}MiB"

log "org=$ORG bucket=$BUCKET retention=${RETENTION:-infinite}"
log "storage-cache-max-memory-size=$STORAGE_CACHE_MAX_MEMORY_SIZE storage-cache-snapshot-memory-size=$STORAGE_CACHE_SNAPSHOT_MEMORY_SIZE storage-max-concurrent-compactions=$STORAGE_MAX_CONCURRENT_COMPACTIONS"
log "query-concurrency=$QUERY_CONCURRENCY query-queue-size=$QUERY_QUEUE_SIZE query-initial-memory-bytes=$QUERY_INITIAL_MEMORY_BYTES query-memory-bytes=$QUERY_MEMORY_BYTES query-max-memory-bytes=$QUERY_MAX_MEMORY_BYTES"
log "GOMEMLIMIT=$GOMEMLIMIT"

# --- Nightly backup loop (background) ----------------------------------
backup_loop() {
    while true; do
        now=$(date +%s)
        target=$(date -d "today ${BACKUP_HOUR}:00:00" +%s 2>/dev/null || true)
        if [ -z "$target" ] || [ "$target" -le "$now" ]; then
            target=$(date -d "tomorrow ${BACKUP_HOUR}:00:00" +%s)
        fi
        sleep_secs=$((target - now))
        log "next backup in ${sleep_secs}s (around $(date -d "@$target" '+%F %T'))"
        sleep "$sleep_secs"
        BACKUP_KEEP="$BACKUP_KEEP" BACKUP_ROOT="$BACKUP_ROOT" SECRETS_DIR="$SECRETS_DIR" \
            /usr/local/bin/influxdb2-backup.sh || log "nightly backup failed (see above)"
    done
}
backup_loop &

# --- Hand off to the image's own entrypoint, which runs influxd -------
exec /entrypoint.sh
