#!/bin/bash
# Local test of the taiga add-on's start.sh/run.sh option handling.
#   taiga/tests/test.sh
#
# Runs the real start.sh and run.sh, as the real `taiga` user via the real
# `su -w`/`jq` from the upstream base image (edmundhighcock/hass-taiga), with
# a fake /data/options.json per scenario and gunicorn/celery/npm/nginx/curl
# replaced by logging stubs (migrate.sh/backup.sh are also stubbed out: they
# need a real Postgres and are unrelated to the scale-control options this
# test is for). Asserts the exact command lines produced for several option
# combinations. Exit status is the number of failed checks.
set -u
cd "$(dirname "$0")/.."
BASE_IMAGE=edmundhighcock/hass-taiga:withjq
TMP=$(mktemp -d)
FAILS=0
CONTAINER=taiga-test-run

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1; rm -rf "$TMP"; }
trap cleanup EXIT

ok()  { echo "PASS $*"; }
bad() { echo "FAIL $*"; FAILS=$((FAILS + 1)); }
has() { grep -qF -- "$2" "$TMP/calls.log" && ok "$1" || bad "$1 (wanted to find: $2)"; }
hasnt() { grep -qF -- "$2" "$TMP/calls.log" && bad "$1 (did not want: $2)" || ok "$1"; }

# --- stubs, shared across runs -------------------------------------------
mkdir -p "$TMP/bin"
cat > "$TMP/bin/log-stub" <<'EOF'
#!/bin/bash
# Redact the password out of RABBITMQ_URL before it ever reaches the log
# file (which the test harness then cats to stdout) - mirrors the
# redaction used for manual inspection on hass.
REDACTED_RABBITMQ_URL=$(printf '%s' "${RABBITMQ_URL-<unset>}" | sed -E 's#(amqp://[^:]+:)[^@]+@#\1***@#')
echo "CALL: $0 $* | MALLOC_ARENA_MAX=${MALLOC_ARENA_MAX-<unset>} | RABBITMQ_URL=${REDACTED_RABBITMQ_URL}" >> /test-calls.log
EOF
cat > "$TMP/bin/curl-stub" <<'EOF'
#!/bin/bash
# Only ever called for the Supervisor ADDON_INFO lookup in start.sh.
echo '{"data":{"ingress_entry":"/test-entry"}}'
EOF
cat > "$TMP/bin/noop" <<'EOF'
#!/bin/bash
exit 0
EOF
chmod +x "$TMP/bin/log-stub" "$TMP/bin/curl-stub" "$TMP/bin/noop"
touch "$TMP/calls.log"

# --- option fixtures -------------------------------------------------------
base_options() {
  cat <<EOF
{
  "taiga_secret_key": "test-secret",
  "rabbitmq_password": "${RABBITMQ_PASSWORD_OPT:-test-rabbit}",
  "postgres_password": "test-postgres",
  "minutes_between_backups": 61,
  "rabbitmq_advanced_options": {"virtual_host": "taiga", "host": "${RABBITMQ_HOST_OPT:-9547a9e0-rabbitmq}"},
  "postgres_advanced_options": {"host": "local-postgres", "user": "taiga", "database": "taiga"},
  "gunicorn_django_workers": ${GUNICORN_DJANGO_WORKERS:-2},
  "gunicorn_django_threads": ${GUNICORN_DJANGO_THREADS:-2},
  "gunicorn_django_max_requests": ${GUNICORN_DJANGO_MAX_REQUESTS:-500},
  "celery_enabled": ${CELERY_ENABLED:-true},
  "celery_concurrency": ${CELERY_CONCURRENCY:-1},
  "celery_pool": "${CELERY_POOL:-solo}",
  "taiga_protected_workers": ${TAIGA_PROTECTED_WORKERS:-1},
  "events_enabled": ${EVENTS_ENABLED:-true},
  "malloc_arena_max": ${MALLOC_ARENA_MAX:-2}
}
EOF
}

mkdir -p "$TMP/options"
( unset GUNICORN_DJANGO_WORKERS GUNICORN_DJANGO_THREADS GUNICORN_DJANGO_MAX_REQUESTS CELERY_ENABLED CELERY_CONCURRENCY CELERY_POOL TAIGA_PROTECTED_WORKERS EVENTS_ENABLED MALLOC_ARENA_MAX RABBITMQ_HOST_OPT RABBITMQ_PASSWORD_OPT
  base_options ) > "$TMP/options/defaults.json"
( CELERY_ENABLED=false base_options ) > "$TMP/options/celery-off.json"
( EVENTS_ENABLED=false base_options ) > "$TMP/options/events-off.json"
( GUNICORN_DJANGO_WORKERS=4 GUNICORN_DJANGO_THREADS=1 GUNICORN_DJANGO_MAX_REQUESTS=0 \
  CELERY_CONCURRENCY=3 CELERY_POOL=prefork TAIGA_PROTECTED_WORKERS=4 base_options ) > "$TMP/options/workers4-threads1.json"
( MALLOC_ARENA_MAX=0 base_options ) > "$TMP/options/arena0.json"
# Non-default RabbitMQ host, plus a password with amqp URL-reserved
# characters, to check the computed RABBITMQ_URL is built from the option
# (not the base image's broken "local-rabbitmq" default) and that the
# password is percent-encoded rather than leaking into the URL verbatim.
( RABBITMQ_HOST_OPT=custom-rabbitmq RABBITMQ_PASSWORD_OPT='p@ss/word' base_options ) > "$TMP/options/custom-rabbitmq.json"
# Options saved by an install that predates p9 carry no rabbitmq host key.
( base_options ) | jq 'del(.rabbitmq_advanced_options.host)' > "$TMP/options/no-host-key.json"

run_scenario() {  # name, options-file
    local name="$1" optfile="$2"
    rm -f "$TMP/calls.log"; touch "$TMP/calls.log"
    mkdir -p "$TMP/data" "$TMP/share"
    docker run -d --name "$CONTAINER" \
        -v "$optfile:/data/options.json:ro" \
        -v "$TMP/calls.log:/test-calls.log" \
        -v "$TMP/data:/data" \
        -v "$TMP/share:/share" \
        -v "$TMP/bin/noop:/migrate.sh:ro" \
        -v "$TMP/bin/noop:/backup.sh:ro" \
        -v "$TMP/bin/log-stub:/home/taiga/taiga-back/.venv/bin/gunicorn:ro" \
        -v "$TMP/bin/log-stub:/home/taiga/taiga-back/.venv/bin/celery:ro" \
        -v "$TMP/bin/log-stub:/home/taiga/taiga-protected/.venv/bin/gunicorn:ro" \
        -v "$TMP/bin/log-stub:/usr/local/bin/npm:ro" \
        -v "$TMP/bin/log-stub:/usr/local/bin/nginx:ro" \
        -v "$TMP/bin/curl-stub:/usr/local/bin/curl:ro" \
        -v "$(pwd)/start.sh:/start.sh:ro" \
        -v "$(pwd)/run.sh:/run.sh:ro" \
        "$BASE_IMAGE" bash /start.sh >/dev/null
    sleep 3
    docker rm -f "$CONTAINER" >/dev/null 2>&1
    echo "--- $name: calls.log ---"
    cat "$TMP/calls.log"
}

GUNICORN_DJANGO="/home/taiga/taiga-back/.venv/bin/gunicorn"
CELERY="/home/taiga/taiga-back/.venv/bin/celery"
GUNICORN_PROTECTED="/home/taiga/taiga-protected/.venv/bin/gunicorn"
# run.sh invokes npm by bare name (PATH lookup), so argv[0] in the stub's
# log line is "npm", not the resolved /usr/local/bin/npm.
NPM="npm"

echo "== defaults =="
run_scenario defaults "$TMP/options/defaults.json"
has   "defaults: django workers/threads/max-requests" \
      "$GUNICORN_DJANGO --workers 2 --worker-class gthread --threads 2 --timeout 60 --log-level=info --access-logfile - --bind 0.0.0.0:8001 --max-requests 500 --max-requests-jitter 50 taiga.wsgi"
has   "defaults: celery concurrency/pool/beat" \
      "$CELERY -A taiga.celery worker -B --concurrency 1 --pool solo -l INFO"
has   "defaults: protected workers" \
      "$GUNICORN_PROTECTED --workers 1 --timeout 60 --log-level=info --access-logfile - --bind 0.0.0.0:8003 server:app"
has   "defaults: events (npm) started" "$NPM run start:production"
has   "defaults: MALLOC_ARENA_MAX reaches gunicorn via su -w" "MALLOC_ARENA_MAX=2"
has   "defaults: RABBITMQ_URL built from the add-on's rabbitmq host option (not the base image's broken local-rabbitmq)" \
      "RABBITMQ_URL=amqp://taiga:***@9547a9e0-rabbitmq:5672/taiga"
hasnt "defaults: rabbitmq password not logged" "test-rabbit"

echo "== celery off =="
run_scenario celery-off "$TMP/options/celery-off.json"
hasnt "celery off: celery not started" "$CELERY"
has   "celery off: django still started" "$GUNICORN_DJANGO"
has   "celery off: events still started" "$NPM run start:production"

echo "== events off =="
run_scenario events-off "$TMP/options/events-off.json"
hasnt "events off: npm not started" "$NPM"
has   "events off: celery still started" "$CELERY"
has   "events off: django still started" "$GUNICORN_DJANGO"

echo "== workers 4, threads 1 (+ other non-defaults) =="
run_scenario workers4-threads1 "$TMP/options/workers4-threads1.json"
has   "workers4/threads1: django workers/threads" \
      "$GUNICORN_DJANGO --workers 4 --worker-class gthread --threads 1 --timeout 60 --log-level=info --access-logfile - --bind 0.0.0.0:8001 taiga.wsgi"
hasnt "workers4/threads1: max-requests 0 disables the flag" "--max-requests"
has   "workers4/threads1: celery concurrency/pool" \
      "$CELERY -A taiga.celery worker -B --concurrency 3 --pool prefork -l INFO"
has   "workers4/threads1: protected workers" "$GUNICORN_PROTECTED --workers 4"

echo "== malloc_arena_max 0 (unset) =="
run_scenario arena0 "$TMP/options/arena0.json"
has   "arena0: MALLOC_ARENA_MAX left unset" "MALLOC_ARENA_MAX=<unset>"

echo "== custom rabbitmq host + password needing URL-encoding =="
run_scenario custom-rabbitmq "$TMP/options/custom-rabbitmq.json"
has   "custom-rabbitmq: RABBITMQ_URL host follows the option, password redacted" \
      "RABBITMQ_URL=amqp://taiga:***@custom-rabbitmq:5672/taiga"
hasnt "custom-rabbitmq: raw password not logged" "p@ss/word"
hasnt "custom-rabbitmq: raw password not logged (url-decoded form)" "taiga:p@ss"
has   "custom-rabbitmq: events (npm) still started" "$NPM run start:production"

run_scenario no-host-key "$TMP/options/no-host-key.json"
has   "no-host-key: pre-p9 options fall back to the RabbitMQ add-on alias" \
      "RABBITMQ_URL=amqp://taiga:***@9547a9e0-rabbitmq:5672/taiga"
has   "no-host-key: events (npm) still started" "$NPM run start:production"

echo "$FAILS failed"
exit $FAILS
