#!/usr/bin/env bash
# Prove operator alert delivery on an installed host, end to end:
#
#   1. A real failed job (a Git deploy of a ref that does not exist) is
#      delivered as email over verified STARTTLS to an SMTP server whose
#      certificate chains to a lab CA added to the host trust store.
#   2. With the SMTP server stopped, a second failed job is not delivered,
#      and /api/health/operational reports the delivery failure.
#   3. After the SMTP server returns, the outbox retries and delivers the
#      second alert, and the failure clears.
#
# The webhook channel is not covered: its SSRF policy refuses loopback and
# private destinations by design, so it needs a separately hosted receiver.
#
# Changes /etc/ste-panel.env, /etc/hosts and the system trust store, and
# restores them on exit. Run only on a disposable --unsafe-lab install.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'operator alert smoke must run as root' >&2; exit 1; }
grep -q '^STEPANEL_UNSAFE_LAB="\?1"\?$' /etc/ste-panel.env || {
  echo 'refusing to run: /etc/ste-panel.env is not an --unsafe-lab install' >&2
  exit 1
}
for tool in openssl python3 curl; do command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }; done

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${ALERT_GIT_REPOSITORY:=https://github.com/octocat/Hello-World.git}"

smtp_host=alerts.stepanel-lab.test
smtp_port=2525
suffix=$(date -u +%H%M%S)
site=alert-smoke-$suffix
env_file=/etc/ste-panel.env
env_backup=$(mktemp)
hosts_backup=$(mktemp)
work=$(mktemp -d)
mail_dir=$work/mail
cookies=$work/cookies.txt
mkdir -p "$mail_dir"
cp -p "$env_file" "$env_backup"
cp -p /etc/hosts "$hosts_backup"

if [[ -d /etc/pki/ca-trust/source/anchors ]]; then
  ca_anchor=/etc/pki/ca-trust/source/anchors/stepanel-alert-smoke.crt
  trust_update=(update-ca-trust extract)
else
  ca_anchor=/usr/local/share/ca-certificates/stepanel-alert-smoke.crt
  trust_update=(update-ca-certificates --fresh)
fi

services=(stepanel.service)
if systemctl is-enabled --quiet stepanel-worker.service 2>/dev/null; then services+=(stepanel-worker.service); fi

smtp_pid=
fail() { echo "operator alert smoke: $*" >&2; exit 1; }
restart_panel() {
  systemctl restart "${services[@]}"
  for _ in $(seq 1 120); do
    curl --fail --silent --max-time 5 "$PANEL/readyz" >/dev/null && return 0
    sleep 1
  done
  fail "panel did not become ready: $(curl --silent --max-time 5 "$PANEL/readyz" || true)"
}
cleanup() {
  local status=$?
  [[ -z $smtp_pid ]] || kill "$smtp_pid" 2>/dev/null || true
  cp -p "$env_backup" "$env_file"
  cp -p "$hosts_backup" /etc/hosts
  rm -f "$ca_anchor"
  "${trust_update[@]}" >/dev/null 2>&1 || true
  systemctl restart "${services[@]}" || true
  rm -rf -- "$work" "$env_backup" "$hosts_backup"
  exit "$status"
}
trap cleanup EXIT

# --- Lab CA, server certificate, name resolution ------------------------------
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=StePanel alert smoke CA' \
  -keyout "$work/ca.key" -out "$work/ca.crt" \
  -addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj "/CN=$smtp_host" -keyout "$work/server.key" -out "$work/server.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\n' "$smtp_host" > "$work/server.ext"
openssl x509 -req -in "$work/server.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial \
  -days 1 -extfile "$work/server.ext" -out "$work/server.crt" >/dev/null 2>&1
install -m 0644 "$work/ca.crt" "$ca_anchor"
"${trust_update[@]}" >/dev/null
printf '127.0.0.1 %s\n' "$smtp_host" >> /etc/hosts

cat > "$work/smtp.py" <<'PY'
"""Minimal STARTTLS SMTP sink: refuses mail before TLS, stores each message."""
import os
import socketserver
import ssl
import sys
import uuid

port, cert, key, out = int(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
context = ssl.create_default_context(ssl.Purpose.CLIENT_AUTH)
context.minimum_version = ssl.TLSVersion.TLSv1_2
context.load_cert_chain(cert, key)


class Handler(socketserver.BaseRequestHandler):
    def send(self, line):
        self.sock.sendall((line + "\r\n").encode())

    def handle(self):
        self.sock, tls = self.request, False
        reader = self.sock.makefile("rb")
        self.send("220 alerts.stepanel-lab.test ESMTP alert smoke")
        while True:
            raw = reader.readline(4096)
            if not raw:
                return
            command = raw.decode("utf-8", "replace").strip()
            verb = command.upper()
            if verb.startswith(("EHLO", "HELO")):
                self.send("250-alerts.stepanel-lab.test")
                if not tls:
                    self.send("250-STARTTLS")
                self.send("250 8BITMIME")
            elif verb == "STARTTLS" and not tls:
                self.send("220 ready for TLS")
                self.sock = context.wrap_socket(self.sock, server_side=True)
                reader, tls = self.sock.makefile("rb"), True
            elif not tls and verb.startswith(("MAIL", "RCPT", "DATA")):
                self.send("530 STARTTLS required")
            elif verb.startswith(("MAIL FROM:", "RCPT TO:")):
                self.send("250 ok")
            elif verb == "DATA":
                self.send("354 end with .")
                lines = []
                while True:
                    line = reader.readline(65536)
                    if not line or line in (b".\r\n", b".\n"):
                        break
                    lines.append(line)
                stage = os.path.join(out, "." + uuid.uuid4().hex)
                with open(stage, "wb") as target:
                    target.writelines(lines)
                os.replace(stage, os.path.join(out, uuid.uuid4().hex + ".eml"))
                self.send("250 queued")
            elif verb == "QUIT":
                self.send("221 bye")
                return
            else:
                self.send("502 not implemented")


socketserver.ThreadingTCPServer.allow_reuse_address = True
with socketserver.ThreadingTCPServer(("127.0.0.1", port), Handler) as server:
    server.serve_forever()
PY
start_smtp() {
  python3 -I "$work/smtp.py" "$smtp_port" "$work/server.crt" "$work/server.key" "$mail_dir" &
  smtp_pid=$!
  for _ in $(seq 1 50); do
    (exec 3<>"/dev/tcp/127.0.0.1/$smtp_port") 2>/dev/null && return 0
    sleep 0.1
  done
  fail 'SMTP sink did not start'
}
stop_smtp() {
  kill "$smtp_pid"
  wait "$smtp_pid" 2>/dev/null || true
  smtp_pid=
}
start_smtp

# --- Configure email alerts --------------------------------------------------
sed -i '/^STEPANEL_ALERT_/d' "$env_file"
cat >> "$env_file" <<EOF
STEPANEL_ALERT_EMAIL_TO=operator@stepanel-lab.test
STEPANEL_ALERT_EMAIL_FROM=panel@stepanel-lab.test
STEPANEL_ALERT_SMTP_HOST=$smtp_host
STEPANEL_ALERT_SMTP_PORT=$smtp_port
EOF
restart_panel

# --- API helpers -----------------------------------------------------------
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
    curl --silent --show-error --max-time 60 -X "$method" -H "Cookie: $cookie_header" -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' --data "$body" "$PANEL$path"
  else
    curl --silent --show-error --max-time 60 -X "$method" -H "Cookie: $cookie_header" -H "X-CSRF-Token: $csrf" "$PANEL$path"
  fi
}
field() { python3 -c 'import json, sys; print(json.loads(sys.argv[1]).get(sys.argv[2], ""))' "$1" "$2" 2>/dev/null || true; }
wait_terminal() {
  local job=$1 state
  for _ in $(seq 1 900); do
    state=$(field "$(api GET "/api/jobs/$job")" state)
    case $state in completed|failed|dead-letter|cancelled) echo "$state"; return 0 ;; esac
    sleep 1
  done
  echo "${state:-unknown}"
  return 1
}
notification_field() {
  api GET /api/health/operational | python3 -c 'import json, sys; print(json.load(sys.stdin).get("operator_notifications", {}).get(sys.argv[1], ""))' "$1"
}
mail_for() { grep -lF "Job: $1" "$mail_dir"/*.eml 2>/dev/null | head -1; }
failing_deploy() {
  local response job state
  response=$(api POST /api/sites/git-deploy "{\"site\":\"$site\",\"repository\":\"$ALERT_GIT_REPOSITORY\",\"ref\":\"stepanel-alert-smoke-missing-$1\"}")
  job=$(field "$response" job_id)
  [[ -n $job ]] || fail "failing Git deploy was not queued: $response"
  state=$(wait_terminal "$job") || fail "failing Git deploy $job did not finish ($state)"
  [[ $state == failed || $state == dead-letter ]] || fail "Git deploy of a missing ref ended $state"
  echo "$job"
}

login
response=$(api POST /api/sites "{\"site\":\"$site\",\"template\":\"php\"}")
create_job=$(field "$response" job_id)
if [[ -n $create_job ]]; then
  state=$(wait_terminal "$create_job") || fail "site creation did not finish ($state)"
  [[ $state == completed ]] || fail "site creation ended $state"
fi

# --- 1. Delivery over verified STARTTLS --------------------------------------
job1=$(failing_deploy one)
for _ in $(seq 1 90); do [[ -n $(mail_for "$job1") ]] && break; sleep 1; done
message=$(mail_for "$job1")
[[ -n $message ]] || fail "no alert email arrived for failed job $job1"
grep -q '^Subject: StePanel operator job alert' "$message" || fail 'alert email has an unexpected subject'
echo "delivered: alert for failed job $job1 arrived over STARTTLS"

# --- 2. Outage: undelivered and visible --------------------------------------
stop_smtp
job2=$(failing_deploy two)
failed=0
for _ in $(seq 1 90); do
  failed=$(notification_field failed_deliveries)
  [[ $failed =~ ^[0-9]+$ ]] && (( failed >= 1 )) && break
  sleep 1
done
(( failed >= 1 )) || fail "operational health did not report the SMTP outage (failed_deliveries=$failed)"
[[ -z $(mail_for "$job2") ]] || fail 'an alert was recorded while the SMTP server was down'
echo "outage: alert for job $job2 undelivered; operational health reports $failed failed delivery(ies), pending $(notification_field pending)"

# --- 3. Recovery: retried and delivered --------------------------------------
start_smtp
for _ in $(seq 1 180); do [[ -n $(mail_for "$job2") ]] && break; sleep 1; done
[[ -n $(mail_for "$job2") ]] || fail "alert for job $job2 was not retried after the SMTP server returned"
for _ in $(seq 1 30); do
  [[ $(notification_field failed_deliveries) == 0 ]] && break
  sleep 1
done
[[ $(notification_field failed_deliveries) == 0 ]] || fail 'operational health still reports failed deliveries after recovery'
echo "recovered: alert for job $job2 delivered on retry; failed deliveries cleared"
echo 'operator alert smoke passed: STARTTLS delivery, outage visibility, and retry after recovery'
