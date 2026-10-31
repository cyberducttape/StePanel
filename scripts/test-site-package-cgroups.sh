#!/usr/bin/env bash
set -Eeuo pipefail

helper=deploy/integrations/stepanel-appctl
require() {
  local expected=$1
  if ! grep -Fq -- "$expected" "$helper"; then
    echo "site package command isolation contract is missing: $expected" >&2
    exit 1
  fi
}

# A detached child remains inside this transient unit's cgroup. The site slice
# carries configured CPU/memory ceilings; this unit adds task and time bounds
# and only exposes the site's public/private writable trees.
require 'systemd-run --quiet --wait --collect --service-type=exec'
require '--property=KillMode=control-group --property=TasksMax=256'
require '--property=RuntimeMaxSec=15min --property=TimeoutStopSec=10s'
require '--property="Slice=stepanel-site-$site.slice"'
require '--property="ReadWritePaths=$public_root $site_private"'
require '--property="SupplementaryGroups=$supplementary_groups"'
require 'trap stop_package_unit_on_signal TERM INT HUP'
require 'systemctl stop "$active_package_unit"'

# These entry points execute package-provided scripts or build backends.
require 'run_site_package_command "$root" /usr/bin/env -i HOME="$site_private"'
require '"$venv_stage/bin/pip" install --quiet --require-hashes -r "$lockfile"'
require 'run_site_package_command "$root" /usr/bin/env -i HOME="$root" NODE_ENV=production'
require 'run_site_package_command "$root" /usr/bin/env -i HOME="$root" COMPOSER_HOME="$root/.composer"'

echo 'site package command cgroup contract passed'
