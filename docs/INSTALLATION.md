# Installation guide

## Supported operating systems

The installer supports systems with `apt-get` (Debian/Ubuntu) or `dnf` (Fedora, Rocky, Alma, and compatible RHEL-family systems). It installs Caddy, Apache, or OpenLiteSpeed, MySQL/MariaDB/PostgreSQL, PHP-FPM, ACL and archive utilities, and StePanel.

Support tiers are intentionally explicit: Caddy is the Tier 1 default and is
tested on AlmaLinux 9, Rocky Linux 9, and Ubuntu 24.04; Apache is Tier 1 and
has disposable install coverage on Debian 12; the current database/webserver
matrix is published in `.github/workflows/install-smoke.yml`. OpenLiteSpeed is
Tier 2 operator-integrated support until its repository, listener/template,
PHP routing, and upgrade paths receive equivalent disposable-host coverage.

The default is `STEPANEL_WEBSERVER=caddy`. Set `STEPANEL_WEBSERVER=apache` or
`STEPANEL_WEBSERVER=openlitespeed` to opt into another webserver. OpenLiteSpeed
installations require the OpenLiteSpeed package repository to be configured;
the installer verifies that `lswsctrl` is available. Apache-specific optional
integrations (ModSecurity and the Certbot Apache plugin) require an explicitly
selected Apache install and are rejected for Caddy or OpenLiteSpeed.
OpenLiteSpeed reverse-proxy snippets are managed by the bundled helper and
written below `/usr/local/lsws/conf/vhosts/stepanel/proxy`; include that
directory from the target OLS listener/vhost rewrite configuration before
serving traffic. PHP site-vhost provisioning uses the root-owned
`stepanel-ols-vhostctl` helper and requires `/etc/stepanel-ols-vhost.template`.
The template is rendered with `{{SITE}}`, `{{DOMAIN}}`, and `{{ROOT}}`
placeholders and must be included by the target OLS listener/vhost. Validate
the template against the host's OpenLiteSpeed configuration before enabling
customer traffic.
Caddy installs import `/etc/caddy/stepanel.d/*.caddy`. Root-owned helpers write
validated, rollback-safe PHP site and `reverse_proxy` definitions there and
validate/reload the complete Caddyfile after each change. Caddy manages HTTPS
certificates automatically. Apache `.htaccess` migrations are described in
[`HTACCESS_MIGRATION.md`](HTACCESS_MIGRATION.md).

## Build and install

For production, use a verified tagged release archive:

```sh
release=v0.7.0
arch=amd64 # use arm64 on aarch64 hosts
curl -fsSLO "https://github.com/cyberducttape/StePanel/releases/download/${release}/stepanel_${release#v}_linux_${arch}.tar.gz"
curl -fsSLO "https://github.com/cyberducttape/StePanel/releases/download/${release}/SHA256SUMS"
grep "stepanel_${release#v}_linux_${arch}.tar.gz" SHA256SUMS | sha256sum -c -
tar -xzf "stepanel_${release#v}_linux_${arch}.tar.gz"

# Generate encryption keys (MUST be machine-generated, see docs/ENCRYPTION_KEYS.md)
ACCOUNT_KEY=$(openssl rand -hex 32)
ENVIRONMENT_KEY=$(openssl rand -hex 32)
BACKUP_SIGNING_KEY=$(openssl rand -hex 32)

sudo STEPANEL_ADMIN_PASSWORD='use-a-password-manager' \
  STEPANEL_ADMIN_TOTP_SECRET='BASE32_SECRET' \
  STEPANEL_ACCOUNT_KEY="$ACCOUNT_KEY" \
  STEPANEL_ENVIRONMENT_KEY="$ENVIRONMENT_KEY" \
  STEPANEL_BACKUP_SIGNING_KEY="$BACKUP_SIGNING_KEY" \
  STEPANEL_REQUIRE_OFFSITE_BACKUP=1 \
  STEPANEL_OFFSITE_TARGET='s3:bucket/stepanel' \
  STEPANEL_PANEL_HOSTNAME=panel.example.com \
  STEPANEL_DB_ENGINE=mariadb \
  STEPANEL_DB_VERSION=default ./install.sh
```

**IMPORTANT:** Encryption keys must be machine-generated random values. See [**docs/ENCRYPTION_KEYS.md**](ENCRYPTION_KEYS.md) for detailed requirements, generation methods, and security best practices.

Building from source is for contributors and development hosts; release
archives include the binary, installer, helpers, service files, and web assets.

For a fresh installation, the installer requires an admin password, stores only
its bcrypt hash, generates a session secret when one is not supplied, and starts in production mode. It
supports `mysql`, `mariadb`, and `postgresql`. Use `default` for the
distribution-provided version, or provide an exact repository version for
MySQL/MariaDB or an available AppStream stream for PostgreSQL on RHEL-family
systems. The installer verifies requested versions before installing the
database package and fails rather than silently selecting another version. For
a local MySQL/MariaDB database it installs a root-owned restore helper and does
not place a global database credential in the control-plane environment. The
helper creates only validated new schemas, imports through a temporary account
restricted to that schema, and records managed objects for cleanup. Local
PostgreSQL is verified through peer-authenticated socket access. Set
`STEPANEL_DB_HOST`, `STEPANEL_DB_USER`, and `STEPANEL_DB_PASSWORD` together
when using a pre-provisioned remote database; remote credential scope remains
the operator's responsibility. cPanel and WordPress SQL restore remains
MySQL/MariaDB-only; PostgreSQL dump conversion must be performed separately.
When upgrading an older installation, verify that `STEPANEL_DBCTL` is active
and restores succeed before manually removing the legacy `stepanel_admin`
database account; the installer does not delete an existing account implicitly.
It also requires the panel's fully qualified hostname and writes it into the
selected webserver route. Caddy obtains a certificate automatically. For an
explicit Apache install, complete TLS termination before attempting to sign in.
When Certbot integration is installed, issuance can be bootstrapped from the host with
`sudo stepanel-certbot panel.example.com admin@example.com`.

## Upgrades and rollback boundary

Use the installed binary to confirm the release, commit, and build timestamp
before and after an upgrade:

```sh
/opt/stepanel/stepanel version
```

Running the installer over an existing installation preserves values from the
root-owned, non-writable `/etc/ste-panel.env` unless the corresponding
`STEPANEL_*` variable is explicitly supplied. This preserves the administrator
hash, session secret, database selection and credentials, hostname, and runtime
paths; an upgrade therefore does not require the plaintext administrator
password.

Before replacing StePanel-owned files, the installer snapshots the binary, web
assets, helpers, environment, sudoers policy, systemd unit, logrotate policy,
and selected panel webserver configuration into a private
`/var/tmp/stepanel-install.*` transaction. It stops the old daemon, installs
the candidate, validates the complete selected webserver configuration, starts
the candidate, and requires the local health endpoint to succeed. A failure
restores those files and the prior StePanel service state. Runtime Caddy and
Apache changes share their respective lock with site and proxy operations;
Apache certificate operations use the Apache lock too.

Package-manager transactions and optional third-party mail, FTP, ModSecurity,
Fail2ban, Node, ClamAV, or Certbot package configuration are outside that file
rollback boundary. Snapshot the host and verified backup artifacts before an
upgrade, and review package-manager history if dependency installation fails.

## Administrator MFA and audit key

Set `STEPANEL_ADMIN_TOTP_SECRET` to an unpadded base32 secret representing at
least 20 random bytes to require a six-digit authenticator code at every login.
Production configuration validation rejects startup without this setting.
Generate a secret on the host and enroll the same value manually in the
operator's authenticator before running the installer:

```sh
umask 077
totp_secret=$(head -c 20 /dev/urandom | base32 | tr -d '=\n')
sudo STEPANEL_ADMIN_TOTP_SECRET="$totp_secret" ./install.sh
```

Test a fresh login before closing the recovery console. If the authenticator is
lost, a local root operator recovers by generating a **new** secret and
enrolling it, the same way as initial setup - not by removing MFA. Production
installations reject a blank `STEPANEL_ADMIN_TOTP_SECRET` (`install.sh` and
startup configuration validation both refuse it), so rerunning the installer
with an empty value will not work and is not the supported recovery path:

```sh
umask 077
totp_secret=$(head -c 20 /dev/urandom | base32 | tr -d '=\n')
sudo STEPANEL_ADMIN_TOTP_SECRET="$totp_secret" ./install.sh
```

Enroll `$totp_secret` in the operator's authenticator before restarting the
service, then test a fresh login before closing the recovery console. StePanel
remains a single-administrator system; teams should not share the account when
individual attribution is required.

Customer accounts are provisioned after installation by an authenticated
administrator, not by installer variables. They require their own TOTP seed
and explicit site assignments. Set `STEPANEL_ACCOUNT_KEY` before enabling
customer accounts; it encrypts their TOTP secrets at rest. The service stores
their authoritative private account state in the SQLite control-plane database.
`STEPANEL_ACCOUNT_STATE` remains a one-time legacy import source, defaulting to
`/var/lib/ste-panel/accounts.json`. Back up the account key and control-plane
state together, and never expose either through a site or support archive. See
[`SHARED_HOSTING.md`](SHARED_HOSTING.md) for the enforced beta scope and
limitations.

The installer generates a separate audit HMAC key, stores a root-only copy at
`/etc/stepanel-audit.key`, and supplies it through the protected service
environment. Preserve both in the host's secret backup.

Production validation also requires `STEPANEL_REQUIRE_OFFSITE_BACKUP=1` and a
valid `STEPANEL_OFFSITE_TARGET` (for example `s3:my-bucket/stepanel`). Configure
and test the rclone destination before running the installer; rclone must be
installed and available on `PATH`. Provider-side
retention lock/immutability remains an operator responsibility.

Production also requires stable `STEPANEL_ENVIRONMENT_KEY` and
`STEPANEL_BACKUP_SIGNING_KEY` values, each at least 32 characters. The installer
generates them on a fresh installation and preserves them during upgrades.

FTP is opt-in. Installation alone leaves a newly installed vsftpd service
disabled. Activation requires `STEPANEL_ACTIVATE_FTP=1` and readable certificate
and private-key paths; the resulting configuration requires TLS for both login
and data channels. The installer does not create FTP accounts. Mail packages
are likewise installed without activating newly installed daemons unless
`STEPANEL_ACTIVATE_MAIL=1` is explicit. Configure domains, TLS, DNS, anti-relay
rules, firewall policy, and monitoring before activating mail.

In an interactive terminal, the installer asks for the database engine and version. For automation, use:

| Variable | Values | Meaning |
| --- | --- | --- |
| `STEPANEL_DB_ENGINE` | `mysql`, `mariadb`, `postgresql` | Database distribution |
| `STEPANEL_ADMIN_TOTP_SECRET` | Unpadded base32, 160 bits or more | Require TOTP MFA for administrator login |
| `STEPANEL_PANEL_HOSTNAME` | Fully qualified domain | Required panel route hostname |
| `STEPANEL_DB_VERSION` | `default` or an exact package version | Requested repository version |
| `STEPANEL_INSTALL_DB_ADMIN` | `0` or `1` | Install phpMyAdmin or phpPgAdmin for the selected engine |
| `STEPANEL_DB_ADMIN_ALLOW` | IP/CIDR list | Apache access allowlist for the database administrator; defaults to `127.0.0.1 ::1` |
| `STEPANEL_INSTALL_MAIL` | `0` or `1` | Install Exim, Dovecot, SpamAssassin, and enable mailbox staging |
| `STEPANEL_ACTIVATE_MAIL` | `0` or `1` | Explicitly enable/start the installed mail services |
| `STEPANEL_INSTALL_FTP` | `0` or `1` | Install vsftpd with local-user chrooting; newly installed service remains disabled |
| `STEPANEL_ACTIVATE_FTP` | `0` or `1` | Require FTPS configuration and enable/start vsftpd |
| `STEPANEL_FTP_CERT_FILE` / `STEPANEL_FTP_KEY_FILE` | Absolute paths | Required certificate and key when activating FTPS |
| `STEPANEL_FTP_PASSIVE_MIN` / `STEPANEL_FTP_PASSIVE_MAX` | Port range | Passive FTP port range; defaults to `40100-40200` |
| `STEPANEL_INSTALL_NODE` | `0` or `1` | Install NVM for the StePanel service account |
| `STEPANEL_NODE_VERSIONS` | Comma-separated versions | Node versions to install through NVM |
| `STEPANEL_GIT_ALLOWED_HOSTS` | Comma-separated hostnames | Exact public HTTPS/private SSH repository hosts allowed for Git deployment; defaults to GitHub, GitLab, and Bitbucket |
| `STEPANEL_GIT_WEBHOOK_SECRET` | Legacy shared secret (deprecated) | Retained for configuration compatibility; webhook deployment requires a distinct per-site control-plane secret. |
| `STEPANEL_GITCTL` | Absolute helper path | Root-owned per-site deploy-key and private SSH clone helper; defaults to `/usr/local/sbin/stepanel-gitctl` |
| `STEPANEL_GIT_RELEASE_RETENTION` | `1`–`100` | Number of preserved Git rollback releases per site; defaults to `3` |
| `STEPANEL_GIT_RELEASE_MAX_AGE_HOURS` | Positive hours | Remove older non-current rollback releases; defaults to `168` (7 days) |
| `STEPANEL_GIT_RELEASE_MAX_BYTES` | Positive bytes | Maximum retained previous-release storage per site; defaults to `5368709120` (5 GiB) |
| `STEPANEL_RUNNERCTL` | Absolute helper path | Rootless Podman build-runner helper; defaults to `/usr/local/sbin/stepanel-runnerctl` |
| `STEPANEL_INSTALL_SECURITY` | `0` or `1` | Install ClamAV and the PHP malware guard |
| `STEPANEL_OFFSITE_TARGET` | rclone destination | Optional offsite copy target, for example `s3:bucket/stepanel` |
| `STEPANEL_REQUIRE_OFFSITE_BACKUP` | `0` or `1` | Reject production startup unless an offsite target is configured |
| `STEPANEL_CLOUD_PROVIDER` | `linode`, `aws`, or `openstack` | Optional provider for cloud inventory and lifecycle actions |
| `STEPANEL_SSH_SERVERS` | comma-separated SSH aliases | Optional strict-host-key inventory and allowlisted actions |
| `STEPANEL_INSTALL_TLS` | `0` or `1` | Install Certbot and the Apache certificate integration |
| `STEPANEL_STAGE_RETENTION_HOURS` | Positive hours | Retain completed restore staging directories for audit |
| `STEPANEL_MIN_FREE_BYTES` | Bytes | Refuse new restores below this free-space threshold |
| `STEPANEL_MAX_UPLOAD_BYTES` | Bytes, up to 20 GiB | Maximum compressed restore request size |
| `STEPANEL_MAX_ARCHIVE_ENTRIES` | `1`–`1000000` | Maximum filesystem entries in a restore or backup |
| `STEPANEL_MAX_CONCURRENT_JOBS` | `1`–`32` | Global restore, backup, and certificate job slots |
| `STEPANEL_WORKER_MODE` | `embedded` or `external` | Run jobs in the panel process or in the separately supervised worker service; the installer selects `external` |
| `STEPANEL_CONTROL_PLANE_DB` | Absolute path in production | SQLite control-plane database for durable jobs, customer accounts, site ownership, and sessions |
| `STEPANEL_ACCOUNT_STATE` | Absolute path in production | Optional private customer-account state file; defaults beside session state |
| `STEPANEL_ENVIRONMENT_KEY` | Secret string, at least 32 characters in production | AES-GCM key for encrypted site environment storage; keep stable and back it up securely |
| `STEPANEL_ACCOUNT_KEY` | Secret string | Encrypts customer TOTP secrets and sensitive durable job payloads (including WordPress restore credentials); required for those features and must be backed up with the control-plane database |
| `STEPANEL_BACKUP_SIGNING_KEY` | Secret string, at least 32 characters in production | Signs backup manifests with HMAC-SHA256; keep outside the backup root and escrow separately for disaster recovery |
| `STEPANEL_ENVIRONMENT_STATE` | Filesystem path | Site environment state file; defaults beside the job state |
| `STEPANEL_REDIS_STATE` | Filesystem path | Redis/Valkey site allocation state; defaults beside the job state |

For Apache installations, it enables proxy, proxy_http, proxy_fcgi, setenvif,
rewrite, and headers modules on Debian-family systems. For Caddy installations,
it writes the panel hostname into a managed Caddy site and enables automatic
HTTPS. The installer creates:

On SELinux systems the installer restores the standard file contexts and
enables `httpd_can_network_connect`, which is required for Apache reverse
proxying. Review that boolean against the server's PHP execution model and
outbound-network policy.

| Path | Purpose |
| --- | --- |
| `/opt/stepanel` | Binary and web assets |
| `/var/lib/ste-panel/imports` | Private cpmove staging |
| `/var/backups/stepanel` | Private, verified site backup artifacts |
| `/var/lib/ste-panel/mail` | Private staged mailbox data |
| `/etc/caddy/stepanel.d` | Default root-owned Caddy panel, PHP-site, and reverse-proxy definitions |
| `/etc/apache2/stepanel-proxy` or `/etc/httpd/conf.d/stepanel-proxy` | Optional root-owned managed Apache reverse-proxy snippets |
| `/etc/apache2/stepanel-sites` or `/etc/httpd/conf.d/stepanel-sites` | Optional root-owned managed Apache PHP site vhosts |
| `/var/lib/ste-panel/apps` | Managed Node application manifests |
| `/var/lib/ste-panel/quarantine` | Recoverable malware quarantine |
| `STEPANEL_CONTROL_PLANE_DB` (default `/var/lib/ste-panel/stepanel-control.db`) | Authoritative SQLite control-plane state; legacy JSON paths are import-only |
| `/var/www/sites/.stepanel-recovery` | Journaled site rollback data |
| `/etc/ste-panel.env` | Runtime configuration |
| `/etc/stepanel-audit.key` | Root-only HMAC key for audit verification |
| `/etc/systemd/system/stepanel.service` | Panel service definition |
| `/etc/systemd/system/stepanel-worker.service` | Durable job worker service definition |
| `/etc/logrotate.d/stepanel` | Audit-log retention policy |

Caddy obtains and renews HTTPS certificates automatically. On an explicitly
selected Apache installation, the installer enables Certbot integration only
when `STEPANEL_INSTALL_TLS=1`. Metrics are authenticated by default; set
`STEPANEL_METRICS_PUBLIC=1` only on a protected monitoring network.

## Site isolation

Before a host restore writes a document root, the root-owned site helper creates
a deterministic system user and unique primary group, establishes inherited
ACL access for the unprivileged control plane, and renders private PHP-FPM
pools for installed PHP versions. Restored files are sealed to the site user
and selected webserver group afterward. Site users are not added to the shared
webserver group; Caddy or Apache receives group read access to static files and
PHP-FPM sockets instead. The site deployment API renders a validated Caddy or
Apache vhost that routes PHP requests to an active pool and refuses conflicting
domains. OpenLiteSpeed PHP routing remains operator-integrated.

## Reverse proxy

The default host installation creates a Caddy panel route and imports managed
definitions from `/etc/caddy/stepanel.d`. Apache users can instead copy
`deploy/apache/stepanel.conf` to the Apache configuration directory, replace
the example hostname, enable the required modules, and configure TLS.
Container deployments must either mount application TLS certificates or set
`STEPANEL_TLS_TERMINATED=1` only behind an ingress that enforces HTTPS. The
Kubernetes, Helm, and Terraform examples assume that trusted ingress boundary;
generic Docker deployments still require the operator to provide it explicitly.

**Important:** When proxying StePanel, configure your reverse proxy to allow
long-running uploads (20 GB at typical bandwidth requires 30 minutes). Set
timeouts accordingly: Apache's `ProxyTimeout 1800`, Nginx's `proxy_read_timeout
1800s`, or Caddy's `timeout 30m`. See [TROUBLESHOOTING.md](TROUBLESHOOTING.md)
for configuration examples.

### Database engine and administration UI

Set `STEPANEL_DB_ENGINE=postgresql` to install PostgreSQL instead of MySQL or
MariaDB. On RHEL-family systems, set `STEPANEL_DB_VERSION` to any PostgreSQL
stream shown by `dnf module list postgresql --all`; the installer validates the
stream, enables it, installs the server, initializes the cluster, and enables
the PostgreSQL service. `default` uses the distribution default. Debian and
Ubuntu installations support the default PostgreSQL package; explicit
PostgreSQL streams are intentionally restricted to AppStream-capable DNF
systems.

Set `STEPANEL_INSTALL_DB_ADMIN=1` to install the matching browser management
tool: phpMyAdmin for MySQL/MariaDB or phpPgAdmin for PostgreSQL. The dashboard
shows the selected engine, version, service state, client, and a link when the
package and Apache PHP integration are present. For Caddy or OpenLiteSpeed,
install the package but configure its PHP route in that webserver separately;
StePanel intentionally does not generate an unreviewed PHP proxy route.
These tools have their own authentication and must be protected with HTTPS
plus an IP allowlist, VPN, or equivalent access control; the StePanel session
does not authenticate them.

The generated Apache route is restricted to loopback by default. Set
`STEPANEL_DB_ADMIN_ALLOW` to a space- or comma-separated list of trusted IP
addresses or CIDR networks only when remote browser access is required. The
installer validates the values and Apache configuration before service reload.
Prefer a VPN or management network; never expose either administrator to the
public internet solely because it has its own login form.
The generated Alias is included only inside the StePanel virtual host; Debian
package-wide phpMyAdmin/phpPgAdmin aliases are disabled to prevent the console
from appearing on customer domains.

## Operations

```sh
systemctl status stepanel
journalctl -u stepanel -f
curl -i http://127.0.0.1:8090/livez
curl -fsS http://127.0.0.1:8090/readyz | jq
```

## Uninstall

Stop and disable the service, remove `/opt/stepanel`, and decide separately whether staged imports under `/var/lib/ste-panel` should be retained for audit or deleted after verification.
