#!/usr/bin/env bash

# Execute an optional installer action after the core installation commits.
# The action plan must already be durable before commit. Every attempted
# action then records its start and terminal outcome so interruption leaves
# an explicit recovery point instead of an apparently complete installation.
install_optional_postcommit_action() {
  local label=$1 retry=$2
  shift 2

  if ! install_journal_record post_install started partial "action=$label"; then
    POST_INSTALL_PENDING+=(installer_journal "$label")
    echo "Post-install journal update failed before action; not attempted: $label (retry: $retry)." >&2
    return 0
  fi

  if "$@"; then
    if ! install_journal_record post_install complete retained "action=$label"; then
      POST_INSTALL_PENDING+=(installer_journal)
      echo "Post-install action completed but its journal result could not be recorded: $label" >&2
    fi
    return 0
  fi

  POST_INSTALL_PENDING+=("$label")
  echo "Post-install action pending: $label (retry: $retry)." >&2
  if ! install_journal_record post_install pending manual "action=$label retry=$retry"; then
    POST_INSTALL_PENDING+=(installer_journal)
    echo "Post-install failure could not be written to the installer journal: $label" >&2
  fi
}
