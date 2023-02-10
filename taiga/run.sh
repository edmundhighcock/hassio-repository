# bashio::log.info "Starting Taiga backend."
# Start the taiga backend
cd /home/taiga/taiga-back
PYTHONUNBUFFERED=true DJANGO_SETTINGS_MODULE=settings.config /home/taiga/taiga-back/.venv/bin/gunicorn --workers 4 --timeout 60 --log-level=info --access-logfile - --bind 0.0.0.0:8001 taiga.wsgi &

# Start taiga async
PYTHONUNBUFFERED=true DJANGO_SETTINGS_MODULE=settings.config /home/taiga/taiga-back/.venv/bin/celery -A taiga.celery worker -B --concurrency 4 -l INFO &

# Start taiga protected (which serves user-uploaded media securely)
PYTHONUNBUFFERED=true /home/taiga/taiga-protected/.venv/bin/gunicorn --workers 4 --timeout 60 --log-level=info --access-logfile - --bind 0.0.0.0:8003 server:app &

# Start the events service which keeps each open webpage in sync
cd /home/taiga/taiga-events
npm run start:production &

wait
