#!/bin/bash

echo "Starting Postgres"

# Read password from config - try new field name first, fall back to old name
CONFIGURED_PASSWORD=$(cat /data/options.json | jq -r '.password // .initial_password')

if [[ -z "$CONFIGURED_PASSWORD" || "$CONFIGURED_PASSWORD" == "pleasechange" || "$CONFIGURED_PASSWORD" == "null" ]]; then
  echo "Please set the password in the configuration! Aborting."
  exit 1
fi

export POSTGRES_PASSWORD="$CONFIGURED_PASSWORD"

# Prepare data directory
mkdir -p /data/postgres
chown postgres /data/postgres

# Create symlink only if it doesn't already exist
if ! [[ -L /var/lib/postgresql/data ]]; then
  ln -s /data/postgres /var/lib/postgresql/data
fi
chown postgres:postgres /var/lib/postgresql/data

if [[ -f /data/postgres/PG_VERSION ]]; then
  # Existing database — start in background, sync password, then wait.
  # On restarts, docker-entrypoint.sh just execs postgres (no temp server),
  # so pg_isready reliably detects the real server.
  su postgres -c 'docker-entrypoint.sh postgres' &
  PG_PID=$!

  trap "kill -INT $PG_PID 2>/dev/null; wait $PG_PID; exit" SIGINT SIGTERM

  echo "Waiting for PostgreSQL to start..."
  for i in $(seq 1 30); do
    if su postgres -c 'pg_isready -q'; then
      break
    fi
    if ! kill -0 $PG_PID 2>/dev/null; then
      echo "PostgreSQL process exited unexpectedly."
      exit 1
    fi
    sleep 1
  done

  if ! su postgres -c 'pg_isready -q'; then
    echo "PostgreSQL did not become ready in time. Aborting."
    kill $PG_PID 2>/dev/null
    exit 1
  fi

  # Sync the password from config to the database.
  # Connects as DB user 'taiga' (the superuser created by POSTGRES_USER=taiga).
  # Uses trust authentication over local unix socket.
  ESCAPED_PASSWORD=$(echo "$CONFIGURED_PASSWORD" | sed "s/'/''/g")
  su postgres -c "psql -U taiga -c \"ALTER USER taiga WITH PASSWORD '${ESCAPED_PASSWORD}';\""

  if [[ $? -eq 0 ]]; then
    echo "Password synchronized from configuration."
  else
    echo "Warning: Failed to synchronize database password."
  fi

  wait $PG_PID
else
  # First start — docker-entrypoint.sh handles password via POSTGRES_PASSWORD
  # env var during initdb. No need for ALTER USER.
  echo "Initializing new database..."
  su postgres -c 'docker-entrypoint.sh postgres'
fi
