#!/usr/bin/env bash

# Guided native setup requires a private rclone configuration and at least
# one remote. Check these prerequisites before compiling StePanel; the wizard
# later probes the selected destination for actual write/read/delete access.
install_preflight_offsite() {
  local rclone_cmd=${1:-rclone} remotes config_output config_path config_bytes
  if ! command -v "$rclone_cmd" >/dev/null 2>&1; then
    printf '%s' 'rclone is not installed. Install it with `sudo apt-get install -y rclone` (or `sudo dnf install -y rclone`), configure a remote with `sudo rclone config`, then rerun setup. For a local UI preview, use `make evaluate`.'
    return 1
  fi
  if ! remotes=$("$rclone_cmd" listremotes 2>/dev/null); then
    printf '%s' 'rclone could not read its configuration. Run `sudo rclone config`, verify the remote credentials, then rerun setup.'
    return 1
  fi
  if ! grep -q '[^[:space:]]' <<<"$remotes"; then
    printf '%s' 'No rclone remote is configured. Run `sudo rclone config`, then rerun setup. For a local UI preview, use `make evaluate`.'
    return 1
  fi
  if ! config_output=$("$rclone_cmd" config file 2>/dev/null); then
    printf '%s' 'rclone could not locate its configuration file. Run `sudo rclone config`, then rerun setup.'
    return 1
  fi
  config_path=$(awk 'NF { path=$0 } END { sub(/^[[:space:]]+/, "", path); sub(/[[:space:]]+$/, "", path); print path }' <<<"$config_output")
  if [[ $config_path != /* || ! -f $config_path || -L $config_path ]]; then
    printf '%s' 'rclone must report an existing absolute, regular, non-symlink configuration file. Run `sudo rclone config`, then rerun setup.'
    return 1
  fi
  if ! config_bytes=$(stat -c %s -- "$config_path") ||
    [[ ! $config_bytes =~ ^[0-9]+$ || $config_bytes -gt 1048576 ]]; then
    printf '%s' 'The rclone configuration file is unreadable or exceeds the installer 1 MiB limit; repair it before setup.'
    return 1
  fi
  printf 'rclone is configured with %s remote(s); the setup wizard will verify the selected destination.' \
    "$(grep -c '[^[:space:]]' <<<"$remotes")"
}
