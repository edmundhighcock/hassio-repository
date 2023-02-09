#! /bin/bash

# If we don't have the taiga_admin user then we haven't imported the initial database
if ! PGPASSWORD="$POSTGRES_PASSWORD" psql -U taiga -h 9547a9e0-taiga-postgres -c 'select username from users_user;' | grep taiga_admin
then
  PGPASSWORD="$POSTGRES_PASSWORD" psql -U taiga -h 9547a9e0-taiga-postgres < /taiga.sql
fi

