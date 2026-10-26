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
bash scripts/check-smoke-workflow-bounds.sh
if ! grep -Fq 'install_journal_record post_install planned retained "actions=$POST_INSTALL_PLAN_TEXT"' install.sh ||
   ! grep -Fq 'install_journal_record post_install started partial' deploy/lib/install-postcommit.sh ||
   ! grep -Fq 'install_journal_record post_install pending manual' deploy/lib/install-postcommit.sh ||
   ! grep -Fq 'install_journal_record post_install complete retained' deploy/lib/install-postcommit.sh; then
  echo "optional post-commit actions are not durably planned and individually journaled" >&2
  exit 1
fi
if ! grep -Fq 'if ! rm -f "$temporary"; then POST_COMMIT_CLEANUP_PENDING+=' install.sh ||
   ! grep -Fq 'if ! rm -rf "$INSTALL_TXN"; then POST_COMMIT_CLEANUP_PENDING+=' install.sh ||
   ! grep -Fq "echo 'post_install_cleanup_status=complete'" install.sh; then
  echo "post-commit transaction cleanup can still turn a committed install into a failed exit or hide leftovers" >&2
  exit 1
fi
core_activation_line=$(grep -nFx 'systemctl enable --now stepanel.service stepanel-worker.service' install.sh | cut -d: -f1 || true)
readiness_gate_line=$(grep -nFx 'if (( health_ready != 1 )); then' install.sh | cut -d: -f1 || true)
commit_line=$(grep -nFx 'INSTALL_COMMITTED=1' install.sh | cut -d: -f1 || true)
post_commit_plan_line=$(grep -nFx 'install_journal_record post_install planned retained "actions=$POST_INSTALL_PLAN_TEXT"' install.sh | cut -d: -f1 || true)
post_commit_action_line=$(grep -nF 'install_optional_postcommit_action stepanel-malware-guard' install.sh | cut -d: -f1 || true)
if [[ -z $core_activation_line || -z $readiness_gate_line || -z $post_commit_plan_line || -z $commit_line || -z $post_commit_action_line ]] ||
   (( core_activation_line >= readiness_gate_line || readiness_gate_line >= post_commit_plan_line || post_commit_plan_line >= commit_line || commit_line >= post_commit_action_line )); then
  echo "installer must activate core services and pass readiness before commit; optional actions must be planned before and attempted after commit" >&2
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
for broker_unit in deploy/stepanel-root-broker.service deploy/lab/stepanel-root-broker.service; do
  if ! grep -Fxq 'AmbientCapabilities=CAP_SETUID CAP_SETGID' "$broker_unit"; then
    echo "$broker_unit does not retain the account-switching capabilities required by customer file operations" >&2
    exit 1
  fi
done
if ! grep -Fq "prlimit --as=2147483648 --fsize=\"\$max_file_bytes\" -- git " deploy/integrations/stepanel-gitctl; then
  echo "private Git clone must preserve its 2 GiB address-space limit and enforce the configured per-file limit" >&2
  exit 1
fi
if ! grep -Fq "'/dev/vdc /mnt/stepanel-enospc ext4 defaults 0 2' >> /etc/fstab" .github/workflows/quota-install-smoke.yml; then
  echo "quota smoke does not persist its ENOSPC volume across guest reboot" >&2
  exit 1
fi

# Every build pins the same patched Go toolchain: go.mod's toolchain line
# (source builds), the Docker build image, and each workflow's setup-go.
toolchain=$(sed -n 's/^toolchain go\([0-9.]*\)$/\1/p' go.mod)
if [ -z "$toolchain" ]; then
  echo "go.mod does not pin a toolchain" >&2
  exit 1
fi
if ! grep -Eq "^FROM golang:$toolchain-" Dockerfile; then
  echo "Dockerfile build image does not use Go $toolchain from go.mod" >&2
  exit 1
fi
if grep -En "go-version: *'[0-9.]+'|go_version: *\['[0-9.]+'\]" .github/workflows/*.yml | grep -Fv "'$toolchain'" >&2; then
  echo "workflow Go version pins above differ from go.mod toolchain $toolchain" >&2
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
