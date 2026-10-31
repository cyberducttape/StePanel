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

# Directory enumeration is capped at the source, not after materializing a
# root-owned temporary listing. A directory twice the advertised limit must
# produce only the bounded 5,001-record helper response within its deadline.
"$filectl" mkdir "$site" "$account" large-list
runuser -u "$account" -- python3 - "$docroot/large-list" <<'PY'
import os
import sys
root = sys.argv[1]
for index in range(10000):
    fd = os.open(os.path.join(root, f"entry-{index:05d}"), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(fd)
PY
large_listing=$(timeout 10 "$filectl" list "$site" "$account" large-list) || fail 'large directory listing exceeded its execution deadline'
large_listing_lines=$(printf '%s\n' "$large_listing" | wc -l)
[[ $large_listing_lines -eq 5001 ]] || fail "large listing emitted $large_listing_lines records, want the 5,001-record truncation bound"
(( ${#large_listing} < 2 * 1024 * 1024 )) || fail 'large listing exceeded the bounded output budget'

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

# Interpose a symlink swap after helper validation but immediately before the
# site-user process starts. The helper must resolve every component from its
# opened document-root descriptor, so none of these requests may reach the
# account-owned sibling directory outside public_html.
race_bin=$(mktemp -d)
outside="/home/$account/filectl-race-outside"
install -d -m 0750 -o "$account" -g "$web_group" "$outside"
printf 'outside-sentinel' >"$outside/secret"
printf 'delete-sentinel' >"$outside/victim"
printf 'rename-sentinel' >"$outside/rename-source"
install -d -m 0750 -o "$account" -g "$web_group" "$docroot/race"
printf 'inside-sentinel' >"$docroot/race/secret"
printf 'inside-delete' >"$docroot/race/victim"
printf 'inside-rename' >"$docroot/race/rename-source"
cat >"$race_bin/runuser" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ -n ${STEPANEL_FILECTL_SWAP_PATH:-} && ! -e ${STEPANEL_FILECTL_SWAP_MARKER:-} ]]; then
  mv -- "$STEPANEL_FILECTL_SWAP_PATH" "$STEPANEL_FILECTL_SWAP_PATH.saved"
  ln -s ../filectl-race-outside "$STEPANEL_FILECTL_SWAP_PATH"
  : >"$STEPANEL_FILECTL_SWAP_MARKER"
fi
exec /usr/sbin/runuser "$@"
SH
chmod 0755 "$race_bin/runuser"

assert_swap_refused() {
  local marker="$race_bin/swapped"
  if PATH="$race_bin:/usr/sbin:/usr/bin:/bin" \
    STEPANEL_FILECTL_SWAP_PATH="$docroot/race" \
    STEPANEL_FILECTL_SWAP_MARKER="$marker" \
    "$filectl" "$@" </dev/null >/dev/null 2>&1; then
    fail "symlink swap escaped document root: $*"
  fi
  [[ -L $docroot/race && -d $docroot/race.saved ]] || fail "swap interposer did not run: $*"
  rm -- "$docroot/race"
  mv -- "$docroot/race.saved" "$docroot/race"
  rm -f -- "$marker"
}

assert_swap_refused read "$site" "$account" race/secret
[[ $(cat "$outside/secret") == outside-sentinel ]] || fail 'raced read changed outside fixture'
assert_swap_refused write "$site" "$account" race/new-file
[[ ! -e $outside/new-file ]] || fail 'raced upload escaped document root'
assert_swap_refused mkdir "$site" "$account" race/new-directory
[[ ! -e $outside/new-directory ]] || fail 'raced mkdir escaped document root'
assert_swap_refused delete "$site" "$account" race/victim
[[ $(cat "$outside/victim") == delete-sentinel ]] || fail 'raced delete escaped document root'
assert_swap_refused rename "$site" "$account" race/rename-source race/rename-destination
[[ $(cat "$outside/rename-source") == rename-sentinel && ! -e $outside/rename-destination ]] || fail 'raced rename escaped document root'
assert_swap_refused list "$site" "$account" race
rm -rf -- "$race_bin"

# The account user cannot reach another account's or root's files even
# without the helper's checks, because every operation runs as that user.
if runuser -u "$account" -- cat /etc/shadow >/dev/null 2>&1; then fail 'account user can read /etc/shadow'; fi

"$filectl" delete "$site" "$account" assets
[[ ! -e $docroot/assets ]] || fail 'delete left the directory behind'
echo 'filectl smoke passed'
