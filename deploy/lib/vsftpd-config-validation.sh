#!/usr/bin/env bash

# Validate a vsftpd configuration by starting a short-lived listener on
# loopback. vsftpd has no config-test mode. Keep the config file before the
# -o overrides: vsftpd applies options in command-line order, so options placed
# before the file can be overwritten by values in the file.
stepanel_validate_vsftpd_config() {
  local binary=$1 config=$2 probe_port output status
  [[ -x $binary ]] || { echo 'vsftpd executable is unavailable' >&2; return 1; }
  [[ -f $config && ! -L $config ]] || { echo 'vsftpd configuration is not a regular file' >&2; return 1; }
  command -v timeout >/dev/null 2>&1 || { echo 'timeout is required to validate vsftpd configuration' >&2; return 1; }
  command -v shuf >/dev/null 2>&1 || { echo 'shuf is required to validate vsftpd configuration' >&2; return 1; }

  # The config's standalone listener is overridden to bind only loopback and
  # to stay in the foreground. A two-second timeout is the expected result for
  # a valid daemon configuration; parse, bind, and startup errors exit sooner.
  probe_port=$(shuf -i 49152-65535 -n 1) || return 1
  if output=$(timeout --foreground --signal=TERM --kill-after=2s 2s \
    "$binary" "$config" \
    -olisten=YES -olisten_ipv6=NO -obackground=NO \
    -olisten_address=127.0.0.1 -olisten_port="$probe_port" 2>&1); then
    status=0
  else
    status=$?
  fi

  if (( status == 124 )); then
    return 0
  fi
  echo "vsftpd configuration failed its bounded loopback startup probe (exit $status)." >&2
  [[ -z $output ]] || printf '%s\n' "$output" >&2
  return 1
}
