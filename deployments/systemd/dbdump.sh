#!/usr/bin/env bash
# Nightly dump of the control-plane database (devices, selections, audit,
# the sealed Key Vault copies). Kept 14 days on the server and, when
# STOWLINE_DBDUMP_REMOTE is set in dbdump.env (an rclone destination such as
# mydrive:stowline-db-backups), copied there so losing the server loses nothing.
set -euo pipefail
dir=/opt/stowline/var/pgdump
mkdir -p "$dir"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
out="$dir/stowline-control-$stamp.dump"
PGPASSWORD="$STOWLINE_DB_PASSWORD" pg_dump -h 127.0.0.1 -U stowline -d stowline -Fc -f "$out.tmp"
mv "$out.tmp" "$out"
find "$dir" -name 'stowline-control-*.dump' -mtime +14 -delete
if [[ -n "${STOWLINE_DBDUMP_REMOTE:-}" ]]; then
  rclone copyto "$out" "$STOWLINE_DBDUMP_REMOTE/$(basename "$out")"
fi
echo "dumped $(basename "$out") ($(stat -c %s "$out") bytes)"
