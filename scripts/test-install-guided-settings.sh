#!/usr/bin/env bash
set -Eeuo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
uid=$(id -u)
source "$repo/deploy/lib/install-env-file.sh"

# Promotion runs for real against files owned by the test user; the target
# owner is the test user because an unprivileged test cannot chown to root.
secure() { SUDO_UID=$uid INSTALL_SETTINGS_TARGET_UID=$uid secure_install_settings_file "$@"; }
refuses() {
  local reason=$1; shift
  if (secure "$@") 2>/dev/null; then
    echo "settings promotion accepted $reason" >&2
    exit 1
  fi
}

# Both a manually generated and a guided-setup file pass through the same
# promotion step and end up private.
for origin in direct-setup guided-setup; do
  file="$tmp/$origin.env"
  printf 'STEPANEL_PANEL_HOSTNAME="example.test"\n' > "$file"
  chmod 0600 "$file"
  secure "$file"
  [[ $(stat -c '%u %a %h' -- "$file") == "$uid 600 1" ]]
done

# A symlink is refused and its target's metadata is never changed.
target="$tmp/symlink-target.env"
printf 'x\n' > "$target"; chmod 0600 "$target"
ln -s "$target" "$tmp/linked.env"
refuses 'a symlink' "$tmp/linked.env"

# Group/world access and extra hard links are refused untouched.
printf 'x\n' > "$tmp/shared.env"; chmod 0640 "$tmp/shared.env"
refuses 'a group-readable file' "$tmp/shared.env"
[[ $(stat -c %a -- "$tmp/shared.env") == 640 ]]
printf 'x\n' > "$tmp/linked-twice.env"; chmod 0600 "$tmp/linked-twice.env"
ln "$tmp/linked-twice.env" "$tmp/second-link.env"
refuses 'a file with two hard links' "$tmp/linked-twice.env"

# A file owned by someone other than root or the sudo user is refused. The
# owner check is exercised by naming a different sudo and target user.
if (SUDO_UID=$((uid + 1)) INSTALL_SETTINGS_TARGET_UID=$((uid + 2)) secure_install_settings_file "$tmp/direct-setup.env") 2>/dev/null; then
  if (( uid != 0 )); then
    echo 'settings promotion accepted a file owned by another user' >&2
    exit 1
  fi
fi

# The promotion must use the no-follow descriptor path, not path-based
# chown/chmod that follow a symlink swapped in after validation.
grep -Fq 'os.O_NOFOLLOW' "$repo/deploy/lib/install-env-file.sh"
grep -Fq 'os.fchown(fd' "$repo/deploy/lib/install-env-file.sh"
! grep -Eq '^[[:space:]]*(chown|chmod) ' "$repo/deploy/lib/install-env-file.sh"

# Keep the promotion outside the branch that generates new answers, so reuse
# and freshly generated settings both receive the same treatment.
reuse_line=$(grep -n 'if \[\[ \$reuse_answers != y \]\]' "$repo/install.sh" | head -1 | cut -d: -f1)
config_branch_line=$(grep -n 'if \[\[ -n \$CONFIG_FILE \]\]; then' "$repo/install.sh" | head -1 | cut -d: -f1)
secure_line=$(grep -n 'secure_install_settings_file "\$CONFIG_FILE"' "$repo/install.sh" | head -1 | cut -d: -f1)
load_line=$(grep -n 'load_env_file "\$CONFIG_FILE" private' "$repo/install.sh" | head -1 | cut -d: -f1)
[[ -n $reuse_line && -n $config_branch_line && -n $secure_line && -n $load_line && \
  $reuse_line -lt $config_branch_line && $config_branch_line -lt $secure_line &&
  $secure_line -lt $load_line ]] || {
  echo 'every installer settings file must be secured before loading it' >&2
  exit 1
}

grep -Fq 'No system services were changed. Your saved settings were secured at $CONFIG_FILE and can be reused;' "$repo/install.sh" || {
  echo 'installer cancellation must distinguish secured setup settings from system-service changes' >&2
  exit 1
}
grep -Fq 'print_install_settings_reuse_command "$CONFIG_FILE"' "$repo/install.sh" || {
  echo 'installer cancellation must print a shell-safe saved-settings command' >&2
  exit 1
}
reuse_config="$tmp/StePanel settings with 'quotes.env"
reuse_command=$(print_install_settings_reuse_command "$reuse_config")
expected_command=$(printf 'install later with: sudo ./install.sh --config %q' "$reuse_config")
[[ $reuse_command == "$expected_command" ]] || {
  echo 'saved-settings reuse command did not quote a path containing spaces and quotes' >&2
  exit 1
}
quoted_config=${reuse_command#*--config }
round_trip_config=$(bash -c "printf '%s' $quoted_config")
[[ $round_trip_config == "$reuse_config" ]] || {
  echo 'saved-settings reuse command does not preserve the original path when copied' >&2
  exit 1
}
if grep -Fq 'Nothing was changed.' "$repo/install.sh"; then
  echo 'installer cancellation must not claim that no changes were made' >&2
  exit 1
fi

echo 'guided installer saved-settings ownership contract passed'
