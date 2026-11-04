#!/bin/bash
# Local test of the influxdb2 image (amd64 build, run under plain docker -
# no Supervisor involved). See DOCS.md for what each check corresponds to:
#   - build, run with a 700m memory cap, a temp /data, and the published
#     host port
#   - first-run setup, then an idempotent restart
#   - the memory/query flags actually reached the running influxd
#   - a line-protocol write with a string field and a numeric field,
#     read back
#   - the Home Assistant write-only token can write to the homeassistant
#     bucket but cannot read it, write another bucket, or administer
#   - /debug/pprof and /metrics are refused by default
#   - admin_token_generation rotation: raising it swaps admin_token for a
#     new working operator token and 401s the old one, re-running with
#     the same generation is a no-op, raising it again rotates again,
#     and ha_token is untouched throughout
#   - the same rotation and ha_token-recreation checks again, but each
#     step in a brand-new container on the same /data (the real upgrade
#     path: a container update is a new container, never a `docker
#     restart` of the one that ran first-run `influx setup`) - this is
#     the scenario that caught the 0.1.1 bug where rotation relied on the
#     influx CLI's own saved config, which does not survive a new
#     container
#   - a backup taken over HTTP from outside the container with the
#     operator token, restored into a second throwaway container, with
#     a matching point count
# Exit status is the number of failed checks. Everything this script
# creates (containers, image, temp dirs) is removed on exit.
set -u
cd "$(dirname "$0")/.."
IMG=influxdb2-addon-test
HOST_PORT=${PORT:-18086}
TMP=$(mktemp -d)
FAILS=0

ok()  { echo "PASS $*"; }
bad() { echo "FAIL $*"; FAILS=$((FAILS + 1)); }
expect() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (want '$2', got '$3')"; fi; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
hash() { printf '%s' "$1" | sha256sum | cut -d' ' -f1; }
wait_healthy_url() {
    local i
    for i in $(seq 1 60); do
        curl -s "$URL/health" 2>/dev/null | grep -q '"status":"pass"' && return 0
        sleep 1
    done
    return 1
}
set_option() { docker exec influxdb2-test-a sh -c "echo '$1' > /data/options.json"; }

# wait_health_at <url>: like wait_healthy_url, for a container other than
# influxdb2-test-a (used by the multi-container rotation scenario below,
# where each step is a brand-new container on the same /data).
wait_health_at() {
    local u="$1" i
    for i in $(seq 1 60); do
        curl -s "$u/health" 2>/dev/null | grep -q '"status":"pass"' && return 0
        sleep 1
    done
    return 1
}
# wait_new_token_at <container> <old_hash>: polls admin_token inside
# <container> until it differs from <old_hash>, echoes it (whatever it
# last read, even on timeout, so the caller's own hash-comparison assert
# fails cleanly instead of comparing against an empty string).
wait_new_token_at() {
    local c="$1" oldhash="$2" tok i
    for i in $(seq 1 30); do
        tok=$(docker exec "$c" cat /data/secrets/admin_token 2>/dev/null)
        if [ -n "$tok" ] && [ "$(hash "$tok")" != "$oldhash" ]; then
            printf '%s' "$tok"
            return 0
        fi
        sleep 1
    done
    printf '%s' "$tok"
}

cleanup() {
    docker rm -f influxdb2-test-a influxdb2-test-restore \
        influxdb2-test-b influxdb2-test-c influxdb2-test-d influxdb2-test-e influxdb2-test-f \
        >/dev/null 2>&1
    # Files under $TMP are root-owned (influxd runs as root in the
    # container); clean them up from a throwaway container too.
    docker run --rm -v "$TMP:/cleanup" "$IMG" sh -c 'rm -rf /cleanup/*' >/dev/null 2>&1
    rm -rf "$TMP"
    docker rmi "$IMG" >/dev/null 2>&1
}
trap cleanup EXIT

echo "Building image..."
docker build -q -t "$IMG" . >/dev/null || { echo "build failed"; exit 99; }

mkdir -p "$TMP/data-a"

echo "Starting container (700m memory cap, temp /data, published port $HOST_PORT)..."
docker run -d --name influxdb2-test-a --memory=700m \
    -p "127.0.0.1:$HOST_PORT:8086" \
    -v "$TMP/data-a:/data" \
    "$IMG" >/dev/null

URL="http://127.0.0.1:$HOST_PORT"

# Wait for /health to report pass (first-run setup takes a few seconds).
ready=0
for _ in $(seq 1 60); do
    if curl -s "$URL/health" 2>/dev/null | grep -q '"status":"pass"'; then
        ready=1
        break
    fi
    sleep 1
done
[ "$ready" = 1 ] && ok "container became healthy" || bad "container became healthy"

LOG_A="$(docker logs influxdb2-test-a 2>&1)"
echo "$LOG_A" | grep -q "first run: generating admin credentials" && ok "first-run setup ran" || bad "first-run setup ran"

# The secrets dir is mode 0700 owned by root inside the container, so it
# is read through `docker exec` (as the test harness's own host user, not
# root) rather than directly off the bind-mounted host path.
docker exec influxdb2-test-a test -s /data/secrets/admin_password && ok "admin_password file created" || bad "admin_password file created"
docker exec influxdb2-test-a test -s /data/secrets/admin_token && ok "admin_token file created" || bad "admin_token file created"
PERM=$(docker exec influxdb2-test-a stat -c '%a' /data/secrets/admin_token 2>/dev/null)
expect "admin_token file mode 0600" "600" "$PERM"

docker exec influxdb2-test-a test -s /data/influxdb2/influxd.bolt && ok "bolt file under /data/influxdb2" || bad "bolt file under /data/influxdb2"

echo "$LOG_A" | grep -q 'concurrency_quota=2 initial_memory_bytes_quota_per_query=10485760 memory_bytes_quota_per_query=209715200 max_memory_bytes=419430400 queue_size=10' \
    && ok "query flags took effect" || bad "query flags took effect"
echo "$LOG_A" | grep -q 'max_concurrent_compactions=1' && ok "storage-max-concurrent-compactions took effect" || bad "storage-max-concurrent-compactions took effect"

ADMIN_TOKEN=$(docker exec influxdb2-test-a cat /data/secrets/admin_token 2>/dev/null)
[ -n "$ADMIN_TOKEN" ] && ok "read back admin token" || bad "read back admin token"

echo "Waiting for the Home Assistant write-only token..."
HA_TOKEN=""
for _ in $(seq 1 30); do
    HA_TOKEN=$(docker exec influxdb2-test-a cat /data/secrets/ha_token 2>/dev/null)
    [ -n "$HA_TOKEN" ] && break
    sleep 1
done
[ -n "$HA_TOKEN" ] && ok "Home Assistant write-only token created" || bad "Home Assistant write-only token created"
[ "$HA_TOKEN" != "$ADMIN_TOKEN" ] && ok "Home Assistant token differs from the operator token" || bad "Home Assistant token differs from the operator token"

echo "Writing a line-protocol point with a string and a numeric field (operator token)..."
WRITE_CODE=$(code -XPOST "$URL/api/v2/write?org=household&bucket=homeassistant&precision=s" \
    -H "Authorization: Token $ADMIN_TOKEN" \
    --data-raw 'sensor,entity_id=test1 state="on",value=42.5')
expect "write accepted" "204" "$WRITE_CODE"

QUERY_OUT=$(curl -s -XPOST "$URL/api/v2/query?org=household" \
    -H "Authorization: Token $ADMIN_TOKEN" -H "Content-Type: application/vnd.flux" \
    --data-raw 'from(bucket:"homeassistant") |> range(start:-10m) |> filter(fn:(r)=>r._measurement=="sensor")')
echo "$QUERY_OUT" | grep -q ',"on",state,sensor,test1\|,on,state,sensor,test1' && ok "string field read back" || bad "string field read back"
echo "$QUERY_OUT" | grep -q ',42.5,value,sensor,test1' && ok "numeric field read back" || bad "numeric field read back"

echo "Checking the Home Assistant token's permissions..."
HA_WRITE_CODE=$(code -XPOST "$URL/api/v2/write?org=household&bucket=homeassistant&precision=s" \
    -H "Authorization: Token $HA_TOKEN" --data-raw 'sensor,entity_id=test2 state="off",value=1')
expect "HA token can write its own bucket" "204" "$HA_WRITE_CODE"

HA_READ_CODE=$(code -XPOST "$URL/api/v2/query?org=household" \
    -H "Authorization: Token $HA_TOKEN" -H "Content-Type: application/vnd.flux" \
    --data-raw 'from(bucket:"homeassistant") |> range(start:-10m)')
expect "HA token cannot read/query its bucket" "404" "$HA_READ_CODE"

HA_OTHER_BUCKET_CODE=$(code -XPOST "$URL/api/v2/write?org=household&bucket=_monitoring&precision=s" \
    -H "Authorization: Token $HA_TOKEN" --data-raw 'sensor,entity_id=test3 state="off",value=1')
expect "HA token cannot write another bucket" "403" "$HA_OTHER_BUCKET_CODE"

ORG_ID=$(curl -s "$URL/api/v2/orgs" -H "Authorization: Token $ADMIN_TOKEN" | jq -r '.orgs[0].id')
HA_ADMIN_CODE=$(code -XPOST "$URL/api/v2/buckets" -H "Authorization: Token $HA_TOKEN" \
    -H "Content-Type: application/json" --data-raw "{\"orgID\":\"$ORG_ID\",\"name\":\"evil\"}")
expect "HA token cannot administer (create a bucket)" "401" "$HA_ADMIN_CODE"

HA_BUCKETS_BODY=$(curl -s "$URL/api/v2/buckets" -H "Authorization: Token $HA_TOKEN")
HA_BUCKETS_COUNT=$(echo "$HA_BUCKETS_BODY" | jq '.buckets | length')
expect "HA token's own bucket listing is empty (no read permission)" "0" "$HA_BUCKETS_COUNT"

echo "Checking unauthenticated endpoints are refused by default..."
expect "/debug/pprof refused by default" "403" "$(code "$URL/debug/pprof/")"
expect "/metrics refused by default" "403" "$(code "$URL/metrics")"

echo "Restarting container (idempotency check)..."
OLD_PASS=$(docker exec influxdb2-test-a cat /data/secrets/admin_password)
OLD_TOKEN=$(docker exec influxdb2-test-a cat /data/secrets/admin_token)
OLD_HA_TOKEN=$(docker exec influxdb2-test-a cat /data/secrets/ha_token)
docker restart influxdb2-test-a >/dev/null
ready=0
for _ in $(seq 1 60); do
    if curl -s "$URL/health" 2>/dev/null | grep -q '"status":"pass"'; then
        ready=1
        break
    fi
    sleep 1
done
[ "$ready" = 1 ] && ok "container healthy after restart" || bad "container healthy after restart"
LOG_RESTART="$(docker logs influxdb2-test-a 2>&1)"
echo "$LOG_RESTART" | grep -q "found existing boltdb file, skipping setup wrapper" \
    && ok "restart skipped setup (idempotent)" || bad "restart skipped setup (idempotent)"
expect "admin_password unchanged after restart" "$OLD_PASS" "$(docker exec influxdb2-test-a cat /data/secrets/admin_password)"
expect "admin_token unchanged after restart" "$OLD_TOKEN" "$(docker exec influxdb2-test-a cat /data/secrets/admin_token)"
expect "ha_token unchanged after restart" "$OLD_HA_TOKEN" "$(docker exec influxdb2-test-a cat /data/secrets/ha_token)"

echo "Deleting ha_token and restarting, to check the upgrade path..."
docker exec influxdb2-test-a rm -f /data/secrets/ha_token
docker restart influxdb2-test-a >/dev/null
RECREATED=""
for _ in $(seq 1 60); do
    if curl -s "$URL/health" 2>/dev/null | grep -q '"status":"pass"'; then
        RECREATED=$(docker exec influxdb2-test-a cat /data/secrets/ha_token 2>/dev/null)
        [ -n "$RECREATED" ] && break
    fi
    sleep 1
done
[ -n "$RECREATED" ] && ok "ha_token re-created on next start when missing" || bad "ha_token re-created on next start when missing"
HA_TOKEN="$RECREATED"

echo "Testing admin_token_generation rotation (0 -> 1)..."
GEN0_TOKEN=$(docker exec influxdb2-test-a cat /data/secrets/admin_token)
GEN0_HASH=$(hash "$GEN0_TOKEN")
set_option '{"admin_token_generation":1}'
docker restart influxdb2-test-a >/dev/null
wait_healthy_url && ok "container healthy after gen-1 restart" || bad "container healthy after gen-1 restart"
GEN1_TOKEN=""
for _ in $(seq 1 30); do
    GEN1_TOKEN=$(docker exec influxdb2-test-a cat /data/secrets/admin_token 2>/dev/null)
    [ -n "$GEN1_TOKEN" ] && [ "$(hash "$GEN1_TOKEN")" != "$GEN0_HASH" ] && break
    sleep 1
done
GEN1_HASH=$(hash "$GEN1_TOKEN")
[ "$GEN1_HASH" != "$GEN0_HASH" ] && ok "admin_token changed after generation 1 rotation" || bad "admin_token changed after generation 1 rotation"
expect "admin_token_generation recorded as 1" "1" "$(docker exec influxdb2-test-a cat /data/secrets/admin_token_generation 2>/dev/null)"
expect "new (gen 1) operator token works" "200" "$(code "$URL/api/v2/buckets" -H "Authorization: Token $GEN1_TOKEN")"
expect "old (gen 0) operator token now 401" "401" "$(code "$URL/api/v2/buckets" -H "Authorization: Token $GEN0_TOKEN")"
expect "ha_token unchanged by rotation" "$HA_TOKEN" "$(docker exec influxdb2-test-a cat /data/secrets/ha_token)"
expect "ha_token still writes after rotation" "204" "$(code -XPOST "$URL/api/v2/write?org=household&bucket=homeassistant&precision=s" \
    -H "Authorization: Token $HA_TOKEN" --data-raw 'sensor,entity_id=test4 state="on",value=1')"

echo "Restarting again at generation 1 (should not rotate again)..."
docker restart influxdb2-test-a >/dev/null
wait_healthy_url && ok "container healthy after no-op gen-1 restart" || bad "container healthy after no-op gen-1 restart"
sleep 2
expect "admin_token unchanged on restart at the same generation" "$GEN1_HASH" "$(hash "$(docker exec influxdb2-test-a cat /data/secrets/admin_token 2>/dev/null)")"

echo "Testing admin_token_generation rotation (1 -> 2)..."
set_option '{"admin_token_generation":2}'
docker restart influxdb2-test-a >/dev/null
wait_healthy_url && ok "container healthy after gen-2 restart" || bad "container healthy after gen-2 restart"
GEN2_TOKEN=""
for _ in $(seq 1 30); do
    GEN2_TOKEN=$(docker exec influxdb2-test-a cat /data/secrets/admin_token 2>/dev/null)
    [ -n "$GEN2_TOKEN" ] && [ "$(hash "$GEN2_TOKEN")" != "$GEN1_HASH" ] && break
    sleep 1
done
GEN2_HASH=$(hash "$GEN2_TOKEN")
[ "$GEN2_HASH" != "$GEN1_HASH" ] && ok "admin_token changed again for generation 2" || bad "admin_token changed again for generation 2"
expect "admin_token_generation recorded as 2" "2" "$(docker exec influxdb2-test-a cat /data/secrets/admin_token_generation 2>/dev/null)"
expect "new (gen 2) operator token works" "200" "$(code "$URL/api/v2/buckets" -H "Authorization: Token $GEN2_TOKEN")"
expect "gen 1 operator token now 401" "401" "$(code "$URL/api/v2/buckets" -H "Authorization: Token $GEN1_TOKEN")"
expect "ha_token still unchanged after second rotation" "$HA_TOKEN" "$(docker exec influxdb2-test-a cat /data/secrets/ha_token)"

# All later steps (backup/restore) use the currently-valid operator token.
ADMIN_TOKEN="$GEN2_TOKEN"

echo "=== Multi-container rotation scenario (the real upgrade path) ==="
# Reproduces the 0.1.1 bug: container A does first-run setup (which is
# also when the influx CLI's own config gets written, inside *that*
# container's filesystem, not /data); every following container here is
# brand-new, never restarted, so none of them has that CLI config - only
# /data is shared. Separate port/data dir from the influxdb2-test-a
# scenario above.
PORT_B=$((HOST_PORT + 1))
URL_B="http://127.0.0.1:$PORT_B"
mkdir -p "$TMP/multi/data"

echo "Starting container B (first-run setup)..."
docker run -d --name influxdb2-test-b --memory=700m \
    -p "127.0.0.1:$PORT_B:8086" \
    -v "$TMP/multi/data:/data" \
    "$IMG" >/dev/null
wait_health_at "$URL_B" && ok "container B became healthy" || bad "container B became healthy"
docker logs influxdb2-test-b 2>&1 | grep -q "first run: generating admin credentials" \
    && ok "container B ran first-run setup" || bad "container B ran first-run setup"

MULTI_GEN0_TOKEN=$(docker exec influxdb2-test-b cat /data/secrets/admin_token 2>/dev/null)
MULTI_GEN0_HASH=$(hash "$MULTI_GEN0_TOKEN")
MULTI_HA_TOKEN=""
for _ in $(seq 1 30); do
    MULTI_HA_TOKEN=$(docker exec influxdb2-test-b cat /data/secrets/ha_token 2>/dev/null)
    [ -n "$MULTI_HA_TOKEN" ] && break
    sleep 1
done
[ -n "$MULTI_HA_TOKEN" ] && ok "container B created ha_token" || bad "container B created ha_token"

echo "Removing container B, writing admin_token_generation=1 to /data, starting container C (a NEW container)..."
docker rm -f influxdb2-test-b >/dev/null
printf '{"admin_token_generation":1}' >"$TMP/multi/data/options.json"
docker run -d --name influxdb2-test-c --memory=700m \
    -p "127.0.0.1:$PORT_B:8086" \
    -v "$TMP/multi/data:/data" \
    "$IMG" >/dev/null
wait_health_at "$URL_B" && ok "container C became healthy" || bad "container C became healthy"

MULTI_GEN1_TOKEN=$(wait_new_token_at influxdb2-test-c "$MULTI_GEN0_HASH")
MULTI_GEN1_HASH=$(hash "$MULTI_GEN1_TOKEN")
[ "$MULTI_GEN1_HASH" != "$MULTI_GEN0_HASH" ] \
    && ok "rotation to generation 1 succeeded in a new container (admin_token changed)" \
    || bad "rotation to generation 1 succeeded in a new container (admin_token changed)"
expect "container C: admin_token_generation recorded as 1" "1" "$(docker exec influxdb2-test-c cat /data/secrets/admin_token_generation 2>/dev/null)"
expect "container C: new (gen 1) operator token works" "200" "$(code "$URL_B/api/v2/buckets" -H "Authorization: Token $MULTI_GEN1_TOKEN")"
expect "container C: old (gen 0) operator token now 401" "401" "$(code "$URL_B/api/v2/buckets" -H "Authorization: Token $MULTI_GEN0_TOKEN")"
expect "container C: ha_token unchanged by rotation" "$MULTI_HA_TOKEN" "$(docker exec influxdb2-test-c cat /data/secrets/ha_token)"
expect "container C: ha_token still writes after rotation" "204" "$(code -XPOST "$URL_B/api/v2/write?org=household&bucket=homeassistant&precision=s" \
    -H "Authorization: Token $MULTI_HA_TOKEN" --data-raw 'sensor,entity_id=multi1 state="on",value=1')"
# `--` before each pattern: a server-generated token can start with `-`
# (base64url alphabet), which would otherwise be read as a grep option.
docker logs influxdb2-test-c 2>&1 | grep -qiF -- "$MULTI_GEN1_TOKEN" && bad "container C logs never contain a token value" \
    || { docker logs influxdb2-test-c 2>&1 | grep -qiF -- "$MULTI_GEN0_TOKEN" && bad "container C logs never contain a token value" \
    || ok "container C logs never contain a token value"; }

echo "Removing container C, generation still 1 (no-op), starting container D (a NEW container)..."
docker rm -f influxdb2-test-c >/dev/null
docker run -d --name influxdb2-test-d --memory=700m \
    -p "127.0.0.1:$PORT_B:8086" \
    -v "$TMP/multi/data:/data" \
    "$IMG" >/dev/null
wait_health_at "$URL_B" && ok "container D became healthy" || bad "container D became healthy"
sleep 2
expect "container D: admin_token unchanged (generation 1 is a no-op in a new container)" \
    "$MULTI_GEN1_HASH" "$(hash "$(docker exec influxdb2-test-d cat /data/secrets/admin_token 2>/dev/null)")"

echo "Removing container D, writing admin_token_generation=2, starting container E (a NEW container)..."
docker rm -f influxdb2-test-d >/dev/null
printf '{"admin_token_generation":2}' >"$TMP/multi/data/options.json"
docker run -d --name influxdb2-test-e --memory=700m \
    -p "127.0.0.1:$PORT_B:8086" \
    -v "$TMP/multi/data:/data" \
    "$IMG" >/dev/null
wait_health_at "$URL_B" && ok "container E became healthy" || bad "container E became healthy"

MULTI_GEN2_TOKEN=$(wait_new_token_at influxdb2-test-e "$MULTI_GEN1_HASH")
MULTI_GEN2_HASH=$(hash "$MULTI_GEN2_TOKEN")
[ "$MULTI_GEN2_HASH" != "$MULTI_GEN1_HASH" ] \
    && ok "rotation to generation 2 succeeded in another new container" \
    || bad "rotation to generation 2 succeeded in another new container"
expect "container E: admin_token_generation recorded as 2" "2" "$(docker exec influxdb2-test-e cat /data/secrets/admin_token_generation 2>/dev/null)"
expect "container E: new (gen 2) operator token works" "200" "$(code "$URL_B/api/v2/buckets" -H "Authorization: Token $MULTI_GEN2_TOKEN")"
expect "container E: gen 1 operator token now 401" "401" "$(code "$URL_B/api/v2/buckets" -H "Authorization: Token $MULTI_GEN1_TOKEN")"
expect "container E: ha_token still unchanged after second rotation" "$MULTI_HA_TOKEN" "$(docker exec influxdb2-test-e cat /data/secrets/ha_token)"

echo "Deleting ha_token, removing container E, starting container F (a NEW container) - upgrade path for ha_token too..."
docker exec influxdb2-test-e rm -f /data/secrets/ha_token
docker rm -f influxdb2-test-e >/dev/null
docker run -d --name influxdb2-test-f --memory=700m \
    -p "127.0.0.1:$PORT_B:8086" \
    -v "$TMP/multi/data:/data" \
    "$IMG" >/dev/null
wait_health_at "$URL_B" && ok "container F became healthy" || bad "container F became healthy"
MULTI_HA_TOKEN_2=""
for _ in $(seq 1 30); do
    MULTI_HA_TOKEN_2=$(docker exec influxdb2-test-f cat /data/secrets/ha_token 2>/dev/null)
    [ -n "$MULTI_HA_TOKEN_2" ] && break
    sleep 1
done
[ -n "$MULTI_HA_TOKEN_2" ] && ok "ensure_ha_token re-created ha_token in a new container" || bad "ensure_ha_token re-created ha_token in a new container"
expect "container F: new ha_token writes" "204" "$(code -XPOST "$URL_B/api/v2/write?org=household&bucket=homeassistant&precision=s" \
    -H "Authorization: Token $MULTI_HA_TOKEN_2" --data-raw 'sensor,entity_id=multi2 state="on",value=2')"

docker rm -f influxdb2-test-f >/dev/null 2>&1

echo "Backing up over HTTP from outside the container (operator token)..."
mkdir -p "$TMP/backup"
BACKUP_OUT=$(docker run --rm -v "$TMP/backup:/backup" --network host "$IMG" \
    influx backup /backup -t "$ADMIN_TOKEN" --host "$URL" 2>&1)
echo "$BACKUP_OUT"
echo "$BACKUP_OUT" | grep -q "Backing up TSM" && ok "remote backup over HTTP succeeded" || bad "remote backup over HTTP succeeded"

echo "Restoring into a throwaway container..."
mkdir -p "$TMP/restore-data"
docker run -d --name influxdb2-test-restore \
    -v "$TMP/restore-data:/var/lib/influxdb2" \
    --entrypoint /entrypoint.sh \
    "$IMG" >/dev/null
RIP=""
for _ in $(seq 1 60); do
    RIP=$(docker inspect influxdb2-test-restore -f '{{(index .NetworkSettings.Networks "bridge").IPAddress}}')
    if [ -n "$RIP" ] && curl -s "http://$RIP:8086/health" 2>/dev/null | grep -q '"status":"pass"'; then
        break
    fi
    sleep 1
done
docker exec influxdb2-test-restore influx setup --host http://localhost:8086 --force \
    --username admin --password "Restoretest12345678" --org household --bucket homeassistant \
    --token "$ADMIN_TOKEN" >/dev/null 2>&1
RESTORE_OUT=$(docker run --rm -v "$TMP/backup:/backup:ro" --network "container:influxdb2-test-restore" "$IMG" \
    influx restore --host http://localhost:8086 -t "$ADMIN_TOKEN" --full /backup 2>&1)
echo "$RESTORE_OUT"
echo "$RESTORE_OUT" | grep -q "Restoring TSM snapshot" && ok "restore ran" || bad "restore ran"

SRC_COUNT=$(curl -s -XPOST "$URL/api/v2/query?org=household" \
    -H "Authorization: Token $ADMIN_TOKEN" -H "Content-Type: application/vnd.flux" \
    --data-raw 'from(bucket:"homeassistant") |> range(start:-1h) |> filter(fn:(r)=>r._measurement=="sensor" and r._field=="value") |> count()' \
    | grep -o ',[0-9]*,value,' | head -1 | tr -d ',value')
DST_COUNT=$(curl -s -XPOST "http://$RIP:8086/api/v2/query?org=household" \
    -H "Authorization: Token $ADMIN_TOKEN" -H "Content-Type: application/vnd.flux" \
    --data-raw 'from(bucket:"homeassistant") |> range(start:-1h) |> filter(fn:(r)=>r._measurement=="sensor" and r._field=="value") |> count()' \
    | grep -o ',[0-9]*,value,' | head -1 | tr -d ',value')
expect "restored point count matches source" "$SRC_COUNT" "$DST_COUNT"

echo "$FAILS failed"
exit "$FAILS"
