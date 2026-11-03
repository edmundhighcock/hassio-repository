#!/bin/bash
# Full, consistent influx backup into $BACKUP_ROOT/<timestamp>/, then prune
# to the last $BACKUP_KEEP. Safe to run directly (used by run.sh's nightly
# loop, and by tests), as long as influxd is already up and the admin
# token file exists.
#
#   BACKUP_ROOT   addon_config-mapped folder to write into (default /config/backups)
#   SECRETS_DIR   where admin_token lives (default /data/secrets)
#   BACKUP_KEEP   how many timestamped backups to keep (default 2)
set -eu

BACKUP_ROOT="${BACKUP_ROOT:-/config/backups}"
SECRETS_DIR="${SECRETS_DIR:-/data/secrets}"
BACKUP_KEEP="${BACKUP_KEEP:-2}"

log() { echo "[influxdb2-backup] $*"; }

TOKEN_FILE="$SECRETS_DIR/admin_token"
if [ ! -s "$TOKEN_FILE" ]; then
    log "no admin token at $TOKEN_FILE yet (setup not finished?) - skipping"
    exit 1
fi
TOKEN="$(cat "$TOKEN_FILE")"

mkdir -p "$BACKUP_ROOT"
TS="$(date -u +%Y-%m-%dT%H%M%SZ)"
DEST="$BACKUP_ROOT/$TS"

log "backing up to $DEST"
if ! influx backup "$DEST" -t "$TOKEN" --host http://localhost:8086; then
    log "backup command failed, removing partial $DEST"
    rm -rf "$DEST"
    exit 1
fi
log "backup OK"

# Prune: keep the BACKUP_KEEP most recent timestamped directories.
# shellcheck disable=SC2012
count=0
for dir in $(ls -1dt "$BACKUP_ROOT"/*/ 2>/dev/null); do
    count=$((count + 1))
    if [ "$count" -gt "$BACKUP_KEEP" ]; then
        log "pruning old backup $dir"
        rm -rf "$dir"
    fi
done
