#!/bin/bash
# Local test script for the postgres addon password sync feature.
# Builds the Docker image and tests password setting, changing, and backwards compat.
# Requires: docker
#
# Usage: bash postgres/test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE_NAME="postgres-addon-test"
CONTAINER_NAME="postgres-addon-test"
TEST_DIR="/tmp/addon-test-$$"
TIMEOUT=30

passed=0
failed=0

cleanup() {
  echo ""
  echo "=== Cleaning up ==="
  docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
  rm -rf "$TEST_DIR" 2>/dev/null || true
}
trap cleanup EXIT

assert_eq() {
  local desc="$1" expected="$2" actual="$3"
  if [[ "$expected" == "$actual" ]]; then
    echo "  PASS: $desc"
    ((passed++))
  else
    echo "  FAIL: $desc (expected='$expected', actual='$actual')"
    ((failed++))
  fi
}

assert_neq() {
  local desc="$1" not_expected="$2" actual="$3"
  if [[ "$not_expected" != "$actual" ]]; then
    echo "  PASS: $desc"
    ((passed++))
  else
    echo "  FAIL: $desc (should not equal '$not_expected')"
    ((failed++))
  fi
}

assert_contains() {
  local desc="$1" needle="$2" haystack="$3"
  if echo "$haystack" | grep -q "$needle"; then
    echo "  PASS: $desc"
    ((passed++))
  else
    echo "  FAIL: $desc (expected to contain '$needle')"
    ((failed++))
  fi
}

wait_for_log() {
  local pattern="$1"
  for i in $(seq 1 "$TIMEOUT"); do
    if docker logs "$CONTAINER_NAME" 2>&1 | grep -q "$pattern"; then
      return 0
    fi
    sleep 1
  done
  echo "  FAIL: log pattern '$pattern' not found in ${TIMEOUT}s"
  ((failed++))
  return 1
}

wait_for_ready() {
  # Wait for pg_isready inside the container (avoids temp-server race on first start)
  for i in $(seq 1 "$TIMEOUT"); do
    if docker exec "$CONTAINER_NAME" su postgres -c 'pg_isready -q' 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "  FAIL: postgres not ready in ${TIMEOUT}s"
  ((failed++))
  return 1
}

get_password_hash() {
  docker exec "$CONTAINER_NAME" su postgres -c \
    "psql -U taiga -t -A -c \"SELECT passwd FROM pg_shadow WHERE usename='taiga';\"" 2>/dev/null || echo ""
}

start_container() {
  docker run -d --name "$CONTAINER_NAME" \
    -v "$TEST_DIR/data:/data" \
    "$IMAGE_NAME" >/dev/null
}

stop_container() {
  docker stop "$CONTAINER_NAME" >/dev/null 2>&1
  docker rm "$CONTAINER_NAME" >/dev/null 2>&1
}

set_options() {
  local json="$1"
  echo "$json" > "$TEST_DIR/data/options.json"
}

# ============================================================
echo "=== Building Docker image ==="
if ! docker build -q -t "$IMAGE_NAME" "$SCRIPT_DIR"; then
  echo "FAIL: Docker build failed"
  exit 1
fi
echo "  Build complete."

mkdir -p "$TEST_DIR/data"

# ============================================================
echo ""
echo "=== Test 1: Fresh start with password ==="
set_options '{"password": "first_pass"}'
start_container

# On first start, wait for init to complete then the real server to be ready
if ! wait_for_log "PostgreSQL init process complete"; then
  echo "Skipping remaining tests"
  exit 1
fi
# Wait a moment for the real server to start after init
if ! wait_for_ready; then
  echo "Skipping remaining tests"
  exit 1
fi

LOGS=$(docker logs "$CONTAINER_NAME" 2>&1)
assert_contains "Logs show initializing" "Initializing new database" "$LOGS"

HASH1=$(get_password_hash)
assert_neq "Password hash is set (not empty)" "" "$HASH1"

stop_container

# ============================================================
echo ""
echo "=== Test 2: Restart with same password (no-op sync) ==="
set_options '{"password": "first_pass"}'
start_container
if ! wait_for_log "Password synchronized"; then
  echo "Skipping remaining tests"
  exit 1
fi

HASH2=$(get_password_hash)
assert_eq "Password hash unchanged after no-op sync" "$HASH1" "$HASH2"

stop_container

# ============================================================
echo ""
echo "=== Test 3: Password change on restart ==="
set_options '{"password": "second_pass"}'
start_container
if ! wait_for_log "Password synchronized"; then
  echo "Skipping remaining tests"
  exit 1
fi

HASH3=$(get_password_hash)
assert_neq "Password hash changed after password update" "$HASH1" "$HASH3"

stop_container

# ============================================================
echo ""
echo "=== Test 4: Backwards compat with initial_password ==="
set_options '{"initial_password": "third_pass"}'
start_container
if ! wait_for_log "Password synchronized"; then
  echo "Skipping remaining tests"
  exit 1
fi

HASH4=$(get_password_hash)
assert_neq "Password hash changed with initial_password field" "$HASH3" "$HASH4"

stop_container

# ============================================================
echo ""
echo "=== Test 5: Rejects default placeholder password ==="
set_options '{"password": "pleasechange"}'
start_container
sleep 5
STATUS=$(docker inspect -f '{{.State.Running}}' "$CONTAINER_NAME" 2>/dev/null || echo "false")
assert_eq "Container exited with placeholder password" "false" "$STATUS"

LOGS=$(docker logs "$CONTAINER_NAME" 2>&1)
assert_contains "Logs show abort message" "Please set the password" "$LOGS"

docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1

# ============================================================
echo ""
echo "=== Test 6: Clean shutdown via SIGINT ==="
set_options '{"password": "shutdown_test"}'
start_container
if ! wait_for_log "Password synchronized"; then
  echo "Skipping test 6"
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1
else
  docker kill --signal=SIGINT "$CONTAINER_NAME" >/dev/null
  sleep 5
  LOGS=$(docker logs "$CONTAINER_NAME" 2>&1)
  assert_contains "Clean shutdown message in logs" "database system is shut down" "$LOGS"
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1
fi

# ============================================================
echo ""
echo "==========================================="
echo "Results: $passed passed, $failed failed"
echo "==========================================="

if [[ $failed -gt 0 ]]; then
  exit 1
fi
