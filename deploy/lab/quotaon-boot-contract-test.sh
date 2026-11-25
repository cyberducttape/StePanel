#!/usr/bin/env bash
# Contract for deploy/integrations/stepanel-quotaon-boot: a quotaon failure is
# accepted only when every mounted quota filesystem reports the requested
# quota types on; otherwise quotaon's exit status is preserved.
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
wrapper=deploy/integrations/stepanel-quotaon-boot
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
mkdir -p "$work/bin"

# Mock quotaon: -aug exits $MOCK_AUG_STATUS; -p reports types listed in
# $MOCK_ON ("user:/var/www group:/srv") as on and exits with a count, like
# the real tool.
cat > "$work/bin/quotaon" <<'MOCK'
#!/usr/bin/env bash
if [[ $1 == -aug ]]; then
  echo 'quotaon: using . on /dev/vdb [/var/www]: File exists' >&2
  exit "${MOCK_AUG_STATUS:-0}"
fi
[[ $1 == -p ]] || exit 9
case $2 in -u) label=user ;; -g) label=group ;; *) exit 9 ;; esac
target=$3
if [[ " ${MOCK_ON:-} " == *" $label:$target "* ]]; then
  echo "$label quota on $target (/dev/mock) is on"
  exit 1
fi
echo "$label quota on $target (/dev/mock) is off"
exit 0
MOCK
# Mock findmnt -M: mounted targets are listed in $MOCK_MOUNTED.
cat > "$work/bin/findmnt" <<'MOCK'
#!/usr/bin/env bash
target=${*: -1}
[[ " ${MOCK_MOUNTED:-} " == *" $target "* ]]
MOCK
chmod 0755 "$work/bin/quotaon" "$work/bin/findmnt"

cat > "$work/fstab" <<'FSTAB'
# comment
UUID=root / ext4 defaults 0 1
/dev/vdb /var/www ext4 usrquota,prjquota 0 2
/dev/vdc /srv xfs uquota,gquota 0 2
/dev/vde /archive ext4 noauto,usrquota 0 2
/dev/vdf /unmounted ext4 usrquota 0 2
FSTAB

run() {
  PATH="$work/bin:$PATH" STEPANEL_QUOTAON="$work/bin/quotaon" STEPANEL_QUOTAON_FSTAB="$work/fstab" \
    bash "$wrapper" 2>"$work/stderr"
}
expect() {
  local want=$1 name=$2 got=0
  run || got=$?
  if [[ $got != "$want" ]]; then
    echo "quotaon-boot contract: $name exited $got, want $want" >&2
    cat "$work/stderr" >&2
    exit 1
  fi
}

export MOCK_MOUNTED='/ /var/www /srv /archive'

MOCK_AUG_STATUS=0 MOCK_ON='' expect 0 'successful quotaon'
MOCK_AUG_STATUS=2 MOCK_ON='user:/var/www user:/srv group:/srv' expect 0 'kernel-enabled quotas'
grep -Fq 'File exists' "$work/stderr" || { echo 'quotaon output must stay visible in the journal' >&2; exit 1; }
grep -Fq 'already enabled them at mount' "$work/stderr" || { echo 'accepted failure must be explained' >&2; exit 1; }
MOCK_AUG_STATUS=2 MOCK_ON='user:/var/www user:/srv' expect 2 'missing group quota on XFS'
grep -Fq 'group quotas requested for /srv' "$work/stderr" || { echo 'missing quota must be named' >&2; exit 1; }
MOCK_AUG_STATUS=3 MOCK_ON='user:/srv group:/srv' expect 3 'missing ext4 user quota'
# noauto and unmounted entries are not quotaon's responsibility.
MOCK_AUG_STATUS=2 MOCK_ON='user:/var/www user:/srv group:/srv' MOCK_MOUNTED='/ /var/www /srv' expect 0 'unmounted quota filesystem'
# With no mounted quota filesystem there is nothing to vouch for.
MOCK_AUG_STATUS=4 MOCK_ON='' MOCK_MOUNTED='/' expect 4 'no quota filesystems'

# The installer must register both files for rollback and only replace the
# vendor command through a drop-in.
grep -Fxq '  /usr/local/sbin/stepanel-quotaon-boot' install.sh
grep -Fxq '  /etc/systemd/system/quotaon.service.d/stepanel-kernel-quota.conf' install.sh
grep -Fq "printf '[Service]\\nExecStart=\\nExecStart=/usr/local/sbin/stepanel-quotaon-boot\\n'" install.sh
echo 'quotaon-boot contract passed'
