cd /home/taiga/taiga-back
source .venv/bin/activate

# PGPASSWORD=postgres psql -U taiga taiga_local -h local-taiga-postgres -c 'select json_agg(slug) from projects_project;' | grep '\[' | jq -r '. | join(",")' | RABBITMQ_VHOST=x RABBITMQ_DEFAULT_PASS=x INGRESS_ENTRY=x TAIGA_SECRET_KEY=pleasechangeme DJANGO_SETTINGS_MODULE=settings.config POSTGRES_HOST=local-taiga-postgres POSTGRES_DB=taiga_local POSTGRES_PASSWORD=postgres POSTGRES_USER=taiga xargs python manage.py dump_project -d /data/.

while true
do
  PGPASSWORD="$POSTGRES_PASSWORD" psql "$POSTGRES_DB" -U "$POSTGRES_USER" -h "$POSTGRES_HOST" -c 'select json_agg(slug) from projects_project;' | grep '\[' | jq -r '. | join(",")' | DJANGO_SETTINGS_MODULE=settings.config xargs python manage.py dump_project -d /share/$HOSTNAME/.
  echo "Backup complete, sleeping for $MINUTES_BETWEEN_BACKUPS minutes."
  sleep "$MINUTES_BETWEEN_BACKUPS"m
done
