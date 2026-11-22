#!/usr/bin/env bash
set -Eeuo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
fake_repo="$tmp/repo"
fake_bin="$tmp/bin"
mkdir -p "$fake_repo/scripts" "$fake_bin"
cp "$repo/scripts/install-guided.sh" "$fake_repo/scripts/install-guided.sh"
touch "$fake_repo/go.mod" "$fake_repo/install.sh"
chmod 0755 "$fake_repo/install.sh"

cat > "$fake_bin/go" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$*" >> "$GO_LOG"
output=
while (($#)); do
  if [[ $1 == -o ]]; then
    output=$2
    shift 2
  else
    shift
  fi
done
[[ -n $output ]]
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "$output"
chmod 0755 "$output"
SH
cat > "$fake_bin/sudo" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$*" > "$SUDO_LOG"
SH
chmod 0755 "$fake_bin/go" "$fake_bin/sudo"

GO_LOG="$tmp/go.log" SUDO_LOG="$tmp/sudo.log" GO=go SUDO=sudo PATH="$fake_bin:$PATH" \
  bash "$fake_repo/scripts/install-guided.sh" --take-over-host

[[ -x $fake_repo/stepanel && -x $fake_repo/stepanel-root ]]
grep -Fq './cmd/stepanel' "$tmp/go.log"
grep -Fq './cmd/stepanel-root' "$tmp/go.log"
[[ $(<"$tmp/sudo.log") == './install.sh --guided --take-over-host' ]]
! grep -Fq 'sudo' "$tmp/go.log"

if GO_LOG="$tmp/invalid-go.log" SUDO_LOG="$tmp/invalid-sudo.log" GO=go SUDO=sudo PATH="$fake_bin:$PATH" \
  bash "$fake_repo/scripts/install-guided.sh" --unsafe-lab >/dev/null 2>&1; then
  echo 'bootstrap accepted an unsupported installer option' >&2
  exit 1
fi
[[ ! -e $tmp/invalid-go.log && ! -e $tmp/invalid-sudo.log ]]

echo 'Guided source installer bootstrap tests passed.'
