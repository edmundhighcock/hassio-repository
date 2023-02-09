#!/bin/bash


echo Starting Postgres

export POSTGRES_PASSWORD=$(cat /data/options.json | jq -r .initial_password) 

if [[ "$POSTGRES_PASSWORD" == "pleasechange" ]]
then 
  echo "Please set the password in the configuration! Aborting."
  exit 1
fi

mkdir -p /data/postgres
chown postgres /data/postgres 
ln -s /data/postgres /var/lib/postgresql/data
chown postgres:postgres /var/lib/postgresql/data
su postgres -c 'docker-entrypoint.sh postgres'

