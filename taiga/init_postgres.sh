#! /bin/bash

# Check we can get to the database
if ! PGPASSWORD="$POSTGRES_PASSWORD" psql "$POSTGRES_DB" -U "$POSTGRES_USER" -h "$POSTGRES_HOST" -c ''
then
  # We assume that if this first command fails we need to create the database
  # If in fact the problem was that the host, username, password are wrong etc this
  # will fail too, but it won't make things worse.
  PGPASSWORD="$POSTGRES_PASSWORD" psql -U "$POSTGRES_USER" -h "$POSTGRES_HOST" <<EOF
CREATE DATABASE $POSTGRES_DB;
GRANT ALL PRIVILEGES ON DATABASE "$POSTGRES_DB" to $POSTGRES_USER;
EOF
fi

# If we don't have the taiga_admin user then we haven't imported the initial database
if ! PGPASSWORD="$POSTGRES_PASSWORD" psql "$POSTGRES_DB" -U "$POSTGRES_USER" -h "$POSTGRES_HOST" -c 'select username from users_user;' | grep taiga_admin
then
  PGPASSWORD="$POSTGRES_PASSWORD" psql "$POSTGRES_DB" -U "$POSTGRES_USER" -h "$POSTGRES_HOST" < /taiga.sql
fi

