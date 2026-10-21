#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
checker="$repo_root/scripts/check-action-pins.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/valid" "$tmp/invalid" "$tmp/empty" "$tmp/no-grep"
printf '%s\n' 'steps:' '  - uses: actions/checkout@0123456789abcdef0123456789abcdef01234567' > "$tmp/valid/workflow.yml"
printf '%s\n' 'steps:' '  - uses: actions/checkout@v4' > "$tmp/invalid/workflow.yml"

bash "$checker" "$tmp/valid"
if bash "$checker" "$tmp/invalid"; then
  echo 'un-pinned action unexpectedly passed validation' >&2
  exit 1
fi
if bash "$checker" "$tmp/empty"; then
  echo 'workflow directory with no action references unexpectedly passed validation' >&2
  exit 1
fi
if bash "$checker" "$tmp/missing"; then
  echo 'missing workflow directory unexpectedly passed validation' >&2
  exit 1
fi
if PATH="$tmp/no-grep" /bin/bash "$checker" "$tmp/valid"; then
  echo 'checker unexpectedly passed without grep' >&2
  exit 1
fi

echo 'Action-pin checker positive and negative cases passed.'
