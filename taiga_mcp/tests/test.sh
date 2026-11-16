#!/bin/bash
# Local test of the taiga_mcp add-on (amd64 image, plain docker).
#   taiga_mcp/tests/test.sh
# 1. builds the add-on image for linux/amd64;
# 2. starts the REAL Taiga test stack from the taiga-mcp checkout
#    (tests/stack/docker-compose.yml, reused in place) and seeds it with that
#    repo's seed.py, exactly as its `make test` does;
# 3. runs the add-on image with a /data/options.json whose taiga_url points at
#    the stack, on the stack's docker network;
# 4. smoke-tests it with a real MCP client over HTTP (smoke.py);
# 5. removes the add-on container, the stack's containers/volumes and the image.
# Needs docker + compose, and a taiga-mcp checkout with its .venv (uv sync).
# Exit status = number of failed checks (99 = harness problem).
set -u
cd "$(dirname "$0")/.."
TAIGA_MCP_DIR=${TAIGA_MCP_DIR:-$HOME/Code/taiga-mcp}
STACK=$TAIGA_MCP_DIR/tests/stack/docker-compose.yml
PY=$TAIGA_MCP_DIR/.venv/bin/python
export TAIGA_TEST_PORT=${TAIGA_TEST_PORT:-18001}
PORT=${PORT:-18095}
IMG=taiga-mcp-addon-test
CT=taiga-mcp-addon-test
NET=taigamcp-test_default
TMP=$(mktemp -d)
compose() { docker compose -f "$STACK" "$@"; }
cleanup() {
    docker rm -f $CT >/dev/null 2>&1
    compose down -v --remove-orphans >/dev/null 2>&1
    docker rmi -f $IMG >/dev/null 2>&1
    rm -rf "$TMP"
}
trap cleanup EXIT
[ -x "$PY" ] && [ -f "$STACK" ] || { echo "need $PY and $STACK"; exit 99; }

echo "== build (linux/amd64)"
docker build --platform linux/amd64 -t $IMG . >"$TMP/build.log" 2>&1 || { tail -30 "$TMP/build.log"; exit 99; }

echo "== start Taiga stack"
compose down -v --remove-orphans >/dev/null 2>&1
compose up -d >/dev/null 2>&1 || { echo "compose up failed"; exit 99; }
for i in $(seq 120); do
    [ "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$TAIGA_TEST_PORT/api/v1/)" = 200 ] && break
    sleep 2
done
[ "$i" -lt 120 ] || { echo "Taiga did not come up"; exit 99; }
compose exec -T back python manage.py shell < "$TAIGA_MCP_DIR/tests/stack/seed.py" > "$TMP/seed.out" 2>&1
sed -n 's/^SEED_RESULT //p' "$TMP/seed.out" > "$TMP/seed.json"
[ -s "$TMP/seed.json" ] || { echo "seeding failed"; tail -20 "$TMP/seed.out"; exit 99; }

echo "== run add-on container"
# Only taiga_url is given: project_id and log_level must fall back to defaults.
echo '{"taiga_url": "http://back:8000"}' > "$TMP/options.json"
chmod 644 "$TMP/options.json"
docker run -d --name $CT --network $NET -p 127.0.0.1:$PORT:8095 \
    -v "$TMP/options.json:/data/options.json:ro" $IMG >/dev/null || exit 99
for i in $(seq 30); do
    [ "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$PORT/health)" = 200 ] && break
    sleep 1
done

FAILS=0
chk() { if [ "$2" = "$3" ]; then echo "PASS $1"; else echo "FAIL $1 (want '$2', got '$3')"; FAILS=$((FAILS+1)); fi; }
chk "server runs as non-root" 10001 "$(docker exec $CT sh -c 'for p in /proc/[0-9]*; do grep -q "run.py" $p/cmdline 2>/dev/null && awk "/^Uid:/{print \$2}" $p/status; done' | head -1)"
chk "startup log shows options" 1 "$(docker logs $CT 2>&1 | grep -c 'Taiga at http://back:8000, default project 1, log level info')"

echo "== MCP smoke test"
"$PY" tests/smoke.py http://127.0.0.1:$PORT "$TMP/seed.json" || FAILS=$((FAILS + $?))
TOK=$("$PY" -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["tokens"]["agent-a"]["token"])' "$TMP/seed.json")
chk "token never in container logs" 0 "$(docker logs $CT 2>&1 | grep -cF "$TOK")"
echo "== teardown, then $FAILS failure(s)"
exit $FAILS
