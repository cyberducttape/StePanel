#!/usr/bin/env bash

# Print the selected optional installer effects without exposing configuration
# secrets. Keep the wording aligned with the install branches and post-commit
# service activation policy.
install_print_optional_change_plan() {
  local web_server=$1 db_admin=$2 modsec=$3 modsec_mode=$4
  local mail=$5 activate_mail=$6 ftp=$7 activate_ftp=$8
  local security=$9 fail2ban=${10} tls=${11} node=${12} node_versions=${13}

  if [[ $modsec == 1 ]]; then
    printf '  CONFIGURE Apache ModSecurity and OWASP CRS (mode: %s); validate Apache configuration before commit\n' "$modsec_mode"
  fi
  if [[ $db_admin == 1 ]]; then
    if [[ $web_server == apache ]]; then
      printf '  INSTALL database-admin web application and expose it through an Apache route restricted to configured IP/CIDR addresses\n'
    else
      printf '  INSTALL database-admin package only; no web route is configured for %s\n' "$web_server"
    fi
  fi
  if [[ $mail == 1 ]]; then
    if [[ $activate_mail == 1 ]]; then
      printf '  INSTALL and ACTIVATE Exim, Dovecot, and spam filtering after core commit\n'
    else
      printf '  INSTALL and configure Exim, Dovecot, and spam filtering; leave new services stopped and preserve pre-existing service state\n'
    fi
  fi
  if [[ $ftp == 1 ]]; then
    if [[ $activate_ftp == 1 ]]; then
      printf '  INSTALL and ACTIVATE vsftpd with FTPS after core commit\n'
    else
      printf '  INSTALL and configure vsftpd; leave a new service stopped and preserve pre-existing service state\n'
    fi
  fi
  if [[ $security == 1 ]]; then
    printf '  INSTALL ClamAV malware scanning and activate stepanel-malware-guard after core commit\n'
  fi
  if [[ $fail2ban == 1 ]]; then
    printf '  CONFIGURE and activate Fail2ban using the supplied trusted-address allowlist\n'
  fi
  if [[ $tls == 1 && $web_server == apache ]]; then
    printf '  INSTALL Certbot Apache integration and activate certbot.timer after core commit\n'
  fi
  if [[ $node == 1 ]]; then
    printf '  INSTALL Node.js versions %s for the StePanel runtime account\n' "$node_versions"
  fi
  if [[ $mail == 0 && $ftp == 0 && $security == 0 && $fail2ban == 0 && ! ($tls == 1 && $web_server == apache) && $node == 0 && $db_admin == 0 && $modsec == 0 ]]; then
    printf '  No optional integrations selected\n'
  fi
}
