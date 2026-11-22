#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/repo/scripts"
cp "$ROOT_DIR/scripts/evaluate.sh" "$tmp/repo/scripts/evaluate.sh"
ROOT_DIR="$tmp/repo"
binary="$tmp/fake-stepanel"
report="$tmp/environment"
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
