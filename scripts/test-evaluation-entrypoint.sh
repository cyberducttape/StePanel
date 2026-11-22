#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
grep -Fq '| Preview StePanel locally, no host setup | [Run it locally](#local-evaluation) |' "$ROOT_DIR/README.md" || {
  echo 'README quick-start does not point local preview users to the evaluation flow' >&2
  exit 1
}
evaluation_docs=$(awk '
  /^### Local evaluation$/ { in_evaluation = 1 }
  /^### / && in_evaluation && $0 != "### Local evaluation" { exit }
  in_evaluation { print }
' "$ROOT_DIR/README.md")
grep -Fq 'Requirements: Git and either Go 1.21 or later' <<<"$evaluation_docs" || {
  echo 'README local evaluation instructions omit the supported compiler/bootstrap prerequisites' >&2
  exit 1
}
grep -Fq 'make evaluate` downloads the repository-pinned Go 1.26.11 toolchain' <<<"$evaluation_docs" || {
  echo 'README local evaluation instructions omit the temporary toolchain bootstrap behavior' >&2
  exit 1
}
grep -Fxq 'make evaluate' <<<"$evaluation_docs" || {
  echo 'README local evaluation instructions omit the one-command launcher' >&2
  exit 1
}
development_docs=$(awk '
  /^### Local development$/ { in_development = 1 }
  /^### / && in_development && $0 != "### Local development" { exit }
  in_development { print }
' "$ROOT_DIR/README.md")
grep -Fq "development commands such as \`make check\` and \`go run\` require an installed Go" <<<"$development_docs" || {
  echo 'README must distinguish the local preview bootstrap from full development toolchain requirements' >&2
  exit 1
}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/repo/scripts"
cp "$ROOT_DIR/scripts/evaluate.sh" "$tmp/repo/scripts/evaluate.sh"
ROOT_DIR="$tmp/repo"
binary="$tmp/fake-stepanel"
report="$tmp/environment"
mkdir -p "$tmp/bin"
cat > "$tmp/bin/make" <<'EOF'
#!/usr/bin/env bash
printf 'unexpected make invocation\n' > "$EVALUATION_TEST_MAKE_MARKER"
exit 99
EOF
chmod 0700 "$tmp/bin/make"
if EVALUATION_TEST_MAKE_MARKER="$tmp/make-invoked" PATH="$tmp/bin:$PATH" \
  bash "$ROOT_DIR/scripts/evaluate.sh" "$tmp/missing-stepanel" >"$tmp/missing-output" 2>&1; then
  echo 'missing evaluation binary was accepted' >&2
  exit 1
fi
grep -Fq 'binary is not executable' "$tmp/missing-output"
grep -Fq "run \`make evaluate\` from the checkout" "$tmp/missing-output"
[[ ! -e $tmp/make-invoked ]] || {
  echo 'evaluation error message unexpectedly executed make' >&2
  exit 1
}
cat > "$binary" <<'EOF'
#!/usr/bin/env bash
env | sort > "$EVALUATION_TEST_REPORT"
EOF
chmod 0700 "$binary"

EVALUATION_TEST_REPORT=$report \
STEPANEL_ENV=production \
STEPANEL_DEVELOPMENT_ADMIN=0 \
STEPANEL_LISTEN=0.0.0.0:443 \
STEPANEL_ADMIN_PASSWORD=should-not-leak \
bash "$ROOT_DIR/scripts/evaluate.sh" "$binary" > "$tmp/output"

grep -Fxq 'STEPANEL_ENV=development' "$report"
grep -Fxq 'STEPANEL_DEVELOPMENT_ADMIN=1' "$report"
grep -Fxq 'STEPANEL_LISTEN=127.0.0.1:8080' "$report"
audit_key=$(sed -n 's/^STEPANEL_AUDIT_KEY=//p' "$report")
[[ $audit_key =~ ^[[:xdigit:]]{64}$ ]]
if grep -Eq 'should-not-leak|STEPANEL_ADMIN_PASSWORD=' "$report"; then
  echo 'evaluation inherited a production credential' >&2
  exit 1
fi
grep -Fq 'Do not expose this listener or use customer data.' "$tmp/output"
[[ $(stat -c %a "$ROOT_DIR/data/.evaluation/audit.key") == 600 ]]
EVALUATION_TEST_REPORT=$report \
STEPANEL_AUDIT_KEY=inherited-key \
bash "$ROOT_DIR/scripts/evaluate.sh" "$binary" > "$tmp/restart-output"
[[ $(sed -n 's/^STEPANEL_AUDIT_KEY=//p' "$report") == "$audit_key" ]]

echo 'local evaluation entrypoint contract passed'
