#!/usr/bin/env bash
# Installed file manager helper smoke (ADR-0007): stepanel-filectl runs every
# operation as the hosting account's Unix user inside the site's document
# root, refuses traversal and symbolic links, and keeps permissions on
# replace. Runs on an installed lab host as root.
set -Eeuo pipefail

account=cismokefiles
site=ci-smoke-files
filectl=/usr/local/sbin/stepanel-filectl
userctl=/usr/local/sbin/stepanel-userctl
web_group=$(sed -n 's/^STEPANEL_WEB_GROUP="\([a-z_][a-z0-9_-]*\)"$/\1/p' /etc/ste-panel.env)

cleanup() {
  "$userctl" unplace "$site" "$account" >/dev/null 2>&1 || true
  "$userctl" account-delete "$account" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() { echo "filectl smoke: $*" >&2; exit 1; }
refused() { if "$filectl" "$@" </dev/null >/dev/null 2>&1; then fail "accepted: $*"; fi; }

"$userctl" account-ensure "$account" >/dev/null
"$userctl" place "$site" "$account" main public_html >/dev/null
docroot="/home/$account/public_html"
install -d -m 2750 -o "$account" -g "$web_group" "$docroot"

"$filectl" mkdir "$site" "$account" assets
printf 'body{}' | "$filectl" write "$site" "$account" assets/site.css
[[ $(stat -c %U "$docroot/assets/site.css") == "$account" ]] || fail 'written file is not owned by the account user'
[[ $(stat -c %G "$docroot/assets/site.css") == "$web_group" ]] || fail 'written file did not inherit the web group'
[[ $("$filectl" read "$site" "$account" assets/site.css) == 'body{}' ]] || fail 'read returned different bytes'
"$filectl" list "$site" "$account" assets | grep -Fq $'\tsite.css' || fail 'listing does not show the written file'

# A replaced file keeps its permissions.
chmod 0604 "$docroot/assets/site.css"
printf 'main{}' | "$filectl" write "$site" "$account" assets/site.css
[[ $(stat -c %a "$docroot/assets/site.css") == 604 ]] || fail 'replacing a file reset its permissions'
compgen -G "$docroot/assets/.stepanel-upload-*" >/dev/null && fail 'a temporary upload file was left behind'

"$filectl" rename "$site" "$account" assets/site.css style.css
[[ -f $docroot/style.css && ! -e $docroot/assets/site.css ]] || fail 'rename did not move the file'
refused rename "$site" "$account" assets assets/inner

# Traversal, absolute paths, links and foreign users are refused.
refused list "$site" "$account" ../..
refused read "$site" "$account" /etc/shadow
refused read "$site" "$account" assets/../../../etc/shadow
refused list "$site" root ''
refused delete "$site" "$account" ''
ln -s /etc "$docroot/escape"
refused list "$site" "$account" escape
refused read "$site" "$account" escape/passwd
printf 'x' | refused write "$site" "$account" escape/stepanel-smoke
[[ ! -e /etc/stepanel-smoke ]] || fail 'a write followed a link out of the document root'
"$filectl" delete "$site" "$account" escape
[[ -d /etc && ! -L $docroot/escape ]] || fail 'deleting a link did not remove only the link'

# The account user cannot reach another account's or root's files even
# without the helper's checks, because every operation runs as that user.
if runuser -u "$account" -- cat /etc/shadow >/dev/null 2>&1; then fail 'account user can read /etc/shadow'; fi

"$filectl" delete "$site" "$account" assets
[[ ! -e $docroot/assets ]] || fail 'delete left the directory behind'
echo 'filectl smoke passed'
