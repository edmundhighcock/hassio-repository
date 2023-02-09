echo INGRESS_ENTRY is $INGRESS_ENTRY
cd /home/taiga/taiga-back
source .venv/bin/activate
bash /init_postgres.sh
DJANGO_SETTINGS_MODULE=settings.config python manage.py migrate --noinput
# CELERY_ENABLED=False DJANGO_SETTINGS_MODULE=settings.config python manage.py createsuperuser
DJANGO_SETTINGS_MODULE=settings.config python manage.py loaddata initial_project_templates
DJANGO_SETTINGS_MODULE=settings.config python manage.py compilemessages
DJANGO_SETTINGS_MODULE=settings.config python manage.py collectstatic --noinput
