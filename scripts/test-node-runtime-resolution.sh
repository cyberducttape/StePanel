#!/usr/bin/env bash
# Exercise stepanel-appctl's Node.js runtime resolution: a managed Node app
# runs only the exact installed version, by absolute path, and never falls
# back to another node or npm on the host.
set -Eeuo pipefail
helper=deploy/integrations/stepanel-appctl
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

# Load only the resolver; runuser is replaced by a stub that runs the
# command as the current (unprivileged) user.
sed -n '/^# BEGIN resolve_node_runtime$/,/^# END resolve_node_runtime$/p' "$helper" > "$work/resolver.sh"
grep -q 'resolve_node_runtime()' "$work/resolver.sh" || { echo 'resolver not found in stepanel-appctl' >&2; exit 1; }
runuser() { [[ $1 == -u && $3 == -- ]] || return 99; shift 3; "$@"; }
# shellcheck source=/dev/null
. "$work/resolver.sh"

failures=0
# install VERSION [REPORTS]: a runtime whose node prints REPORTS.
install() {
  local dir="$work/nvm/versions/node/v$1"
  mkdir -p "$dir/bin" "$dir/lib/node_modules/npm/bin"
  printf '#!/bin/sh\necho %s\n' "${2:-v$1}" > "$dir/bin/node"
  chmod 0755 "$dir/bin/node"
  printf '// npm\n' > "$dir/lib/node_modules/npm/bin/npm-cli.js"
  chmod 0644 "$dir/lib/node_modules/npm/bin/npm-cli.js"
}
expect_ok() {
  local out
  if ! out=$(resolve_node_runtime "$work/nvm" "$1" nobody 2>&1) || [[ $out != "$work/nvm/versions/node/v$1" ]]; then
    echo "FAIL: $2: $out" >&2; failures=$((failures + 1))
  fi
}
expect_refused() {
  local out
  if out=$(resolve_node_runtime "$work/nvm" "$1" nobody 2>&1); then
    echo "FAIL: $2 was accepted ($out)" >&2; failures=$((failures + 1))
  elif [[ -n ${3:-} && $out != *"$3"* ]]; then
    echo "FAIL: $2: unexpected refusal: $out" >&2; failures=$((failures + 1))
  fi
}

mkdir -p "$work/nvm"
expect_refused 20.11.1 'a missing version' 'is not installed'
install 20.11.1
expect_ok 20.11.1 'an installed version'
expect_refused '20.11' 'a malformed version' 'invalid'

install 18.19.0 v20.11.1
expect_refused 18.19.0 'a binary reporting another version' 'reports v20.11.1'

install 18.20.0; printf '#!/bin/sh\nexit 1\n' > "$work/nvm/versions/node/v18.20.0/bin/node"
expect_refused 18.20.0 'a binary that does not run' 'does not run'

install 16.20.2; rm "$work/nvm/versions/node/v16.20.2/lib/node_modules/npm/bin/npm-cli.js"
expect_refused 16.20.2 'an incomplete install (no npm)' 'incomplete'

install 16.20.1; chmod 0644 "$work/nvm/versions/node/v16.20.1/bin/node"
expect_refused 16.20.1 'a non-executable node' 'no executable'

install 16.20.0; chmod 0777 "$work/nvm/versions/node/v16.20.0/bin/node"
expect_refused 16.20.0 'a world-writable node' 'unsafe'

install 16.19.0; rm "$work/nvm/versions/node/v16.19.0/bin/node"; ln -s /bin/true "$work/nvm/versions/node/v16.19.0/bin/node"
expect_refused 16.19.0 'a symlinked node' 'unsafe'

# A failed runtime upgrade that left the version directory half-written
# (bin/ only) is refused, and the previously working version still resolves.
mkdir -p "$work/nvm/versions/node/v22.1.0/bin"
expect_refused 22.1.0 'a half-written upgrade' 'incomplete'
expect_ok 20.11.1 'the previous version after a failed upgrade'

# The generated unit runs the resolved runtime by absolute path, with no
# shell, nvm, or PATH lookup that could select another node.
grep -Fq 'ExecStart=$node_bin $npm_cli start' "$helper" || { echo 'FAIL: the unit does not run the resolved node by absolute path' >&2; failures=$((failures + 1)); }
! grep -Fq 'nvm use' "$helper" || { echo 'FAIL: the unit still selects Node through nvm' >&2; failures=$((failures + 1)); }

(( failures == 0 )) || exit 1
echo 'Node.js runtime resolution contract passed'
