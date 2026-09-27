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

broker_rule='NOPASSWD: /usr/local/sbin/stepanel-root -webroot /var/www'
if ! grep -Fqx "printf '%s ALL=(root) $broker_rule\\n' \"\$APP_USER\" >> \"\$sudoers_tmp\"" install.sh; then
  echo "install.sh does not pin the root broker sudo rule to /var/www" >&2
  exit 1
fi
if grep -Eq 'NOPASSWD: /usr/local/sbin/stepanel-root[[:space:]]*\\n' install.sh; then
  echo "install.sh contains an unrestricted root broker sudo rule" >&2
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
