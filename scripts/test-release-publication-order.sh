#!/usr/bin/env bash
set -euo pipefail

workflow=.github/workflows/release.yml
line_of() {
  local needle=$1
  local line
  line=$(grep -nF "$needle" "$workflow" | head -n 1 | cut -d: -f1 || true)
  if [[ -z $line ]]; then
    echo "release workflow is missing: $needle" >&2
    return 1
  fi
  printf '%s\n' "$line"
}
line_of_exact() {
  local value=$1
  local line
  line=$(grep -nFx "      - name: $value" "$workflow" | cut -d: -f1 || true)
  if [[ -z $line ]]; then
    echo "release workflow is missing exact step: $value" >&2
    return 1
  fi
  printf '%s\n' "$line"
}

preflight=$(line_of 'name: Build and verify release artifacts before publishing')
preflight_build_args=$(line_of 'args: release --skip=publish --clean')
preflight_evidence=$(line_of 'name: Generate exact-source release evidence for preflight')
preflight_sbom=$(line_of 'name: Generate preflight SBOM')
preflight_verify=$(line_of 'name: Verify release artifacts before publishing')
publish=$(line_of 'name: Build and publish release artifacts with GoReleaser')
publish_args=$(line_of 'args: release --clean')
published_verify=$(line_of_exact 'Verify release artifacts')
attest=$(line_of 'name: Attest release provenance')

if (( preflight >= preflight_build_args ||
      preflight_build_args >= preflight_evidence ||
      preflight_evidence >= preflight_sbom ||
      preflight_sbom >= preflight_verify ||
      preflight_verify >= publish ||
      publish >= publish_args ||
      publish_args >= published_verify ||
      published_verify >= attest )); then
  echo 'release artifacts must be built and verified before any publication or attestation' >&2
  exit 1
fi

echo 'release publication ordering contract passed'
