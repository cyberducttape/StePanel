#!/usr/bin/env bash

# Keep the runtime configuration of the release an upgrade replaces. An older
# release's installer can refuse the environment file a newer one writes (for
# example v0.7.0 rejects any key outside STEPANEL_*, and the current installer
# writes RCLONE_CONFIG), so undoing a committed upgrade (docs/COMPATIBILITY.md)
# restores the file kept here before reinstalling the older release. Copies
# are private to the installing user (root), like the file itself.
#
#   install_retain_replaced_configuration ROOT ENV_FILE INSTALLED_BINARY NEW_BINARY LABEL [KEEP]
#
# It runs before the installer changes anything, and keeps ENV_FILE only when
# this run replaces the installed release: nothing is kept for a fresh
# install, for a rerun of the installed release, or when the newest copy was
# already made for this same new release. The last rule matters after an
# interrupted upgrade: the killed run may already have rewritten ENV_FILE, so
# its rerun must not replace the copy the first attempt made of the previous
# release's file. The newest KEEP copies (default 5) are kept. On success it
# prints the directory holding a new copy, or nothing.
install_retain_replaced_configuration() {
  local root=$1 env_file=$2 installed_binary=$3 new_binary=$4 label=$5 keep=${6:-5}
  local owner stamp target name newest new_sha installed_sha=none
  local -a expired
  [[ -f $env_file && ! -L $env_file ]] || return 0
  [[ $keep =~ ^[1-9][0-9]*$ ]] || { echo "invalid retention count: $keep" >&2; return 1; }
  [[ $label =~ ^[A-Za-z0-9._+-]{1,64}$ ]] || { echo "invalid release label: $label" >&2; return 1; }
  new_sha=$(sha256sum -- "$new_binary") || return 1
  new_sha=${new_sha%% *}
  if [[ -f $installed_binary ]]; then
    installed_sha=$(sha256sum -- "$installed_binary") || return 1
    installed_sha=${installed_sha%% *}
  fi
  [[ $installed_sha != "$new_sha" ]] || return 0
  owner=$(id -u)
  if [[ -e $root || -L $root ]]; then
    [[ -d $root && ! -L $root && $(stat -c '%u:%a' -- "$root") == "$owner:700" ]] || {
      echo "$root must be a private directory owned by the installing user" >&2
      return 1
    }
  else
    mkdir -m 0700 -- "$root" || return 1
  fi
  # Directory names start with a UTC timestamp, so they sort by age.
  newest=$(find "$root" -mindepth 1 -maxdepth 1 -type d -name '[0-9]*T*Z-*' -printf '%f\n' | sort -r | head -n 1)
  if [[ -n $newest && -f $root/$newest/upgrade ]] && grep -Fxq "replaced_by_sha256=$new_sha" "$root/$newest/upgrade"; then
    return 0
  fi
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  target="$root/$stamp-$$"
  mkdir -m 0700 -- "$target" || return 1
  if ! ( umask 077 && cp -- "$env_file" "$target/ste-panel.env" &&
         printf 'replaced_by=%s\nreplaced_by_sha256=%s\nprevious_binary_sha256=%s\nreplaced_at=%s\n' "$label" "$new_sha" "$installed_sha" "$stamp" > "$target/upgrade" ) ||
     ! sync -f "$target/ste-panel.env"; then
    rm -rf -- "$target"
    return 1
  fi
  mapfile -t expired < <(find "$root" -mindepth 1 -maxdepth 1 -type d -name '[0-9]*T*Z-*' -printf '%f\n' | sort -r | tail -n "+$(( keep + 1 ))")
  for name in "${expired[@]}"; do
    rm -rf -- "${root:?}/$name" || echo "could not expire retained configuration $root/$name" >&2
  done
  printf '%s\n' "$target"
}
