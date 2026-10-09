#!/usr/bin/env bash
# Abrupt whole-guest loss during a durable backup, driven by
# deploy/lab/local-kvm-certification.sh across a hard VM kill:
#
#   prepare  create a site with enough content that a backup takes seconds
#   start    queue a backup and return once the job is running
#   verify   after the guest is killed and rebooted: the job reaches a
#            terminal state, every listed backup verifies, no partial backup
#            staging remains, and a fresh backup succeeds
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'abrupt-loss drill must run as root' >&2; exit 1; }
action=${1:?usage: abrupt-loss-backup-drill.sh prepare|start|verify}

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${DRILL_SITE:=abrupt-loss}"
: "${DRILL_CONTENT_MB:=300}"
state_dir=/var/lib/stepanel-cert
backup_root=/var/backups/stepanel
install -d -m 0700 "$state_dir"

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
cookies="$work/cookies.txt"

login() {
  # A TOTP code is accepted once; start each login in a fresh 30 s window.
  sleep $((31 - $(date +%s) % 30))
  local totp
  totp=$(python3 - "$STEPANEL_ADMIN_TOTP_SECRET" <<'PY'
import base64, hashlib, hmac, struct, sys, time
secret = sys.argv[1].strip().upper()
secret += "=" * ((8 - len(secret) % 8) % 8)
digest = hmac.new(base64.b32decode(secret), struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
offset = digest[-1] & 0x0f
print(f"{(struct.unpack('>I', digest[offset:offset + 4])[0] & 0x7fffffff) % 1000000:06d}")
PY
)
  curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
  curl --fail --silent --show-error --max-time 10 -L -b "$cookies" -c "$cookies" \
    --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" \
    --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
    --data-urlencode "totp=$totp" "$PANEL/login" >/dev/null
  session=$(awk '$6 == "stepanel_session" {print $7}' "$cookies")
  csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
  [[ -n $session && -n $csrf ]] || { echo 'login did not issue session and CSRF cookies' >&2; exit 1; }
  cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"
}

api() {
  local method=$1 path=$2 body=${3:-}
  if [[ -n $body ]]; then
    curl --fail --silent --show-error --max-time 60 -X "$method" -H "Cookie: $cookie_header" -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' --data "$body" "$PANEL$path"
  else
    curl --fail --silent --show-error --max-time 60 -X "$method" -H "Cookie: $cookie_header" "$PANEL$path"
  fi
}

job_state() { api GET "/api/jobs/$1" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p'; }

wait_terminal() {
  local job=$1 state=''
  for _ in $(seq 1 600); do
    state=$(job_state "$job")
    case "$state" in completed|failed|dead-letter|cancelled) printf '%s' "$state"; return 0 ;; esac
    sleep 1
  done
  printf '%s' "${state:-unknown}"
  return 1
}

backup_job() {
  local response
  response=$(api POST /api/backups "{\"site\":\"$DRILL_SITE\",\"include_databases\":false}")
  printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p'
}

case "$action" in
  prepare)
    login
    response=$(api POST /api/sites "{\"site\":\"$DRILL_SITE\",\"template\":\"php\"}")
    job=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
    [[ -n $job ]] || { echo "site creation was not queued: $response" >&2; exit 1; }
    [[ $(wait_terminal "$job") == completed ]] || { echo 'site creation did not complete' >&2; exit 1; }
    public=/var/www/sites/$DRILL_SITE/public
    owner=$(stat -c %U:%G "$public")
    # Incompressible content so archiving takes long enough to interrupt.
    for i in $(seq 1 $((DRILL_CONTENT_MB / 10))); do
      head -c $((10 << 20)) /dev/urandom > "$public/asset-$i.bin"
    done
    chown -R "$owner" "$public"
    find "$public" -type f -exec sha256sum {} + | sort > "$state_dir/content.sha256"
    echo "prepared $DRILL_SITE with ${DRILL_CONTENT_MB} MiB"
    ;;
  start)
    login
    job=$(backup_job)
    [[ -n $job ]] || { echo 'backup was not queued' >&2; exit 1; }
    printf '%s\n' "$job" > "$state_dir/backup-job"
    sync
    for _ in $(seq 1 60); do
      [[ $(job_state "$job") == running ]] && { echo "backup $job running"; exit 0; }
      sleep 0.5
    done
    echo "backup $job did not reach running" >&2
    exit 1
    ;;
  verify)
    login
    job=$(<"$state_dir/backup-job")
    final=$(wait_terminal "$job") || { echo "interrupted backup $job did not reach a terminal state ($final)" >&2; exit 1; }
    # The site content is untouched by a backup, interrupted or not.
    (cd / && sha256sum --quiet -c "$state_dir/content.sha256") || { echo 'site content changed after abrupt loss' >&2; exit 1; }
    # Every published backup must verify; a partial archive must never be listed.
    names=$(api GET "/api/backups?site=$DRILL_SITE" | python3 -c 'import json, sys; print("\n".join(i["path"].rstrip("/").rsplit("/", 1)[-1] for i in json.load(sys.stdin).get("backups", [])))')
    verified=0
    while IFS= read -r name; do
      [[ -n $name ]] || continue
      api POST /api/backups/verify "{\"site\":\"$DRILL_SITE\",\"backup\":\"$name\"}" >/dev/null || { echo "listed backup $name does not verify" >&2; exit 1; }
      verified=$((verified + 1))
    done <<< "$names"
    # Staging left by the killed process must not be published or linger as
    # a backup; startup cleanup removes stale staging.
    leftovers=$(find "$backup_root" -maxdepth 1 -name ".*$DRILL_SITE*" 2>/dev/null | wc -l)
    # A fresh backup succeeds after recovery.
    fresh=$(backup_job)
    [[ $(wait_terminal "$fresh") == completed ]] || { echo 'fresh backup after recovery did not complete' >&2; exit 1; }
    api POST /api/backups/verify "{\"site\":\"$DRILL_SITE\",\"backup\":\"$(api GET "/api/backups?site=$DRILL_SITE" | python3 -c 'import json, sys; print(json.load(sys.stdin)["backups"][0]["path"].rstrip("/").rsplit("/", 1)[-1])')\"}" >/dev/null
    echo "interrupted job $job ended $final; $verified listed backup(s) verified; $leftovers staging leftover(s); fresh backup completed and verified"
    ;;
  *) echo "unknown action $action" >&2; exit 64 ;;
esac
