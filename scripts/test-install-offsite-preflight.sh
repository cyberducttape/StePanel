#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../deploy/lib/install-offsite-preflight.sh
. "$ROOT_DIR/deploy/lib/install-offsite-preflight.sh"

tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
config="$tmp/rclone.conf"
printf '[remote]\ntype = s3\n' > "$config"

cat > "$tmp/rclone" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
case "${1:-}" in
  listremotes)
    if [[ ${RCLONE_TEST_MODE:-ok} == fail-list ]]; then
      exit 1
    fi
    [[ ${RCLONE_TEST_MODE:-ok} == empty ]] || printf 'testremote:\n'
    ;;
  config)
    [[ ${2:-} == file && ${RCLONE_TEST_MODE:-ok} != fail-config ]]
    printf '%s\n' "$RCLONE_TEST_CONFIG"
    ;;
  *) exit 64 ;;
esac
SH
chmod 0755 "$tmp/rclone"

result=$(RCLONE_TEST_CONFIG="$config" install_preflight_offsite "$tmp/rclone")
[[ $result == *'1 remote(s)'* && $result == *'wizard will verify'* ]]

for mode in fail-list fail-config; do
  if RCLONE_TEST_MODE=$mode RCLONE_TEST_CONFIG="$config" \
    install_preflight_offsite "$tmp/rclone" >/dev/null 2>&1; then
    echo "rclone preflight unexpectedly accepted $mode" >&2
    exit 1
  fi
done

empty_result=$(RCLONE_TEST_MODE=empty RCLONE_TEST_CONFIG="$tmp/no-config-yet" install_preflight_offsite "$tmp/rclone")
[[ $empty_result == *'no remote yet'* && $empty_result == *'after the platform support check'* ]]

if RCLONE_TEST_CONFIG="$tmp/missing.conf" \
  install_preflight_offsite "$tmp/rclone" >/dev/null 2>&1; then
  echo 'rclone preflight accepted a missing config file' >&2
  exit 1
fi

ln -s "$config" "$tmp/rclone-link.conf"
if RCLONE_TEST_CONFIG="$tmp/rclone-link.conf" \
  install_preflight_offsite "$tmp/rclone" >/dev/null 2>&1; then
  echo 'rclone preflight accepted a symlink config file' >&2
  exit 1
fi

truncate -s 1048577 "$config"
if RCLONE_TEST_CONFIG="$config" \
  install_preflight_offsite "$tmp/rclone" >/dev/null 2>&1; then
  echo 'rclone preflight accepted a config larger than 1 MiB' >&2
  exit 1
fi

echo 'installer offsite preflight contract passed'
