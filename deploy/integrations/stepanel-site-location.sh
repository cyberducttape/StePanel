# shellcheck shell=bash disable=SC2034 # the location variables are this library's output
# StePanel site location library (ADR-0007), installed root-owned at
# /usr/local/lib/stepanel/site-location.sh and sourced by the privileged
# helpers. It is the single root-side answer to "where does this site live".
#
# A legacy site lives in /var/www/sites/SITE and serves .../public. A site
# placed in a hosting account has a root-owned location record at
# /etc/stepanel/sites/SITE/location naming its account user and directory;
# it serves /home/USER/public_html (main site) or /home/USER/DOMAIN, and
# keeps per-site private state in /home/USER/.stepanel/sites/SITE.
#
# Helpers never take a document root from their caller. The record is
# written only by `stepanel-userctl place`, after the same checks that
# stepanel_site_location repeats on every read.

stepanel_site_registry=/etc/stepanel/sites
stepanel_home_root=/home
stepanel_managed_group=stepanel-users

# stepanel_account_user_valid USER: USER is a StePanel hosting account: a
# portable name, home exactly /home/USER as a real directory, and member of
# the root-managed stepanel-users group. System accounts never qualify.
stepanel_account_user_valid() {
  local user=$1 entry home groups
  [[ $user =~ ^[a-z_][a-z0-9_-]{0,31}$ && $user != root && $user != sp-* && $user != stepanel* ]] || return 1
  entry=$(getent passwd "$user") || return 1
  home=$(cut -d: -f6 <<<"$entry")
  [[ $home == "$stepanel_home_root/$user" && -d $home && ! -L $home ]] || return 1
  [[ ! -L $stepanel_home_root ]] || return 1
  groups=$(id -nG "$user" 2>/dev/null) || return 1
  [[ " $groups " == *" $stepanel_managed_group "* ]]
}

# stepanel_site_directory_valid DIRECTORY: public_html or a lowercase domain.
stepanel_site_directory_valid() {
  local directory=$1
  [[ $directory == public_html ]] && return 0
  (( ${#directory} <= 253 )) && [[ $directory =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$ ]]
}

# stepanel_site_location SITE sets, for a legacy or placed site:
#   site_placed        0 or 1
#   site_location_user the account user of a placed site ("" when legacy)
#   site_directory     public_html or the domain of a placed site
#   site_home          the home holding the document root
#   site_private       per-site private state (PHP state, venv, builds)
#   public_root        the document root
#   php_state          PHP sessions, temp files and runtime profiles
# It fails closed on a malformed, foreign or unsafe record.
stepanel_site_location() {
  local site=$1 record user='' directory='' key value
  [[ $site =~ ^[a-z0-9_-]{1,32}$ ]] || { echo 'invalid site' >&2; return 1; }
  record="$stepanel_site_registry/$site/location"
  if [[ ! -e $record && ! -L $record ]]; then
    site_placed=0
    site_location_user=''
    site_directory=public
    site_home="/var/www/sites/$site"
    site_private=$site_home
    public_root="$site_home/public"
    php_state="$site_home/.php"
    return 0
  fi
  [[ -d $stepanel_site_registry && ! -L $stepanel_site_registry && $(stat -c '%u:%a' -- "$stepanel_site_registry") == 0:755 ]] || { echo 'site location registry is unsafe' >&2; return 1; }
  [[ -f $record && ! -L $record && $(stat -c '%u:%a' -- "$record") == 0:644 ]] || { echo "site location record for $site is unsafe" >&2; return 1; }
  while IFS='=' read -r key value || [[ -n $key ]]; do
    case $key in
      user) user=$value ;;
      directory) directory=$value ;;
      *) echo "site location record for $site is malformed" >&2; return 1 ;;
    esac
  done < "$record"
  stepanel_account_user_valid "$user" || { echo "site $site is placed in an account that is not a StePanel hosting account" >&2; return 1; }
  stepanel_site_directory_valid "$directory" || { echo "site $site has an invalid document root directory" >&2; return 1; }
  site_placed=1
  site_location_user=$user
  site_directory=$directory
  site_home="$stepanel_home_root/$user"
  site_private="$site_home/.stepanel/sites/$site"
  public_root="$site_home/$directory"
  php_state="$site_private/php"
}

# stepanel_require_site_user SITE_USER fails unless SITE_USER is the account
# user recorded for a placed site. Legacy sites keep their own checks.
stepanel_require_site_user() {
  local requested=$1
  (( site_placed )) || return 0
  [[ $requested == "$site_location_user" ]] || { echo "site belongs to account $site_location_user, not $requested" >&2; return 1; }
}
