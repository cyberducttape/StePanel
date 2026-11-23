#!/usr/bin/env bash
set -Eeuo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
fake_repo="$tmp/repo"
fake_bin="$tmp/bin"
mkdir -p "$fake_repo/scripts" "$fake_bin"
cp "$repo/scripts/install-guided.sh" "$fake_repo/scripts/install-guided.sh"
cp "$repo/scripts/install-host-preflight.sh" "$fake_repo/scripts/install-host-preflight.sh"
cp "$repo/scripts/go-toolchain.sh" "$fake_repo/scripts/go-toolchain.sh"
printf 'module guided-test\n\ntoolchain go1.26.11\n' > "$fake_repo/go.mod"
touch "$fake_repo/install.sh"
chmod 0755 "$fake_repo/install.sh"

cat > "$fake_bin/go" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ ${1:-} == version ]]; then
  printf 'go version %s linux/amd64\n' "${GO_TEST_VERSION:-go1.26.11}"
  exit 0
fi
if [[ ${VERIFY_RECOVERED_SET:-0} == 1 && " $* " == *' build '* ]]; then
  [[ $($FAKE_REPO/stepanel) == previous-panel ]]
  [[ $($FAKE_REPO/stepanel-root) == previous-root ]]
  [[ $($FAKE_REPO/stepanelctl) == previous-cli ]]
fi
printf '%s\n' "$*" >> "$GO_LOG"
output=
while (($#)); do
  if [[ $1 == -o ]]; then output=$2; shift 2; else shift; fi
done
[[ -n $output ]]
printf '%s\n' '#!/usr/bin/env bash' "printf '%s\\n' '${BUILD_ID:-fake-build}'" > "$output"
chmod 0755 "$output"
SH
cat > "$fake_bin/sudo" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$*" >> "$SUDO_LOG"
if [[ -n ${SUDO_ARGV_LOG:-} ]]; then printf '%s\n' "$@" >> "$SUDO_ARGV_LOG"; fi
if [[ ${1:-} == test && ${2:-} == -f && ${3:-} == /etc/ste-panel.env ]]; then
  [[ ${EXISTING_INSTALL:-0} == 1 ]]
fi
if [[ ${2:-} == */install-host-preflight.sh && ${PREFLIGHT_FAIL:-0} == 1 ]]; then
  exit 42
fi
SH
cat > "$fake_bin/curl" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$*" >> "$DOWNLOAD_LOG"
while (($#)); do
  if [[ $1 == --output ]]; then : > "$2"; exit 0; fi
  shift
done
exit 1
SH
cat > "$fake_bin/sha256sum" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
read -r expected archive
printf 'verify:%s\n' "$expected" >> "$BOOTSTRAP_LOG"
[[ ${BAD_CHECKSUM:-0} != 1 && -f ${archive#*  } ]]
SH
cat > "$fake_bin/tar" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
printf 'extract\n' >> "$BOOTSTRAP_LOG"
dest=
while (($#)); do
  if [[ $1 == -C ]]; then dest=$2; shift 2; else shift; fi
done
mkdir -p "$dest/go/bin"
cat > "$dest/go/bin/go" <<'GO'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ ${1:-} == version ]]; then echo 'go version go1.26.11 linux/amd64'; exit 0; fi
[[ ${GOTOOLCHAIN:-} == local ]]
printf '%s\n' "$*" >> "$GO_LOG"
output=
while (($#)); do
  if [[ $1 == -o ]]; then output=$2; shift 2; else shift; fi
done
printf '%s\n' '#!/usr/bin/env bash' "printf '%s\\n' '${BUILD_ID:-fake-build}'" > "$output"
chmod 0755 "$output"
GO
chmod 0755 "$dest/go/bin/go"
SH
chmod 0755 "$fake_bin"/*

GO_LOG="$tmp/go.log" SUDO_LOG="$tmp/sudo.log" GO=go SUDO=sudo \
  GOTOOLCHAIN=auto PATH="$fake_bin:$PATH" \
  bash "$fake_repo/scripts/install-guided.sh" --take-over-host
[[ -x $fake_repo/stepanel && -x $fake_repo/stepanel-root ]]
[[ -x $fake_repo/stepanelctl ]]
grep -Fq './cmd/stepanel' "$tmp/go.log"
grep -Fq './cmd/stepanel-root' "$tmp/go.log"
grep -Fq './cmd/stepanelctl' "$tmp/go.log"
[[ $(wc -l < "$tmp/sudo.log") == 3 ]]
grep -Fq 'test -f /etc/ste-panel.env' "$tmp/sudo.log"
grep -Fq 'install-host-preflight.sh' "$tmp/sudo.log"
[[ $(tail -n 1 "$tmp/sudo.log") == './install.sh --guided --take-over-host' ]]
if grep -Fq sudo "$tmp/go.log"; then
  echo 'Go build unexpectedly ran with sudo' >&2
  exit 1
fi

if env GO=go GO_LOG="$tmp/preflight-fail-go.log" \
  SUDO_LOG="$tmp/preflight-fail-sudo.log" SUDO=sudo PREFLIGHT_FAIL=1 \
  PATH="$fake_bin:$PATH" bash "$fake_repo/scripts/install-guided.sh" \
  >"$tmp/preflight-fail.out" 2>&1; then
  echo 'bootstrap continued after a failed host preflight' >&2
  exit 1
fi
grep -Fq 'install-host-preflight.sh' "$tmp/preflight-fail-sudo.log"
[[ ! -e $tmp/preflight-fail-go.log ]]

for compiler_mode in missing old local; do
  rm -f -- "$fake_repo/stepanel" "$fake_repo/stepanel-root" "$fake_repo/stepanelctl"
  : > "$tmp/bootstrap.log"
  env_args=(GO=go GO_LOG="$tmp/$compiler_mode-go.log" SUDO_LOG="$tmp/$compiler_mode-sudo.log"
    BOOTSTRAP_LOG="$tmp/bootstrap.log" DOWNLOAD_LOG="$tmp/download.log"
    SUDO=sudo PATH="$fake_bin:$PATH")
  case "$compiler_mode" in
    missing) env_args+=(GO="$tmp/missing-go") ;;
    old) env_args+=(GO_TEST_VERSION=go1.20.14) ;;
    local) env_args+=(GOTOOLCHAIN=local) ;;
  esac
  env "${env_args[@]}" bash "$fake_repo/scripts/install-guided.sh"
  [[ -x $fake_repo/stepanel && -x $fake_repo/stepanel-root && -x $fake_repo/stepanelctl ]]
  [[ $(<"$tmp/bootstrap.log") == $'verify:'* && $(tail -n 1 "$tmp/bootstrap.log") == extract ]]
  [[ $(wc -l < "$tmp/$compiler_mode-sudo.log") == 3 ]]
  grep -Fq 'test -f /etc/ste-panel.env' "$tmp/$compiler_mode-sudo.log"
  grep -Fq 'install-host-preflight.sh' "$tmp/$compiler_mode-sudo.log"
  [[ $(tail -n 1 "$tmp/$compiler_mode-sudo.log") == './install.sh --guided' ]]
  grep -Fq './cmd/stepanel' "$tmp/$compiler_mode-go.log"
  if [[ $compiler_mode == missing ]]; then
    grep -Fq -- '--progress-bar' "$tmp/download.log"
  fi
done

rm -f -- "$fake_repo/stepanel" "$fake_repo/stepanel-root" "$fake_repo/stepanelctl"
env GO=go GO_LOG="$tmp/existing-go.log" SUDO_LOG="$tmp/existing-sudo.log" \
  BOOTSTRAP_LOG="$tmp/existing-bootstrap.log" DOWNLOAD_LOG="$tmp/existing-download.log" \
  GOTOOLCHAIN=auto SUDO=sudo EXISTING_INSTALL=1 PATH="$fake_bin:$PATH" \
  bash "$fake_repo/scripts/install-guided.sh" >"$tmp/existing.out"
[[ $(wc -l < "$tmp/existing-sudo.log") == 2 ]]
grep -Fq 'test -f /etc/ste-panel.env' "$tmp/existing-sudo.log"
! grep -Fq 'install-host-preflight.sh' "$tmp/existing-sudo.log"
[[ $(tail -n 1 "$tmp/existing-sudo.log") == './install.sh' ]]
grep -Fq 'Starting the native upgrade.' "$tmp/existing.out"

# A setup file generated by `stepanel setup --output` can flow through the
# same pinned build and read-only preflight, with argv boundaries preserved.
config_path="$tmp/operator setup.env"
: > "$config_path"
env GO=go SUDO=sudo SUDO_LOG="$tmp/config-sudo.log" \
  SUDO_ARGV_LOG="$tmp/config-sudo-argv.log" GO_LOG="$tmp/config-go.log" \
  PATH="$fake_bin:$PATH" BUILD_ID=config-build \
  bash "$fake_repo/scripts/install-guided.sh" --config "$config_path" --dry-run \
  > "$tmp/config.out"
[[ $(tail -n 4 "$tmp/config-sudo-argv.log") == "$(printf './install.sh\n--config\n%s\n--dry-run' "$config_path")" ]]
grep -Fq 'Starting the native installer with the saved settings.' "$tmp/config.out"
grep -Fq 'install-host-preflight.sh' "$tmp/config-sudo.log"

if env GO=go SUDO=sudo GO_LOG="$tmp/duplicate-config-go.log" \
  SUDO_LOG="$tmp/duplicate-config-sudo.log" PATH="$fake_bin:$PATH" \
  bash "$fake_repo/scripts/install-guided.sh" --config a.env --config=b.env \
  > "$tmp/duplicate-config.out" 2>&1; then
  echo 'guided installer accepted conflicting setup files' >&2
  exit 1
fi
[[ ! -e $tmp/duplicate-config-go.log && ! -e $tmp/duplicate-config-sudo.log ]]

# If publishing the final executable fails, earlier renames are rolled back.
# A subsequent complete run must publish all binaries from one build identity.
cat > "$fake_repo/stepanel" <<'SH'
#!/usr/bin/env bash
echo previous-panel
SH
cat > "$fake_repo/stepanel-root" <<'SH'
#!/usr/bin/env bash
echo previous-root
SH
cat > "$fake_repo/stepanelctl" <<'SH'
#!/usr/bin/env bash
echo previous-cli
SH
chmod 0755 "$fake_repo/stepanel" "$fake_repo/stepanel-root" "$fake_repo/stepanelctl"
real_mv=$(command -v mv)
cat > "$fake_bin/mv" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
target=${@: -1}
if [[ ${FAIL_FINAL_PUBLISH:-0} == 1 && $target == */stepanelctl ]]; then exit 1; fi
"$REAL_MV" "$@"
if [[ ${KILL_AFTER_PANEL_PUBLISH:-0} == 1 && $target == */stepanel ]]; then
  kill -KILL "$PPID"
fi
if [[ ${KILL_AFTER_ROOT_PUBLISH:-0} == 1 && $target == */stepanel-root ]]; then
  kill -KILL "$PPID"
fi
SH
chmod 0755 "$fake_bin/mv"
if env GO=go SUDO=sudo SUDO_LOG="$tmp/interrupted-sudo.log" \
  GO_LOG="$tmp/interrupted-go.log" PATH="$fake_bin:$PATH" \
  GOTOOLCHAIN=auto BUILD_ID=interrupted-build \
  REAL_MV="$real_mv" FAIL_FINAL_PUBLISH=1 \
  bash "$fake_repo/scripts/install-guided.sh" >"$tmp/interrupted.out" 2>&1; then
  echo 'guided build continued after the final binary publication failed' >&2
  exit 1
fi
[[ $("$fake_repo/stepanel") == previous-panel ]]
[[ $("$fake_repo/stepanel-root") == previous-root ]]
[[ $("$fake_repo/stepanelctl") == previous-cli ]]
env GO=go SUDO=sudo SUDO_LOG="$tmp/restart-sudo.log" \
  GO_LOG="$tmp/restart-go.log" PATH="$fake_bin:$PATH" \
  GOTOOLCHAIN=auto BUILD_ID=restart-build \
  REAL_MV="$real_mv" bash "$fake_repo/scripts/install-guided.sh" >"$tmp/restart.out"
[[ $("$fake_repo/stepanel") == restart-build ]]
[[ $("$fake_repo/stepanel-root") == restart-build ]]
[[ $("$fake_repo/stepanelctl") == restart-build ]]

# A hard process kill after the first rename leaves a durable journal. On the
# next invocation recovery must restore the old matched set before the Go
# build starts, then publish the new set by one build identity.
cat > "$fake_repo/stepanel" <<'SH'
#!/usr/bin/env bash
echo previous-panel
SH
cat > "$fake_repo/stepanel-root" <<'SH'
#!/usr/bin/env bash
echo previous-root
SH
cat > "$fake_repo/stepanelctl" <<'SH'
#!/usr/bin/env bash
echo previous-cli
SH
chmod 0755 "$fake_repo/stepanel" "$fake_repo/stepanel-root" "$fake_repo/stepanelctl"
if env GO=go SUDO=sudo SUDO_LOG="$tmp/killed-sudo.log" \
  GO_LOG="$tmp/killed-go.log" PATH="$fake_bin:$PATH" \
  GOTOOLCHAIN=auto BUILD_ID=killed-build REAL_MV="$real_mv" \
  KILL_AFTER_PANEL_PUBLISH=1 \
  bash "$fake_repo/scripts/install-guided.sh" >"$tmp/killed.out" 2>&1; then
  echo 'guided installer survived a forced kill during binary publication' >&2
  exit 1
fi
[[ -d $fake_repo/.stepanel-guided-publish ]]
[[ $("$fake_repo/stepanel") == killed-build ]]
[[ $("$fake_repo/stepanel-root") == previous-root ]]
[[ $("$fake_repo/stepanelctl") == previous-cli ]]
env GO=go SUDO=sudo SUDO_LOG="$tmp/recovered-sudo.log" \
  GO_LOG="$tmp/recovered-go.log" PATH="$fake_bin:$PATH" \
  GOTOOLCHAIN=auto BUILD_ID=recovered-build REAL_MV="$real_mv" \
  FAKE_REPO="$fake_repo" VERIFY_RECOVERED_SET=1 \
  bash "$fake_repo/scripts/install-guided.sh" >"$tmp/recovered.out" 2>&1
[[ ! -e $fake_repo/.stepanel-guided-publish ]]
[[ $("$fake_repo/stepanel") == recovered-build ]]
[[ $("$fake_repo/stepanel-root") == recovered-build ]]
[[ $("$fake_repo/stepanelctl") == recovered-build ]]
grep -Fq 'Recovering an interrupted guided binary publication' "$tmp/recovered.out"

# Also interrupt after the second of three publishes. Recovery must restore all
# old artifacts before the next build, including the CLI that had not yet been
# published when the process died.
cat > "$fake_repo/stepanel" <<'SH'
#!/usr/bin/env bash
echo previous-panel
SH
cat > "$fake_repo/stepanel-root" <<'SH'
#!/usr/bin/env bash
echo previous-root
SH
cat > "$fake_repo/stepanelctl" <<'SH'
#!/usr/bin/env bash
echo previous-cli
SH
chmod 0755 "$fake_repo/stepanel" "$fake_repo/stepanel-root" "$fake_repo/stepanelctl"
if env GO=go SUDO=sudo SUDO_LOG="$tmp/killed-middle-sudo.log" \
  GO_LOG="$tmp/killed-middle-go.log" PATH="$fake_bin:$PATH" \
  GOTOOLCHAIN=auto BUILD_ID=killed-middle-build REAL_MV="$real_mv" \
  KILL_AFTER_ROOT_PUBLISH=1 \
  bash "$fake_repo/scripts/install-guided.sh" >"$tmp/killed-middle.out" 2>&1; then
  echo 'guided installer survived a forced kill during binary-set publication' >&2
  exit 1
fi
[[ $("$fake_repo/stepanel") == killed-middle-build ]]
[[ $("$fake_repo/stepanel-root") == killed-middle-build ]]
[[ $("$fake_repo/stepanelctl") == previous-cli ]]
env GO=go SUDO=sudo SUDO_LOG="$tmp/recovered-middle-sudo.log" \
  GO_LOG="$tmp/recovered-middle-go.log" PATH="$fake_bin:$PATH" \
  GOTOOLCHAIN=auto BUILD_ID=recovered-middle-build REAL_MV="$real_mv" \
  FAKE_REPO="$fake_repo" VERIFY_RECOVERED_SET=1 \
  bash "$fake_repo/scripts/install-guided.sh" >"$tmp/recovered-middle.out" 2>&1
[[ $("$fake_repo/stepanel") == recovered-middle-build ]]
[[ $("$fake_repo/stepanel-root") == recovered-middle-build ]]
[[ $("$fake_repo/stepanelctl") == recovered-middle-build ]]
rm -f -- "$fake_bin/mv"

rm -f -- "$fake_repo/stepanel" "$fake_repo/stepanel-root" "$fake_repo/stepanelctl" "$tmp/bad-sudo.log"
if env GO="$tmp/missing-go" SUDO=sudo SUDO_LOG="$tmp/bad-sudo.log" \
  BOOTSTRAP_LOG="$tmp/bad-bootstrap.log" DOWNLOAD_LOG="$tmp/bad-download.log" \
  BAD_CHECKSUM=1 PATH="$fake_bin:$PATH" \
  bash "$fake_repo/scripts/install-guided.sh" >"$tmp/bad-checksum.out" 2>&1; then
  echo 'bootstrap accepted an archive with a failed SHA-256 check' >&2
  exit 1
fi
grep -Fq 'failed its SHA-256 check' "$tmp/bad-checksum.out"
if grep -Fq extract "$tmp/bad-bootstrap.log"; then
  echo 'bootstrap extracted an archive before verifying its checksum' >&2
  exit 1
fi
[[ ! -e $fake_repo/stepanel && ! -e $fake_repo/stepanel-root && ! -e $fake_repo/stepanelctl ]]
grep -Fq 'install-host-preflight.sh' "$tmp/bad-sudo.log"
if grep -Fq './install.sh' "$tmp/bad-sudo.log"; then
  echo 'bootstrap handed off to the installer after a failed checksum' >&2
  exit 1
fi

printf 'module guided-test\n\ntoolchain go1.27.0\n' > "$fake_repo/go.mod"
if env GO="$tmp/missing-go" SUDO=sudo SUDO_LOG="$tmp/unsupported-sudo.log" \
  PATH="$fake_bin:$PATH" bash "$fake_repo/scripts/install-guided.sh" \
  >"$tmp/unsupported-pin.out" 2>&1; then
  echo 'bootstrap accepted a pinned toolchain without a verified checksum' >&2
  exit 1
fi
grep -Fq 'No verified Go archive checksum is configured for go1.27.0 linux/amd64' "$tmp/unsupported-pin.out"
grep -Fq 'install-host-preflight.sh' "$tmp/unsupported-sudo.log"
if grep -Fq './install.sh' "$tmp/unsupported-sudo.log"; then
  echo 'bootstrap handed off after an unverified Go toolchain pin' >&2
  exit 1
fi

if GO_LOG="$tmp/invalid-go.log" SUDO_LOG="$tmp/invalid-sudo.log" GO=go SUDO=sudo PATH="$fake_bin:$PATH" \
  bash "$fake_repo/scripts/install-guided.sh" --unsafe-lab >/dev/null 2>&1; then
  echo 'bootstrap accepted an unsupported installer option' >&2
  exit 1
fi
[[ ! -e $tmp/invalid-go.log && ! -e $tmp/invalid-sudo.log ]]

echo 'Guided source installer bootstrap tests passed.'
