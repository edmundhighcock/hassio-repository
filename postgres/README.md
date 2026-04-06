# taiga-postgres

A postgres database addon for home assistant

It is configured to have a single database and user with no UI. It is also not the latest version of postgres... it is the latest that taiga is compatible with.

If you would like an up-to-date postgres addon, consider [this timescaledb addon](https://github.com/Expaso/hassos-addons/tree/master/timescaledb) which includes postgres and a UI for managing it.

## Setting the password

Before starting for the first time, it is essential to set the password in the configuration. The addon will abort until you do.

## Changing the password

To change the postgres password:

1. Go to the addon configuration in the Home Assistant UI
2. Change the `password` field to the new password
3. Restart the postgres addon
4. Update the `postgres_password` field in the Taiga addon configuration to match
5. Restart the Taiga addon

The password is synchronized to the database on every addon restart.

## Upgrading from a previous version

If you are upgrading from a version that used `initial_password`, the addon will continue to read that field if the new `password` field is not set. However, it is recommended to update your configuration to use the `password` field.
