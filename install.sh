#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $EUID -ne 0 ]]; then echo "Run as root: sudo ./install.sh" >&2; exit 1; fi
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_USER="stepanel"; APP_DIR="/opt/stepanel"; DATA_DIR="/var/lib/ste-panel"; ENV_FILE="/etc/ste-panel.env"
if [[ ! -f "$ROOT_DIR/stepanel" || ! -x "$ROOT_DIR/stepanel" ]]; then echo "Build an executable stepanel binary before installing." >&2; exit 1; fi

# On upgrades, retain the generated runtime configuration unless the operator
# explicitly supplied a replacement STEPANEL_* environment variable.
if [[ -f "$ENV_FILE" ]]; then
  env_owner=$(stat -c %u "$ENV_FILE")
  env_mode=$(stat -c %a "$ENV_FILE")
  [[ $env_owner == 0 && $env_mode =~ ^[0-7]{3,4}$ && $(( 8#$env_mode & 022 )) == 0 ]] || { echo "$ENV_FILE must be root-owned and not group/world-writable." >&2; exit 1; }
  while IFS='=' read -r env_key env_encoded; do
    [[ -n $env_key ]] || continue
    [[ $env_key =~ ^STEPANEL_[A-Z0-9_]+$ && $env_encoded == \"*\" ]] || { echo "$ENV_FILE contains an unsupported entry." >&2; exit 1; }
    [[ -v $env_key ]] && continue
    env_encoded=${env_encoded:1:${#env_encoded}-2}
    env_value=
    while [[ -n $env_encoded ]]; do
      env_character=${env_encoded:0:1}
      env_encoded=${env_encoded:1}
      if [[ $env_character == '\' ]]; then
        [[ -n $env_encoded ]] || { echo "$ENV_FILE contains an invalid escape." >&2; exit 1; }
        env_character=${env_encoded:0:1}
        [[ $env_character == '\' || $env_character == '"' ]] || { echo "$ENV_FILE contains an unsupported escape." >&2; exit 1; }
        env_encoded=${env_encoded:1}
      fi
      env_value+=$env_character
    done
    printf -v "$env_key" '%s' "$env_value"
    export "${env_key?}"
  done < "$ENV_FILE"
fi

if [[ ! -v STEPANEL_DB_PASSWORD ]]; then
  database_password_source=${STEPANEL_DB_PASSWORD_FILE:-/etc/stepanel-db.password}
  if [[ -e $database_password_source || -L $database_password_source ]]; then
    [[ -f $database_password_source && ! -L $database_password_source ]] || { echo 'Database password credential must be a regular file.' >&2; exit 1; }
    [[ $(stat -c %u "$database_password_source") == 0 ]] || { echo 'Database password credential must be root-owned.' >&2; exit 1; }
    database_password_mode=$(stat -c %a "$database_password_source")
    [[ $database_password_mode =~ ^[0-7]{3,4}$ && $(( 8#$database_password_mode & 077 )) == 0 ]] || { echo 'Database password credential must not be group/world accessible.' >&2; exit 1; }
    [[ $(stat -c %s "$database_password_source") -le 4096 ]] || { echo 'Database password credential exceeds 4 KiB.' >&2; exit 1; }
    STEPANEL_DB_PASSWORD=$(<"$database_password_source")
  fi
fi

ADMIN_USERNAME="${STEPANEL_ADMIN_USERNAME:-admin}"; ADMIN_PASSWORD="${STEPANEL_ADMIN_PASSWORD:-}"; SESSION_SECRET="${STEPANEL_SESSION_SECRET:-}"
EXISTING_ADMIN_PASSWORD_HASH="${STEPANEL_ADMIN_PASSWORD_HASH:-}"
AUDIT_KEY="${STEPANEL_AUDIT_KEY:-}"
ENVIRONMENT_KEY="${STEPANEL_ENVIRONMENT_KEY:-}"
BACKUP_SIGNING_KEY="${STEPANEL_BACKUP_SIGNING_KEY:-}"
ADMIN_TOTP_SECRET="${STEPANEL_ADMIN_TOTP_SECRET:-}"
ACCOUNT_KEY="${STEPANEL_ACCOUNT_KEY:-}"
DB_ENGINE="${STEPANEL_DB_ENGINE:-}"; DB_VERSION="${STEPANEL_DB_VERSION:-default}"
INSTALL_DB_ADMIN="${STEPANEL_INSTALL_DB_ADMIN:-0}"
DB_ADMIN_URL="${STEPANEL_DB_ADMIN_URL:-}"
DB_ADMIN_ALLOW="${STEPANEL_DB_ADMIN_ALLOW:-127.0.0.1 ::1}"
DB_HOST="${STEPANEL_DB_HOST:-localhost}"; DB_USER="${STEPANEL_DB_USER:-}"; DB_PASSWORD="${STEPANEL_DB_PASSWORD:-}"
INSTALL_FAIL2BAN="${STEPANEL_INSTALL_FAIL2BAN:-0}"; FAIL2BAN_JAILS="${STEPANEL_FAIL2BAN_JAILS:-auto}"; FAIL2BAN_IGNORE_IP="${STEPANEL_FAIL2BAN_IGNORE_IP:-}"
FPM_LENS_BINARY="${STEPANEL_FPM_LENS_BINARY:-}"
INSTALL_MODSEC="${STEPANEL_INSTALL_MODSEC:-0}"; MODSEC_MODE="${STEPANEL_MODSEC_MODE:-DetectionOnly}"
INSTALL_MAIL="${STEPANEL_INSTALL_MAIL:-0}"
ACTIVATE_MAIL="${STEPANEL_ACTIVATE_MAIL:-0}"
INSTALL_FTP="${STEPANEL_INSTALL_FTP:-0}"; ACTIVATE_FTP="${STEPANEL_ACTIVATE_FTP:-0}"
FTP_PASSIVE_MIN="${STEPANEL_FTP_PASSIVE_MIN:-40100}"
FTP_PASSIVE_MAX="${STEPANEL_FTP_PASSIVE_MAX:-40200}"
FTP_CERT_FILE="${STEPANEL_FTP_CERT_FILE:-}"; FTP_KEY_FILE="${STEPANEL_FTP_KEY_FILE:-}"
INSTALL_NODE="${STEPANEL_INSTALL_NODE:-0}"; NODE_VERSIONS="${STEPANEL_NODE_VERSIONS:-20.18.0}"
INSTALL_SECURITY="${STEPANEL_INSTALL_SECURITY:-0}"
INSTALL_TLS="${STEPANEL_INSTALL_TLS:-0}"
WEB_SERVER="${STEPANEL_WEBSERVER:-}"
WPRESS_EXTRACT="${STEPANEL_WPRESS_EXTRACT:-/usr/local/bin/wpress-extract}"
WPCLI="${STEPANEL_WPCLI:-/usr/local/bin/wp}"
PANEL_HOSTNAME="${STEPANEL_PANEL_HOSTNAME:-}"
STAGE_RETENTION_HOURS="${STEPANEL_STAGE_RETENTION_HOURS:-168}"
MIN_FREE_BYTES="${STEPANEL_MIN_FREE_BYTES:-1073741824}"
REQUIRE_OFFSITE_BACKUP="${STEPANEL_REQUIRE_OFFSITE_BACKUP:-0}"
MAX_UPLOAD_BYTES="${STEPANEL_MAX_UPLOAD_BYTES:-21474836480}"
MAX_ARCHIVE_ENTRIES="${STEPANEL_MAX_ARCHIVE_ENTRIES:-1000000}"
MAX_CONCURRENT_JOBS="${STEPANEL_MAX_CONCURRENT_JOBS:-2}"
unset STEPANEL_ADMIN_PASSWORD STEPANEL_SESSION_SECRET STEPANEL_DB_PASSWORD STEPANEL_DB_PASSWORD_FILE
if [[ -z "$ADMIN_PASSWORD" && -t 0 ]]; then read -r -s -p "StePanel admin password: " ADMIN_PASSWORD; echo; fi
if [[ -z "$ADMIN_PASSWORD" && -z "$EXISTING_ADMIN_PASSWORD_HASH" ]]; then echo "Set STEPANEL_ADMIN_PASSWORD or run the installer interactively." >&2; exit 1; fi
if [[ -z "$SESSION_SECRET" ]]; then SESSION_SECRET="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"; fi
if [[ -e /etc/stepanel-audit.key || -L /etc/stepanel-audit.key ]]; then
  [[ -f /etc/stepanel-audit.key && ! -L /etc/stepanel-audit.key ]] || { echo '/etc/stepanel-audit.key must be a regular file.' >&2; exit 1; }
  audit_key_owner=$(stat -c %u /etc/stepanel-audit.key)
  audit_key_mode=$(stat -c %a /etc/stepanel-audit.key)
  [[ $audit_key_owner == 0 && $audit_key_mode =~ ^[0-7]{3,4}$ && $(( 8#$audit_key_mode & 077 )) == 0 ]] || { echo '/etc/stepanel-audit.key must be root-owned and mode 0600 or stricter.' >&2; exit 1; }
  existing_audit_key=$(< /etc/stepanel-audit.key)
  if [[ -z $AUDIT_KEY ]]; then AUDIT_KEY=$existing_audit_key; fi
  [[ $existing_audit_key == "$AUDIT_KEY" ]] || { echo 'Refusing to replace the audit key while an existing audit chain may depend on it.' >&2; exit 1; }
fi
if [[ -z "$AUDIT_KEY" ]]; then AUDIT_KEY="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"; fi
if [[ -z "$ENVIRONMENT_KEY" ]]; then ENVIRONMENT_KEY="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"; fi
if [[ -z "$BACKUP_SIGNING_KEY" ]]; then BACKUP_SIGNING_KEY="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"; fi
if [[ -n "$ADMIN_PASSWORD" ]] && (( ${#ADMIN_PASSWORD} < 12 )); then echo "STEPANEL_ADMIN_PASSWORD must be at least 12 characters." >&2; exit 1; fi
if (( ${#SESSION_SECRET} < 32 )); then echo "STEPANEL_SESSION_SECRET must be at least 32 characters." >&2; exit 1; fi
if (( ${#AUDIT_KEY} < 32 )) || [[ $AUDIT_KEY == *$'\n'* || $AUDIT_KEY == *$'\r'* ]]; then echo 'STEPANEL_AUDIT_KEY must be at least 32 characters and contain no newlines.' >&2; exit 1; fi
if (( ${#ENVIRONMENT_KEY} < 32 )) || [[ $ENVIRONMENT_KEY == *$'\n'* || $ENVIRONMENT_KEY == *$'\r'* ]]; then echo 'STEPANEL_ENVIRONMENT_KEY must be at least 32 characters and contain no newlines.' >&2; exit 1; fi
if (( ${#BACKUP_SIGNING_KEY} < 32 )) || [[ $BACKUP_SIGNING_KEY == *$'\n'* || $BACKUP_SIGNING_KEY == *$'\r'* ]]; then echo 'STEPANEL_BACKUP_SIGNING_KEY must be at least 32 characters and contain no newlines.' >&2; exit 1; fi
if [[ $AUDIT_KEY == "$SESSION_SECRET" ]]; then echo 'STEPANEL_AUDIT_KEY must differ from STEPANEL_SESSION_SECRET.' >&2; exit 1; fi
ADMIN_TOTP_SECRET=${ADMIN_TOTP_SECRET// /}
ADMIN_TOTP_SECRET=${ADMIN_TOTP_SECRET^^}
totp_remainder=$(( ${#ADMIN_TOTP_SECRET} % 8 ))
if [[ -n $ADMIN_TOTP_SECRET && ( ! $ADMIN_TOTP_SECRET =~ ^[A-Z2-7]{32,}$ || ! $totp_remainder =~ ^(0|2|4|5|7)$ ) ]]; then echo 'STEPANEL_ADMIN_TOTP_SECRET must be an unpadded base32 secret of at least 160 bits.' >&2; exit 1; fi
if [[ -z $ADMIN_TOTP_SECRET ]]; then echo 'Production installations require STEPANEL_ADMIN_TOTP_SECRET for administrator MFA.' >&2; exit 1; fi
if [[ ${#ACCOUNT_KEY} -lt 32 || "$ACCOUNT_KEY" == *$'\n'* || "$ACCOUNT_KEY" == *$'\r'* ]]; then echo 'Production installations require STEPANEL_ACCOUNT_KEY of at least 32 characters and no newlines.' >&2; exit 1; fi
if [[ ! "$ADMIN_USERNAME" =~ ^[a-zA-Z0-9._-]{1,64}$ || "$ADMIN_USERNAME" == *$'\n'* || "$ADMIN_USERNAME" == *$'\r'* ]]; then echo "Invalid admin username." >&2; exit 1; fi
if [[ -z "$PANEL_HOSTNAME" ]]; then
  for panel_config in /etc/apache2/sites-available/stepanel.conf /etc/httpd/conf.d/stepanel.conf /etc/caddy/stepanel.d/panel.caddy; do
    [[ -f $panel_config ]] || continue
    PANEL_HOSTNAME=$(awk 'tolower($1) == "servername" { print tolower($2); exit } NR == 1 && $1 ~ /^[A-Za-z0-9.-]+$/ && $2 == "{" { print tolower($1); exit }' "$panel_config")
    [[ -n $PANEL_HOSTNAME ]] && break
  done
fi
if [[ -z "$WEB_SERVER" && -t 0 ]]; then
  read -r -p "Web server [caddy/apache/openlitespeed] (caddy): " WEB_SERVER
fi
WEB_SERVER="${WEB_SERVER:-caddy}"
WEB_SERVER="${WEB_SERVER,,}"
if [[ "$WEB_SERVER" != "apache" && "$WEB_SERVER" != "openlitespeed" && "$WEB_SERVER" != "caddy" ]]; then echo "STEPANEL_WEBSERVER must be apache, openlitespeed, or caddy." >&2; exit 1; fi
if [[ "$WEB_SERVER" == "openlitespeed" && "$INSTALL_TLS" == "1" ]]; then echo 'STEPANEL_INSTALL_TLS is not integrated with OpenLiteSpeed; configure TLS separately for this webserver.' >&2; exit 1; fi
if [[ "$WEB_SERVER" == "openlitespeed" && "$INSTALL_MODSEC" == "1" ]]; then echo 'STEPANEL_INSTALL_MODSEC is currently supported only with Apache.' >&2; exit 1; fi
if [[ "$WEB_SERVER" == "caddy" && "$INSTALL_MODSEC" == "1" ]]; then echo 'STEPANEL_INSTALL_MODSEC is currently supported only with Apache.' >&2; exit 1; fi
if [[ ! "$PANEL_HOSTNAME" =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$ ]]; then echo "Set STEPANEL_PANEL_HOSTNAME to the panel's fully qualified domain name." >&2; exit 1; fi
if [[ "$ADMIN_PASSWORD" == *$'\n'* || "$ADMIN_PASSWORD" == *$'\r'* || "$SESSION_SECRET" == *$'\n'* || "$SESSION_SECRET" == *$'\r'* || "$DB_PASSWORD" == *$'\n'* || "$DB_PASSWORD" == *$'\r'* ]]; then echo "Credentials may not contain newlines." >&2; exit 1; fi
if [[ "$DB_HOST" == *$'\n'* || "$DB_HOST" == *$'\r'* ]]; then echo "STEPANEL_DB_HOST may not contain newlines." >&2; exit 1; fi
if [[ -n "$DB_USER" && ! "$DB_USER" =~ ^[A-Za-z0-9_]{1,32}$ ]]; then echo "STEPANEL_DB_USER must contain only letters, numbers, and underscores." >&2; exit 1; fi
if [[ -n "$DB_USER" && -z "$DB_PASSWORD" ]]; then echo "STEPANEL_DB_PASSWORD is required when STEPANEL_DB_USER is set." >&2; exit 1; fi
if [[ "$WPRESS_EXTRACT" == *$'\n'* || "$WPRESS_EXTRACT" == *$'\r'* || "$WPCLI" == *$'\n'* || "$WPCLI" == *$'\r'* ]]; then echo "WordPress executable paths may not contain newlines." >&2; exit 1; fi
if [[ "$WPRESS_EXTRACT" != /* || "$WPCLI" != /* ]]; then echo "STEPANEL_WPRESS_EXTRACT and STEPANEL_WPCLI must be absolute executable paths in production." >&2; exit 1; fi

if [[ "$INSTALL_FAIL2BAN" != "0" && "$INSTALL_FAIL2BAN" != "1" ]]; then echo "STEPANEL_INSTALL_FAIL2BAN must be 0 or 1." >&2; exit 1; fi
if [[ "$INSTALL_FAIL2BAN" == "1" && -z "$FAIL2BAN_IGNORE_IP" && -t 0 ]]; then read -r -p "Trusted management IPs/CIDRs for Fail2ban (required): " FAIL2BAN_IGNORE_IP; fi
if [[ "$INSTALL_FAIL2BAN" == "1" && -z "$FAIL2BAN_IGNORE_IP" ]]; then echo "Set STEPANEL_FAIL2BAN_IGNORE_IP before enabling Fail2ban; refusing an unattended lockout risk." >&2; exit 1; fi
if [[ "$FAIL2BAN_IGNORE_IP" == *$'\n'* || "$FAIL2BAN_IGNORE_IP" == *$'\r'* ]]; then echo "STEPANEL_FAIL2BAN_IGNORE_IP may not contain newlines." >&2; exit 1; fi
if [[ -n "$FPM_LENS_BINARY" && ! -x "$FPM_LENS_BINARY" ]]; then echo "STEPANEL_FPM_LENS_BINARY must point to an executable fpm-lens binary." >&2; exit 1; fi
if [[ "$INSTALL_MODSEC" != "0" && "$INSTALL_MODSEC" != "1" ]]; then echo "STEPANEL_INSTALL_MODSEC must be 0 or 1." >&2; exit 1; fi
if [[ "$MODSEC_MODE" != "Off" && "$MODSEC_MODE" != "DetectionOnly" && "$MODSEC_MODE" != "On" ]]; then echo "STEPANEL_MODSEC_MODE must be Off, DetectionOnly, or On." >&2; exit 1; fi
if [[ "$INSTALL_MAIL" != "0" && "$INSTALL_MAIL" != "1" ]]; then echo "STEPANEL_INSTALL_MAIL must be 0 or 1." >&2; exit 1; fi
if [[ "$ACTIVATE_MAIL" != "0" && "$ACTIVATE_MAIL" != "1" ]]; then echo "STEPANEL_ACTIVATE_MAIL must be 0 or 1." >&2; exit 1; fi
if [[ "$INSTALL_FTP" != "0" && "$INSTALL_FTP" != "1" ]]; then echo "STEPANEL_INSTALL_FTP must be 0 or 1." >&2; exit 1; fi
if [[ "$ACTIVATE_FTP" != "0" && "$ACTIVATE_FTP" != "1" ]]; then echo "STEPANEL_ACTIVATE_FTP must be 0 or 1." >&2; exit 1; fi
if [[ "$ACTIVATE_MAIL" == "1" && "$INSTALL_MAIL" != "1" ]]; then echo "STEPANEL_ACTIVATE_MAIL requires STEPANEL_INSTALL_MAIL=1." >&2; exit 1; fi
if [[ "$ACTIVATE_FTP" == "1" && "$INSTALL_FTP" != "1" ]]; then echo "STEPANEL_ACTIVATE_FTP requires STEPANEL_INSTALL_FTP=1." >&2; exit 1; fi
if [[ "$ACTIVATE_FTP" == "1" && ( ! -f "$FTP_CERT_FILE" || ! -r "$FTP_CERT_FILE" || ! -f "$FTP_KEY_FILE" || ! -r "$FTP_KEY_FILE" ) ]]; then echo "Activating FTP requires readable STEPANEL_FTP_CERT_FILE and STEPANEL_FTP_KEY_FILE for FTPS." >&2; exit 1; fi
if [[ "$ACTIVATE_FTP" == "1" && ( "$FTP_CERT_FILE" != /* || "$FTP_KEY_FILE" != /* || "$FTP_CERT_FILE" =~ [[:space:]] || "$FTP_KEY_FILE" =~ [[:space:]] ) ]]; then echo "FTPS certificate and key paths must be absolute and contain no whitespace." >&2; exit 1; fi
if [[ ! "$FTP_PASSIVE_MIN" =~ ^[0-9]+$ || ! "$FTP_PASSIVE_MAX" =~ ^[0-9]+$ || "$FTP_PASSIVE_MIN" -lt 1024 || "$FTP_PASSIVE_MAX" -gt 65535 || "$FTP_PASSIVE_MIN" -ge "$FTP_PASSIVE_MAX" ]]; then echo "STEPANEL_FTP_PASSIVE_MIN/MAX must define a valid increasing port range." >&2; exit 1; fi
if [[ "$INSTALL_NODE" != "0" && "$INSTALL_NODE" != "1" ]]; then echo "STEPANEL_INSTALL_NODE must be 0 or 1." >&2; exit 1; fi
if [[ "$INSTALL_SECURITY" != "0" && "$INSTALL_SECURITY" != "1" ]]; then echo "STEPANEL_INSTALL_SECURITY must be 0 or 1." >&2; exit 1; fi
if [[ "$INSTALL_TLS" != "0" && "$INSTALL_TLS" != "1" ]]; then echo "STEPANEL_INSTALL_TLS must be 0 or 1." >&2; exit 1; fi
if [[ ! "$STAGE_RETENTION_HOURS" =~ ^[1-9][0-9]*$ ]]; then echo "STEPANEL_STAGE_RETENTION_HOURS must be a positive integer." >&2; exit 1; fi
if [[ ! "$MIN_FREE_BYTES" =~ ^[1-9][0-9]*$ ]]; then echo "STEPANEL_MIN_FREE_BYTES must be a positive integer." >&2; exit 1; fi
if [[ ! $MAX_UPLOAD_BYTES =~ ^[1-9][0-9]*$ ]] || (( MAX_UPLOAD_BYTES > 21474836480 )); then echo 'STEPANEL_MAX_UPLOAD_BYTES must be between 1 and 21474836480.' >&2; exit 1; fi
if [[ ! $MAX_ARCHIVE_ENTRIES =~ ^[1-9][0-9]*$ ]] || (( MAX_ARCHIVE_ENTRIES > 1000000 )); then echo 'STEPANEL_MAX_ARCHIVE_ENTRIES must be between 1 and 1000000.' >&2; exit 1; fi
if [[ ! $MAX_CONCURRENT_JOBS =~ ^[1-9][0-9]*$ ]] || (( MAX_CONCURRENT_JOBS > 32 )); then echo 'STEPANEL_MAX_CONCURRENT_JOBS must be between 1 and 32.' >&2; exit 1; fi
if [[ "$REQUIRE_OFFSITE_BACKUP" != "0" && "$REQUIRE_OFFSITE_BACKUP" != "1" ]]; then echo 'STEPANEL_REQUIRE_OFFSITE_BACKUP must be 0 or 1.' >&2; exit 1; fi
if [[ "$REQUIRE_OFFSITE_BACKUP" != "1" ]]; then echo 'Production installations require STEPANEL_REQUIRE_OFFSITE_BACKUP=1.' >&2; exit 1; fi
if [[ "$REQUIRE_OFFSITE_BACKUP" == "1" && -z "${STEPANEL_OFFSITE_TARGET:-}" ]]; then echo 'STEPANEL_REQUIRE_OFFSITE_BACKUP=1 requires STEPANEL_OFFSITE_TARGET.' >&2; exit 1; fi
if [[ "$REQUIRE_OFFSITE_BACKUP" == "1" ]] && ! command -v rclone >/dev/null 2>&1; then
  echo 'rclone is required before installing production StePanel with offsite backups enabled; install and configure it first.' >&2
  exit 1
fi
if [[ "$INSTALL_DB_ADMIN" != "0" && "$INSTALL_DB_ADMIN" != "1" ]]; then echo 'STEPANEL_INSTALL_DB_ADMIN must be 0 or 1.' >&2; exit 1; fi
[[ "$NODE_VERSIONS" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+(,v?[0-9]+\.[0-9]+\.[0-9]+)*$ ]] || { echo "Invalid STEPANEL_NODE_VERSIONS." >&2; exit 1; }

# shellcheck source=/etc/os-release
source /etc/os-release
if command -v apt-get >/dev/null; then PKG="apt"; APACHE_SERVICE="apache2"; elif command -v dnf >/dev/null; then PKG="dnf"; APACHE_SERVICE="httpd"; else echo "Unsupported operating system: $ID" >&2; exit 1; fi

if [[ -z "${STEPANEL_DB_ENGINE:-}" && -t 0 ]]; then
  read -r -p "Database engine [mysql/mariadb/postgresql] (mysql): " DB_ENGINE
fi
DB_ENGINE="${DB_ENGINE:-mysql}"
DB_ENGINE="${DB_ENGINE,,}"
if [[ "$DB_ENGINE" != "mysql" && "$DB_ENGINE" != "mariadb" && "$DB_ENGINE" != "postgresql" ]]; then echo "STEPANEL_DB_ENGINE must be mysql, mariadb, or postgresql." >&2; exit 1; fi
if [[ -z "$DB_ADMIN_URL" ]]; then DB_ADMIN_URL="$([[ "$DB_ENGINE" == "postgresql" ]] && echo /phppgadmin || echo /phpmyadmin)"; fi
if [[ ! "$DB_ADMIN_URL" =~ ^/[A-Za-z0-9][A-Za-z0-9._~/-]*$ || "$DB_ADMIN_URL" == //* || "$DB_ADMIN_URL" == *//* || "$DB_ADMIN_URL" == *"/../"* || "$DB_ADMIN_URL" == *"/./"* || "$DB_ADMIN_URL" == *"/.." || "$DB_ADMIN_URL" == *"/." ]]; then echo 'STEPANEL_DB_ADMIN_URL must be a normalized local URL path beginning with /.' >&2; exit 1; fi
DB_ADMIN_ALLOW="${DB_ADMIN_ALLOW//,/ }"
for db_admin_network in $DB_ADMIN_ALLOW; do
  [[ "$db_admin_network" =~ ^[0-9A-Fa-f:.]+(/[0-9]{1,3})?$ ]] || { echo 'STEPANEL_DB_ADMIN_ALLOW must contain only IP addresses or CIDR networks.' >&2; exit 1; }
done
[[ -n "${DB_ADMIN_ALLOW// /}" ]] || { echo 'STEPANEL_DB_ADMIN_ALLOW must contain at least one trusted IP address or network.' >&2; exit 1; }
if [[ -z "${STEPANEL_DB_VERSION:-}" && -t 0 ]]; then
  read -r -p "${DB_ENGINE} version (default distro version): " DB_VERSION
fi
DB_VERSION="${DB_VERSION:-default}"
if [[ ! "$DB_VERSION" =~ ^(default|[0-9][0-9A-Za-z.+:~-]*)$ ]]; then echo "Invalid database version: $DB_VERSION" >&2; exit 1; fi
if [[ -n "$ADMIN_PASSWORD" ]]; then ADMIN_PASSWORD_HASH="$(printf '%s' "$ADMIN_PASSWORD" | "$ROOT_DIR/stepanel" hash-password)"; else ADMIN_PASSWORD_HASH="$EXISTING_ADMIN_PASSWORD_HASH"; fi
unset ADMIN_PASSWORD

if [[ "$DB_ENGINE" == "mysql" ]]; then DB_PACKAGE="mysql-server"; DB_PHP_PACKAGE="php-mysql"; DB_SERVICE="mysql"; elif [[ "$DB_ENGINE" == "mariadb" ]]; then DB_PACKAGE="mariadb-server"; DB_PHP_PACKAGE="php-mysql"; DB_SERVICE="mariadb"; else DB_PACKAGE="postgresql-server"; DB_PHP_PACKAGE="php-pgsql"; DB_SERVICE="postgresql"; fi
if [[ "$PKG" == "apt" ]]; then export DEBIAN_FRONTEND=noninteractive; apt-get update; apt-get install -y php php-cli php-fpm "$DB_PHP_PACKAGE" php-curl php-mbstring php-xml acl tar gzip ca-certificates curl sudo logrotate
else dnf install -y php php-cli php-fpm "$DB_PHP_PACKAGE" php-curl php-mbstring php-xml acl tar gzip ca-certificates curl sudo logrotate; fi
if [[ "$WEB_SERVER" == "apache" ]]; then
  if [[ "$PKG" == "apt" ]]; then apt-get install -y apache2; APACHE_SERVICE=apache2; else dnf install -y httpd; APACHE_SERVICE=httpd; fi
elif [[ "$WEB_SERVER" == "openlitespeed" ]]; then
  LSWSCTRL="$(command -v lswsctrl 2>/dev/null || printf /usr/local/lsws/bin/lswsctrl)"
  if [[ ! -x "$LSWSCTRL" ]]; then
    if [[ "$PKG" == "apt" ]]; then apt-get install -y openlitespeed; else dnf install -y openlitespeed; fi
  fi
  [[ -x "$LSWSCTRL" ]] || { echo 'OpenLiteSpeed was selected but lswsctrl is unavailable; configure the OpenLiteSpeed repository first.' >&2; exit 1; }
  APACHE_SERVICE=lsws
else
  if [[ "$PKG" == "apt" ]]; then apt-get install -y caddy; else dnf install -y caddy; fi
  command -v caddy >/dev/null 2>&1 || { echo 'Caddy was selected but the caddy executable is unavailable; configure the Caddy repository first.' >&2; exit 1; }
  APACHE_SERVICE=caddy
fi

if [[ "$DB_ENGINE" == "postgresql" && "$PKG" == "dnf" && "$DB_VERSION" != "default" ]]; then
  dnf module list postgresql --all >/dev/null 2>&1 || { echo 'PostgreSQL AppStream metadata is unavailable; enable the appropriate RHEL-family repositories first.' >&2; exit 1; }
  if ! dnf module list postgresql --all 2>/dev/null | awk -v requested="$DB_VERSION" '$1 == "postgresql" && $2 == requested {found=1} END {exit(found ? 0 : 1)}'; then
    echo "Requested PostgreSQL AppStream stream $DB_VERSION is not available." >&2
    dnf module list postgresql --all || true
    exit 1
  fi
  dnf module enable -y "postgresql:$DB_VERSION"
fi
if [[ "$DB_VERSION" == "default" ]]; then
  if [[ "$PKG" == "apt" ]]; then
    if [[ "$DB_ENGINE" == "postgresql" ]]; then apt-get install -y postgresql postgresql-contrib; else apt-get install -y "$DB_PACKAGE"; fi
  else
    dnf install -y "$DB_PACKAGE"
  fi
elif [[ "$PKG" == "apt" ]]; then
  [[ "$DB_ENGINE" != "postgresql" ]] || { echo 'Explicit PostgreSQL versions on Debian/Ubuntu require a configured versioned PostgreSQL repository; use an AppStream stream on RHEL-family systems.' >&2; exit 1; }
  if ! apt-cache madison "$DB_PACKAGE" | awk '{print $3}' | grep -Fxq "$DB_VERSION"; then echo "Requested $DB_PACKAGE version $DB_VERSION is not available. Configure the appropriate repository first." >&2; apt-cache madison "$DB_PACKAGE" || true; exit 1; fi
  apt-get install -y "$DB_PACKAGE=$DB_VERSION"
elif [[ "$DB_ENGINE" == "postgresql" ]]; then
  # The selected AppStream module controls the PostgreSQL server package
  # version; the RPM itself remains named postgresql-server.
  dnf install -y "$DB_PACKAGE"
else
  if ! dnf --assumeno install "$DB_PACKAGE-$DB_VERSION" >/dev/null 2>&1; then echo "Requested $DB_PACKAGE version $DB_VERSION is not available. Enable the appropriate DNF repository/module first." >&2; dnf list --showduplicates "$DB_PACKAGE" || true; exit 1; fi
  dnf install -y "$DB_PACKAGE-$DB_VERSION"
fi
if [[ "$INSTALL_DB_ADMIN" == "1" ]]; then
  if [[ "$DB_ENGINE" == "postgresql" ]]; then DB_ADMIN_PACKAGE=phppgadmin; DB_ADMIN_PATH=/usr/share/phppgadmin; else DB_ADMIN_PACKAGE=phpmyadmin; DB_ADMIN_PATH=/usr/share/phpmyadmin; fi
  if [[ "$PKG" == "apt" ]]; then
    dpkg-query -W "$DB_ADMIN_PACKAGE" >/dev/null 2>&1 || apt-cache show "$DB_ADMIN_PACKAGE" >/dev/null 2>&1 || { echo "$DB_ADMIN_PACKAGE is unavailable; enable the distribution repository that provides it." >&2; exit 1; }
    apt-get install -y "$DB_ADMIN_PACKAGE"
  else
    if [[ "$DB_ADMIN_PACKAGE" == "phpmyadmin" ]]; then DB_ADMIN_PACKAGE=phpMyAdmin; DB_ADMIN_PATH=/usr/share/phpMyAdmin; else DB_ADMIN_PATH=/usr/share/phpPgAdmin; fi
    rpm -q "$DB_ADMIN_PACKAGE" >/dev/null 2>&1 || dnf --quiet list --available "$DB_ADMIN_PACKAGE" >/dev/null 2>&1 || { echo "$DB_ADMIN_PACKAGE is unavailable; enable a trusted repository such as EPEL that provides it." >&2; exit 1; }
    dnf install -y "$DB_ADMIN_PACKAGE"
  fi
  [[ -d "$DB_ADMIN_PATH" ]] || { echo "$DB_ADMIN_PACKAGE installed but its expected application directory $DB_ADMIN_PATH is missing." >&2; exit 1; }
fi

WEB_GROUP="$([[ "$PKG" == "apt" ]] && printf www-data || printf apache)"
if [[ "$WEB_SERVER" == "openlitespeed" ]]; then WEB_GROUP="$(id -gn nobody 2>/dev/null || printf nogroup)"; fi
if [[ "$WEB_SERVER" == "caddy" ]]; then WEB_GROUP="$(id -gn caddy 2>/dev/null || printf caddy)"; fi
getent group "$WEB_GROUP" >/dev/null || { echo "Web server group $WEB_GROUP was not created by the package installation." >&2; exit 1; }
if [[ "$WEB_SERVER" == "openlitespeed" ]]; then PROXY_ROOT=/usr/local/lsws/conf/vhosts/stepanel/proxy; VHOST_ROOT=/usr/local/lsws/conf/vhosts/stepanel/sites; elif [[ "$WEB_SERVER" == "caddy" ]]; then PROXY_ROOT=/etc/caddy/stepanel.d; VHOST_ROOT=/etc/caddy/stepanel.d; elif [[ "$PKG" == "apt" ]]; then PROXY_ROOT=/etc/apache2/stepanel-proxy; VHOST_ROOT=/etc/apache2/stepanel-sites; else PROXY_ROOT=/etc/httpd/conf.d/stepanel-proxy; VHOST_ROOT=/etc/httpd/conf.d/stepanel-sites; fi

if [[ "$DB_ENGINE" == "postgresql" && "$PKG" == "dnf" && ! -f /var/lib/pgsql/data/PG_VERSION ]]; then
  command -v postgresql-setup >/dev/null 2>&1 || { echo 'postgresql-setup is unavailable after PostgreSQL installation.' >&2; exit 1; }
  postgresql-setup --initdb
fi
systemctl enable --now "$DB_SERVICE"
mapfile -t FPM_UNITS < <(systemctl list-unit-files --type=service --no-legend 'php*-fpm.service' 'php-fpm.service' 2>/dev/null | awk '{print $1}')
(( ${#FPM_UNITS[@]} > 0 )) || { echo 'No PHP-FPM systemd service was found after installation.' >&2; exit 1; }
systemctl enable --now "${FPM_UNITS[@]}"
DB_LOCAL_HELPER=0
if [[ -z "$DB_USER" ]]; then
  [[ "$DB_HOST" == "localhost" ]] || { echo "Set STEPANEL_DB_USER and STEPANEL_DB_PASSWORD for a non-local database host." >&2; exit 1; }
  DB_LOCAL_HELPER=1
  if [[ "$DB_ENGINE" == "postgresql" ]]; then
    runuser -u postgres -- psql --tuples-only --no-align --command 'SELECT 1' | grep -Fxq 1 || { echo "Local PostgreSQL socket administration is unavailable." >&2; exit 1; }
  else
    DB_CLIENT=mysql
    command -v mariadb >/dev/null 2>&1 && DB_CLIENT=mariadb
    "$DB_CLIENT" --protocol=socket --batch --skip-column-names --execute 'SELECT 1' | grep -Fxq 1 || { echo "Local database socket administration is unavailable." >&2; exit 1; }
  fi
else
  if [[ "$DB_ENGINE" == "postgresql" ]]; then
    DB_CLIENT=psql
    database_check=(env PGPASSWORD="$DB_PASSWORD" psql --no-password --host "$DB_HOST" --username "$DB_USER" --dbname postgres --tuples-only --no-align --set ON_ERROR_STOP=1 --command 'SELECT 1')
  else
    DB_CLIENT=mysql
    command -v mariadb >/dev/null 2>&1 && DB_CLIENT=mariadb
    database_check=(env MYSQL_PWD="$DB_PASSWORD" "$DB_CLIENT" --host "$DB_HOST" --user "$DB_USER" --batch --skip-column-names --execute 'SELECT 1')
  fi
  if ! "${database_check[@]}" | grep -Fxq 1; then
    echo "StePanel database credentials could not connect to $DB_HOST." >&2
    exit 1
  fi
fi

install_mail_stack() {
  if [[ "$PKG" == "apt" ]]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get install -y exim4 dovecot-core dovecot-imapd spamassassin
    MAIL_SPAM_SERVICE="spamassassin"
  else
    dnf install -y exim dovecot spamassassin
    MAIL_SPAM_SERVICE="spamd"
  fi
  install -d -m 0750 "$DATA_DIR/mail"
  if [[ "$ACTIVATE_MAIL" == "0" ]]; then
    [[ "$MAIL_EXIM_PREEXISTING" == "1" ]] || systemctl disable --now "$([[ "$PKG" == "apt" ]] && echo exim4 || echo exim)" 2>/dev/null || true
    [[ "$MAIL_DOVECOT_PREEXISTING" == "1" ]] || systemctl disable --now dovecot 2>/dev/null || true
    [[ "$MAIL_SPAM_PREEXISTING" == "1" ]] || systemctl disable --now "$MAIL_SPAM_SERVICE" 2>/dev/null || true
  fi
}

install_ftp_stack() {
  local config backup
  if [[ "$FTP_PREEXISTING" == "1" && "$ACTIVATE_FTP" == "0" ]]; then
    echo "Existing vsftpd installation detected; preserving its configuration and service state."
    return
  fi
  if [[ "$PKG" == "apt" ]]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get install -y vsftpd
    config="/etc/vsftpd.conf"
  else
    dnf install -y vsftpd
    config="/etc/vsftpd/vsftpd.conf"
  fi
  backup="${config}.bak.$(date -u +%Y%m%d%H%M%S)"
  [[ -e "$config" ]] && cp -p "$config" "$backup"
  install -d -m 0750 /etc/vsftpd
  cat > "$config" <<EOF
# Managed by StePanel. Review and enable FTPS before internet exposure.
listen=YES
listen_ipv6=NO
anonymous_enable=NO
local_enable=YES
write_enable=YES
local_umask=022
chroot_local_user=YES
allow_writeable_chroot=YES
user_sub_token=\$USER
local_root=/var/www/sites/\$USER
pasv_min_port=$FTP_PASSIVE_MIN
pasv_max_port=$FTP_PASSIVE_MAX
use_localtime=YES
xferlog_enable=YES
log_ftp_protocol=YES
EOF
  if [[ "$ACTIVATE_FTP" == "1" ]]; then
    printf 'ssl_enable=YES\nallow_anon_ssl=NO\nforce_local_data_ssl=YES\nforce_local_logins_ssl=YES\nssl_tlsv1=YES\nssl_sslv2=NO\nssl_sslv3=NO\nrsa_cert_file=%s\nrsa_private_key_file=%s\n' "$FTP_CERT_FILE" "$FTP_KEY_FILE" >> "$config"
  else
    printf 'ssl_enable=NO\n' >> "$config"
  fi
  if ! vsftpd -olisten=NO -olisten_ipv6=NO "$config" >/dev/null 2>&1; then
    [[ -e "$backup" ]] && mv -f "$backup" "$config"
    echo "vsftpd rejected its configuration; changes were rolled back." >&2
    return 1
  fi
  if [[ "$ACTIVATE_FTP" == "0" && "$FTP_PREEXISTING" == "0" ]]; then
    systemctl disable --now vsftpd 2>/dev/null || true
  fi
}

MAIL_EXIM_PREEXISTING=0
MAIL_DOVECOT_PREEXISTING=0
MAIL_SPAM_PREEXISTING=0
FTP_PREEXISTING=0
if command -v exim >/dev/null 2>&1 || command -v exim4 >/dev/null 2>&1; then MAIL_EXIM_PREEXISTING=1; fi
command -v dovecot >/dev/null 2>&1 && MAIL_DOVECOT_PREEXISTING=1
if command -v spamd >/dev/null 2>&1 || command -v spamassassin >/dev/null 2>&1; then MAIL_SPAM_PREEXISTING=1; fi
command -v vsftpd >/dev/null 2>&1 && FTP_PREEXISTING=1
if [[ "$INSTALL_MAIL" == "1" ]]; then install_mail_stack; fi
if [[ "$INSTALL_FTP" == "1" ]]; then install_ftp_stack; fi

configure_modsecurity() {
  local apache_conf modsec_conf audit_log crs_load backup apache_backup
  if [[ "$PKG" == "apt" ]]; then
    apt-get install -y libapache2-mod-security2 modsecurity-crs
    a2enmod security2 >/dev/null
    apache_conf="/etc/apache2/conf-available/stepanel-modsecurity.conf"
    modsec_conf="/etc/modsecurity/stepanel.conf"
    audit_log="/var/log/apache2/modsec_audit.log"
  else
    dnf install -y mod_security mod_security_crs
    apache_conf="/etc/httpd/conf.d/stepanel-modsecurity.conf"
    modsec_conf="/etc/modsecurity.d/stepanel.conf"
    audit_log="/var/log/httpd/modsec_audit.log"
  fi
  install -d -m 0750 "$(dirname "$modsec_conf")"
  backup="${modsec_conf}.bak.$(date -u +%Y%m%d%H%M%S)"
  apache_backup="${apache_conf}.bak.$(date -u +%Y%m%d%H%M%S)"
  if [[ -e "$apache_conf" ]]; then cp -p "$apache_conf" "$apache_backup"; fi
  if [[ -e "$modsec_conf" ]]; then cp -p "$modsec_conf" "$backup"; fi
  printf 'SecRuleEngine %s\nSecAuditEngine RelevantOnly\nSecAuditLogType Serial\nSecAuditLog %s\nSecRequestBodyAccess On\nSecResponseBodyAccess Off\n' "$MODSEC_MODE" "$audit_log" > "$modsec_conf"
  crs_load=""
  for candidate in /usr/share/modsecurity-crs/owasp-crs.load /etc/modsecurity/owasp-crs.load /etc/modsecurity.d/owasp-crs.load; do
    if [[ -f "$candidate" ]]; then crs_load="$candidate"; break; fi
  done
  if [[ -n "$crs_load" ]]; then printf 'IncludeOptional %s\n' "$crs_load" >> "$modsec_conf"; fi
  printf 'IncludeOptional %s\n' "$modsec_conf" > "$apache_conf"
  if [[ "$PKG" == "apt" ]]; then a2enconf stepanel-modsecurity >/dev/null 2>&1 || true; fi
  if ! apachectl -t >/dev/null 2>&1 && ! httpd -t >/dev/null 2>&1; then
    if [[ -e "$backup" ]]; then mv -f "$backup" "$modsec_conf"; else rm -f "$modsec_conf"; fi
    if [[ -e "$apache_backup" ]]; then mv -f "$apache_backup" "$apache_conf"; else rm -f "$apache_conf"; fi
    if [[ "$PKG" == "apt" ]]; then a2disconf stepanel-modsecurity >/dev/null 2>&1 || true; fi
    echo "Apache rejected the ModSecurity configuration; changes were rolled back." >&2
    return 1
  fi
  echo "ModSecurity configured in $MODSEC_MODE mode${crs_load:+ with OWASP CRS}."
}

command -v flock >/dev/null 2>&1 || { echo 'flock is required for installation locking.' >&2; exit 1; }
exec 8>/run/lock/stepanel-apache.lock
flock -x 8
if [[ "$INSTALL_MODSEC" == "1" ]]; then configure_modsecurity; fi

INSTALL_TXN=$(mktemp -d /var/tmp/stepanel-install.XXXXXX)
chmod 0700 "$INSTALL_TXN"
declare -a TXN_TARGETS=() TXN_BACKUPS=() TXN_EXISTED=() TXN_TEMPS=()
INSTALL_COMMITTED=0
STEPANEL_WAS_ACTIVE=0
STEPANEL_WAS_ENABLED=0
STEPANEL_WORKER_WAS_ACTIVE=0
STEPANEL_WORKER_WAS_ENABLED=0
systemctl is-active --quiet stepanel.service 2>/dev/null && STEPANEL_WAS_ACTIVE=1
systemctl is-enabled --quiet stepanel.service 2>/dev/null && STEPANEL_WAS_ENABLED=1
systemctl is-active --quiet stepanel-worker.service 2>/dev/null && STEPANEL_WORKER_WAS_ACTIVE=1
systemctl is-enabled --quiet stepanel-worker.service 2>/dev/null && STEPANEL_WORKER_WAS_ENABLED=1

backup_managed_target() {
  local target=$1 index backup existing=0
  for index in "${!TXN_TARGETS[@]}"; do [[ ${TXN_TARGETS[$index]} != "$target" ]] || return 0; done
  [[ ! -d $target || -L $target ]] || { echo "Refusing unexpected directory at managed file path $target." >&2; return 1; }
  backup="$INSTALL_TXN/${#TXN_TARGETS[@]}"
  if [[ -e $target || -L $target ]]; then cp -a "$target" "$backup"; existing=1; fi
  TXN_TARGETS+=("$target")
  TXN_BACKUPS+=("$backup")
  TXN_EXISTED+=("$existing")
}

rollback_install() {
  local status=$? index temporary
  trap - ERR EXIT INT TERM
  if (( INSTALL_COMMITTED )); then exit "$status"; fi
  echo 'Installation failed; restoring the previous StePanel-owned files.' >&2
  for (( index=${#TXN_TARGETS[@]}-1; index>=0; index-- )); do
    rm -f "${TXN_TARGETS[$index]}"
    if [[ ${TXN_EXISTED[$index]} == 1 ]]; then cp -a "${TXN_BACKUPS[$index]}" "${TXN_TARGETS[$index]}"; fi
  done
  for temporary in "${TXN_TEMPS[@]}"; do rm -f "$temporary"; done
  systemctl daemon-reload 2>/dev/null || true
  if (( STEPANEL_WAS_ENABLED )); then systemctl enable stepanel.service 2>/dev/null || true; else systemctl disable stepanel.service 2>/dev/null || true; fi
  if (( STEPANEL_WAS_ACTIVE )); then systemctl restart stepanel.service 2>/dev/null || true; else systemctl stop stepanel.service 2>/dev/null || true; fi
  if (( STEPANEL_WORKER_WAS_ENABLED )); then systemctl enable stepanel-worker.service 2>/dev/null || true; else systemctl disable stepanel-worker.service 2>/dev/null || true; fi
  if (( STEPANEL_WORKER_WAS_ACTIVE )); then systemctl restart stepanel-worker.service 2>/dev/null || true; else systemctl stop stepanel-worker.service 2>/dev/null || true; fi
  if command -v apachectl >/dev/null 2>&1 && apachectl -t >/dev/null 2>&1; then systemctl reload "$APACHE_SERVICE" 2>/dev/null || true
  elif command -v httpd >/dev/null 2>&1 && httpd -t >/dev/null 2>&1; then systemctl reload "$APACHE_SERVICE" 2>/dev/null || true
  fi
  rm -rf "$INSTALL_TXN"
  exit "$status"
}
trap rollback_install ERR EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

managed_targets=(
  "$APP_DIR/stepanel"
  "$APP_DIR/integrations/install-fail2ban.sh"
  /usr/local/sbin/stepanel-appctl
  /usr/local/sbin/stepanel-gitctl
  /usr/local/sbin/stepanel-proxyctl
  /usr/local/sbin/stepanel-sitectl
  /usr/local/sbin/stepanel-vhostctl
  /usr/local/sbin/stepanel-dbctl
  /etc/stepanel-dbctl.conf
  /etc/stepanel-db.password
  "$APP_DIR/web/index.html"
  "$APP_DIR/web/static/app.css"
  "$APP_DIR/web/static/import.css"
  "$APP_DIR/web/static/cpmove.js"
  "$APP_DIR/web/static/deploy.js"
  "$APP_DIR/web/static/certificates.js"
  "$APP_DIR/web/static/wpress.js"
  "$APP_DIR/web/static/database.js"
  "$APP_DIR/web/static/favicon.svg"
  "$ENV_FILE"
  /etc/systemd/system/stepanel.service
  /etc/systemd/system/stepanel-worker.service
  /etc/logrotate.d/stepanel
  /etc/sudoers.d/stepanel
  /etc/stepanel-audit.key
)
if [[ "$INSTALL_DB_ADMIN" == "1" && "$WEB_SERVER" == "apache" && "$PKG" == "apt" ]]; then
  managed_targets+=(/etc/apache2/stepanel-panel/db-admin.conf /etc/apache2/conf-enabled/phpmyadmin.conf /etc/apache2/conf-enabled/phppgadmin.conf)
elif [[ "$INSTALL_DB_ADMIN" == "1" && "$WEB_SERVER" == "apache" ]]; then
  managed_targets+=(/etc/httpd/stepanel-panel/db-admin.conf)
fi
if [[ "$WEB_SERVER" == "apache" && "$PKG" == "apt" ]]; then
  managed_targets+=(/etc/apache2/sites-available/stepanel.conf /etc/apache2/sites-enabled/stepanel.conf /etc/apache2/conf-enabled/stepanel-proxy.conf)
elif [[ "$WEB_SERVER" == "apache" ]]; then
  managed_targets+=(/etc/httpd/conf.d/stepanel.conf /etc/httpd/conf.d/stepanel-proxy.conf)
elif [[ "$WEB_SERVER" == "caddy" ]]; then
  managed_targets+=(/etc/caddy/Caddyfile /etc/caddy/stepanel.d/panel.caddy)
fi
if [[ "$INSTALL_TLS" == "1" && "$WEB_SERVER" == "apache" ]]; then managed_targets+=(/usr/local/sbin/stepanel-certbot); fi
if [[ -n "$FPM_LENS_BINARY" ]]; then managed_targets+=(/usr/local/bin/fpm-lens); fi
if [[ "$INSTALL_SECURITY" == "1" ]]; then managed_targets+=(/usr/local/sbin/stepanel-malware-guard /etc/systemd/system/stepanel-malware-guard.service); fi
for managed_target in "${managed_targets[@]}"; do backup_managed_target "$managed_target"; done
if (( STEPANEL_WAS_ACTIVE )); then systemctl stop stepanel.service; fi
if (( STEPANEL_WORKER_WAS_ACTIVE )); then systemctl stop stepanel-worker.service; fi

install -d -m 0750 "$APP_DIR" "$DATA_DIR/imports" "$DATA_DIR/mail" "$DATA_DIR/apps" /var/www/sites
install -d -m 0755 -o root -g root "$PROXY_ROOT" "$VHOST_ROOT"
install -d -m 0700 -o root -g root /var/lib/stepanel-privileged
install -d -m 0755 "$APP_DIR/integrations"
id "$APP_USER" >/dev/null 2>&1 || useradd --system --home-dir "$APP_DIR" --shell /usr/sbin/nologin "$APP_USER"
install -d -m 0750 -o "$APP_USER" -g "$APP_USER" /var/backups/stepanel
usermod -a -G "$WEB_GROUP" "$APP_USER"
if [[ "$INSTALL_NODE" == "1" ]]; then bash "$ROOT_DIR/deploy/integrations/install-node-nvm.sh" "$APP_USER" "$APP_DIR/.nvm" "$NODE_VERSIONS"; fi
if [[ "$INSTALL_SECURITY" == "1" ]]; then
  if [[ "$PKG" == "apt" ]]; then apt-get install -y clamav clamav-daemon inotify-tools; else dnf install -y clamav clamav-update inotify-tools; fi
  install -d -m 0700 "$DATA_DIR/quarantine"
fi
if [[ "$INSTALL_TLS" == "1" && "$WEB_SERVER" == "apache" ]]; then
  if [[ "$PKG" == "apt" ]]; then apt-get install -y certbot python3-certbot-apache; else dnf install -y certbot python3-certbot-apache; fi
fi
install -m 0755 "$ROOT_DIR/stepanel" "$APP_DIR/stepanel"
printf '%s\n' "$AUDIT_KEY" > "$INSTALL_TXN/audit.key"
install -m 0600 -o root -g root "$INSTALL_TXN/audit.key" /etc/stepanel-audit.key
install -m 0755 "$ROOT_DIR/deploy/integrations/install-fail2ban.sh" "$APP_DIR/integrations/install-fail2ban.sh"
install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-appctl" /usr/local/sbin/stepanel-appctl
install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-gitctl" /usr/local/sbin/stepanel-gitctl
install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-runnerctl" /usr/local/sbin/stepanel-runnerctl
if [[ "$WEB_SERVER" == "caddy" ]]; then
  install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-caddy-proxyctl" /usr/local/sbin/stepanel-proxyctl
elif [[ "$WEB_SERVER" == "openlitespeed" ]]; then
  install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-ols-proxyctl" /usr/local/sbin/stepanel-proxyctl
else
  install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-proxyctl" /usr/local/sbin/stepanel-proxyctl
fi
install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-sitectl" /usr/local/sbin/stepanel-sitectl
if [[ "$WEB_SERVER" == "caddy" ]]; then
  install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-caddy-vhostctl" /usr/local/sbin/stepanel-vhostctl
elif [[ "$WEB_SERVER" == "openlitespeed" ]]; then
  install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-ols-vhostctl" /usr/local/sbin/stepanel-vhostctl
else
  install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-vhostctl" /usr/local/sbin/stepanel-vhostctl
fi
install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-dbctl" /usr/local/sbin/stepanel-dbctl
printf 'engine=%s\n' "$DB_ENGINE" > "$INSTALL_TXN/stepanel-dbctl.conf"
install -m 0600 -o root -g root "$INSTALL_TXN/stepanel-dbctl.conf" /etc/stepanel-dbctl.conf
printf '%s' "$DB_PASSWORD" > "$INSTALL_TXN/stepanel-db.password"
install -m 0600 -o root -g root "$INSTALL_TXN/stepanel-db.password" /etc/stepanel-db.password
if [[ "$INSTALL_TLS" == "1" && "$WEB_SERVER" == "apache" ]]; then install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-certbot" /usr/local/sbin/stepanel-certbot; fi
if [[ -n "$FPM_LENS_BINARY" ]]; then install -m 0755 "$FPM_LENS_BINARY" /usr/local/bin/fpm-lens; fi
install -m 0644 -D "$ROOT_DIR/web/index.html" "$APP_DIR/web/index.html"
install -m 0644 -D "$ROOT_DIR/web/static/app.css" "$APP_DIR/web/static/app.css"
install -m 0644 -D "$ROOT_DIR/web/static/import.css" "$APP_DIR/web/static/import.css"
install -m 0644 -D "$ROOT_DIR/web/static/cpmove.js" "$APP_DIR/web/static/cpmove.js"
install -m 0644 -D "$ROOT_DIR/web/static/deploy.js" "$APP_DIR/web/static/deploy.js"
install -m 0644 -D "$ROOT_DIR/web/static/certificates.js" "$APP_DIR/web/static/certificates.js"
install -m 0644 -D "$ROOT_DIR/web/static/wpress.js" "$APP_DIR/web/static/wpress.js"
install -m 0644 -D "$ROOT_DIR/web/static/database.js" "$APP_DIR/web/static/database.js"
install -m 0644 -D "$ROOT_DIR/web/static/htaccess.js" "$APP_DIR/web/static/htaccess.js"
install -m 0644 -D "$ROOT_DIR/web/static/favicon.svg" "$APP_DIR/web/static/favicon.svg"
if [[ "$WEB_SERVER" == "apache" && "$PKG" == "apt" ]]; then
  install -m 0644 "$ROOT_DIR/deploy/apache/stepanel.conf" /etc/apache2/sites-available/stepanel.conf
  sed -i "s/panel\.example\.com/$PANEL_HOSTNAME/g" /etc/apache2/sites-available/stepanel.conf
  a2enmod proxy proxy_http proxy_fcgi setenvif rewrite headers >/dev/null
  if [[ -e /etc/apache2/conf-enabled/stepanel-proxy.conf ]]; then a2disconf stepanel-proxy >/dev/null; fi
  a2ensite stepanel >/dev/null
elif [[ "$WEB_SERVER" == "apache" ]]; then
  install -m 0644 "$ROOT_DIR/deploy/apache/stepanel-rhel.conf" /etc/httpd/conf.d/stepanel.conf
  sed -i "s/panel\.example\.com/$PANEL_HOSTNAME/g" /etc/httpd/conf.d/stepanel.conf
  if [[ -f /etc/httpd/conf.d/stepanel-proxy.conf ]]; then printf '%s\n' '# Superseded by the root-owned stepanel-proxy directory.' > /etc/httpd/conf.d/stepanel-proxy.conf; fi
fi
if [[ "$INSTALL_DB_ADMIN" == "1" && "$WEB_SERVER" == "apache" ]]; then
  db_admin_conf="$([[ "$PKG" == "apt" ]] && echo /etc/apache2/stepanel-panel/db-admin.conf || echo /etc/httpd/stepanel-panel/db-admin.conf)"
  db_admin_candidate="$INSTALL_TXN/stepanel-db-admin.conf"
  install -d -m 0755 -o root -g root "$(dirname "$db_admin_conf")"
  {
    printf 'Alias "%s" "%s"\n' "$DB_ADMIN_URL" "$DB_ADMIN_PATH"
    printf '<Directory "%s">\n' "$DB_ADMIN_PATH"
    printf '    DirectoryIndex index.php\n    Options SymLinksIfOwnerMatch\n    AllowOverride None\n    Require ip %s\n</Directory>\n' "$DB_ADMIN_ALLOW"
  } > "$db_admin_candidate"
  rm -f "$db_admin_conf"
  install -m 0644 -o root -g root "$db_admin_candidate" "$db_admin_conf"
  if [[ "$PKG" == "apt" ]]; then
    a2disconf phpmyadmin >/dev/null 2>&1 || true
    a2disconf phppgadmin >/dev/null 2>&1 || true
  fi
fi
if [[ "$WEB_SERVER" == "caddy" ]]; then
  install -d -m 0755 /etc/caddy/stepanel.d
  printf '%s\n' "$PANEL_HOSTNAME {" $'\treverse_proxy 127.0.0.1:8080' '}' > /etc/caddy/stepanel.d/panel.caddy
  if [[ ! -f /etc/caddy/Caddyfile ]]; then
    printf 'import /etc/caddy/stepanel.d/*.caddy\n' > /etc/caddy/Caddyfile
  elif ! grep -Fqx 'import /etc/caddy/stepanel.d/*.caddy' /etc/caddy/Caddyfile; then
    printf '\nimport /etc/caddy/stepanel.d/*.caddy\n' >> /etc/caddy/Caddyfile
  fi
  caddy validate --config /etc/caddy/Caddyfile
fi
chown -R root:root "$APP_DIR"
chmod 0755 "$APP_DIR"
if [[ -d "$APP_DIR/.nvm" ]]; then
  chown -R "$APP_USER:$APP_USER" "$APP_DIR/.nvm"
  # Isolated per-site identities (sp-*) are not members of the $APP_USER
  # group, so they need read/traverse access to run the managed Node
  # runtime without being able to write to it. o+rX adds read to every
  # file and execute/traverse only where owner or group already has it,
  # so plain data files do not become spuriously executable.
  chmod -R o+rX "$APP_DIR/.nvm"
fi
chown -R "$APP_USER:$APP_USER" "$DATA_DIR"
chown "$APP_USER:$WEB_GROUP" /var/www/sites
chmod 2750 /var/www/sites
install -d -m 0700 -o "$APP_USER" -g "$APP_USER" /var/www/sites/.stepanel-recovery
write_env() {
  local key=$1 value=$2
  [[ "$value" != *$'\n'* && "$value" != *$'\r'* ]] || { echo "Invalid newline in $key." >&2; exit 1; }
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  printf '%s="%s"\n' "$key" "$value"
}
env_tmp=$(mktemp /etc/ste-panel.env.XXXXXX)
TXN_TEMPS+=("$env_tmp")
{
  write_env STEPANEL_ENV production
  write_env STEPANEL_WEBSERVER "$WEB_SERVER"
  write_env STEPANEL_LISTEN 127.0.0.1:8090
  write_env STEPANEL_ADMIN_USERNAME "$ADMIN_USERNAME"
  write_env STEPANEL_ADMIN_PASSWORD_HASH "$ADMIN_PASSWORD_HASH"
  if [[ -n $ADMIN_TOTP_SECRET ]]; then write_env STEPANEL_ADMIN_TOTP_SECRET "$ADMIN_TOTP_SECRET"; fi
  write_env STEPANEL_SESSION_SECRET "$SESSION_SECRET"
  write_env STEPANEL_ACCOUNT_KEY "$ACCOUNT_KEY"
  write_env STEPANEL_ENVIRONMENT_KEY "$ENVIRONMENT_KEY"
  write_env STEPANEL_PANEL_HOSTNAME "$PANEL_HOSTNAME"
  write_env STEPANEL_DB_ENGINE "$DB_ENGINE"
  write_env STEPANEL_DB_VERSION "$DB_VERSION"
  write_env STEPANEL_DB_ADMIN_URL "$DB_ADMIN_URL"
  write_env STEPANEL_DB_ADMIN_ALLOW "$DB_ADMIN_ALLOW"
  write_env STEPANEL_DB_HOST "$DB_HOST"
  write_env STEPANEL_DB_USER "$DB_USER"
  write_env STEPANEL_IMPORT_ROOT "$DATA_DIR/imports"
  write_env STEPANEL_WEB_ROOT /var/www
  write_env STEPANEL_MAIL_ROOT "$DATA_DIR/mail"
  write_env STEPANEL_NVM_DIR "$APP_DIR/.nvm"
  write_env STEPANEL_PROXY_ROOT "$PROXY_ROOT"
  write_env STEPANEL_APP_ROOT "$DATA_DIR/apps"
  write_env STEPANEL_MALWARE_ROOT "$DATA_DIR/quarantine"
  write_env STEPANEL_APPCTL /usr/local/sbin/stepanel-appctl
  write_env STEPANEL_GITCTL /usr/local/sbin/stepanel-gitctl
  write_env STEPANEL_PROXYCTL /usr/local/sbin/stepanel-proxyctl
  write_env STEPANEL_SITECTL /usr/local/sbin/stepanel-sitectl
  write_env STEPANEL_VHOSTCTL /usr/local/sbin/stepanel-vhostctl
  write_env STEPANEL_VHOST_ROOT "$VHOST_ROOT"
  if [[ "$DB_LOCAL_HELPER" == "1" ]]; then write_env STEPANEL_DBCTL /usr/local/sbin/stepanel-dbctl; fi
  write_env STEPANEL_AUDIT_LOG "$DATA_DIR/audit.jsonl"
  write_env STEPANEL_AUDIT_KEY "$AUDIT_KEY"
  write_env STEPANEL_BACKUP_SIGNING_KEY "$BACKUP_SIGNING_KEY"
  write_env STEPANEL_BACKUP_ROOT /var/backups/stepanel
  if [[ -n "${STEPANEL_OFFSITE_TARGET:-}" ]]; then write_env STEPANEL_OFFSITE_TARGET "$STEPANEL_OFFSITE_TARGET"; fi
  write_env STEPANEL_REQUIRE_OFFSITE_BACKUP "$REQUIRE_OFFSITE_BACKUP"
  if [[ -n "${STEPANEL_CLOUD_PROVIDER:-}" ]]; then write_env STEPANEL_CLOUD_PROVIDER "$STEPANEL_CLOUD_PROVIDER"; fi
  if [[ -n "${STEPANEL_SSH_SERVERS:-}" ]]; then write_env STEPANEL_SSH_SERVERS "$STEPANEL_SSH_SERVERS"; fi
  write_env STEPANEL_JOB_STATE "$DATA_DIR/jobs.json"
  write_env STEPANEL_CONTROL_PLANE_DB "$DATA_DIR/stepanel-control.db"
  write_env STEPANEL_SESSION_STATE "$DATA_DIR/sessions.json"
  write_env STEPANEL_RECOVERY_ROOT /var/www/sites/.stepanel-recovery
  write_env STEPANEL_WPRESS_EXTRACT "$WPRESS_EXTRACT"
  write_env STEPANEL_WPCLI "$WPCLI"
  write_env STEPANEL_SUDO /usr/bin/sudo
  write_env STEPANEL_STAGE_RETENTION_HOURS "$STAGE_RETENTION_HOURS"
  write_env STEPANEL_MIN_FREE_BYTES "$MIN_FREE_BYTES"
  write_env STEPANEL_MAX_UPLOAD_BYTES "$MAX_UPLOAD_BYTES"
  write_env STEPANEL_MAX_ARCHIVE_ENTRIES "$MAX_ARCHIVE_ENTRIES"
  write_env STEPANEL_MAX_CONCURRENT_JOBS "$MAX_CONCURRENT_JOBS"
  write_env STEPANEL_WORKER_MODE external
  write_env STEPANEL_FTP_PASSIVE_MIN "$FTP_PASSIVE_MIN"
  write_env STEPANEL_FTP_PASSIVE_MAX "$FTP_PASSIVE_MAX"
  if [[ "$WEB_SERVER" == "apache" && ( "$INSTALL_TLS" == "1" || -x /usr/local/sbin/stepanel-certbot ) ]]; then write_env STEPANEL_CERTBOT /usr/local/sbin/stepanel-certbot; fi
} > "$env_tmp"
chmod 0600 "$env_tmp"
mv -f "$env_tmp" "$ENV_FILE"
unset AUDIT_KEY
install -m 0644 "$ROOT_DIR/deploy/stepanel.service" /etc/systemd/system/stepanel.service
install -m 0644 "$ROOT_DIR/deploy/stepanel-worker.service" /etc/systemd/system/stepanel-worker.service
install -m 0644 "$ROOT_DIR/deploy/stepanel.logrotate" /etc/logrotate.d/stepanel
sudoers_tmp=$(mktemp)
TXN_TEMPS+=("$sudoers_tmp")
printf '%s ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-appctl *\n%s ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-proxyctl *\n%s ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-sitectl *\n' "$APP_USER" "$APP_USER" "$APP_USER" > "$sudoers_tmp"
printf '%s ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-vhostctl *\n' "$APP_USER" >> "$sudoers_tmp"
printf '%s ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-runnerctl *\n' "$APP_USER" >> "$sudoers_tmp"
printf '%s ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-gitctl *\n' "$APP_USER" >> "$sudoers_tmp"
if [[ "$DB_LOCAL_HELPER" == "1" ]]; then printf '%s ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-dbctl *\n' "$APP_USER" >> "$sudoers_tmp"; fi
if [[ "$INSTALL_TLS" == "1" && "$WEB_SERVER" == "apache" ]]; then printf '%s ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-certbot *\n' "$APP_USER" >> "$sudoers_tmp"; fi
visudo -cf "$sudoers_tmp" >/dev/null
install -m 0440 -o root -g root "$sudoers_tmp" /etc/sudoers.d/stepanel
if [[ "$INSTALL_SECURITY" == "1" ]]; then install -m 0755 "$ROOT_DIR/deploy/integrations/stepanel-malware-guard" /usr/local/sbin/stepanel-malware-guard; install -m 0644 "$ROOT_DIR/deploy/stepanel-malware-guard.service" /etc/systemd/system/stepanel-malware-guard.service; fi
if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then
  if command -v restorecon >/dev/null 2>&1; then
    restorecon_paths=(/opt/stepanel /var/lib/ste-panel /var/lib/stepanel-privileged /var/backups/stepanel /var/www/sites "$PROXY_ROOT" "$VHOST_ROOT")
    [[ "$WEB_SERVER" == "apache" ]] && restorecon_paths+=(/etc/httpd/conf.d/stepanel.conf)
    restorecon -RF "${restorecon_paths[@]}"
  fi
  if command -v setsebool >/dev/null 2>&1; then setsebool -P httpd_can_network_connect 1; fi
fi
systemctl daemon-reload
if [[ "$WEB_SERVER" == "apache" ]]; then
  if command -v apachectl >/dev/null 2>&1; then apachectl -t; else httpd -t; fi
fi
systemctl enable --now "$APACHE_SERVICE" "$DB_SERVICE" "${FPM_UNITS[@]}"
if [[ "$WEB_SERVER" == "apache" ]]; then
  systemctl reload "$APACHE_SERVICE"
elif [[ "$WEB_SERVER" == "openlitespeed" ]]; then
  "$LSWSCTRL" restart
else
  systemctl reload "$APACHE_SERVICE"
fi
systemctl enable --now stepanel.service stepanel-worker.service
health_ready=0
for _ in {1..30}; do
  if curl --fail --silent --max-time 2 http://127.0.0.1:8090/readyz >/dev/null; then health_ready=1; break; fi
  sleep 1
done
(( health_ready == 1 )) || { echo 'StePanel failed its post-install health check.' >&2; false; }
INSTALL_COMMITTED=1
trap - ERR EXIT INT TERM
for temporary in "${TXN_TEMPS[@]}"; do rm -f "$temporary"; done
rm -rf "$INSTALL_TXN"
flock -u 8
if [[ "$INSTALL_SECURITY" == "1" ]]; then systemctl enable --now stepanel-malware-guard; fi
if [[ "$INSTALL_MAIL" == "1" && "$ACTIVATE_MAIL" == "1" ]]; then
  systemctl enable --now "$([[ "$PKG" == "apt" ]] && echo exim4 || echo exim)" dovecot "$MAIL_SPAM_SERVICE"
elif [[ "$INSTALL_MAIL" == "1" ]]; then
  [[ "$MAIL_EXIM_PREEXISTING" == "1" ]] || systemctl disable --now "$([[ "$PKG" == "apt" ]] && echo exim4 || echo exim)" 2>/dev/null || true
  [[ "$MAIL_DOVECOT_PREEXISTING" == "1" ]] || systemctl disable --now dovecot 2>/dev/null || true
  [[ "$MAIL_SPAM_PREEXISTING" == "1" ]] || systemctl disable --now "$MAIL_SPAM_SERVICE" 2>/dev/null || true
fi
if [[ "$INSTALL_FTP" == "1" && "$ACTIVATE_FTP" == "1" ]]; then
  systemctl enable --now vsftpd
elif [[ "$INSTALL_FTP" == "1" && "$FTP_PREEXISTING" == "0" ]]; then
  systemctl disable --now vsftpd 2>/dev/null || true
fi
if [[ "$INSTALL_TLS" == "1" && "$WEB_SERVER" == "apache" ]]; then systemctl enable --now certbot.timer 2>/dev/null || true; fi
if [[ "$INSTALL_FAIL2BAN" == "1" ]]; then
  bash "$APP_DIR/integrations/install-fail2ban.sh" --yes --jails "$FAIL2BAN_JAILS" --ignore-ip "$FAIL2BAN_IGNORE_IP"
fi
echo "StePanel installed with $WEB_SERVER and $DB_ENGINE ($DB_VERSION). Verify services with: systemctl status $APACHE_SERVICE $DB_SERVICE"
echo "Panel hostname: $PANEL_HOSTNAME. Complete TLS termination before signing in; Secure cookies are mandatory in production."
if [[ "$INSTALL_MAIL" == "1" ]]; then echo "Mail stack installed. Mailbox data is staged under $DATA_DIR/mail; services are active only when STEPANEL_ACTIVATE_MAIL=1."; fi
if [[ "$INSTALL_FTP" == "1" ]]; then echo "vsftpd installed with local-user chroot and passive ports $FTP_PASSIVE_MIN-$FTP_PASSIVE_MAX; it is active only when STEPANEL_ACTIVATE_FTP=1 with FTPS certificates."; fi
