#! /bin/bash

if ! PGPASSWORD=taiga psql -U taiga -h local-postgres-taiga -c 'select username from users_user;' | grep taiga_admin
then
  PGPASSWORD=taiga psql -U taiga -h local-postgres-taiga < /taiga.sql
fi

