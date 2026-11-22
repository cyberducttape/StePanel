#!/usr/bin/env bash

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
