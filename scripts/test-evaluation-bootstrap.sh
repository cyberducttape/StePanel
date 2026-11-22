#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
repo="$tmp/repo"
mkdir -p "$repo/scripts" "$tmp/bin" "$tmp/build"
cp "$ROOT_DIR/Makefile" "$repo/Makefile"
cp "$ROOT_DIR/scripts/evaluate-local.sh" "$repo/scripts/evaluate-local.sh"
cp "$ROOT_DIR/scripts/evaluate.sh" "$repo/scripts/evaluate.sh"
cp "$ROOT_DIR/scripts/go-toolchain.sh" "$repo/scripts/go-toolchain.sh"
printf 'module evaluate-test\n\ntoolchain go1.26.11\n' > "$repo/go.mod"

cat > "$tmp/bin/go" <<'GO'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ ${1:-} == version ]]; then
  echo 'go version go1.26.11 linux/amd64'
  exit 0
fi
printf '%s\n' "$*" >> "$EVALUATION_GO_LOG"
output=
while (($#)); do
  if [[ $1 == -o ]]; then output=$2; shift 2; else shift; fi
done
[[ -n $output ]]
cat > "$output" <<'BIN'
#!/usr/bin/env bash
echo 'evaluation binary started'
BIN
chmod 0755 "$output"
GO
chmod 0755 "$tmp/bin/go"

output=$(cd "$repo" && EVALUATION_GO_LOG="$tmp/go.log" TMPDIR="$tmp/build" \
  make evaluate GO="$tmp/bin/go" 2>&1)
grep -Fq 'evaluation binary started' <<<"$output"
grep -Fq './cmd/stepanel' "$tmp/go.log"
grep -Fq 'Starting local StePanel evaluation at http://127.0.0.1:8080' <<<"$output"
[[ -z $(find "$tmp/build" -mindepth 1 -maxdepth 1 -print -quit) ]]

echo 'Local evaluation builds through the pinned-toolchain bootstrap and cleans its temporary compiler.'
