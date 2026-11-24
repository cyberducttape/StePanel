#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
binary=${1:-"$ROOT_DIR/stepanel"}
if (( $# > 1 )); then
  echo "usage: $0 [stepanel-binary]" >&2
  exit 64
fi
if (( EUID == 0 )); then
  printf '%s\n' 'Local evaluation must not run as root; run `bash scripts/evaluate-local.sh` as your normal user.' >&2
  exit 1
fi
if [[ ! -x $binary ]]; then
  printf 'StePanel binary is not executable: %s (run `bash scripts/evaluate-local.sh` from the checkout).\n' "$binary" >&2
  exit 1
fi

# Do not let an operator's installed-production environment redirect this
# local demo to production paths, enable integrations, or change its listener.
while IFS= read -r name; do
  [[ $name == STEPANEL_* ]] && unset "$name"
done < <(compgen -e)

state_dir="$ROOT_DIR/data/.evaluation"
if [[ -L "$ROOT_DIR/data" || ( -e "$ROOT_DIR/data" && ! -d "$ROOT_DIR/data" ) ]]; then
  echo "Refusing unsafe evaluation data path: $ROOT_DIR/data" >&2
  exit 1
fi
if [[ -L $state_dir || ( -e $state_dir && ! -d $state_dir ) ]]; then
  echo "Refusing unsafe evaluation state path: $state_dir" >&2
  exit 1
fi
mkdir -p -- "$state_dir"
[[ ! -L $state_dir ]] || { echo "Refusing symlinked evaluation state path: $state_dir" >&2; exit 1; }
chmod 0700 "$state_dir"
key_file="$state_dir/audit.key"
if [[ -e $key_file || -L $key_file ]]; then
  if [[ ! -f $key_file || -L $key_file || $(stat -c %u "$key_file") != "$EUID" || $(stat -c %a "$key_file") != 600 ]]; then
    echo "Evaluation audit key must be a regular user-owned mode-0600 file: $key_file" >&2
    exit 1
  fi
else
  audit_key=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
  [[ $audit_key =~ ^[[:xdigit:]]{64}$ ]] || { echo 'Could not generate an evaluation audit key.' >&2; exit 1; }
  staged_key=$(mktemp "$state_dir/.audit-key.XXXXXX")
  chmod 0600 "$staged_key"
  printf '%s\n' "$audit_key" > "$staged_key"
  if ! ln "$staged_key" "$key_file" 2>/dev/null && [[ ! -f $key_file ]]; then
    rm -f -- "$staged_key"
    echo "Could not save the evaluation audit key in $state_dir" >&2
    exit 1
  fi
  rm -f -- "$staged_key"
fi
if [[ ! -f $key_file || -L $key_file || $(stat -c %u "$key_file") != "$EUID" || $(stat -c %a "$key_file") != 600 ]]; then
  echo "Evaluation audit key must be a regular user-owned mode-0600 file: $key_file" >&2
  exit 1
fi
audit_key=$(<"$key_file")
[[ $audit_key =~ ^[[:xdigit:]]{64}$ ]] || { echo "Evaluation audit key is malformed: $key_file" >&2; exit 1; }

export STEPANEL_ENV=development
export STEPANEL_DEVELOPMENT_ADMIN=1
export STEPANEL_LISTEN=127.0.0.1:8080
export STEPANEL_AUDIT_KEY="$audit_key"
# Keep every persistence and managed-file root inside the evaluation sandbox.
# The caller may already have a real data/ tree in this checkout; a local
# preview must never reuse its control-plane database, audit log, or websites.
export STEPANEL_AUDIT_LOG="$state_dir/stepanel-audit.jsonl"
export STEPANEL_JOB_STATE="$state_dir/jobs.json"
export STEPANEL_SESSION_STATE="$state_dir/sessions.json"
export STEPANEL_ACCOUNT_STATE="$state_dir/accounts.json"
export STEPANEL_CONTROL_PLANE_DB="$state_dir/stepanel-control.db"
export STEPANEL_ENVIRONMENT_STATE="$state_dir/site-environments.json"
export STEPANEL_REDIS_STATE="$state_dir/redis-allocations.json"
export STEPANEL_IMPORT_ROOT="$state_dir/imports"
export STEPANEL_BACKUP_ROOT="$state_dir/backups"
export STEPANEL_WEB_ROOT="$state_dir/www"
export STEPANEL_HOME_ROOT="$state_dir/home"
export STEPANEL_MAIL_ROOT="$state_dir/mail"
export STEPANEL_NVM_DIR="$state_dir/nvm"
export STEPANEL_PROXY_ROOT="$state_dir/proxy"
export STEPANEL_VHOST_ROOT="$state_dir/vhosts"
export STEPANEL_APP_ROOT="$state_dir/apps"
export STEPANEL_MALWARE_ROOT="$state_dir/quarantine"
export STEPANEL_RECOVERY_ROOT="$state_dir/www/sites/.stepanel-recovery"

echo 'Starting local StePanel evaluation at http://127.0.0.1:8080'
echo 'No login is enabled. Do not expose this listener or use customer data.'
echo "Press Ctrl-C to stop. Evaluation state is stored in $state_dir/."
cd "$ROOT_DIR"
exec "$binary"
