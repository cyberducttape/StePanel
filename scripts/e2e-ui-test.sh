#!/usr/bin/env bash
set -Eeuo pipefail

# Browser end-to-end test for StePanel.
# Starts a disposable control plane with throwaway secrets under a temporary
# root, then runs the Playwright suite in tests/e2e (including axe-core
# accessibility scans) against it. Requires `npm ci` and
# `npx playwright install chromium` to have been run first.

STEPANEL_BIN="${1:-./stepanel}"
TEST_ROOT="${2:-$(mktemp -d)}"
TEST_PORT="${STEPANEL_E2E_PORT:-19190}"
BASE_URL="http://127.0.0.1:$TEST_PORT"
SCREENSHOT_DIR="${STEPANEL_E2E_SCREENSHOT_DIR:-$TEST_ROOT/screenshots}"
ADMIN_USER="e2eadmin"
ADMIN_PASS="E2E-UI-Password-123!"
STEPANEL_PID=""

cleanup() {
  local exit_code=$?
  if [[ -n $STEPANEL_PID ]]; then
    kill "$STEPANEL_PID" 2>/dev/null || true
    wait "$STEPANEL_PID" 2>/dev/null || true
  fi
  if [[ $exit_code -eq 0 ]]; then
    rm -rf "$TEST_ROOT"
  else
    echo "Browser E2E failed; control-plane log follows (root preserved at $TEST_ROOT)" >&2
    cat "$TEST_ROOT/service.log" >&2 || true
  fi
  exit "$exit_code"
}
trap cleanup EXIT

mkdir -p "$TEST_ROOT"/{imports,backups,www,mail,nvm,proxy,vhosts,apps,quarantine,recovery}
mkdir -p "$SCREENSHOT_DIR"

if curl --fail --silent --max-time 1 "$BASE_URL/livez" >/dev/null 2>&1; then
  echo "refusing to run browser E2E: $BASE_URL is already serving a control plane" >&2
  exit 1
fi

STEPANEL_ADMIN_USERNAME="$ADMIN_USER" \
STEPANEL_ADMIN_PASSWORD="$ADMIN_PASS" \
STEPANEL_SESSION_SECRET="e2e-ui-session-secret-0123456789012345" \
STEPANEL_ACCOUNT_KEY="e2e-ui-account-key-012345678901234567" \
STEPANEL_AUDIT_KEY="e2e-ui-audit-key-01234567890123456789" \
STEPANEL_ENVIRONMENT_KEY="e2e-ui-env-key-0123456789012345678901" \
STEPANEL_BACKUP_SIGNING_KEY="e2e-ui-backup-key-012345678901234567" \
STEPANEL_LISTEN="127.0.0.1:$TEST_PORT" \
STEPANEL_IMPORT_ROOT="$TEST_ROOT/imports" \
STEPANEL_WEB_ROOT="$TEST_ROOT/www" \
STEPANEL_BACKUP_ROOT="$TEST_ROOT/backups" \
STEPANEL_MAIL_ROOT="$TEST_ROOT/mail" \
STEPANEL_NVM_DIR="$TEST_ROOT/nvm" \
STEPANEL_APP_ROOT="$TEST_ROOT/apps" \
STEPANEL_PROXY_ROOT="$TEST_ROOT/proxy" \
STEPANEL_VHOST_ROOT="$TEST_ROOT/vhosts" \
STEPANEL_MALWARE_ROOT="$TEST_ROOT/quarantine" \
STEPANEL_AUDIT_LOG="$TEST_ROOT/audit.jsonl" \
STEPANEL_JOB_STATE="$TEST_ROOT/jobs.json" \
STEPANEL_SESSION_STATE="$TEST_ROOT/sessions.json" \
STEPANEL_CONTROL_PLANE_DB="$TEST_ROOT/control-plane.db" \
STEPANEL_RECOVERY_ROOT="$TEST_ROOT/recovery" \
"$STEPANEL_BIN" >"$TEST_ROOT/service.log" 2>&1 &
STEPANEL_PID=$!

ready=0
for _ in {1..30}; do
  if curl --fail --silent --max-time 1 "$BASE_URL/livez" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "$STEPANEL_PID" 2>/dev/null; then
    echo "stepanel exited before becoming ready" >&2
    exit 1
  fi
  sleep 1
done
[[ $ready == 1 ]] || { echo "stepanel did not become ready" >&2; exit 1; }

STEPANEL_E2E_BASE_URL="$BASE_URL" \
STEPANEL_E2E_USERNAME="$ADMIN_USER" \
STEPANEL_E2E_PASSWORD="$ADMIN_PASS" \
STEPANEL_E2E_SCREENSHOT_DIR="$SCREENSHOT_DIR" \
npx playwright test

if [[ -d $SCREENSHOT_DIR ]]; then
  {
    printf 'StePanel live UI evidence\n\n'
    printf 'commit: %s\n' "$(git rev-parse HEAD 2>/dev/null || printf unknown)"
    printf 'captured_at_utc: %s\n' "$(date -u +%FT%TZ)"
    printf 'base_url: %s\n' "$BASE_URL"
    printf 'browser: Playwright Chromium\n'
    printf 'seed: disposable administrator account; no customer data\n'
  } > "$SCREENSHOT_DIR/metadata.txt"
fi
