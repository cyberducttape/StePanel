#!/usr/bin/env bash
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'site ACL smoke must run as root' >&2; exit 1; }
site=${1:-ci-smoke}
helper=/usr/local/sbin/stepanel-sitectl
real_setfacl=$(command -v setfacl) || { echo 'setfacl is required for site ACL smoke' >&2; exit 77; }
tmp=$(mktemp -d /tmp/stepanel-acl-smoke.XXXXXX)
tree="/var/www/sites/$site/public/.stepanel-acl-benchmark"
cleanup() { rm -rf -- "$tmp" "$tree"; }
trap cleanup EXIT
install -d -m 0755 "$tmp/bin" "$tree"
cat > "$tmp/bin/setfacl" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$tmp/setfacl.log"
exec "$real_setfacl" "\$@"
EOF
chmod 0755 "$tmp/bin/setfacl"
count=${STEPANEL_ACL_BENCH_FILES:-2000}
for ((n = 1; n <= count; n++)); do printf '%s\n' "$n" > "$tree/file-$n"; done

start=$(date +%s%N)
PATH="$tmp/bin:$PATH" "$helper" runtime "$site" 8.3 128M 60 32M 32M 1000 1 0 'E_ALL & ~E_DEPRECATED'
elapsed=$(( $(date +%s%N) - start ))
[[ ! -s "$tmp/setfacl.log" ]] || { echo 'routine PHP runtime change recursively applied ACLs' >&2; cat "$tmp/setfacl.log" >&2; exit 1; }
printf 'ACL drift benchmark: %s files, %sms, zero recursive ACL calls\n' "$count" "$((elapsed / 1000000))"

# Sealing runs chown -R as root over the site tree. A hard link the account
# planted there to a file it does not own must make the seal refuse, and the
# file must keep its owner. (Root can plant the link here; an account cannot
# while fs.protected_hardlinks=1, which the installer pins.)
[[ $(sysctl -n fs.protected_hardlinks) == 1 && $(sysctl -n fs.protected_symlinks) == 1 ]] || { echo 'kernel link protections are not enabled' >&2; exit 1; }
site_user=$(stat -c %U "/var/www/sites/$site")
outside=$(mktemp /var/www/.stepanel-hardlink-target.XXXXXX)
planted="/var/www/sites/$site/public/.stepanel-planted-link"
trap 'rm -f -- "$planted" "$outside"; cleanup' EXIT
ln "$outside" "$planted"
if "$helper" seal "$site" "$site_user" 2> "$tmp/seal.err"; then
  echo 'seal changed the ownership of a tree holding a hard link to a root-owned file' >&2
  exit 1
fi
grep -q 'hard link' "$tmp/seal.err" || { echo "seal failed for another reason: $(cat "$tmp/seal.err")" >&2; exit 1; }
[[ $(stat -c %U "$outside") == root ]] || { echo "the linked root-owned file now belongs to $(stat -c %U "$outside")" >&2; exit 1; }
rm -f -- "$planted"
"$helper" seal "$site" "$site_user" >/dev/null
echo 'seal refused a planted hard link to a root-owned file and sealed the site once it was removed'
