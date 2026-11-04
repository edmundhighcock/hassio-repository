#!/bin/bash
# Local test of the virtual_sensors image, run under plain docker - no
# Supervisor, no real Home Assistant, no real MQTT broker. See DOCS.md
# for what each check corresponds to:
#   - build the image (host arch; see the add-on's own hand-off report
#     for the arm64 buildx attempt, which needs more time than this
#     script's own short timeouts allow for under qemu emulation)
#   - the container starts and does NOT crash-loop just because the
#     Supervisor/MQTT broker aren't reachable
#   - it logs a clear, loud, specific error about SUPERVISOR_TOKEN and
#     about MQTT credentials, rather than idling silently
#   - it keeps retrying rather than exiting
#   - RSS after a short settle period (reported, not just asserted under
#     some bound, since the pass/fail threshold is a judgement call for
#     the hand-off report, not this script)
# The Go unit tests and the parity test (tests/parity_test.go) are run
# separately with `go test ./...` under the golang:alpine image - see
# the project's own hand-off report for those numbers; this script is
# about the container, not the Go test suite.
set -u
cd "$(dirname "$0")/.."
IMG=virtual_sensors-addon-test
FAILS=0

ok()  { echo "PASS $*"; }
bad() { echo "FAIL $*"; FAILS=$((FAILS + 1)); }

cleanup() {
    docker rm -f virtual_sensors-test >/dev/null 2>&1
    docker rmi "$IMG" >/dev/null 2>&1
}
trap cleanup EXIT

echo "Building image (host arch)..."
docker build -q -t "$IMG" . >/dev/null || { echo "build failed"; exit 99; }

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
echo '{"modules":["hot_water"],"log_level":"info","go_memlimit_mib":24}' > "$TMP/options.json"

echo "Starting container with no Supervisor, no MQTT broker reachable..."
docker run -d --name virtual_sensors-test --memory=128m \
    -v "$TMP:/data" \
    "$IMG" >/dev/null

sleep 5
RUNNING=$(docker inspect -f '{{.State.Running}}' virtual_sensors-test 2>/dev/null)
[ "$RUNNING" = "true" ] && ok "container still running after 5s (not crash-looping)" \
    || bad "container still running after 5s (not crash-looping)"

LOG="$(docker logs virtual_sensors-test 2>&1)"
echo "$LOG" | grep -qi "SUPERVISOR_TOKEN" && ok "logs a clear SUPERVISOR_TOKEN error" \
    || bad "logs a clear SUPERVISOR_TOKEN error"
echo "$LOG" | grep -qi "mqtt" && ok "logs something about MQTT" || bad "logs something about MQTT"
echo "$LOG" | grep -qiE "retry|retrying" && ok "logs that it is retrying" \
    || bad "logs that it is retrying"

sleep 10
STILL_RUNNING=$(docker inspect -f '{{.State.Running}}' virtual_sensors-test 2>/dev/null)
[ "$STILL_RUNNING" = "true" ] && ok "container still running after 15s total (kept retrying, did not give up)" \
    || bad "container still running after 15s total (kept retrying, did not give up)"

echo "Memory stats (RSS):"
docker stats --no-stream virtual_sensors-test

echo "$FAILS failed"
exit "$FAILS"
