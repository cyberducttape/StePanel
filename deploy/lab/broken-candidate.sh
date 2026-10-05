#!/bin/sh
# Deliberately broken release binary for the upgrade-rollback smoke. CLI
# commands the installer runs (setup, hash-password, version) go to the real
# candidate, so installation reaches the point of starting services. Run as
# the panel or worker service, it damages the control-plane database the way
# a bad migration could and exits, so the post-install health check fails and
# the installer must restore binary and database together.
case "${1:-}" in
  '' | worker)
    db=${STEPANEL_CONTROL_PLANE_DB:-}
    if [ -n "$db" ] && [ -f "$db" ]; then
      printf 'damaged by the broken candidate\n' > "$db"
      rm -f "$db-wal" "$db-shm"
    fi
    exit 1
    ;;
  *)
    exec "${STEPANEL_BROKEN_REAL_BINARY:-/work/broken/stepanel.real}" "$@"
    ;;
esac
