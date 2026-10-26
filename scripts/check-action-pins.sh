#!/usr/bin/env bash
set -Eeuo pipefail

workflow_dir=${1:-.github/workflows}
if ! command -v grep >/dev/null 2>&1; then
  echo 'grep is required to inspect GitHub workflow action references' >&2
  exit 2
fi
if [[ ! -d "$workflow_dir" ]]; then
  printf 'workflow directory does not exist: %s\n' "$workflow_dir" >&2
  exit 2
fi

if refs=$(grep -RhoE --include='*.yml' --include='*.yaml' 'uses:[[:space:]]*[^[:space:]#]+' "$workflow_dir" 2>&1); then
  grep_status=0
else
  grep_status=$?
fi
if (( grep_status > 1 )); then
  printf 'could not inspect GitHub workflow action references:\n%s\n' "$refs" >&2
  exit 2
fi
if (( grep_status == 1 )) || [[ -z "$refs" ]]; then
  printf 'no GitHub Action references found in %s\n' "$workflow_dir" >&2
  exit 1
fi

failed=0
while IFS= read -r match; do
  ref=${match#*uses:}
  ref=${ref#"${ref%%[![:space:]]*}"}
  [[ -n "$ref" ]] || continue
  # Local reusable workflows are versioned with the checked-out source tree,
  # not fetched from an external action repository.
  if [[ "$ref" == ./* ]]; then
    continue
  fi
  action=${ref%%@*}
  revision=${ref##*@}
  if [[ ! "$revision" =~ ^[a-f0-9]{40}$ ]]; then
    printf 'GitHub Action must be pinned to a full commit SHA: %s\n' "$action@$revision" >&2
    failed=1
  fi
done <<< "$refs"

if (( failed )); then
  exit 1
fi
echo "All third-party GitHub Actions in $workflow_dir are pinned to full commit SHAs."
