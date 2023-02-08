#! /bin/bash

if ! PGPASSWORD=taiga psql -U taiga -h 9547a9e0-taiga-postgres -c 'select username from users_user;' | grep taiga_admin
then
  PGPASSWORD=taiga psql -U taiga -h 9547a9e0-taiga-postgres < /taiga.sql
fi

