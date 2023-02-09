# taiga-postgres

A postgres database addon for home assistant

It is configured to have a single database and user with no UI. It is also not the latest version of postgres... it is the latest that taiga is compatible with.

If you would like an up-to-date postgres addon, consider [this timescaledb addon](https://github.com/Expaso/hassos-addons/tree/master/timescaledb) which includes postgres and a UI for managing it.

## Setting the initial password

Before starting for the first time, it is essential to set the initial password in the configuration. The addon will abort until you do.

## Changing the password

This is not currently supported from the configuration. It is necessary to use the terminal (i.e. the core ssh addon) and install `psql`. Instructions will be posted here soon.
