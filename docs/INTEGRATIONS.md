# StePanel integrations

StePanel can install and expose two companion tools from the StePanel toolkit.
Both are opt-in because they operate across the host and can change service
configuration.

## FTP service

Install the optional vsftpd package without activating it:

```sh
sudo STEPANEL_INSTALL_FTP=1 \
  STEPANEL_ADMIN_PASSWORD='use-a-password-manager' \
  STEPANEL_PANEL_HOSTNAME=panel.example.com ./install.sh
```

StePanel configures local-user-only access, chrooting, site-root mapping, and
a bounded passive port range. A newly installed service stays disabled. To
activate it, rerun with `STEPANEL_ACTIVATE_FTP=1` and absolute
`STEPANEL_FTP_CERT_FILE` and `STEPANEL_FTP_KEY_FILE` paths; TLS is then required
for login and data. User creation, password rotation, firewall rules, and
per-site authorization remain operator responsibilities.

## DNS providers and mail boundary

DNS is exposed through a provider-neutral contract, but the currently shipped
mutation adapter is Linode-only at `/api/cloud/dns`. The administrator
`/api/dns/capabilities` endpoint lists the planned adapter set and clearly
reports when no provider is configured. Zone desired-state reconciliation,
DNSSEC, and DNS-01 provider orchestration are not simulated.

Linode reboot actions also verify the operation through the account event API.
The configured personal access token therefore needs the Linode read/write
scope and permission to list account events (`linodes:read_write` and
`events:read_only` for OAuth scopes). StePanel checks event access before it
dispatches a reboot, so an insufficiently scoped token cannot trigger a reboot
that StePanel then cannot verify.

Mail installation is deliberately optional and operator-managed. Core StePanel
does not provide mailbox lifecycle, aliases, quotas, DKIM/SPF/DMARC, queue
management, webmail, or customer mail controls. Use an external mail provider
or a separately reviewed optional mail module; do not interpret staged Exim or
Dovecot packages as supported customer mail hosting.

## ModSecurity and OWASP CRS

ModSecurity is available as an opt-in Apache integration. It installs the
distribution packages, enables the Apache connector, loads OWASP CRS when the
distribution provides it, writes a managed audit log, and validates Apache
before accepting the configuration.

The safe default is `DetectionOnly`; switch to blocking only after reviewing
the audit log and tuning false positives:

```sh
sudo STEPANEL_INSTALL_MODSEC=1 \
  STEPANEL_MODSEC_MODE=DetectionOnly \
  STEPANEL_ADMIN_PASSWORD='use-a-password-manager' \
  STEPANEL_PANEL_HOSTNAME=panel.example.com \
  ./install.sh
```

Supported modes are `Off`, `DetectionOnly`, and `On`. A failed Apache config
test rolls back the managed ModSecurity configuration. The panel reports an
enabled ModSecurity Apache module in health data; per-site rule and audit
management remains a product milestone.

This integration is webserver-specific. With the default Caddy webserver the
Security Center reports `provider: external` and `status: unavailable` for
native StePanel WAF support; ModSecurity is not silently treated as applying
to Caddy. Operators must use a separately reviewed Caddy WAF integration or a
security proxy layer. OpenLiteSpeed has the same external-WAF boundary.

## Fail2ban hardening

Enable it during installation only after declaring the trusted management
network:

```sh
sudo STEPANEL_INSTALL_FAIL2BAN=1 \
  STEPANEL_FAIL2BAN_IGNORE_IP='127.0.0.1/8 ::1 192.0.2.0/24' \
  STEPANEL_FAIL2BAN_JAILS='sshd,recidive,apache-auth,web-scanners' \
  STEPANEL_ADMIN_PASSWORD='use-a-password-manager' \
  STEPANEL_PANEL_HOSTNAME=panel.example.com \
  ./install.sh
```

The installer refuses unattended Fail2ban setup without
`STEPANEL_FAIL2BAN_IGNORE_IP` to reduce lockout risk. The integrated script is
the reviewed Fail2ban Hardening Installer and retains dry-run, backup, config
validation, and restore behavior:

```sh
sudo /opt/stepanel/integrations/install-fail2ban.sh --dry-run
sudo /opt/stepanel/integrations/install-fail2ban.sh --restore-backup --dry-run
```

Keep the source project and its versioned release notes in sync when updating
the vendored installer.

## FPM Lens

FPM Lens is review-first: it inventories PHP-FPM pools, observes evidence,
plans memory-bounded changes, renders staged overrides, and can validate them
with the matching `php-fpm -tt` binary. StePanel never applies an unreviewed
plan automatically.

Install a verified FPM Lens release binary during StePanel installation:

```sh
sudo STEPANEL_FPM_LENS_BINARY=/path/to/fpm-lens \
  STEPANEL_ADMIN_PASSWORD='use-a-password-manager' \
  STEPANEL_PANEL_HOSTNAME=panel.example.com ./install.sh
```

Then run the review workflow on the host:

```sh
sudo fpm-lens inventory
sudo fpm-lens doktor
sudo fpm-lens observe --samples 12 --interval-seconds 5 \
  --status-url 'pool=http://127.0.0.1/fpm-status?json'
sudo fpm-lens render fpm-lens.plan.json --output-dir /var/lib/ste-panel/fpm-review
```

Review generated files and validate them before deployment through your normal
configuration-management process. See the companion FPM Lens repository for
policy, evidence schemas, and supported layouts. The current FPM Lens workflow
is evidence-aware: its reviewed plan exposes target, minimum, maximum, and
confidence for each pool. StePanel resource profiles use the reviewed
`PHPWorkers` ceiling; they do not silently import or apply an unreviewed Lens
plan. Use the Lens target/max recommendation as the operator input, then
observe the resulting FPM pool through StePanel's resource status and usage
views.
