#!/usr/bin/env bash
# Sandboxed-build permission smoke test.
#
# Reproduces the runner-permission failure fixed by moving the build script
# copy into a site-owned scratch path (deploy/integrations/stepanel-runnerctl):
# the submitted script is created inside STEPANEL_APP_ROOT as
# stepanel:stepanel 0600, but the rootless-Podman container drops to the
# isolated sp-* site user, which is not in the stepanel group and cannot read
# that path. Before the fix, the helper bind-mounted the unreadable path
# directly and every sandboxed build failed inside the container.
#
# This test is meant to run inside the lab environment after install-smoke.sh
# has installed StePanel and prepared at least one site. It self-skips (exit
# 77, the autoconf/CI convention for "skipped") when its prerequisites are
# missing so it can be wired into CI without gating on Podman-capable hosts.
#
# Prerequisites (checked below):
#   - EUID 0
#   - STEPANEL_APP_ROOT env set (defaults to /var/lib/ste-panel/apps)
#   - stepanel-runnerctl installed at /usr/local/sbin
#   - podman available
#   - $RUNNER_TEST_SITE set (default: ci-smoke — the site install-smoke.sh
#     prepares — so this test drops in after install-smoke unchanged)
#   - $RUNNER_TEST_IMAGE set to a pinned image ref (`name@sha256:...`).
#     The image only needs `/bin/sh`; a scratch base with busybox works.
#
# The test asserts:
#   1. The helper exits 0.
#   2. The site-owned scratch dir has been cleaned up on exit.
#   3. The artifact directory contains the file the build script wrote,
#      proving the script was reachable from inside the container.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'runner-perm smoke must run as root' >&2; exit 77; }
command -v podman >/dev/null || { echo 'runner-perm smoke requires podman' >&2; exit 77; }
[[ -x /usr/local/sbin/stepanel-runnerctl ]] || { echo 'stepanel-runnerctl not installed' >&2; exit 77; }
: "${STEPANEL_APP_ROOT:=/var/lib/ste-panel/apps}"
[[ -d $STEPANEL_APP_ROOT ]] || { echo "STEPANEL_APP_ROOT ($STEPANEL_APP_ROOT) is not a directory" >&2; exit 77; }
: "${RUNNER_TEST_SITE:=ci-smoke}"
[[ -d /var/www/sites/$RUNNER_TEST_SITE ]] || { echo "site $RUNNER_TEST_SITE has not been prepared" >&2; exit 77; }
: "${RUNNER_TEST_IMAGE:=}"
[[ -n $RUNNER_TEST_IMAGE ]] || { echo 'set RUNNER_TEST_IMAGE to a pinned image (name@sha256:...) with /bin/sh' >&2; exit 77; }

hash=$(printf '%s' "$RUNNER_TEST_SITE" | sha256sum); hash=${hash%% *}
prefix=${RUNNER_TEST_SITE:0:18}; prefix=${prefix//_/-}
site_user="sp-${prefix}-${hash:0:8}"
id "$site_user" >/dev/null 2>&1 || { echo "sp-* user for $RUNNER_TEST_SITE not found ($site_user)" >&2; exit 77; }

# Reproduce the exact permission profile the panel writes: file inside the
# stepanel-owned app root, mode 0600 owned by stepanel. Nothing else is set up
# for the sp-* user to reach it — the helper is what makes it reachable.
script=$(mktemp --tmpdir="$STEPANEL_APP_ROOT" pipeline-XXXXXXXX.sh)
chown stepanel:stepanel "$script"
chmod 0600 "$script"
cat > "$script" <<'EOF'
set -eu
echo runner-perm-smoke > /artifact/proof
EOF
trap 'rm -f -- "$script"' EXIT

site_root="/var/www/sites/$RUNNER_TEST_SITE"
artifact="$site_root/.stepanel-artifact"
scratch="$site_root/.stepanel-runner-scratch"

# Assert clean starting state so a leftover from a previous run cannot mask
# the fix under test.
[[ ! -e $scratch ]] || { echo "leftover scratch at $scratch — clean up before rerunning" >&2; exit 1; }
rm -f -- "$artifact/proof" 2>/dev/null || true

/usr/local/sbin/stepanel-runnerctl build \
  "$RUNNER_TEST_SITE" "$RUNNER_TEST_IMAGE" "$site_root/public" "$script" \
  100 256 128 none

# Post-conditions: helper must have cleaned up the scratch dir (the trap
# inside stepanel-runnerctl) and the build must actually have run inside the
# container, evidenced by the file it wrote to /artifact.
[[ ! -e $scratch ]] || { echo "helper left scratch dir behind at $scratch" >&2; exit 1; }
[[ -f $artifact/proof ]] || { echo "expected artifact was not written — build did not run" >&2; exit 1; }
[[ $(cat "$artifact/proof") == "runner-perm-smoke" ]] || { echo "artifact content is not what the sandbox wrote" >&2; exit 1; }

echo "runner-perm smoke passed"
