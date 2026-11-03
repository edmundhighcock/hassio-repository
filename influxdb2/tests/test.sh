#!/bin/bash
# Local test of the influxdb2 image (amd64 build, run under plain docker -
# no Supervisor involved). See DOCS.md for what each check corresponds to
# in the brief:
#   - build, run with a 700m memory cap and a temp /data
#   - first-run setup, then an idempotent restart
#   - the memory/query flags actually reached the running influxd
#   - a line-protocol write with a string field and a numeric field,
#     read back
#   - a nightly-style backup, restored into a second throwaway container,
#     with a matching point count
# Exit status is the number of failed checks. Everything this script
# creates (containers, image, temp dirs) is removed on exit.
set -u
cd "$(dirname "$0")/.."
IMG=influxdb2-addon-test
TMP=$(mktemp -d)
FAILS=0

ok()  { echo "PASS $*"; }
bad() { echo "FAIL $*"; FAILS=$((FAILS + 1)); }
expect() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (want '$2', got '$3')"; fi; }

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

mkdir -p "$TMP/data-a" "$TMP/config-a"

echo "Starting container (700m memory cap, temp /data)..."
docker run -d --name influxdb2-test-a --memory=700m \
    -v "$TMP/data-a:/data" -v "$TMP/config-a:/config" \
    "$IMG" >/dev/null

# Wait for /health to report pass (first-run setup takes a few seconds).
ready=0
for _ in $(seq 1 60); do
    IP=$(docker inspect influxdb2-test-a -f '{{(index .NetworkSettings.Networks "bridge").IPAddress}}')
    if [ -n "$IP" ] && curl -s "http://$IP:8086/health" 2>/dev/null | grep -q '"status":"pass"'; then
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

TOKEN=$(docker exec influxdb2-test-a cat /data/secrets/admin_token 2>/dev/null)
[ -n "$TOKEN" ] && ok "read back admin token" || bad "read back admin token"

echo "Writing a line-protocol point with a string and a numeric field..."
WRITE_CODE=$(curl -s -o /dev/null -w '%{http_code}' \
    -XPOST "http://$IP:8086/api/v2/write?org=household&bucket=homeassistant&precision=s" \
    -H "Authorization: Token $TOKEN" \
    --data-raw 'sensor,entity_id=test1 state="on",value=42.5')
expect "write accepted" "204" "$WRITE_CODE"

QUERY_OUT=$(curl -s -XPOST "http://$IP:8086/api/v2/query?org=household" \
    -H "Authorization: Token $TOKEN" -H "Content-Type: application/vnd.flux" \
    --data-raw 'from(bucket:"homeassistant") |> range(start:-10m) |> filter(fn:(r)=>r._measurement=="sensor")')
echo "$QUERY_OUT" | grep -q ',"on",state,sensor,test1\|,on,state,sensor,test1' && ok "string field read back" || bad "string field read back"
echo "$QUERY_OUT" | grep -q ',42.5,value,sensor,test1' && ok "numeric field read back" || bad "numeric field read back"

echo "Restarting container (idempotency check)..."
OLD_PASS=$(docker exec influxdb2-test-a cat /data/secrets/admin_password)
OLD_TOKEN=$(docker exec influxdb2-test-a cat /data/secrets/admin_token)
docker restart influxdb2-test-a >/dev/null
ready=0
for _ in $(seq 1 60); do
    if curl -s "http://$IP:8086/health" 2>/dev/null | grep -q '"status":"pass"'; then
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

echo "Running the backup job..."
docker exec influxdb2-test-a /usr/local/bin/influxdb2-backup.sh >/tmp/backup-out.$$ 2>&1
BACKUP_OK=$?
cat /tmp/backup-out.$$
rm -f /tmp/backup-out.$$
[ "$BACKUP_OK" = 0 ] && ok "backup command succeeded" || bad "backup command succeeded"

LATEST_BACKUP=$(ls -1dt "$TMP/config-a/backups"/*/ 2>/dev/null | head -1)
[ -n "$LATEST_BACKUP" ] && ok "backup directory created under addon_config" || bad "backup directory created under addon_config"

# Prune check: a second and third backup should leave only backup_keep=2.
sleep 2
docker exec influxdb2-test-a /usr/local/bin/influxdb2-backup.sh >/dev/null 2>&1
sleep 2
docker exec influxdb2-test-a /usr/local/bin/influxdb2-backup.sh >/dev/null 2>&1
COUNT=$(ls -1d "$TMP/config-a/backups"/*/ 2>/dev/null | wc -l)
expect "backup_keep=2 prunes older backups" "2" "$COUNT"
LATEST_BACKUP=$(ls -1dt "$TMP/config-a/backups"/*/ 2>/dev/null | head -1)

echo "Restoring the latest backup into a throwaway container..."
mkdir -p "$TMP/restore-data"
docker run -d --name influxdb2-test-restore \
    -v "$TMP/restore-data:/var/lib/influxdb2" \
    -v "$LATEST_BACKUP:/backup:ro" \
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
    --token "$TOKEN" >/dev/null 2>&1
RESTORE_OUT=$(docker exec influxdb2-test-restore influx restore --host http://localhost:8086 -t "$TOKEN" --full /backup 2>&1)
echo "$RESTORE_OUT"
echo "$RESTORE_OUT" | grep -q "Restoring TSM snapshot" && ok "restore ran" || bad "restore ran"

SRC_COUNT=$(curl -s -XPOST "http://$IP:8086/api/v2/query?org=household" \
    -H "Authorization: Token $TOKEN" -H "Content-Type: application/vnd.flux" \
    --data-raw 'from(bucket:"homeassistant") |> range(start:-1h) |> filter(fn:(r)=>r._measurement=="sensor" and r._field=="value") |> count()' \
    | grep -o ',[0-9]*,value,' | head -1 | tr -d ',value')
DST_COUNT=$(curl -s -XPOST "http://$RIP:8086/api/v2/query?org=household" \
    -H "Authorization: Token $TOKEN" -H "Content-Type: application/vnd.flux" \
    --data-raw 'from(bucket:"homeassistant") |> range(start:-1h) |> filter(fn:(r)=>r._measurement=="sensor" and r._field=="value") |> count()' \
    | grep -o ',[0-9]*,value,' | head -1 | tr -d ',value')
expect "restored point count matches source" "$SRC_COUNT" "$DST_COUNT"

echo "$FAILS failed"
exit "$FAILS"
