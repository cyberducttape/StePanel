#!/usr/bin/env bash
set -Eeuo pipefail

# Regenerates the product screenshots in docs/assets/screenshots from the
# real UI. It builds StePanel, starts a disposable control plane with
# throwaway secrets, seeds synthetic sites, domains, backups, and a customer
# account through the API, and captures the dashboard with Playwright.
#
# Usage: scripts/capture-screenshots.sh [OUTPUT_DIR]
#
# Requires `npm ci` and a Playwright browser (`npx playwright install
# chromium`, or set PLAYWRIGHT_CHANNEL=chrome to use an installed Chrome).
#
# The demo uses a fixed root so the document roots shown in the UI read as
# a plain path rather than a random temporary directory. The root helpers
# are replaced by stand-ins: the vhost helper only writes the route file
# StePanel reads back, and the application and site isolation helpers do
# nothing. Routes, accounts, and resource limits are recorded by StePanel
# exactly as on a host, but no web server, Unix account, or cgroup is
# changed. Everything on screen is real server state.

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT_DIR="${1:-$REPO_ROOT/docs/assets/screenshots}"
DEMO_ROOT="${STEPANEL_SHOT_ROOT:-/tmp/stepanel-demo}"
PORT="${STEPANEL_SHOT_PORT:-19391}"
BASE_URL="http://127.0.0.1:$PORT"
ADMIN_USER="operator"
ADMIN_PASS="Demo-Operator-Password-2026!"
PID=""

# Prefer the system Chrome channel when it is available. Some minimal
# Playwright Chromium builds can lay out text as zero-size glyphs, producing
# attractive panels with an unreadable screenshot.
if [[ -z "${PLAYWRIGHT_CHANNEL:-}" ]] && command -v google-chrome >/dev/null 2>&1; then
  export PLAYWRIGHT_CHANNEL=chrome
fi

if [[ -e $DEMO_ROOT ]]; then
  echo "refusing to capture: $DEMO_ROOT already exists; remove it or set STEPANEL_SHOT_ROOT" >&2
  exit 1
fi
if curl --fail --silent --max-time 1 "$BASE_URL/livez" >/dev/null 2>&1; then
  echo "refusing to capture: $BASE_URL is already serving" >&2
  exit 1
fi

cleanup() {
  local status=$?
  if [[ -n $PID ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  if [[ $status -ne 0 && -f $DEMO_ROOT/service.log ]]; then
    echo "capture failed; control-plane log follows" >&2
    tail -n 40 "$DEMO_ROOT/service.log" >&2 || true
  fi
  rm -rf "$DEMO_ROOT"
  exit "$status"
}
trap cleanup EXIT

mkdir -p "$DEMO_ROOT"/{imports,backups,www,mail,nvm,proxy,vhosts,apps,quarantine,recovery,bin}
printf '#!/bin/sh\nexit 0\n' > "$DEMO_ROOT/bin/noop-helper"
# The demo vhost helper records a route file with the name the real helper
# uses, so domains appear exactly as StePanel discovers them on a host.
cat > "$DEMO_ROOT/bin/demo-vhostctl" <<HELPER
#!/bin/sh
[ "\$1" = apply ] || exit 0
printf '# demo route for %s\n' "\$3" > "$DEMO_ROOT/vhosts/site-\$2-\$(printf '%s' "\$3" | tr . _).caddy"
HELPER
chmod 0755 "$DEMO_ROOT/bin/noop-helper" "$DEMO_ROOT/bin/demo-vhostctl"

(cd "$REPO_ROOT" && go build -o "$DEMO_ROOT/bin/stepanel" ./cmd/stepanel)

env -i PATH="$PATH" HOME="$DEMO_ROOT" \
  STEPANEL_ADMIN_USERNAME="$ADMIN_USER" \
  STEPANEL_ADMIN_PASSWORD="$ADMIN_PASS" \
  STEPANEL_SESSION_SECRET="screenshot-session-secret-0123456789012" \
  STEPANEL_ACCOUNT_KEY="screenshot-account-key-01234567890123456" \
  STEPANEL_AUDIT_KEY="screenshot-audit-key-0123456789012345678" \
  STEPANEL_ENVIRONMENT_KEY="screenshot-env-key-012345678901234567890" \
  STEPANEL_BACKUP_SIGNING_KEY="screenshot-backup-key-0123456789012345" \
  STEPANEL_LISTEN="127.0.0.1:$PORT" \
  STEPANEL_IMPORT_ROOT="$DEMO_ROOT/imports" \
  STEPANEL_WEB_ROOT="$DEMO_ROOT/www" \
  STEPANEL_BACKUP_ROOT="$DEMO_ROOT/backups" \
  STEPANEL_MAIL_ROOT="$DEMO_ROOT/mail" \
  STEPANEL_NVM_DIR="$DEMO_ROOT/nvm" \
  STEPANEL_APP_ROOT="$DEMO_ROOT/apps" \
  STEPANEL_PROXY_ROOT="$DEMO_ROOT/proxy" \
  STEPANEL_VHOST_ROOT="$DEMO_ROOT/vhosts" \
  STEPANEL_MALWARE_ROOT="$DEMO_ROOT/quarantine" \
  STEPANEL_AUDIT_LOG="$DEMO_ROOT/audit.jsonl" \
  STEPANEL_JOB_STATE="$DEMO_ROOT/jobs.json" \
  STEPANEL_SESSION_STATE="$DEMO_ROOT/sessions.json" \
  STEPANEL_CONTROL_PLANE_DB="$DEMO_ROOT/control-plane.db" \
  STEPANEL_RECOVERY_ROOT="$DEMO_ROOT/recovery" \
  STEPANEL_VHOSTCTL="$DEMO_ROOT/bin/demo-vhostctl" \
  STEPANEL_PROXYCTL="$DEMO_ROOT/bin/noop-helper" \
  STEPANEL_APPCTL="$DEMO_ROOT/bin/noop-helper" \
  STEPANEL_SITECTL="$DEMO_ROOT/bin/noop-helper" \
  "$DEMO_ROOT/bin/stepanel" >"$DEMO_ROOT/service.log" 2>&1 &
PID=$!

for _ in {1..60}; do
  curl --fail --silent --max-time 1 "$BASE_URL/livez" >/dev/null 2>&1 && break
  sleep 0.5
done
curl --fail --silent --max-time 1 "$BASE_URL/livez" >/dev/null || { echo "StePanel did not become ready" >&2; exit 1; }

STEPANEL_SHOT_BASE_URL="$BASE_URL" \
STEPANEL_SHOT_DIR="$OUT_DIR" \
STEPANEL_SHOT_ADMIN_USER="$ADMIN_USER" \
STEPANEL_SHOT_ADMIN_PASSWORD="$ADMIN_PASS" \
node "$REPO_ROOT/scripts/capture-screenshots.js"

{
  printf 'StePanel UI screenshots\n\n'
  printf 'commit: %s\n' "$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || printf unknown)"
  printf 'version: %s\n' "$(git -C "$REPO_ROOT" describe --tags --match 'v*' --always --dirty 2>/dev/null || printf unknown)"
  printf 'captured_at_utc: %s\n' "$(date -u +%FT%TZ)"
  printf 'browser: Playwright %s\n' "${PLAYWRIGHT_CHANNEL:-chromium}"
  printf 'theme: retro-neon (WayExpand-inspired)\n'
  printf 'data: synthetic sites, .example domains, backups, and one customer account seeded through the API\n'
  printf 'host: disposable development control plane; root helpers replaced by stand-ins (vhost helper writes route files only; application and site isolation helpers are no-ops)\n'
} > "$OUT_DIR/metadata.txt"
echo "wrote $OUT_DIR"
