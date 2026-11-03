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

cleanup() {
    docker rm -f influxdb2-test-a influxdb2-test-restore >/dev/null 2>&1
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
