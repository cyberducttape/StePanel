#!/usr/bin/env bash
set -Eeuo pipefail

# Bounded, read-only HTTP workload for a disposable control plane. This is
# intentionally an engineering regression gate, not a capacity claim for
# representative production hardware.
STEPANEL_BIN="${1:-./stepanel}"
TEST_ROOT="${2:-$(mktemp -d)}"
TEST_PORT="${STEPANEL_E2E_PORT:-19091}"
BASE_URL="http://127.0.0.1:$TEST_PORT"
WORKERS="${STEPANEL_LOAD_WORKERS:-4}"
REQUESTS="${STEPANEL_LOAD_REQUESTS:-40}"
ADMIN_USER="loadadmin"
ADMIN_PASS="E2E-Load-Password-123!"
COOKIE_JAR="$TEST_ROOT/cookies.txt"
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
    echo "mixed HTTP load failed; root preserved at $TEST_ROOT" >&2
    cat "$TEST_ROOT/service.log" >&2 || true
  fi
  exit "$exit_code"
}
trap cleanup EXIT

[[ $WORKERS =~ ^[1-9][0-9]*$ ]] || { echo "STEPANEL_LOAD_WORKERS must be a positive integer" >&2; exit 2; }
[[ $REQUESTS =~ ^[1-9][0-9]*$ ]] || { echo "STEPANEL_LOAD_REQUESTS must be a positive integer" >&2; exit 2; }

mkdir -p "$TEST_ROOT"/{imports,backups,www,mail,nvm,proxy,vhosts,apps,quarantine,recovery}
if curl --fail --silent --max-time 1 "$BASE_URL/livez" >/dev/null 2>&1; then
  echo "refusing to run mixed HTTP load: $BASE_URL is already serving a control plane" >&2
  exit 1
fi

STEPANEL_ADMIN_USERNAME="$ADMIN_USER" \
STEPANEL_ADMIN_PASSWORD="$ADMIN_PASS" \
STEPANEL_SESSION_SECRET="e2e-load-session-secret-0123456789012345" \
STEPANEL_ACCOUNT_KEY="e2e-load-account-key-012345678901234567" \
STEPANEL_AUDIT_KEY="e2e-load-audit-key-01234567890123456789" \
STEPANEL_ENVIRONMENT_KEY="e2e-load-env-key-0123456789012345678901" \
STEPANEL_BACKUP_SIGNING_KEY="e2e-load-backup-key-012345678901234567" \
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

login_code=$(curl --silent --show-error --output "$TEST_ROOT/login.out" \
  --cookie-jar "$COOKIE_JAR" --write-out '%{http_code}' --max-time 5 \
  --data-urlencode "username=$ADMIN_USER" \
  --data-urlencode "password=$ADMIN_PASS" "$BASE_URL/login")
[[ $login_code == 303 ]] || { echo "administrator login failed (HTTP $login_code)" >&2; cat "$TEST_ROOT/login.out" >&2; exit 1; }

endpoints=(
  /readyz
  /api/health
  /api/capabilities
  /api/services
  /api/sites
  /api/sites/overview
  '/api/backups?limit=10'
  /api/jobs
  /api/deployments
  /api/accounts
  /api/admin/production-readiness
  /metrics
)

worker_pids=()
for worker in $(seq 1 "$WORKERS"); do
  (
    result="$TEST_ROOT/results.$worker"
    : > "$result"
    for request in $(seq 1 "$REQUESTS"); do
      endpoint="${endpoints[$(( (request - 1) % ${#endpoints[@]} ))]}"
      curl --silent --show-error --output /dev/null --max-time 5 \
        --write-out "%{http_code} %{time_total}\n" --cookie "$COOKIE_JAR" \
        "$BASE_URL$endpoint" >> "$result" || printf '000 5\n' >> "$result"
    done
  ) &
  worker_pids+=("$!")
done
for worker_pid in "${worker_pids[@]}"; do
  wait "$worker_pid"
done

awk -v workers="$WORKERS" '
  { total++; if ($1 !~ /^2[0-9][0-9]$/) bad++; sum += $2; if ($2 > max) max = $2; times[total] = $2 }
  END {
    if (total == 0 || bad != 0) exit 1
    for (i = 1; i <= total; i++) for (j = i + 1; j <= total; j++) if (times[j] < times[i]) { tmp = times[i]; times[i] = times[j]; times[j] = tmp }
    rank = int(total * 0.95); if (rank < 1) rank = 1
    printf "mixed HTTP load passed: requests=%d workers=%d avg_seconds=%.4f p95_seconds=%.4f max_seconds=%.4f\n", total, workers, sum / total, times[rank], max
  }
' "$TEST_ROOT"/results.*
