#!/usr/bin/env bash
set -Eeuo pipefail

# Build StePanel as the checkout owner, then use sudo for the read-only host
# preflight and privileged install or upgrade. This keeps the Go toolchain and
# module cache out of the root account while keeping source setup one command.
if (( EUID == 0 )); then
  echo 'Run this command as your normal user; it uses sudo for host checks and installation.' >&2
  exit 1
fi

takeover=()
for arg in "$@"; do
  case "$arg" in
    --take-over-host) takeover+=(--take-over-host) ;;
    *) echo "usage: $0 [--take-over-host]" >&2; exit 64 ;;
  esac
done

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
[[ -f "$repo_root/go.mod" && -f "$repo_root/install.sh" ]] || {
  echo 'Run this command from a StePanel source checkout.' >&2
  exit 1
}

# Serialize builds and recover an interrupted two-binary publication before
# checking or building anything. The journal lives in the checkout so it
# survives a killed process and /tmp cleanup.
command -v flock >/dev/null 2>&1 || {
  echo 'flock is required to publish guided build artifacts safely.' >&2
  exit 1
}
exec 9>"$repo_root/.stepanel-guided.lock"
flock -x 9

publish_journal="$repo_root/.stepanel-guided-publish"
restore_journal_binary() {
  local name=$1 target="$repo_root/$1" restore_tmp
  if [[ -f "$publish_journal/$name.existed" ]]; then
    restore_tmp=$(mktemp "$repo_root/.${name}.restore.XXXXXX")
    cp -p -- "$publish_journal/$name" "$restore_tmp"
    mv -f -- "$restore_tmp" "$target"
  else
    rm -f -- "$target"
  fi
}
recover_interrupted_publish() {
  [[ -d $publish_journal && ! -L $publish_journal ]] || return 0
  echo 'Recovering an interrupted guided binary publication before continuing.' >&2
  restore_journal_binary stepanel
  restore_journal_binary stepanel-root
  sync -f "$repo_root"
  rm -rf -- "$publish_journal"
  sync -f "$repo_root"
}
recover_interrupted_publish

sudo_bin=${SUDO:-sudo}
command -v -- "$sudo_bin" >/dev/null 2>&1 || {
  echo 'sudo is required to configure host services.' >&2
  exit 1
}

existing_install=0
if "$sudo_bin" test -f /etc/ste-panel.env; then
  existing_install=1
  echo 'Existing StePanel installation detected; preparing the standard upgrade path.'
else
  # Fail before downloading a compiler or building binaries when this host
  # cannot run a fresh native installation. The check is read-only; install.sh
  # repeats it before making any host changes.
  "$sudo_bin" bash "$repo_root/scripts/install-host-preflight.sh"
fi

build_dir=$(mktemp -d "${TMPDIR:-/tmp}/stepanel-guided-build.XXXXXX")
panel_tmp=
root_tmp=
pending_journal=
cleanup() {
  local status=$?
  recover_interrupted_publish || echo 'Could not recover the previous binary pair; rerun the guided build to retry recovery.' >&2
  [[ -z $panel_tmp ]] || rm -f -- "$panel_tmp"
  [[ -z $root_tmp ]] || rm -f -- "$root_tmp"
  [[ -z $pending_journal ]] || rm -rf -- "$pending_journal"
  rm -rf -- "$build_dir"
  return "$status"
}
trap cleanup EXIT

. "$repo_root/scripts/go-toolchain.sh"
stepanel_prepare_go "$repo_root" "$build_dir"

run_go() {
  stepanel_go "$@"
}

echo 'Building StePanel with the repository-pinned Go toolchain as your user...'
(
  cd "$repo_root"
  run_go build -trimpath -o "$build_dir/stepanel" ./cmd/stepanel
  run_go build -trimpath -o "$build_dir/stepanel-root" ./cmd/stepanel-root
)

# Prepare a durable rollback journal before either binary is published. If
# power is lost between the two renames, the next invocation restores the
# previous pair before it builds or invokes the installer.
panel_tmp=$(mktemp "$repo_root/.stepanel.XXXXXX")
root_tmp=$(mktemp "$repo_root/.stepanel-root.XXXXXX")
install -m 0755 -- "$build_dir/stepanel" "$panel_tmp"
install -m 0755 -- "$build_dir/stepanel-root" "$root_tmp"
pending_journal=$(mktemp -d "$repo_root/.stepanel-guided-publish.new.XXXXXX")
if [[ -e $repo_root/stepanel || -L $repo_root/stepanel ]]; then
  : > "$pending_journal/stepanel.existed"
  cp -p -- "$repo_root/stepanel" "$pending_journal/stepanel"
fi
if [[ -e $repo_root/stepanel-root || -L $repo_root/stepanel-root ]]; then
  : > "$pending_journal/stepanel-root.existed"
  cp -p -- "$repo_root/stepanel-root" "$pending_journal/stepanel-root"
fi
sync -f "$pending_journal"
[[ ! -e $publish_journal ]] || {
  echo 'An unrecovered guided publication journal remains; refusing to overwrite it.' >&2
  exit 1
}
mv -- "$pending_journal" "$publish_journal"
pending_journal=
sync -f "$repo_root"
mv -f -- "$panel_tmp" "$repo_root/stepanel"
panel_tmp=
sync -f "$repo_root"
mv -f -- "$root_tmp" "$repo_root/stepanel-root"
root_tmp=
sync -f "$repo_root"
rm -rf -- "$publish_journal"
sync -f "$repo_root"

cd "$repo_root"
if (( existing_install )); then
  echo 'Build complete. Starting the native upgrade.'
  "$sudo_bin" ./install.sh
else
  echo 'Build complete. Starting the guided native installer.'
  "$sudo_bin" ./install.sh --guided "${takeover[@]}"
fi
