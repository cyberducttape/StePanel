#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

smoke=.github/workflows/install-smoke.yml
release=.github/workflows/release.yml
preflight=.github/workflows/release-preflight.yml

require_fixed() {
  local file=$1 needle=$2
  if ! grep -Fq "$needle" "$file"; then
    echo "$file is missing required release-preflight contract: $needle" >&2
    exit 1
  fi
}

require_fixed "$smoke" '      release_tag:'
require_fixed "$smoke" '      - name: Prepare exact local release tag'
require_fixed "$smoke" 'git tag "$RELEASE_TAG" "$GITHUB_SHA"'
require_fixed "$smoke" "args: \${{ inputs.release_tag != '' && 'release --skip=publish --clean' || 'release --snapshot --clean' }}"
require_fixed "$smoke" 'GORELEASER_CURRENT_TAG: ${{ inputs.release_tag }}'
require_fixed "$release" 'release_tag: ${{ github.ref_name }}'

require_fixed "$preflight" '  workflow_dispatch:'
require_fixed "$preflight" '      release_tag:'
require_fixed "$preflight" '  contents: read'
require_fixed "$preflight" 'release_tag: ${{ inputs.release_tag }}'
require_fixed "$preflight" 'args: release --skip=publish --clean'
require_fixed "$preflight" 'bash scripts/verify-release-artifacts.sh dist/'
require_fixed "$preflight" 'retention-days: 30'
require_fixed "$preflight" 'make release-check'
require_fixed "$preflight" 'git tag "$RELEASE_TAG" "$GITHUB_SHA"'

# This workflow must never acquire publication permissions or invoke the
# publishing form of GoReleaser. Keep candidate validation safe to dispatch.
if grep -Eq 'contents:[[:space:]]*write|packages:[[:space:]]*write|id-token:[[:space:]]*write|attestations:[[:space:]]*write|args:[[:space:]]*release --clean([[:space:]]|$)|softprops/action-gh-release|gh release create' "$preflight"; then
  echo "$preflight contains a release-publication capability" >&2
  exit 1
fi

echo 'release preflight workflow contract passed'
