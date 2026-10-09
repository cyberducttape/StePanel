#!/usr/bin/env bash
# End-to-end release pipeline on an installed host: Git checkout, a build in
# the rootless runner with a pinned image, artifact validation, atomic
# activation, and runner cleanup. On SELinux-enforcing hosts it also proves
# the activated release keeps the site's label (readable by the web server).
#
# Run after install-smoke.sh. Needs outbound HTTPS to github.com and the
# image registry. Skips (exit 77) when prerequisites are missing.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'release pipeline smoke must run as root' >&2; exit 77; }
command -v podman >/dev/null || { echo 'release pipeline smoke requires podman' >&2; exit 77; }
command -v python3 >/dev/null || { echo 'release pipeline smoke requires python3' >&2; exit 77; }

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${PIPELINE_SITE:=ci-smoke}"
: "${PIPELINE_REPOSITORY:=https://github.com/octocat/Hello-World.git}"
: "${PIPELINE_REF:=master}"
: "${PIPELINE_IMAGE:=ghcr.io/containerd/busybox@sha256:907ca53d7e2947e849b839b1cd258c98fd3916c60f2e6e70c30edbf741ab6754}"

site_root="/var/www/sites/$PIPELINE_SITE"
public="$site_root/public"
[[ -d $public ]] || { echo "site $PIPELINE_SITE has not been prepared" >&2; exit 77; }
hash=$(printf '%s' "$PIPELINE_SITE" | sha256sum); hash=${hash%% *}
prefix=${PIPELINE_SITE:0:18}; prefix=${prefix//_/-}
site_user="sp-${prefix}-${hash:0:8}"

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
cookies="$work/cookies.txt"
sleep $((31 - $(date +%s) % 30))
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
  --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
  --data-urlencode "totp=$totp" "$PANEL/login" >/dev/null
session=$(awk '$6 == "stepanel_session" {print $7}' "$cookies")
csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
[[ -n $session && -n $csrf ]] || { echo 'login did not issue session and CSRF cookies' >&2; exit 1; }

selinux=0
if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then selinux=1; fi
public_type_before=$( (( selinux )) && stat -c %C -- "$public" | cut -d: -f3 || true)

body=$(python3 - "$PIPELINE_SITE" "$PIPELINE_REPOSITORY" "$PIPELINE_REF" "$PIPELINE_IMAGE" <<'PY'
import json, sys
print(json.dumps({"site": sys.argv[1], "repository": sys.argv[2], "ref": sys.argv[3], "image": sys.argv[4],
                  "commands": ["cp /src/README /artifact/index.html", "printf 'built\\n' > /artifact/build.txt"]}))
PY
)
status=$(curl --silent --show-error --max-time 1800 -o "$work/response.json" -w '%{http_code}' \
  -H "Cookie: stepanel_session=$session; stepanel_csrf=$csrf" -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' --data "$body" "$PANEL/api/deployments/run")
response=$(cat "$work/response.json")
[[ $status == 2?? ]] || {
  echo "release pipeline returned HTTP $status: $response" >&2
  echo 'latest deployment record:' >&2
  curl --silent --max-time 10 -H "Cookie: stepanel_session=$session; stepanel_csrf=$csrf" "$PANEL/api/deployments?site=$PIPELINE_SITE" \
    | python3 -c 'import json, sys; records = json.load(sys.stdin).get("deployments", []); print(json.dumps(records[0] if records else {}, indent=1)[:1500])' >&2 || true
  exit 1
}
grep -q '"commit"' <<< "$response" || { echo "pipeline did not report a commit: $response" >&2; exit 1; }

# The activated release is the build's artifact.
grep -q 'Hello World' "$public/index.html" || { echo 'activated release does not contain the built index.html' >&2; exit 1; }
[[ $(cat "$public/build.txt") == built ]] || { echo 'activated release does not contain build output' >&2; exit 1; }
if (( selinux )); then
  for file in "$public" "$public/index.html" "$public/build.txt"; do
    type=$(stat -c %C -- "$file" | cut -d: -f3)
    [[ $type == "$public_type_before" ]] || { echo "activated $file is labelled $type, not $public_type_before" >&2; exit 1; }
  done
fi
# The runner left no scratch, runtime directory, or artifact behind.
[[ ! -e $site_root/.stepanel-runner-scratch ]] || { echo 'runner scratch was not cleaned up' >&2; exit 1; }
[[ ! -e /run/stepanel-runner-$site_user ]] || { echo 'runner runtime directory was not cleaned up' >&2; exit 1; }
[[ ! -e $site_root/.stepanel-artifact ]] || { echo 'build artifact was not moved into the release' >&2; exit 1; }
find "$site_root" -maxdepth 1 -name '.stepanel-release-*' -newer "$work" | grep -q . && { echo 'release staging was not cleaned up' >&2; exit 1; }

echo "release pipeline smoke passed ($PIPELINE_REPOSITORY@$PIPELINE_REF built and activated)"
