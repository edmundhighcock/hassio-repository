#!/bin/sh
# Serve /config/sites/<name>/current (a relative symlink to a release
# directory, swapped atomically by the publisher) through Ingress.
# Nothing here writes content.
set -e
mkdir -p /config/sites
for d in /config/sites/*/; do
    [ -d "$d" ] || continue
    n=$(basename "$d")
    if [ -e "$d/current/index.html" ]; then
        echo "[static_sites] $n: serving $(readlink "$d/current")"
    else
        echo "[static_sites] $n: no release published yet"
    fi
done
exec nginx -g 'daemon off;'
