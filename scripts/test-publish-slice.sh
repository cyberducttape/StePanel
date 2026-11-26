#!/usr/bin/env bash
# Contract for publish_slice in stepanel-appctl: an unchanged slice is left
# alone without a daemon-reload; a changed slice, or one systemd already needs
# to reload, is published and reloaded.
set -Eeuo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
fail() { echo "publish_slice contract: $*" >&2; exit 1; }

awk '/^publish_slice\(\) \{/,/^\}/' deploy/integrations/stepanel-appctl > "$work/publish_slice.sh"
grep -q 'systemctl daemon-reload' "$work/publish_slice.sh" || fail 'publish_slice was not found in stepanel-appctl'

reloads=$work/reloads
need_reload=no
# Stubs: count reloads, report NeedDaemonReload, and skip root-only chown.
systemctl() {
  case $1 in
    daemon-reload) echo reload >> "$reloads" ;;
    show) echo "$need_reload" ;;
  esac
}
chown() { :; }
# shellcheck source=/dev/null
. "$work/publish_slice.sh"

slice=$work/stepanel-site-demo.slice
count() { [[ -f $reloads ]] && wc -l < "$reloads" | tr -d ' ' || echo 0; }

printf '[Slice]\nCPUQuota=100%%\n' | publish_slice "$slice"
[[ $(count) == 1 ]] || fail "a new slice did not reload (reloads=$(count))"
printf '[Slice]\nCPUQuota=100%%\n' | publish_slice "$slice"
[[ $(count) == 1 ]] || fail "an unchanged slice reloaded (reloads=$(count))"
need_reload=yes
printf '[Slice]\nCPUQuota=100%%\n' | publish_slice "$slice"
[[ $(count) == 2 ]] || fail "an unchanged slice systemd needs to reload was skipped (reloads=$(count))"
need_reload=no
printf '[Slice]\nCPUQuota=200%%\n' | publish_slice "$slice"
[[ $(count) == 3 ]] || fail "a changed slice did not reload (reloads=$(count))"
grep -qx 'CPUQuota=200%' "$slice" || fail 'the changed slice was not published'
[[ $(stat -c %a "$slice") == 644 ]] || fail 'the slice is not 0644'
leftover=$(find "$work" -maxdepth 1 -name 'stepanel-site-demo.slice.*' | head -1)
[[ -z $leftover ]] || fail "a staged slice was left behind: $leftover"
echo 'publish_slice contract passed'
