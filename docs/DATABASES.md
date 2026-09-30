# Database operations

The dashboard provides engine health, managed database lifecycle controls, and
credential-safe per-database metadata. `GET /api/databases/<name>` returns the
managed owner, user name, encoding, and allocated bytes; it never returns a
password or arbitrary query text. The site workspace groups these records by
their owning site.

StePanel installs and monitors one selected database engine. Local MySQL,
MariaDB, and PostgreSQL installations support native managed-database
inventory, least-privilege provisioning, password rotation, logical backup,
diagnostics, and safety-dump-protected deletion through the restricted helper.
MySQL and MariaDB additionally support cPanel SQL and WordPress restores.
StePanel does not translate MySQL dumps into PostgreSQL syntax.

## Native lifecycle

The authenticated dashboard and `/api/databases` API create a database and one
site-owned login. PostgreSQL roles are created without superuser, database,
role, or replication administration and have a 20-connection ceiling. MySQL
and MariaDB users receive privileges only on their database and only from the
local host. Names are validated identifiers and existing objects are never
overwritten. Mutations are fenced by per-database, per-user, and where
applicable per-site locks; reconciliation locks one pending database at a
time. Independent tenant databases therefore do not wait behind an unrelated
restore or credential operation, while the database engine still enforces
its own catalog and transactional constraints.

Passwords are accepted only on the provisioning or rotation request, streamed
to the root helper over standard input, and never returned, logged, or added to
the ownership registry. Update the application secret immediately after a
rotation. A delete requires the exact phrase `DROP DATABASE_NAME`; before any
object is removed, StePanel creates and checksums a fresh logical dump under
`STEPANEL_BACKUP_ROOT/.database-deletions`. If that dump fails, deletion is
refused.

`/api/database/sessions` returns session identity, database, state, age, and
wait class but deliberately excludes SQL text. Termination requires the exact
phrase `TERMINATE SESSION_ID` and creates an attributed audit event. Effective
capacity, memory, logging, character-set, WAL/binlog, and autovacuum settings
are exposed read-only at `/api/database/settings`; StePanel does not auto-tune
or silently mutate production settings.

## Monitoring and recovery boundary

`/api/database/diagnostics` and `/metrics` expose connection totals, active
connections, transactions older than five minutes, blocked sessions,
deadlocks, and allocated database bytes. Alert on collection failure, blocking,
long transactions, unexpected deadlock growth, and capacity trends. Backup
schedules retain their last success, duration, last error, and consecutive
failure count. Their `keep_last` policy (default 7, range 1-365) prunes only
verified local site backups after a successful replacement; immutable offsite
copies are not deleted.

Logical dumps are not PITR. WAL/binlog archiving, physical base backups,
replication topology, controlled switchover, and fenced failover remain
external DBA responsibilities and are reported as unavailable capabilities.
Do not claim an RPO until those systems and a restore drill are in place.

## Installation matrix

| Engine | Version selection | PHP extension | Browser administrator |
| --- | --- | --- | --- |
| MySQL | Distribution default or exact repository package version | `php-mysql`/`php-mysqlnd` | phpMyAdmin |
| MariaDB | Distribution default or exact repository package version | `php-mysql`/`php-mysqlnd` | phpMyAdmin |
| PostgreSQL | Distribution default; any available PostgreSQL AppStream stream on RHEL-family systems | `php-pgsql` | phpPgAdmin |

Example RHEL-family PostgreSQL installation:

```sh
dnf module list postgresql --all
sudo STEPANEL_DB_ENGINE=postgresql \
  STEPANEL_DB_VERSION=16 \
  STEPANEL_INSTALL_DB_ADMIN=1 \
  STEPANEL_DB_ADMIN_ALLOW='127.0.0.1 ::1 10.20.0.0/16' \
  STEPANEL_ADMIN_PASSWORD='use-a-password-manager' \
  STEPANEL_PANEL_HOSTNAME=panel.example.com \
  ./install.sh
```

The explicit stream must appear in the `dnf module list` output. The installer
enables the selected stream and installs the unversioned `postgresql-server`
RPM from that stream.

## Administration UI security

`STEPANEL_INSTALL_DB_ADMIN=1` installs phpMyAdmin for MySQL/MariaDB or
phpPgAdmin for PostgreSQL. On Apache, StePanel writes a dedicated Alias and
Directory policy. Access defaults to loopback only and is expanded solely by
`STEPANEL_DB_ADMIN_ALLOW`. Values must be literal IP addresses or CIDR
networks; hostnames and arbitrary Apache directives are rejected.

The database administrator has its own authentication boundary. A StePanel
session does not sign a user into phpMyAdmin or phpPgAdmin. Keep the route
behind HTTPS and a VPN or management network. For Caddy and OpenLiteSpeed, the
package can be installed but the PHP route must be reviewed and configured by
the operator before the dashboard reports it ready.

## Verification and troubleshooting

Use the authenticated `/api/database` endpoint to see the selected engine,
configured version, service state, detected client, and admin-console state.
The authenticated `/api/security/audit` endpoint also warns when an installed
console lacks the StePanel Apache policy or when its policy grants access to
all clients.
On the host, verify the corresponding components:

```sh
systemctl status mysql mariadb postgresql php-fpm --no-pager
php -m | grep -E 'mysqli|pdo_mysql|pgsql|pdo_pgsql'
apachectl -t
```

Debian/Ubuntu may use a versioned PHP-FPM unit such as `php8.3-fpm`; StePanel
detects these units. If the database administrator is installed but marked
unavailable, check `/etc/apache2/stepanel-panel/db-admin.conf` or
`/etc/httpd/stepanel-panel/db-admin.conf`, confirm the application directory
under `/usr/share`, and review the configured IP allowlist.

For a remote database, set `STEPANEL_DB_HOST`, `STEPANEL_DB_USER`, and
`STEPANEL_DB_PASSWORD` together. Packaged systemd installations move that
password into `/etc/stepanel-db.password` and deliver it through a private
systemd runtime credential rather than the process environment; containers can
use `STEPANEL_DB_PASSWORD_FILE` or their secret injection mechanism.
Installation performs a non-interactive
connectivity check with the selected engine's client. Configure the remote
server separately in phpMyAdmin/phpPgAdmin if browser administration should
target it; StePanel does not write remote credentials into those applications.
Native lifecycle and diagnostics are intentionally unavailable for remote
servers because StePanel cannot enforce the local privileged-helper boundary.
