FROM postgres:13.9-alpine as base

FROM scratch
COPY --from=base / /

RUN rm -rf /var/lib/postgresql/data


ENV POSTGRES_DB=taiga
ENV POSTGRES_USER=taiga
ENV POSTGRES_PASSWORD=taiga
ENV PGDATA=/var/lib/postgresql/data

STOPSIGNAL SIGINT

# ENTRYPOINT ["docker-entrypoint.sh"]

EXPOSE 5432
# CMD ["postgres"]

USER root

#CMD bash -c "chown postgres /data  && chown postgres:postgres /var/lib/postgresql/data && su - postgres -c 'PGDATA=/var/lib/postgresql/data docker-entrypoint.sh postgres'"
CMD bash -c "mkdir -p /data/postgres && chown postgres /data/postgres  && ln -s /data/postgres /var/lib/postgresql/data && chown postgres:postgres /var/lib/postgresql/data && su postgres -c 'docker-entrypoint.sh postgres'"
# CMD bash -c "chown postgres /data  && chown postgres:postgres /var/lib/postgresql/data && sleep 200d^"
# CMD bash -c "sleep 200d"


