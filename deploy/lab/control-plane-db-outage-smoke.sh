#!/usr/bin/env bash
# Verify that installed services fail closed while their shared SQLite
# control-plane database is unavailable, then recover the exact database files.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'control-plane database outage smoke must run as root' >&2; exit 1; }
[[ ${STEPANEL_UNSAFE_LAB:-0} == 1 ]] || {
  echo 'control-plane database outage smoke is restricted to the disposable install lab' >&2
  exit 1
}
for command in curl systemctl journalctl; do
  command -v "$command" >/dev/null || { echo "control-plane outage smoke requires $command" >&2; exit 77; }
done

env_file=/etc/ste-panel.env
db_path=$(awk -F '"' '$1 == "STEPANEL_CONTROL_PLANE_DB=" { print $2; exit }' "$env_file")
[[ $db_path == /var/lib/ste-panel/stepanel-control.db ]] || {
  echo "unexpected control-plane database path for this lab: ${db_path:-unset}" >&2
  exit 77
}
[[ -f $db_path && ! -L $db_path ]] || { echo "control-plane database is missing or unsafe: $db_path" >&2; exit 1; }

work=$(mktemp -d /var/lib/ste-panel/.db-outage-smoke.XXXXXX)
chmod 0700 "$work"
stashed=0
services_stopped=0

restore_database() {
  local suffix
  systemctl stop stepanel-worker.service stepanel.service stepanel-root-broker.service || true
  if [[ -d $db_path && ! -L $db_path ]]; then
    rmdir -- "$db_path" || { echo "database outage path is not empty; preserving recovery files in $work" >&2; return 1; }
  elif [[ -e $db_path || -L $db_path ]]; then
    echo "database outage path changed unexpectedly; preserving recovery files in $work" >&2
    return 1
  fi
  for suffix in '' -wal -shm -journal; do
    if [[ -e $work/database$suffix || -L $work/database$suffix ]]; then
      mv -- "$work/database$suffix" "$db_path$suffix" || {
        echo "could not restore $db_path$suffix; recovery files remain in $work" >&2
        return 1
      }
    fi
  done
  stashed=0
  systemctl reset-failed stepanel-root-broker.service stepanel.service stepanel-worker.service 2>/dev/null || true
  systemctl start stepanel-root-broker.service stepanel.service stepanel-worker.service || {
    echo "could not restart StePanel after restoring the control-plane database; inspect $work" >&2
    return 1
  }
  for _ in $(seq 1 120); do
    if systemctl is-active --quiet stepanel.service && \
       systemctl is-active --quiet stepanel-worker.service && \
       systemctl is-active --quiet stepanel-root-broker.service && \
       curl --fail --silent --max-time 2 http://127.0.0.1:8090/readyz >/dev/null; then
      services_stopped=0
      return 0
    fi
    sleep 1
  done
  echo "readiness did not recover after restoring the control-plane database; inspect $work" >&2
  return 1
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if (( stashed )); then
    restore_database || exit 1
  elif (( services_stopped )); then
    systemctl start stepanel-root-broker.service stepanel.service stepanel-worker.service || {
      echo 'could not restart StePanel services; operator recovery is required' >&2
      exit 1
    }
    for _ in $(seq 1 120); do
      if curl --fail --silent --max-time 2 http://127.0.0.1:8090/readyz >/dev/null; then
        services_stopped=0
        break
      fi
      sleep 1
    done
    (( services_stopped == 0 )) || { echo 'readiness did not recover during cleanup' >&2; exit 1; }
  fi
  if (( stashed == 0 )); then rm -rf -- "$work"; fi
  return "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

services_stopped=1
systemctl stop stepanel-worker.service stepanel.service stepanel-root-broker.service
for unit in stepanel-worker.service stepanel.service stepanel-root-broker.service; do
  if systemctl is-active --quiet "$unit"; then
    echo "$unit remained active after the outage drill stop request" >&2
    exit 1
  fi
done

# The services are stopped, so moving the database and SQLite sidecars is a
# consistent, reversible fixture operation. Replacing the configured DB path
# with a directory makes opens fail without deleting or corrupting its bytes.
stashed=1
for suffix in '' -wal -shm -journal; do
  if [[ -e $db_path$suffix || -L $db_path$suffix ]]; then
    mv -- "$db_path$suffix" "$work/database$suffix"
  fi
done
mkdir -m 0700 -- "$db_path"
systemctl start --no-block stepanel-root-broker.service stepanel.service stepanel-worker.service

ready_status='unreachable'
for _ in $(seq 1 20); do
  ready_status=$(curl --silent --show-error --max-time 2 --output "$work/ready.json" \
    --write-out '%{http_code}' http://127.0.0.1:8090/readyz 2>/dev/null || true)
  [[ $ready_status != 200 ]] || {
    echo 'panel reported readiness while its control-plane database path was unavailable' >&2
    exit 1
  }
  sleep 1
done
case "$ready_status" in
  503)
    grep -Eq '"ready"[[:space:]]*:[[:space:]]*false' "$work/ready.json" || {
      echo '/readyz returned 503 without an explicit ready=false state' >&2
      exit 1
    }
    grep -Eq '"control_plane_integrity"[[:space:]]*:[[:space:]]*\{"ready"[[:space:]]*:[[:space:]]*false' "$work/ready.json" || {
      echo '/readyz returned 503 without identifying the failed control-plane check' >&2
      exit 1
    }
    echo 'control-plane database outage failed closed with an explicit readiness diagnostic (HTTP 503)'
    ;;
  000|unreachable)
    journalctl -u stepanel.service --no-pager -n 200 >"$work/panel-outage.log" 2>&1 || true
    grep -Eiq '(open|ping) control-plane database:' "$work/panel-outage.log" || {
      echo 'panel was unreachable during the outage without a control-plane database startup diagnostic' >&2
      cat "$work/panel-outage.log" >&2 || true
      exit 1
    }
    echo 'panel failed startup closed with an explicit control-plane database diagnostic (readiness unreachable)'
    ;;
  *)
    echo "unexpected readiness response during database outage: HTTP $ready_status" >&2
    cat "$work/ready.json" >&2 || true
    exit 1
    ;;
esac

restore_database
echo 'installed control-plane database outage recovery passed'
