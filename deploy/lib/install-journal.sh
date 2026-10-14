#!/usr/bin/env bash

# Small append-only installer journal. Callers validate the trusted ancestor
# chain, create a root-owned journal directory, and pass a per-run file in
# JOURNAL_FILE.
install_journal_validate_directory() {
  local path=$1 expected_owner=$2 owner mode
  [[ -d $path && ! -L $path ]] || {
    echo "Installer journal path component $path must be a real directory." >&2
    return 1
  }
  owner=$(stat -c %u -- "$path")
  mode=$(stat -c %a -- "$path")
  [[ $owner == "$expected_owner" && $mode =~ ^[0-7]{3,4}$ && $(( 8#$mode & 022 )) == 0 ]] || {
    echo "Installer journal path component $path has an unsafe owner or permissions." >&2
    return 1
  }
}

install_journal_init() {
  local directory owner mode ancestor
  local -a path_components=(/var /var/lib /var/lib/stepanel)
  directory=$(dirname -- "$JOURNAL_FILE")
  path_components+=("$directory")
  for ancestor in "${path_components[@]}"; do
    install_journal_validate_directory "$ancestor" 0 || return 1
  done
  [[ -f $JOURNAL_FILE && ! -L $JOURNAL_FILE ]] || {
    echo 'Installer journal paths must be an existing directory and regular file.' >&2
    return 1
  }
  owner=$(stat -c %u -- "$JOURNAL_FILE")
  mode=$(stat -c %a -- "$JOURNAL_FILE")
  [[ $owner == 0 && $mode =~ ^[0-7]{3,4}$ && $(( 8#$mode & 077 )) == 0 ]] || {
    echo 'Installer journal must be root-owned and inaccessible to group/other users.' >&2
    return 1
  }
}

install_journal_record() {
  local phase=$1 status=$2 recovery=$3 detail=$4 timestamp
  [[ $phase =~ ^[a-z_]+$ && $status =~ ^[a-z_]+$ && $recovery =~ ^[a-z_]+$ ]] || {
    echo 'Invalid installer journal record.' >&2
    return 1
  }
  # Keep records single-line and bounded; these fields are operational labels,
  # never secrets or arbitrary command output.
  detail=${detail//$'\t'/ }
  detail=${detail//$'\n'/ }
  detail=${detail:0:240}
  timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  printf '%s\t%s\t%s\t%s\t%s\n' "$timestamp" "$phase" "$status" "$recovery" "$detail" >> "$JOURNAL_FILE"
  chmod 0600 "$JOURNAL_FILE"
  sync -f "$JOURNAL_FILE"
}
