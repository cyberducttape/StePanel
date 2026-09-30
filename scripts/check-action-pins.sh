#!/usr/bin/env bash
set -Eeuo pipefail

workflow_dir=${1:-.github/workflows}
failed=0
while IFS= read -r ref; do
  [[ -n "$ref" ]] || continue
  action=${ref%%@*}
  revision=${ref##*@}
  if [[ ! "$revision" =~ ^[a-f0-9]{40}$ ]]; then
    printf 'GitHub Action must be pinned to a full commit SHA: %s\n' "$action@$revision" >&2
    failed=1
  fi
done < <(rg -o 'uses:[[:space:]]*[^[:space:]#]+' "$workflow_dir" | sed -E 's/^.*uses:[[:space:]]*//')

if (( failed )); then
  exit 1
fi
echo "All third-party GitHub Actions in $workflow_dir are pinned to full commit SHAs."
