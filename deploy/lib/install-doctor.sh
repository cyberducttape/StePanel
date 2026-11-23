#!/usr/bin/env bash

# Run a read-only installer prerequisite check and retain all failures so the
# operator receives one actionable report instead of discovering blockers one
# at a time. The checked command must not mutate host state.
install_doctor_check() {
  local label=$1 remediation=$2 output
  shift 2
  if output=$("$@" 2>&1); then
    printf 'PASS: %s\n' "$label"
    [[ -z $output ]] || printf '  %s\n' "$output"
    return 0
  fi
  INSTALL_DOCTOR_FAILURES=$((INSTALL_DOCTOR_FAILURES + 1))
  printf 'NEEDS ACTION: %s\n' "$label"
  [[ -z $output ]] || printf '  %s\n' "$output"
  printf '  Fix: %s\n' "$remediation"
  return 1
}

install_doctor_finish() {
  local repo_root=${1:-.}
  if (( INSTALL_DOCTOR_FAILURES > 0 )); then
    printf '\n%d prerequisite check(s) need attention. After fixing them, recheck with:\n' "$INSTALL_DOCTOR_FAILURES"
    printf '  sudo bash %q\n' "$repo_root/scripts/install-host-preflight.sh"
    printf 'See docs/INSTALLATION.md for quota and host preparation guidance.\n' >&2
    return 1
  fi
  printf '\nAll host prerequisites passed. The installer will repeat these checks before mutation.\n'
}
