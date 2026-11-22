#!/usr/bin/env bash

# checker is the package-manager-specific availability function. Keep the
# whole required set in one preflight result so missing PHP/database/runtime
# packages are reported together before any package is installed.
install_preflight_packages() {
  local checker=${1:?package availability checker required} package
  shift
  local -a missing=()
  for package in "$@"; do
    if ! "$checker" "$package"; then
      missing+=("$package")
    fi
  done
  if (( ${#missing[@]} )); then
    printf 'required packages are unavailable in the current repository metadata: %s. Refresh package indexes or enable the repository that provides them, then rerun preflight.' "${missing[*]}"
    return 1
  fi
  printf 'all %d required and selected optional packages are available in repository metadata.' "$#"
}

# Return the packages required only by options selected in guided or
# unattended setup as a NUL-delimited list for mapfile.
install_preflight_optional_packages() {
  local package_manager=${1:?package manager required}
  local engine=${2:?database engine required}
  local web_server=${3:?web server required}
  local fail2ban=${4:?fail2ban selection required}
  local modsecurity=${5:?ModSecurity selection required}
  local mail=${6:?mail selection required}
  local ftp=${7:?FTP selection required}
  local db_admin=${8:?database admin selection required}
  local security=${9:?security selection required}
  local tls=${10:?TLS selection required}
  local -a packages=()

  if [[ $fail2ban == 1 ]]; then packages+=(fail2ban); fi
  if [[ $modsecurity == 1 ]]; then
    if [[ $package_manager == apt ]]; then packages+=(libapache2-mod-security2 modsecurity-crs)
    else packages+=(mod_security mod_security_crs); fi
  fi
  if [[ $mail == 1 ]]; then
    if [[ $package_manager == apt ]]; then packages+=(exim4 dovecot-core dovecot-imapd spamd)
    else packages+=(exim dovecot spamassassin); fi
  fi
  if [[ $ftp == 1 ]]; then packages+=(vsftpd); fi
  if [[ $db_admin == 1 ]]; then
    if [[ $engine == postgresql ]]; then packages+=(phppgadmin)
    elif [[ $package_manager == apt ]]; then packages+=(phpmyadmin)
    else packages+=(phpMyAdmin); fi
  fi
  if [[ $security == 1 ]]; then
    if [[ $package_manager == apt ]]; then packages+=(clamav clamav-daemon inotify-tools)
    else packages+=(clamav clamav-update inotify-tools); fi
  fi
  if [[ $tls == 1 && $web_server == apache ]]; then
    packages+=(certbot python3-certbot-apache)
  fi
  if (( ${#packages[@]} )); then printf '%s\0' "${packages[@]}"; fi
}
