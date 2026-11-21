#!/usr/bin/env bash
# Kill the root helper after a file restore has activated the restored tree
# but while it is sealing it (ownership, ACLs), before the job can record
# completion.
#
# A file restore activates the staged tree through the unprivileged site
# manager, then asks the root broker to seal it, then commits its journal.
# Here a wrapper around stepanel-sitectl starts the real seal of the drill
# site and SIGKILLs the whole root broker unit (broker, helper and wrapper)
# part way through; systemd restarts the broker. Then:
#
#   - the restore job must reach a terminal state;
#   - the site must hold exactly the backed-up or the pre-restore tree, and
#     the backed-up one if the job completed;
#   - no file may be left owned by anyone but the site's account (a partly
#     sealed tree would keep files owned by the panel user);
#   - the site must serve whichever tree is on disk;
#   - a fresh restore must complete with the backed-up tree.
#
# Run after install-smoke.sh on a disposable --unsafe-lab host.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'restore helper interruption smoke must run as root' >&2; exit 1; }
grep -q '^STEPANEL_UNSAFE_LAB="\?1"\?$' /etc/ste-panel.env || {
  echo 'refusing to run: /etc/ste-panel.env is not an --unsafe-lab install' >&2
  exit 1
}
systemctl is-active --quiet stepanel-root-broker.service || { echo 'the root broker is not running' >&2; exit 77; }
: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${RESTORE_SMOKE_FILES:=3000}"

suffix=$(date -u +%H%M%S)
site=ci-sealkill-$suffix
domain=$site.localhost
webserver=$(sed -n 's/^STEPANEL_WEBSERVER="\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' /etc/ste-panel.env)
site_ctl=/usr/local/sbin/stepanel-sitectl
site_ctl_real=$site_ctl.restore-smoke-real
fault=/run/stepanel-restore-seal-fault
killed_marker=/run/stepanel-restore-seal-killed
# shellcheck source=../integrations/stepanel-site-location.sh
. /usr/local/lib/stepanel/site-location.sh

work=$(mktemp -d)
cookies=$work/cookies.txt
restore_site_ctl() {
  rm -f "$fault"
  if [[ -f $site_ctl_real ]]; then mv -f "$site_ctl_real" "$site_ctl"; fi
}
trap 'restore_site_ctl; rm -rf -- "$work"' EXIT
fail() { echo "restore helper interruption smoke: $*" >&2; exit 1; }

login() {
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
  rm -f -- "$cookies"
  curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
  curl --fail --silent --show-error --max-time 10 -L -b "$cookies" -c "$cookies" \
    --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
    --data-urlencode "totp=$totp" "$PANEL/login" >/dev/null
  session=$(awk '$6 == "stepanel_session" {print $7}' "$cookies")
  csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
  [[ -n $session && -n $csrf ]] || fail 'login did not issue session and CSRF cookies'
  cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"
}
api() {
  local method=$1 path=$2 body=${3:-}
  if [[ -n $body ]]; then
    curl --fail --silent --show-error --max-time 60 -X "$method" -H "Cookie: $cookie_header" -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' --data "$body" "$PANEL$path"
  else
    curl --fail --silent --show-error --max-time 60 -X "$method" -H "Cookie: $cookie_header" -H "X-CSRF-Token: $csrf" "$PANEL$path"
  fi
}
json() { python3 -c "import json, sys; print(json.dumps(dict(zip(sys.argv[1::2], sys.argv[2::2]))))" "$@"; }
field() { python3 -c 'import json, sys; value = json.loads(sys.argv[1]).get(sys.argv[2], ""); print(value if isinstance(value, str) else json.dumps(value))' "$1" "$2" 2>/dev/null || true; }
wait_terminal() {
  local job=$1 state=''
  for _ in $(seq 1 900); do
    state=$(field "$(api GET "/api/jobs/$job" || true)" state)
    case $state in completed|failed|dead-letter|cancelled) printf '%s' "$state"; return 0 ;; esac
    sleep 1
  done
  printf '%s' "${state:-unknown}"
  return 1
}
require_completed() {
  local job final
  job=$(field "$1" job_id)
  [[ -n $job ]] || fail "$2 was not queued: $1"
  final=$(wait_terminal "$job") || fail "$2 job $job did not finish ($final)"
  [[ $final == completed ]] || fail "$2 job $job ended $final: $(field "$(api GET "/api/jobs/$job")" error)"
}
wait_ready() {
  for _ in $(seq 1 120); do
    curl --fail --silent --max-time 5 "$PANEL/readyz" >/dev/null && return 0
    sleep 1
  done
  fail "panel readiness did not recover: $(curl --silent --max-time 5 "$PANEL/readyz" || true)"
}
snapshot() { (cd "$public_root" && find . -type f -print0 | sort -z | xargs -0 -r sha256sum); }
# Minimal hosts may lack diffutils, so compare snapshots in the shell.
same() { [[ $(<"$1") == $(<"$2") ]]; }
write_state() {
  local label=$1 owner
  owner=$(stat -c %U:%G "$public_root")
  find "$public_root" -mindepth 1 -delete
  printf '<?php echo "SEALKILL_STATE_%s";\n' "$label" > "$public_root/index.php"
  mkdir -p "$public_root/assets"
  for i in $(seq 1 "$RESTORE_SMOKE_FILES"); do printf '%s %s\n' "$label" "$i" > "$public_root/assets/f$i.txt"; done
  chown -R "$owner" "$public_root"
}
probe() {
  local scheme=http port=80
  if [[ $webserver == caddy ]]; then scheme=https port=443; fi
  for _ in $(seq 1 30); do
    if curl --fail --silent --insecure --max-time 5 --resolve "$domain:$port:127.0.0.1" "$scheme://$domain/" | grep -Fq "SEALKILL_STATE_$1"; then
      return 0
    fi
    sleep 1
  done
  return 1
}

login
require_completed "$(api POST /api/sites "$(json site "$site" template php)")" "site creation"
stepanel_site_location "$site" || fail "could not resolve $site"
site_user=$(stat -c %U "$public_root")
write_state A
snapshot > "$work/backup.sha256"
require_completed "$(api POST /api/backups "{\"site\":\"$site\",\"include_databases\":false}")" "backup of state A"
backup=$(api GET "/api/backups?site=$site" | python3 -c 'import json, sys; items = json.load(sys.stdin).get("backups", []); print(items[0]["path"].rstrip("/").rsplit("/", 1)[-1] if items else "")')
[[ -n $backup ]] || fail 'the backup of state A is not listed'
write_state B
snapshot > "$work/before.sha256"
if (( site_placed )); then
  STEPANEL_UNSAFE_LAB=1 "$site_ctl" seal "$site" "$site_location_user" >/dev/null
else
  STEPANEL_UNSAFE_LAB=1 "$site_ctl" seal "$site" >/dev/null
fi
/usr/local/sbin/stepanel-vhostctl apply "$site" "$domain" >/dev/null
probe B || fail 'the site does not serve state B before the restore'

# Arm a one-shot wrapper: the first seal of this site starts the real helper,
# lets it run briefly, then kills the whole root broker unit.
mv "$site_ctl" "$site_ctl_real"
cat > "$site_ctl" <<EOF
#!/usr/bin/env bash
if [[ \$1 == seal && \$2 == $site && -f $fault ]]; then
  rm -f $fault
  $site_ctl_real "\$@" &
  sleep 0.3
  date -u +%FT%TZ > $killed_marker
  exec systemctl kill --signal=SIGKILL stepanel-root-broker.service
fi
exec $site_ctl_real "\$@"
EOF
chown root:root "$site_ctl"
chmod 0755 "$site_ctl"
rm -f "$killed_marker"
touch "$fault"

response=$(api POST /api/backups/restore-files "$(json site "$site" backup "$backup" confirm RESTORE_FILES)")
job=$(field "$response" job_id)
[[ -n $job ]] || fail "file restore was not queued: $response"
final=$(wait_terminal "$job") || fail "restore job $job did not finish ($final)"
[[ -f $killed_marker ]] || fail "the root broker was never killed during the seal (job ended $final)"
restore_site_ctl
for _ in $(seq 1 60); do systemctl is-active --quiet stepanel-root-broker.service && break; sleep 1; done
systemctl is-active --quiet stepanel-root-broker.service || fail 'the root broker did not restart'
wait_ready

snapshot > "$work/after.sha256"
if same "$work/after.sha256" "$work/backup.sha256"; then
  on_disk=A
elif same "$work/after.sha256" "$work/before.sha256"; then
  on_disk=B
else
  echo "files on disk: $(wc -l < "$work/after.sha256"); backed up: $(wc -l < "$work/backup.sha256"); before: $(wc -l < "$work/before.sha256")" >&2
  fail 'the site holds neither the backed-up nor the pre-restore tree'
fi
[[ $final != completed || $on_disk == A ]] || fail "the restore job completed but the site holds state $on_disk"
stray=$(find "$public_root" -xdev ! -user "$site_user" -print -quit)
[[ -z $stray ]] || fail "a file is not owned by the site account $site_user after the interrupted seal: $stray ($(stat -c %U "$stray"))"
probe "$on_disk" || fail "the site does not serve state $on_disk, which is on disk"
echo "interrupted seal: restore job $job ended $final; the site holds state $on_disk, owned entirely by $site_user"

require_completed "$(api POST /api/backups/restore-files "$(json site "$site" backup "$backup" confirm RESTORE_FILES)")" "fresh restore"
snapshot > "$work/fresh.sha256"
same "$work/fresh.sha256" "$work/backup.sha256" || fail 'a fresh restore did not produce the backed-up tree'
probe A || fail 'the site does not serve state A after the fresh restore'
echo 'restore helper interruption smoke passed: a root broker killed mid-seal after activation left a consistent, correctly owned site, and a fresh restore succeeded'
