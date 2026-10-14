#!/usr/bin/env bash

# Acquire a nonblocking lock for the complete installer mutation phase. Keep
# the directory private because /run/lock may be world-writable and a root
# open there must never follow an untrusted symlink.
install_acquire_host_lock() {
  local directory=$1 expected_uid=$2 expected_gid=$3 lock_file directory_owner
  lock_file=${directory%/}/installer.lock

  [[ ! -L $directory ]] || {
    echo 'Refusing a symlinked installer lock directory.' >&2
    return 1
  }
  install -d -o "$expected_uid" -g "$expected_gid" -m 0700 "$directory" || return
  [[ -d $directory && ! -L $directory ]] || {
    echo 'Installer lock directory is not a real directory.' >&2
    return 1
  }
  chmod 0700 "$directory" || return
  directory_owner=$(stat -c %u:%g:%a -- "$directory") || return
  [[ $directory_owner == "$expected_uid:$expected_gid:700" ]] || {
    echo 'Installer lock directory must have its expected owner and mode 0700.' >&2
    return 1
  }
  [[ ! -L $lock_file && ( ! -e $lock_file || -f $lock_file ) ]] || {
    echo 'Refusing a symlink or non-regular installer lock file.' >&2
    return 1
  }
  touch "$lock_file" || return
  chown "$expected_uid:$expected_gid" "$lock_file" || return
  chmod 0600 "$lock_file" || return
  exec 8>>"$lock_file"
  if ! flock -xn 8; then
    exec 8>&-
    echo 'Another StePanel installer is already changing this host; retry after it finishes.' >&2
    return 75
  fi
}
