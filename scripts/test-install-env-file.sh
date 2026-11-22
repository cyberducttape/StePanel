#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
helper="$ROOT_DIR/deploy/lib/install-env-file.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
real_stat=$(command -v stat)
cat > "$tmp/bin/stat" <<'EOF'
#!/usr/bin/env bash
if [[ ${1:-} == -c && ${2:-} == %u ]]; then
  printf '%s\n' "${TEST_STAT_OWNER:-0}"
else
  exec "$REAL_STAT" "$@"
fi
EOF
chmod 0700 "$tmp/bin/stat"
export PATH="$tmp/bin:$PATH" REAL_STAT="$real_stat"

valid="$tmp/valid.env"
cat > "$valid" <<'EOF'
# Generated setup settings
STEPANEL_TEST_TEXT="one\\two\"three"
STEPANEL_TEST_OVERRIDE="from-file"
RCLONE_CONFIG="/root/.config/rclone/rclone.conf"
EOF
chmod 0600 "$valid"
loaded=$(STEPANEL_TEST_OVERRIDE=from-environment bash -c '
  source "$1"
  load_env_file "$2" private
  printf "%s\n%s\n%s" "$STEPANEL_TEST_TEXT" "$STEPANEL_TEST_OVERRIDE" "$RCLONE_CONFIG"
' bash "$helper" "$valid")
expected=$(printf 'one\\two"three\nfrom-environment\n/root/.config/rclone/rclone.conf')
[[ $loaded == "$expected" ]]

expect_rejected() {
  local name=$1 content=$2 expected_error=$3 message
  local file="$tmp/$name.env"
  printf '%s' "$content" > "$file"
  chmod 0600 "$file"
  if message=$(bash -c 'source "$1"; load_env_file "$2" private' bash "$helper" "$file" 2>&1); then
    echo "$name malformed settings were accepted" >&2
    exit 1
  fi
  grep -Fq "$expected_error" <<<"$message" || {
    printf '%s\n' "$message" >&2
    echo "$name did not report $expected_error" >&2
    exit 1
  }
}

expect_rejected truncated 'STEPANEL_PANEL_HOSTNAME="example.org' 'unterminated quoted value'
expect_rejected trailing 'STEPANEL_PANEL_HOSTNAME="example.org"suffix' 'trailing characters'
expect_rejected escape 'STEPANEL_PANEL_HOSTNAME="bad\qescape"' 'unsupported escape'
expect_rejected dangling 'STEPANEL_PANEL_HOSTNAME="bad\' 'invalid escape'
expect_rejected entry 'NOT_A_SETTING="value"' 'unsupported entry'

chmod 0644 "$valid"
if message=$(bash -c 'source "$1"; load_env_file "$2" private' bash "$helper" "$valid" 2>&1); then
  echo 'world-readable secret settings were accepted' >&2
  exit 1
fi
grep -Fq 'must not be readable by other users' <<<"$message"
chmod 0600 "$valid"
if message=$(TEST_STAT_OWNER=1000 bash -c 'source "$1"; load_env_file "$2" private' bash "$helper" "$valid" 2>&1); then
  echo 'unprivileged-owned settings were accepted' >&2
  exit 1
fi
grep -Fq 'must be root-owned' <<<"$message"
ln -s "$valid" "$tmp/linked.env"
if message=$(bash -c 'source "$1"; load_env_file "$2" private' bash "$helper" "$tmp/linked.env" 2>&1); then
  echo 'symlinked settings were accepted' >&2
  exit 1
fi
grep -Fq 'must be a regular file' <<<"$message"

echo 'installer settings parser contract passed'
