#!/bin/bash
# Container-level test of the netconsole_receiver image under plain docker:
# no Supervisor, no Home Assistant. A user-defined docker network with
# fixed addresses stands in for the LAN, so the allow-list is exercised
# with real source addresses:
#   10.213.77.2  the receiver          10.213.77.3  listed sender
#   10.213.77.4  unlisted sender       10.213.77.5  eclipse-mosquitto
# Phase A: no MQTT broker at all. The receiver must keep running, log
#   loudly, write the log lines, drop the stranger, log GAP, and - after the
#   container is REMOVED and re-created on the same /data and /config (an
#   add-on update always creates a new container) - still have its logs and
#   read its options. Also reports RSS after settling and under a burst.
# Phase B: with eclipse-mosquitto: discovery configs, availability, alert
#   event and summary, problem state, and the last-will on a hard kill.
# The Go unit tests are separate: docker run golang:1.27.3-alpine3.24 go test ./...
set -u
cd "$(dirname "$0")/.."
IMG=netconsole-receiver-addon-test
NET=nc-test-net
RX=nc-test-receiver
MQ=nc-test-mosquitto
SUBNET=10.213.77.0/24
RXIP=10.213.77.2
GOOD=10.213.77.3
BAD=10.213.77.4
MQIP=10.213.77.5
BURST=${BURST:-5000}
FAILS=0

ok()  { echo "PASS $*"; }
bad() { echo "FAIL $*"; FAILS=$((FAILS + 1)); }
check() { # check "description" command...
    local d=$1; shift
    if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d"; fi
}

TMP=$(mktemp -d)
cleanup() {
    docker rm -f "$RX" "$MQ" >/dev/null 2>&1
    docker network rm "$NET" >/dev/null 2>&1
    # files written by the container are root-owned
    docker run --rm -v "$TMP:/t" --entrypoint sh "$IMG" -c 'rm -rf /t/*' >/dev/null 2>&1
    rm -rf "$TMP"
    docker rmi "$IMG" >/dev/null 2>&1
}
trap cleanup EXIT

echo "Building image (host arch)..."
docker build -q -t "$IMG" . >/dev/null || { echo "build failed"; exit 99; }
docker network rm "$NET" >/dev/null 2>&1
docker network create --subnet "$SUBNET" "$NET" >/dev/null || { echo "cannot create network $NET"; exit 99; }

mkdir -p "$TMP/data" "$TMP/config"
# The log files are root-owned (0640) inside the container's /config, so read
# them through a throwaway container rather than from the host.
lg() { docker run --rm -v "$TMP/config:/c:ro" --entrypoint sh "$IMG" -c 'cat /c/testbox.log 2>/dev/null'; }

write_options() { # extra json members (leading comma) for options.json
    cat > "$TMP/data/options.json" <<JSON
{"sources":[{"name":"testbox","address":"$GOOD"}],
 "alert_rules":[{"name":"test_alert","regex":"TESTALERT","cooldown_min":1}],
 "log_level":"debug","go_memlimit_mib":24$1}
JSON
}

start_rx() {
    docker run -d --name "$RX" --network "$NET" --ip "$RXIP" --memory=128m \
        -v "$TMP/data:/data" -v "$TMP/config:/config" "$IMG" >/dev/null
}

# send FROM_IP PAYLOAD...: one UDP datagram per argument; PAYLOAD may contain
# printf escapes (\n). bash's /dev/udp is used so no extra tools are needed.
send() {
    local from=$1; shift
    docker run --rm --network "$NET" --ip "$from" --entrypoint bash "$IMG" -c '
        exec 3>/dev/udp/'"$RXIP"'/26666
        for p in "$@"; do printf "$p" >&3; done' _ "$@"
}

# sendml FROM_IP PAYLOAD: ONE datagram whose payload contains newlines (a
# dictionary line, as netconsole userdata does). bash's printf to /dev/udp
# would split at each newline into separate datagrams, so a pipe into
# busybox nc is used: the payload arrives in one read, so one datagram.
sendml() {
    docker run --rm --network "$NET" --ip "$1" --entrypoint bash "$IMG" -c \
        'printf "$1" | nc -u -w1 '"$RXIP"' 26666' _ "$2"
}

rss() { docker exec "$RX" sh -c 'grep -E "VmRSS|VmHWM" /proc/1/status' 2>/dev/null | tr -s ' \n' ' '; }
running() { [ "$(docker inspect -f '{{.State.Running}}' "$RX" 2>/dev/null)" = "true" ]; }

############################ Phase A: no MQTT ############################
echo "== Phase A: no MQTT broker"
write_options ""
start_rx
sleep 3
check "container running after 3s with no MQTT" running
LOG="$(docker logs "$RX" 2>&1)"
check "logs the listen socket and SO_RCVBUF granted" grep -q "SO_RCVBUF requested 4194304 bytes, kernel reports" <<<"$LOG"
check "logs a loud MQTT error and says it retries" grep -qE "ERROR: mqtt not connected.*retrying" <<<"$LOG"

send "$GOOD" '6,100,1000,-;first message'
sendml "$GOOD" '6,101,1001,-;second\n foo=bar'
send "$GOOD" '6,105,1002,-;after a jump'
send "$BAD" '6,1,1,-;from a stranger'
sleep 1
L=$(lg)
check "log file written" test -n "$L"
check "log line has RFC3339 ms UTC time, source ip:port and raw text" \
    grep -Eq "^20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9:]{8}\.[0-9]{3}Z $GOOD:[0-9]+ 6,100,1000,-;first message$" <<<"$L"
check "one datagram is one line (newline escaped)" grep -Fq '6,101,1001,-;second\n foo=bar' <<<"$L"
check "GAP 102..104 logged" grep -Fq ' GAP 102..104' <<<"$L"
check "non-listed source dropped, not logged" bash -c "! grep -q 'from a stranger' <<<'$L'"
check "drop reported in the container log" grep -q "dropping datagram from $BAD" <(docker logs "$RX" 2>&1)
check "container still running after traffic, no MQTT" running

send "$GOOD" '6,3,1003,-;seq went backwards'
sleep 1
L=$(lg)
check "SEQ_RESET logged on a decrease" grep -Fq ' SEQ_RESET 105->3' <<<"$L"

# fragments: raw fragments kept and the joined message logged
send "$GOOD" '6,200,2000,-,ncfrag=0/26;Kernel pa' '6,200,2000,-,ncfrag=9/26;nic - not syncing'
sleep 1
L=$(lg)
check "raw fragments kept in the log" grep -Fq 'ncfrag=0/26;Kernel pa' <<<"$L"
check "fragments reassembled" grep -Fq 'REASSEMBLED seq=200 Kernel panic - not syncing' <<<"$L"

sleep 5
echo "RSS after settling: $(rss)"

echo "-- burst of $BURST datagrams"
docker run --rm --network "$NET" --ip "$GOOD" --entrypoint bash "$IMG" -c '
    exec 3>/dev/udp/'"$RXIP"'/26666
    for ((i=1;i<='"$BURST"';i++)); do
        printf "6,%d,5000,-;burst message %d padding padding padding padding padding" $((1000+i)) $i >&3
    done'
sleep 3
L=$(lg)
BURST_LINES=$(grep -c 'burst message ' <<<"$L")
echo "burst: sent $BURST, logged $BURST_LINES; $(grep -c ' GAP ' <<<"$L") GAP lines in total"
echo "RSS after burst: $(rss)"
check "burst logged (at least 95%)" test "$BURST_LINES" -ge $((BURST * 95 / 100))
check "container still running after burst" running

# ---- recreate: an add-on update always creates a NEW container ----
L=$(lg); BEFORE=$(wc -l <<<"$L")
docker rm -f "$RX" >/dev/null
check "log survives container removal" test -n "$(lg)"
check "options.json survives container removal" test -s "$TMP/data/options.json"
start_rx
sleep 3
check "re-created container running" running
check "re-created container read the surviving options (1 source)" grep -q "sources=1 " <(docker logs "$RX" 2>&1)
send "$GOOD" '6,9000,9000,-;after recreate'
sleep 1
L=$(lg)
check "new container appends to the old log" grep -Fq 'after recreate' <<<"$L"
check "old lines still present after recreate" grep -Fq '6,100,1000,-;first message' <<<"$L"
AFTER=$(wc -l <<<"$L")
check "log only grew ($BEFORE -> $AFTER lines)" test "$AFTER" -gt "$BEFORE"
docker rm -f "$RX" >/dev/null

############################ Phase B: MQTT ############################
echo "== Phase B: eclipse-mosquitto"
if ! docker image inspect eclipse-mosquitto:2 >/dev/null 2>&1 && ! docker pull -q eclipse-mosquitto:2 >/dev/null 2>&1; then
    echo "SKIP phase B: eclipse-mosquitto:2 is not available"
else
    printf 'listener 1883\nallow_anonymous true\n' > "$TMP/mosquitto.conf"
    docker run -d --name "$MQ" --network "$NET" --ip "$MQIP" -v "$TMP/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro" eclipse-mosquitto:2 >/dev/null
    sleep 2
    write_options ",\"mqtt_host\":\"$MQIP\""
    start_rx
    sleep 4
    check "receiver running with MQTT" running
    check "receiver logs the MQTT connection" grep -q "mqttpub: connected to $MQIP:1883 (credentials from options)" <(docker logs "$RX" 2>&1)
    TOPICS=$(docker exec "$MQ" mosquitto_sub -t '#' -v -W 3 2>/dev/null)
    for t in \
        'homeassistant/sensor/netconsole_receiver_testbox/last_message/config' \
        'homeassistant/sensor/netconsole_receiver_testbox/messages/config' \
        'homeassistant/sensor/netconsole_receiver_testbox/sequence_gaps/config' \
        'homeassistant/sensor/netconsole_receiver_testbox/last_alert/config' \
        'homeassistant/binary_sensor/netconsole_receiver_testbox/problem/config' \
        'homeassistant/event/netconsole_receiver_testbox/alert/config' \
        'homeassistant/sensor/netconsole_receiver/dropped_datagrams/config'; do
        check "discovery published: $t" grep -qF "$t " <<<"$TOPICS"
    done
    check "availability online" grep -qF 'netconsole_receiver/status online' <<<"$TOPICS"
    check "event entity lists the rule name" grep -q '"event_types":\["test_alert"\]' <<<"$TOPICS"

    docker exec -d "$MQ" sh -c "mosquitto_sub -t 'netconsole_receiver/testbox/alert_event' -W 100 -v > /tmp/events"
    sleep 1
    send "$GOOD" '6,1,1,-;TESTALERT first' '6,2,2,-;TESTALERT second' '6,3,3,-;TESTALERT third'
    sleep 2
    EV=$(docker exec "$MQ" cat /tmp/events)
    check "alert event fired once for the first match" test "$(grep -c '"kind":"first"' <<<"$EV")" = 1
    check "no event for the matches inside the cooldown window" test "$(grep -c . <<<"$EV")" = 1
    S=$(docker exec "$MQ" mosquitto_sub -t 'netconsole_receiver/testbox/#' -v -W 2 2>/dev/null)
    check "problem binary_sensor ON" grep -qF 'netconsole_receiver/testbox/problem ON' <<<"$S"
    check "last_alert state is rule name and excerpt" grep -qF 'netconsole_receiver/testbox/last_alert test_alert: TESTALERT first' <<<"$S"
    check "last_alert attributes" grep -q 'last_alert_attrs {"count":1,.*"rule":"test_alert"' <<<"$S"
    echo "waiting up to 80s for the cooldown window to close..."
    for _ in $(seq 1 80); do
        grep -q '"kind":"summary"' < <(docker exec "$MQ" cat /tmp/events) && break
        sleep 1
    done
    EV=$(docker exec "$MQ" cat /tmp/events)
    check "ONE summary event with count 3 when the window closed" bash -c "[ \$(grep -c '\"kind\":\"summary\"' <<<'$EV') = 1 ] && grep -q '\"count\":3' <<<'$EV'"
    S=$(docker exec "$MQ" mosquitto_sub -t 'netconsole_receiver/testbox/problem' -v -W 2 2>/dev/null)
    check "problem binary_sensor OFF after the window closed" grep -qF 'netconsole_receiver/testbox/problem OFF' <<<"$S"
    send "$BAD" 'stranger'
    echo "waiting up to 35s for the throttled counter publish..."
    for _ in $(seq 1 35); do
        docker exec "$MQ" mosquitto_sub -t 'netconsole_receiver/dropped_datagrams' -C 1 -W 1 2>/dev/null | grep -qx '[1-9][0-9]*' && break
    done
    D=$(docker exec "$MQ" mosquitto_sub -t 'netconsole_receiver/dropped_datagrams' -C 1 -W 2 2>/dev/null)
    check "dropped_datagrams published (got '$D')" grep -qx '[1-9][0-9]*' <<<"$D"

    docker kill "$RX" >/dev/null
    sleep 2
    AV=$(docker exec "$MQ" mosquitto_sub -t 'netconsole_receiver/status' -C 1 -W 3 2>/dev/null)
    check "last-will: availability offline after the add-on is killed (got '$AV')" test "$AV" = offline
fi

echo "$FAILS failed"
exit "$FAILS"
