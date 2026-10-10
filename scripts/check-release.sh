#!/usr/bin/env bash
set -eu

version=$(sed -n 's/^const Version = "\([^"]*\)"$/\1/p' version.go)
if [ -z "$version" ]; then
  echo "version.go does not declare Version" >&2
  exit 1
fi

check_equal() {
  expected=$1
  actual=$2
  source_file=$3
  if [ "$actual" != "$expected" ]; then
    echo "$source_file declares version $actual, expected $expected" >&2
    exit 1
  fi
}

if ! grep -Eq '^[[:space:]]+write_env STEPANEL_ROOT_BROKER_SOCKET /run/stepanel-root-broker\.sock$' install.sh; then
  echo "install.sh does not configure the production root broker socket" >&2
  exit 1
fi
if ! grep -Eq '^[[:space:]]+write_env STEPANEL_BACKUP_ENCRYPTION_KEY "\$BACKUP_ENCRYPTION_KEY"$' install.sh; then
  echo "install.sh does not persist the backup encryption key" >&2
  exit 1
fi
if grep -Fq 'sudoers_tmp' install.sh; then
  echo "install.sh still provisions a panel sudoers policy" >&2
  exit 1
fi
if ! grep -Fqx 'install -m 0644 "$ROOT_DIR/deploy/stepanel-root-broker.service" /etc/systemd/system/stepanel-root-broker.service' install.sh; then
  echo "install.sh does not install the root broker service" >&2
  exit 1
fi
if grep -Fq 'sudo NOPASSWD' docs/ROOT_BROKER_INTEGRATION.md; then
  echo "active root broker documentation still describes sudo as the native transport" >&2
  exit 1
fi
if ! grep -Fq 'Long-lived root daemon over a peer-authorized Unix socket' docs/ROOT_BROKER_INTEGRATION.md; then
  echo "root broker documentation does not describe the native socket boundary" >&2
  exit 1
fi
if grep -En 'helperCommandContext\([^)]*,[[:space:]]*[^)]*,[[:space:]]*[^)]*,[[:space:]]*"restore(-dump|-wordpress)?"' cpmove.go wpress.go importer.go backup_restore.go staging.go >/dev/null; then
  echo "large database restore callsite bypasses the typed root broker" >&2
  exit 1
fi
if ! grep -Fqx 'install -d -m 0700 -o root -g root /var/lib/stepanel/recovery' install.sh; then
  echo "install.sh does not create root broker durable recovery storage" >&2
  exit 1
fi
if ! grep -Fqx 'chmod 0700 /var/lib/stepanel/recovery' install.sh; then
  echo "install.sh does not tighten root broker recovery storage permissions" >&2
  exit 1
fi
if ! grep -Fqx 'chown root:root /var/lib/stepanel/recovery' install.sh; then
  echo "install.sh does not restore root broker recovery storage ownership" >&2
  exit 1
fi
if ! grep -Fq -- '-recovery-root /var/lib/stepanel/recovery' deploy/stepanel-root-broker.service || grep -Eq '(^|[[:space:]])-recovery-root /var/www/sites/\.stepanel-recovery' deploy/stepanel-root-broker.service; then
  echo "root broker service does not use the separate root-owned recovery root" >&2
  exit 1
fi
if ! grep -Fq -- '-snapshot-recovery-root /var/www/sites/.stepanel-recovery' deploy/stepanel-root-broker.service; then
  echo "root broker service does not declare the panel snapshot recovery root" >&2
  exit 1
fi

chart_version=$(sed -n 's/^version: \([^[:space:]]*\)$/\1/p' deploy/helm/stepanel/Chart.yaml)
chart_app_version=$(sed -n 's/^appVersion: "\([^"]*\)"$/\1/p' deploy/helm/stepanel/Chart.yaml)
openapi_version=$(sed -n 's/^  version: \([^[:space:]]*\)$/\1/p' docs/openapi.yaml)

check_equal "$version" "$chart_version" deploy/helm/stepanel/Chart.yaml
check_equal "$version" "$chart_app_version" deploy/helm/stepanel/Chart.yaml
check_equal "$version" "$openapi_version" docs/openapi.yaml

if ! grep -Eq "^## \[$version\]( |$)" CHANGELOG.md; then
  echo "CHANGELOG.md has no release heading for $version" >&2
  exit 1
fi

echo "release metadata is synchronized for v$version"
