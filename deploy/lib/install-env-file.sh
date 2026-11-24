#!/usr/bin/env bash

# secure_install_settings_file promotes an operator or setup-generated
# settings file to root:root 0600. Validation and the ownership/permission
# change act on one descriptor opened with O_NOFOLLOW, so the path cannot be
# swapped for a symlink or another file between the check and the change.
# INSTALL_SETTINGS_TARGET_UID exists only so tests can run unprivileged.
secure_install_settings_file() {
  local file=$1
  [[ -f $file && ! -L $file ]] || {
    echo "$file must be a regular, non-symlink settings file." >&2
    return 1
  }
  command -v python3 >/dev/null 2>&1 || {
    echo "python3 is required to secure the installer settings file." >&2
    return 1
  }
  python3 -I - "$file" "${SUDO_UID:-0}" "${INSTALL_SETTINGS_TARGET_UID:-0}" <<'PY'
import os, stat, sys
path, invoking_uid, target_uid = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
try:
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
except OSError as exc:
    sys.exit(f"{path} must be a regular, non-symlink settings file: {exc.strerror}")
try:
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode):
        sys.exit(f"{path} must be a regular, non-symlink settings file.")
    if info.st_uid not in (0, invoking_uid, target_uid) or info.st_mode & 0o077 or info.st_nlink != 1:
        sys.exit(f"{path} must be owned by root or the sudo user, private (mode 0600), and have one link.")
    try:
        os.fchown(fd, target_uid, target_uid)
        os.fchmod(fd, 0o600)
    except OSError as exc:
        sys.exit(f"could not secure {path}: {exc.strerror}")
finally:
    os.close(fd)
PY
}

print_install_settings_reuse_command() {
  printf 'install later with: sudo ./install.sh --config %q\n' "$1"
}

# load_env_file reads KEY="value" settings (only \\ and \" escapes) from a
# root-owned file that is not group/world-writable. Variables already set in
# the environment win, so explicit overrides keep working. With "private",
# the file must also not be group/world-readable, as it holds secrets.
load_env_file() {
  local file=$1 visibility=${2:-} owner mode key encoded value character closed
  [[ -f $file && ! -L $file ]] || { echo "$file must be a regular file." >&2; exit 1; }
  owner=$(stat -c %u "$file")
  mode=$(stat -c %a "$file")
  [[ $owner == 0 && $mode =~ ^[0-7]{3,4}$ && $(( 8#$mode & 022 )) == 0 ]] || { echo "$file must be root-owned and not group/world-writable." >&2; exit 1; }
  if [[ $visibility == private ]] && (( 8#$mode & 077 )); then
    echo "$file holds secrets and must not be readable by other users (chmod 600 $file)." >&2; exit 1
  fi
  while IFS='=' read -r key encoded || [[ -n ${key:-}${encoded:-} ]]; do
    [[ -n $key && $key != \#* ]] || continue
    [[ ( $key =~ ^STEPANEL_[A-Z0-9_]+$ || $key == RCLONE_CONFIG ) && $encoded == \"* ]] || { echo "$file contains an unsupported entry." >&2; exit 1; }
    encoded=${encoded:1}
    value=
    closed=0
    while [[ -n $encoded ]]; do
      character=${encoded:0:1}
      encoded=${encoded:1}
      if [[ $character == '"' ]]; then
        [[ -z $encoded ]] || { echo "$file contains trailing characters after a quoted value." >&2; exit 1; }
        closed=1
        break
      fi
      if [[ $character == '\' ]]; then
        [[ -n $encoded ]] || { echo "$file contains an invalid escape." >&2; exit 1; }
        character=${encoded:0:1}
        [[ $character == '\' || $character == '"' ]] || { echo "$file contains an unsupported escape." >&2; exit 1; }
        encoded=${encoded:1}
      fi
      value+=$character
    done
    (( closed == 1 )) || { echo "$file contains an unterminated quoted value." >&2; exit 1; }
    [[ -v $key ]] && continue
    printf -v "$key" '%s' "$value"
    export "${key?}"
  done < "$file"
}
