# See the repository edmundhighcock/hass-taiga
# in the folder build to see what is in this image
FROM edmundhighcock/hass-taiga:withjq

USER root

# RUN apt-get update && apt-get install -y vim-tiny
USER taiga
COPY backend_config.py /home/taiga/taiga-back/settings/config.py
COPY frontend-conf.json /home/taiga/taiga-front-dist/dist/conf.json

USER root
COPY start.sh /start.sh
COPY migrate.sh /migrate.sh
COPY run.sh /run.sh
COPY init_postgres.sh /init_postgres.sh
COPY backup.sh /backup.sh
COPY taiga.sql /taiga.sql
COPY nginx.conf /etc/nginx/conf.d/taiga.conf
RUN chown taiga /migrate.sh /run.sh /init_postgres.sh /taiga.sql /backup.sh
CMD bash /start.sh
# CMD sleep 200d
# USER taiga
