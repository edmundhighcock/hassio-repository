#!/bin/sh
# Entry point for the netconsole_receiver add-on.
#
# The Go binary is exec'd directly as PID 1 (no s6 services directory),
# as in virtual_sensors. The Go soft memory limit is set by the binary
# itself from `go_memlimit_mib` in /data/options.json, so this script needs
# neither jq nor bashio.
set -eu
exec /usr/bin/netconsole_receiver
