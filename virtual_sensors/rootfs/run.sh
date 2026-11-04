#!/bin/sh
# Entry point for the virtual_sensors add-on.
#
# No s6 services directory is used here - the Go binary is exec'd
# directly as PID 1, same convention as static_sites' own run.sh.
# GOMEMLIMIT (the Go runtime's soft heap limit) is set by the binary
# itself from the `go_memlimit_mib` option in /data/options.json (see
# cmd/virtual_sensors/main.go), not by this script - so this script has
# no need for jq or bashio at all.
set -eu
exec /usr/bin/virtual_sensors
