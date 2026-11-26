#!/usr/bin/env bash
# Contract for deploy/integrations/stepanel-driftctl: a recorded manifest
# reports no drift, and each kind of change is reported by path and reason.
set -Eeuo pipefail
cd "$(dirname "$0")/.."
driftctl=deploy/integrations/stepanel-driftctl
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
fail() { echo "driftctl contract: $*" >&2; exit 1; }

mkdir -p "$work/etc" "$work/state"
printf 'a\n' > "$work/etc/unit.service"; chmod 0644 "$work/etc/unit.service"
printf 'secret\n' > "$work/etc/key"; chmod 0600 "$work/etc/key"
printf 'x\n' > "$work/etc/gone"
ln -s unit.service "$work/etc/link"
manifest=$work/etc/managed.manifest
status=$work/state/status

bash "$driftctl" record "$manifest" "$work/etc/unit.service" "$work/etc/key" "$work/etc/gone" "$work/etc/link" "$work/etc/never-existed" "$work/etc/key"
[[ $(stat -c %a "$manifest") == 600 ]] || fail 'manifest is not private'
[[ $(grep -vc '^#' "$manifest") == 4 ]] || fail "expected 4 recorded files (absent and duplicate paths skipped): $(cat "$manifest")"
bash "$driftctl" check "$manifest" "$status" >/dev/null || fail 'a freshly recorded manifest reported drift'
grep -qx 'drifted=0' "$status" && grep -qx 'files=4' "$status" || fail "clean status wrong: $(cat "$status")"
[[ $(stat -c %a "$status") == 644 ]] || fail 'status is not readable by the panel'

printf 'b\n' > "$work/etc/unit.service"
chmod 0644 "$work/etc/key" # mode only
rm "$work/etc/gone"
ln -sfn key "$work/etc/link"
if bash "$driftctl" check "$manifest" "$status" 2>/dev/null; then fail 'drift was not reported by the exit status'; fi
grep -qx 'drifted=4' "$status" || fail "drift count wrong: $(cat "$status")"
for expected in "unit.service	content" "key	mode" "gone	missing" "link	content"; do
  grep -q "^drift	$work/etc/$expected\$" "$status" || fail "missing drift line '$expected': $(cat "$status")"
done
grep -q secret "$status" && fail 'status leaked file contents'

if bash "$driftctl" check "$work/etc/absent.manifest" "$status" 2>/dev/null; then fail 'a missing manifest passed'; fi
grep -qx 'manifest=missing' "$status" || fail 'a missing manifest was not reported'
if bash "$driftctl" record "$manifest" "relative/path" 2>/dev/null; then fail 'a relative path was recorded'; fi
if bash "$driftctl" record "$manifest" "$work/etc/bad path" 2>/dev/null; then fail 'a path with a space was recorded'; fi
echo 'driftctl contract passed'
