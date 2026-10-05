# shellcheck shell=bash
# Control-plane database transaction for install.sh.
#
# An upgrade replaces the binary and then starts it; the new binary migrates
# the control-plane schema (and re-seals secrets) before its health check
# runs. If that check fails, restoring only the binary leaves the previous
# release facing a database it refuses or cannot read. These functions make
# the rollback cover the database too:
#
#   control_plane_txn_begin DB TXN_DIR   while every StePanel process is stopped
#   control_plane_txn_rollback           after stopping the candidate's processes
#
# The snapshot is a byte copy of the SQLite file set (database, -wal, -shm)
# taken while nothing has it open, so it needs no StePanel binary, runs no
# migration, and restores exactly the state the previous release left. On
# rollback the candidate's files are moved aside, not deleted, for diagnosis.

CONTROL_PLANE_TXN_DB=
CONTROL_PLANE_TXN_DIR=
CONTROL_PLANE_TXN_EXISTED=0
CONTROL_PLANE_TXN_ACTIVE=0

control_plane_txn_begin() {
  local db=$1 txn=$2 suffix needed available
  [[ -n $db && -n $txn ]] || { echo 'control_plane_txn_begin requires DB and TXN_DIR' >&2; return 1; }
  CONTROL_PLANE_TXN_DB=$db
  CONTROL_PLANE_TXN_DIR=$txn/control-plane
  CONTROL_PLANE_TXN_EXISTED=0
  if [[ -e $db || -L $db ]]; then
    [[ -f $db && ! -L $db ]] || { echo "Refusing unexpected control-plane database at $db." >&2; return 1; }
    CONTROL_PLANE_TXN_EXISTED=1
  fi
  install -d -m 0700 "$CONTROL_PLANE_TXN_DIR"
  if (( CONTROL_PLANE_TXN_EXISTED )); then
    needed=0
    for suffix in '' -wal -shm; do
      if [[ -e $db$suffix ]]; then needed=$(( needed + $(stat -c %s "$db$suffix") )); fi
    done
    available=$(( $(stat -f -c '%a * %S' "$CONTROL_PLANE_TXN_DIR") ))
    if (( available < needed * 2 )); then
      echo "Not enough space in $txn to snapshot the control-plane database ($needed bytes)." >&2
      return 1
    fi
    for suffix in '' -wal -shm; do
      [[ -e $db$suffix ]] || continue
      [[ -f $db$suffix && ! -L $db$suffix ]] || { echo "Refusing unexpected file at $db$suffix." >&2; return 1; }
      cp -a -- "$db$suffix" "$CONTROL_PLANE_TXN_DIR/db$suffix"
    done
    sync -f "$CONTROL_PLANE_TXN_DIR/db" 2>/dev/null || sync
  fi
  CONTROL_PLANE_TXN_ACTIVE=1
}

# control_plane_txn_unchanged reports whether the database file set is
# byte-identical to the snapshot (the candidate never touched it).
control_plane_txn_unchanged() {
  local db=$CONTROL_PLANE_TXN_DB suffix
  for suffix in '' -wal -shm; do
    if [[ -e $CONTROL_PLANE_TXN_DIR/db$suffix ]]; then
      cmp -s -- "$CONTROL_PLANE_TXN_DIR/db$suffix" "$db$suffix" || return 1
    elif [[ -e $db$suffix || -L $db$suffix ]]; then
      return 1
    fi
  done
}

control_plane_txn_rollback() {
  (( CONTROL_PLANE_TXN_ACTIVE )) || return 0
  local db=$CONTROL_PLANE_TXN_DB suffix aside moved=0
  if control_plane_txn_unchanged; then
    CONTROL_PLANE_TXN_ACTIVE=0
    return 0
  fi
  aside="$db.failed-upgrade-$(date -u +%Y%m%dT%H%M%SZ)"
  for suffix in '' -wal -shm; do
    if [[ -e $db$suffix || -L $db$suffix ]]; then
      mv -f -- "$db$suffix" "$aside$suffix"
      moved=1
    fi
  done
  if (( CONTROL_PLANE_TXN_EXISTED )); then
    for suffix in '' -wal -shm; do
      [[ -e $CONTROL_PLANE_TXN_DIR/db$suffix ]] || continue
      cp -a -- "$CONTROL_PLANE_TXN_DIR/db$suffix" "$db$suffix"
    done
    sync -f "$db" 2>/dev/null || sync
    echo "Restored the control-plane database from before the upgrade." >&2
  fi
  if (( moved )); then
    echo "The failed candidate's control-plane database was kept at $aside for diagnosis." >&2
  fi
  CONTROL_PLANE_TXN_ACTIVE=0
}

control_plane_txn_commit() {
  CONTROL_PLANE_TXN_ACTIVE=0
}
