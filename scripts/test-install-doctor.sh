#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../deploy/lib/install-doctor.sh
. "$ROOT_DIR/deploy/lib/install-doctor.sh"

tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
INSTALL_DOCTOR_FAILURES=0
check_pass() { printf 'check-ran\n' >> "$CHECK_LOG"; printf 'verified\n'; }
check_fail() { printf 'check-ran\n' >> "$CHECK_LOG"; printf 'missing prerequisite\n' >&2; return 1; }
export CHECK_LOG="$tmp/checks.log"

install_doctor_check 'first prerequisite' 'run the documented repair' check_fail >"$tmp/report" || true
install_doctor_check 'second prerequisite' 'none' check_pass >>"$tmp/report"
if install_doctor_finish "$ROOT_DIR" >>"$tmp/report" 2>&1; then
  echo 'doctor accepted a failed prerequisite' >&2
  exit 1
fi
[[ $(wc -l < "$CHECK_LOG") == 2 ]]
grep -Fq 'NEEDS ACTION: first prerequisite' "$tmp/report"
grep -Fq 'Fix: run the documented repair' "$tmp/report"
grep -Fq 'PASS: second prerequisite' "$tmp/report"
grep -Fq "sudo bash $ROOT_DIR/scripts/install-host-preflight.sh" "$tmp/report"
grep -Fq 'docs/INSTALLATION.md#supported-operating-systems' "$ROOT_DIR/scripts/install-host-preflight.sh"
grep -Fq 'docs/INSTALLATION.md#prepare-filesystem-quotas' "$ROOT_DIR/scripts/install-host-preflight.sh"

INSTALL_DOCTOR_FAILURES=0
if ! install_doctor_check 'all clear' 'none' check_pass >"$tmp/pass-report"; then
  echo 'doctor rejected a passing prerequisite' >&2
  exit 1
fi
install_doctor_finish "$ROOT_DIR" >>"$tmp/pass-report"
grep -Fq 'All host prerequisites passed' "$tmp/pass-report"

echo 'installer doctor aggregation contract passed'
