#!/usr/bin/env bash
set -Eeuo pipefail

# Bounded mixed HTTP workload for a disposable control plane: readers hit
# the dashboard APIs over a seeded fleet of sites while writers enqueue real
# backup jobs that the in-process worker executes. It fails on any non-2xx
# response, a p95 latency above STEPANEL_LOAD_MAX_P95_MS, or a backup job that
# does not complete. This is an engineering regression gate, not a capacity
# claim for representative production hardware.
STEPANEL_BIN="${1:-./stepanel}"
TEST_ROOT="${2:-$(mktemp -d)}"
TEST_PORT="${STEPANEL_E2E_PORT:-19091}"
BASE_URL="http://127.0.0.1:$TEST_PORT"
WORKERS="${STEPANEL_LOAD_WORKERS:-4}"
REQUESTS="${STEPANEL_LOAD_REQUESTS:-40}"
SITES="${STEPANEL_LOAD_SITES:-200}"
WRITES="${STEPANEL_LOAD_WRITES:-20}"
MAX_P95_MS="${STEPANEL_LOAD_MAX_P95_MS:-500}"
JOB_TIMEOUT="${STEPANEL_LOAD_JOB_TIMEOUT:-120}"
MIN_FREE_BYTES="${STEPANEL_LOAD_MIN_FREE_BYTES:-67108864}"
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
for setting in SITES WRITES MAX_P95_MS JOB_TIMEOUT; do
  [[ ${!setting} =~ ^[0-9]+$ ]] || { echo "STEPANEL_LOAD_$setting must be a non-negative integer" >&2; exit 2; }
done
[[ $MIN_FREE_BYTES =~ ^[1-9][0-9]*$ ]] || { echo 'STEPANEL_LOAD_MIN_FREE_BYTES must be a positive integer' >&2; exit 2; }
(( WRITES <= SITES )) || { echo "STEPANEL_LOAD_WRITES must not exceed STEPANEL_LOAD_SITES (one backup per site)" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

mkdir -p "$TEST_ROOT"/{imports,backups,www,mail,nvm,proxy,vhosts,apps,quarantine,recovery}
# A fleet of small sites so listing and overview endpoints do real work.
for i in $(seq 1 "$SITES"); do
  mkdir -p "$TEST_ROOT/www/sites/load-$i/public"
  printf '<h1>site %s</h1>\n' "$i" > "$TEST_ROOT/www/sites/load-$i/public/index.html"
  head -c 65536 /dev/urandom > "$TEST_ROOT/www/sites/load-$i/public/asset.bin"
done
# Writers back up files only; the database helper reports no databases.
printf '#!/usr/bin/env bash\nexit 0\n' > "$TEST_ROOT/fake-dbctl"
chmod 0700 "$TEST_ROOT/fake-dbctl"
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
STEPANEL_DBCTL="$TEST_ROOT/fake-dbctl" \
STEPANEL_MIN_FREE_BYTES="$MIN_FREE_BYTES" \
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

csrf=$(awk '$6 == "stepanel_csrf" { print $7 }' "$COOKIE_JAR")
[[ -n $csrf ]] || { echo "login did not issue a CSRF cookie" >&2; exit 1; }

worker_pids=()
# Writers: each enqueues backups for its own slice of sites, so no two
# requests collapse into one idempotent job.
writers=2
for writer in $(seq 1 "$writers"); do
  (
    result="$TEST_ROOT/results.write.$writer"
    : > "$result"
    for site in $(seq "$writer" "$writers" "$WRITES"); do
      curl --silent --show-error --output "$TEST_ROOT/write.$site.json" --max-time 5 \
        --write-out "%{http_code} %{time_total} POST/api/backups\n" --cookie "$COOKIE_JAR" \
        -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' \
        --data "{\"site\":\"load-$site\"}" "$BASE_URL/api/backups" >> "$result" || printf '000 5 POST/api/backups\n' >> "$result"
    done
  ) &
  worker_pids+=("$!")
done
for worker in $(seq 1 "$WORKERS"); do
  (
    result="$TEST_ROOT/results.$worker"
    : > "$result"
    for request in $(seq 1 "$REQUESTS"); do
      endpoint="${endpoints[$(( (request - 1) % ${#endpoints[@]} ))]}"
      curl --silent --show-error --output /dev/null --max-time 5 \
        --write-out "%{http_code} %{time_total} $endpoint\n" --cookie "$COOKIE_JAR" \
        "$BASE_URL$endpoint" >> "$result" || printf '000 5 %s\n' "$endpoint" >> "$result"
    done
  ) &
  worker_pids+=("$!")
done
for worker_pid in "${worker_pids[@]}"; do
  wait "$worker_pid"
done

# Every write must have produced a job that completes.
pending=()
for site in $(seq 1 "$WRITES"); do
  job=$(jq -r '.job_id // empty' "$TEST_ROOT/write.$site.json" 2>/dev/null || true)
  [[ -n $job ]] || { echo "backup request for load-$site returned no job" >&2; cat "$TEST_ROOT/write.$site.json" >&2; exit 1; }
  pending+=("$job")
done
deadline=$((SECONDS + JOB_TIMEOUT))
while (( ${#pending[@]} > 0 )); do
  still=()
  for job in "${pending[@]}"; do
    state=$(curl --silent --max-time 5 --cookie "$COOKIE_JAR" "$BASE_URL/api/jobs/$job" | jq -r '.state // empty')
    case $state in
      completed) ;;
      queued|running) still+=("$job") ;;
      *) echo "backup job $job ended in state '${state:-unknown}'" >&2; exit 1 ;;
    esac
  done
  pending=("${still[@]}")
  if (( ${#pending[@]} > 0 )); then
    (( SECONDS < deadline )) || { echo "${#pending[@]} backup jobs did not finish within ${JOB_TIMEOUT}s" >&2; exit 1; }
    sleep 1
  fi
done

cat "$TEST_ROOT"/results.* > "$TEST_ROOT/all-results"
if awk '$1 !~ /^2[0-9][0-9]$/ { bad++ } END { exit bad ? 0 : 1 }' "$TEST_ROOT/all-results"; then
  echo "non-2xx responses under load:" >&2
  awk '$1 !~ /^2[0-9][0-9]$/ { print $1 }' "$TEST_ROOT/all-results" | sort | uniq -c >&2
  exit 1
fi
total=$(wc -l < "$TEST_ROOT/all-results")
read -r avg max < <(awk '{ sum += $2; if ($2 > max) max = $2 } END { printf "%.4f %.4f\n", sum / NR, max }' "$TEST_ROOT/all-results")
rank=$(( total * 95 / 100 )); (( rank >= 1 )) || rank=1
p95=$(awk '{ print $2 }' "$TEST_ROOT/all-results" | sort -n | sed -n "${rank}p")
p95_ms=$(awk -v p="$p95" 'BEGIN { printf "%d", p * 1000 }')
printf 'mixed HTTP load passed: sites=%d requests=%d readers=%d backup_jobs=%d avg_seconds=%s p95_seconds=%s max_seconds=%s\n' \
  "$SITES" "$total" "$WORKERS" "$WRITES" "$avg" "$p95" "$max"
echo "slowest endpoints by max latency (seconds):"
sort -k3,3 -k2,2nr "$TEST_ROOT/all-results" | awk '!seen[$3]++ { printf "  %-34s %s\n", $3, $2 }' | sort -k2,2nr | head -5
if (( MAX_P95_MS > 0 && p95_ms > MAX_P95_MS )); then
  echo "p95 latency ${p95_ms} ms exceeds the ${MAX_P95_MS} ms limit" >&2
  exit 1
fi
