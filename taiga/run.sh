# bashio::log.info "Starting Taiga backend."

# Scale-control env vars, exported (and whitelisted through `su -w`) by
# start.sh from the add-on options; fall back to the same defaults as
# config.yaml in case this is ever invoked without start.sh (e.g. in tests).
GUNICORN_DJANGO_WORKERS="${GUNICORN_DJANGO_WORKERS:-2}"
GUNICORN_DJANGO_THREADS="${GUNICORN_DJANGO_THREADS:-2}"
GUNICORN_DJANGO_MAX_REQUESTS="${GUNICORN_DJANGO_MAX_REQUESTS:-500}"
CELERY_ENABLED="${CELERY_ENABLED:-true}"
CELERY_CONCURRENCY="${CELERY_CONCURRENCY:-1}"
CELERY_POOL="${CELERY_POOL:-solo}"
TAIGA_PROTECTED_WORKERS="${TAIGA_PROTECTED_WORKERS:-1}"
EVENTS_ENABLED="${EVENTS_ENABLED:-true}"

# Start the taiga backend. --worker-class gthread makes --threads take
# effect (gunicorn's default sync worker class ignores it); --max-requests
# with ~10% jitter staggers worker respawns instead of a thundering herd.
# 0 disables --max-requests entirely.
cd /home/taiga/taiga-back
GUNICORN_DJANGO_ARGS=(--workers "$GUNICORN_DJANGO_WORKERS" --worker-class gthread --threads "$GUNICORN_DJANGO_THREADS" --timeout 60 --log-level=info --access-logfile - --bind 0.0.0.0:8001)
if [ "$GUNICORN_DJANGO_MAX_REQUESTS" != "0" ]; then
  GUNICORN_DJANGO_ARGS+=(--max-requests "$GUNICORN_DJANGO_MAX_REQUESTS" --max-requests-jitter "$((GUNICORN_DJANGO_MAX_REQUESTS / 10))")
fi
PYTHONUNBUFFERED=true DJANGO_SETTINGS_MODULE=settings.config /home/taiga/taiga-back/.venv/bin/gunicorn "${GUNICORN_DJANGO_ARGS[@]}" taiga.wsgi &

# Start taiga async, unless disabled. -B keeps beat (periodic tasks: the
# notification-email digest, telemetry, expired-token cleanup) embedded in
# the worker process; this is compatible with every --pool choice (Celery's
# own docs only call embedded beat unsuitable for *multiple* worker nodes,
# which does not apply here - see CHANGELOG for the citation). When
# celery_enabled is false, Django itself also sees CELERY_ENABLED=False
# (backend_config.py), so webhook delivery and timeline/activity writes run
# synchronously in-request instead of being silently dropped.
if [ "$CELERY_ENABLED" = "true" ]; then
  PYTHONUNBUFFERED=true DJANGO_SETTINGS_MODULE=settings.config /home/taiga/taiga-back/.venv/bin/celery -A taiga.celery worker -B --concurrency "$CELERY_CONCURRENCY" --pool "$CELERY_POOL" -l INFO &
fi

# Start taiga protected (which serves user-uploaded media securely)
cd /home/taiga/taiga-protected
PYTHONUNBUFFERED=true /home/taiga/taiga-protected/.venv/bin/gunicorn --workers "$TAIGA_PROTECTED_WORKERS" --timeout 60 --log-level=info --access-logfile - --bind 0.0.0.0:8003 server:app &

# Start the events service which keeps each open webpage in sync, unless
# disabled. This only stops live WS push to open browser tabs (a manual
# refresh still shows changes); it does not touch RabbitMQ itself, which
# Django's own event publishing (independent of Celery) still needs.
if [ "$EVENTS_ENABLED" = "true" ]; then
  cd /home/taiga/taiga-events
  npm run start:production &
fi

wait
