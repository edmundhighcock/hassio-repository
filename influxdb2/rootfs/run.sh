#!/bin/bash
# Entry point for the influxdb2 add-on.
#
#  1. Reads add-on options (JSON written by the Supervisor to
#     /data/options.json) with jq - there is no bashio in this image,
#     because the base is the official influxdb image, not a
#     home-assistant/base image.
#  2. On the very first start, generates random admin credentials and an
#     admin (operator/all-access) token under /data/secrets/, mode 0600 -
#     see DOCS.md "Token locations".
#  3. Exports the memory-limiting influxd options (DOCS.md "Memory
#     defaults"), GOMEMLIMIT, --pprof-disabled (always) and
#     --metrics-disabled (unless `metrics_enabled` is set).
#  4. Runs the image's own /entrypoint.sh (unmodified) as a child - not
#     exec'd - so this script stays alive to forward signals to it and,
#     once it is actually healthy on :8086, to:
#       a. rotate the operator (admin) token if `admin_token_generation`
#          has been raised above the generation recorded under
#          /data/secrets/ - see DOCS.md "Rotating the operator token".
#       b. make sure a Home Assistant write-only token exists under
#          /data/secrets/, creating it then if missing (first run, or an
#          upgrade from a version of this add-on that predates it -
#          idempotent either way).
set -eu

OPTIONS=/data/options.json
DATA_DIR=/data/influxdb2
SECRETS_DIR=/data/secrets

log() { echo "[influxdb2] $*"; }

# sanitize_stderr <file>: first line of a captured stderr file, with any
# token-like run of 20+ base64/hex-ish characters redacted, for safe
# inclusion in an ERROR log line. Never logs a token value.
sanitize_stderr() {
    head -n1 "$1" 2>/dev/null | sed -E 's/[A-Za-z0-9+\/_=-]{20,}/[REDACTED]/g' | cut -c1-300
}

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
METRICS_ENABLED="$(opt metrics_enabled false)"
ADMIN_TOKEN_GENERATION="$(opt admin_token_generation 0)"

mkdir -p "$DATA_DIR/engine" "$SECRETS_DIR"
chmod 700 "$SECRETS_DIR"

# --- First-run setup: generate random secrets, once. ------------------
# influxd's own entrypoint skips `influx setup` whenever the boltdb file
# already exists, so re-generating these on every restart would silently
# desync the files from the real, already-provisioned account. Generate
# only when the directory is still empty.
random_token() { tr -dc 'A-Za-z0-9' </dev/urandom | head -c "$1"; }

if [ ! -s "$SECRETS_DIR/admin_password" ]; then
    log "first run: generating admin credentials and operator token under $SECRETS_DIR"
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

# --- Unauthenticated endpoints (DOCS.md "Disabled endpoints") ----------
# /debug/pprof is always off: it serves runtime profiling data to anyone
# who can reach the port, with no token check. /metrics is off by default
# for the same reason; `metrics_enabled` turns it back on for a LAN
# Prometheus scraper.
export INFLUXD_PPROF_DISABLED=true
if [ "$METRICS_ENABLED" = "true" ]; then
    export INFLUXD_METRICS_DISABLED=false
else
    export INFLUXD_METRICS_DISABLED=true
fi

log "org=$ORG bucket=$BUCKET retention=${RETENTION:-infinite}"
log "storage-cache-max-memory-size=$STORAGE_CACHE_MAX_MEMORY_SIZE storage-cache-snapshot-memory-size=$STORAGE_CACHE_SNAPSHOT_MEMORY_SIZE storage-max-concurrent-compactions=$STORAGE_MAX_CONCURRENT_COMPACTIONS"
log "query-concurrency=$QUERY_CONCURRENCY query-queue-size=$QUERY_QUEUE_SIZE query-initial-memory-bytes=$QUERY_INITIAL_MEMORY_BYTES query-memory-bytes=$QUERY_MEMORY_BYTES query-max-memory-bytes=$QUERY_MAX_MEMORY_BYTES"
log "GOMEMLIMIT=$GOMEMLIMIT pprof-disabled=true metrics-disabled=$INFLUXD_METRICS_DISABLED"

# --- Rotate the operator (admin) token on request ----------------------
# Compares the `admin_token_generation` option against the generation
# recorded at $SECRETS_DIR/admin_token_generation (missing file = 0, per
# DOCS.md). When the option is higher: create a new operator (all-access,
# instance-wide) token for $ADMIN_USERNAME, verify it actually
# authenticates, write it over admin_token (temp file + mv, so a reader
# never sees a half-written file), then delete the previous authorization
# by ID and confirm it now gets 401 before recording the new generation.
# Never logs a token value - only authorization IDs and the generation
# number. If any step fails, the generation file is left alone so the
# same rotation is retried next start; whichever token is currently in
# admin_token at that point is known-good and left in place, so the
# add-on is never left without a working operator token.
rotate_admin_token() {
    local desired_gen current_gen old_token old_auth_id new_token new_auth_id tmp http_code err_file

    desired_gen="$ADMIN_TOKEN_GENERATION"
    current_gen="$(cat "$SECRETS_DIR/admin_token_generation" 2>/dev/null || true)"
    [ -n "$current_gen" ] || current_gen=0

    if [ "$desired_gen" -le "$current_gen" ] 2>/dev/null; then
        return 0
    fi

    log "admin_token_generation option ($desired_gen) is ahead of the recorded generation ($current_gen): rotating the operator token"

    old_token="$(cat "$SECRETS_DIR/admin_token" 2>/dev/null || true)"
    if [ -z "$old_token" ]; then
        log "ERROR: no current admin_token on disk to rotate from; not rotating, will retry next start"
        return 1
    fi

    err_file="$(mktemp)"

    # --org is passed explicitly on every call below that can take one:
    # the influx CLI's own saved config (~/.influxdbv2, holding the
    # default org from first-run `influx setup`) lives in the container
    # filesystem, not /data, so it is absent in any container created
    # after the one that did setup (e.g. after an add-on update, which is
    # a new container on the same /data) - never rely on it.
    old_auth_id="$(influx auth list --host http://localhost:8086 --token "$old_token" \
        --org "$ORG" --user "$ADMIN_USERNAME" --json 2>"$err_file" \
        | jq -r --arg t "$old_token" '.[] | select(.token==$t) | .id' | head -1)"
    if [ -z "$old_auth_id" ]; then
        log "ERROR: could not find the authorization ID of the current operator token (influx auth list: $(sanitize_stderr "$err_file")); not rotating, old token kept, will retry next start"
        rm -f "$err_file"
        return 1
    fi
    log "current operator token is authorization $old_auth_id"

    new_token="$(influx auth create --host http://localhost:8086 --token "$old_token" \
        --org "$ORG" --operator --user "$ADMIN_USERNAME" \
        --description "operator token (generation $desired_gen)" --json 2>"$err_file" \
        | jq -r .token)"
    if [ -z "$new_token" ] || [ "$new_token" = "null" ]; then
        log "ERROR: failed to create a new operator token (influx auth create: $(sanitize_stderr "$err_file")); old token (authorization $old_auth_id) kept, will retry next start"
        rm -f "$err_file"
        return 1
    fi
    new_auth_id="$(influx auth list --host http://localhost:8086 --token "$new_token" \
        --org "$ORG" --user "$ADMIN_USERNAME" --json 2>"$err_file" \
        | jq -r --arg t "$new_token" '.[] | select(.token==$t) | .id' | head -1)"

    if ! influx bucket list --host http://localhost:8086 --token "$new_token" --org "$ORG" >/dev/null 2>"$err_file"; then
        log "ERROR: new operator token (authorization $new_auth_id) failed to authenticate (influx bucket list: $(sanitize_stderr "$err_file")); deleting it and keeping the old one (authorization $old_auth_id), will retry next start"
        influx auth delete --host http://localhost:8086 --token "$old_token" -i "$new_auth_id" >/dev/null 2>&1 || true
        rm -f "$err_file"
        return 1
    fi
    log "new operator token (authorization $new_auth_id) verified working"

    umask 077
    tmp="$SECRETS_DIR/admin_token.new.$$"
    printf '%s' "$new_token" >"$tmp"
    chmod 600 "$tmp"
    mv -f "$tmp" "$SECRETS_DIR/admin_token"
    log "admin_token replaced with the new operator token (authorization $new_auth_id)"

    # auth delete takes no --org/--org-id flag (checked against `influx
    # auth delete --help`): an authorization ID is already unambiguous.
    if ! influx auth delete --host http://localhost:8086 --token "$new_token" -i "$old_auth_id" >/dev/null 2>"$err_file"; then
        log "ERROR: failed to delete old operator token (authorization $old_auth_id) (influx auth delete: $(sanitize_stderr "$err_file")); the new token (authorization $new_auth_id) is in place and working, but the old one is still valid too - not recording the new generation, will retry next start"
        rm -f "$err_file"
        return 1
    fi

    http_code="$(curl -s -o /dev/null -w '%{http_code}' \
        -H "Authorization: Token $old_token" "http://localhost:8086/api/v2/buckets")"
    if [ "$http_code" != "401" ]; then
        log "ERROR: old operator token (authorization $old_auth_id) did not return 401 after deletion (got $http_code); not recording the new generation, will retry next start"
        rm -f "$err_file"
        return 1
    fi
    log "old operator token (authorization $old_auth_id) confirmed revoked (401)"

    tmp="$SECRETS_DIR/admin_token_generation.new.$$"
    printf '%s' "$desired_gen" >"$tmp"
    chmod 600 "$tmp"
    mv -f "$tmp" "$SECRETS_DIR/admin_token_generation"
    rm -f "$err_file"
    log "operator token rotation complete: generation is now $desired_gen"
}

# --- Ensure the Home Assistant write-only token exists -----------------
# Scoped to write on $BUCKET only (not read: Home Assistant Core's own
# influxdb integration never calls get_influx_connection with
# test_read=True for API v2 - verified against
# homeassistant/components/influxdb/__init__.py and config_flow.py on the
# `dev` branch - its own startup/config-flow check is test_write=True
# only). Runs once the real server answers on :8086, so it also covers an
# add-on that was already set up by a version of this add-on that
# predates this token.
ensure_ha_token() {
    [ -s "$SECRETS_DIR/ha_token" ] && return 0
    log "creating Home Assistant write-only token on bucket $BUCKET"
    local admin_token bucket_id token err_file
    admin_token="$(cat "$SECRETS_DIR/admin_token")"
    err_file="$(mktemp)"
    bucket_id="$(influx bucket list --host http://localhost:8086 --token "$admin_token" \
        --org "$ORG" --name "$BUCKET" --hide-headers 2>"$err_file" | cut -f1)"
    if [ -z "$bucket_id" ]; then
        log "could not look up bucket '$BUCKET' yet (influx bucket list: $(sanitize_stderr "$err_file")), will retry next start"
        rm -f "$err_file"
        return 1
    fi
    token="$(influx auth create --host http://localhost:8086 --token "$admin_token" \
        --org "$ORG" --write-bucket "$bucket_id" \
        --description "Home Assistant write access (bucket: $BUCKET)" --json 2>"$err_file" \
        | jq -r .token)"
    if [ -z "$token" ] || [ "$token" = "null" ]; then
        log "failed to create Home Assistant token (influx auth create: $(sanitize_stderr "$err_file")), will retry next start"
        rm -f "$err_file"
        return 1
    fi
    rm -f "$err_file"
    umask 077
    printf '%s' "$token" >"$SECRETS_DIR/ha_token"
    chmod 600 "$SECRETS_DIR/ha_token"
    log "Home Assistant token created at $SECRETS_DIR/ha_token"
}

wait_healthy() {
    local i
    for i in $(seq 1 120); do
        if curl -fs http://localhost:8086/health 2>/dev/null | grep -q '"status":"pass"'; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# --- Run the image's own entrypoint as a child, not exec'd -------------
/entrypoint.sh &
ENTRY_PID=$!
forward() { kill -TERM "$ENTRY_PID" 2>/dev/null || true; }
trap forward TERM INT

if wait_healthy; then
    rotate_admin_token || true
    ensure_ha_token || true
else
    log "influxd did not become healthy in time; skipping Home Assistant token check this start"
fi

wait "$ENTRY_PID"
exit $?
