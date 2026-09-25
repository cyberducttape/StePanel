# StePanel

> Documentation version: `v0.7.0` release preparation. Previous stable release:
> [`v0.6.0`](https://github.com/cyberducttape/StePanel/tree/v0.6.0).

![CI](https://github.com/cyberducttape/StePanel/actions/workflows/ci.yml/badge.svg) ![Release](https://img.shields.io/github/v/release/cyberducttape/StePanel?display_name=tag) ![License](https://img.shields.io/github/license/cyberducttape/StePanel)

> A safety-first Linux web and application hosting control plane with cPanel migration tooling.

StePanel is an open-source server management panel written in Go. It installs Caddy by default, with Apache and OpenLiteSpeed available explicitly, plus PHP and a selectable MySQL, MariaDB, or PostgreSQL version. It provides a focused operations dashboard and imports cPanel `cpmove` backups through an asynchronous, validated workflow.

It is designed for people who want a small, understandable hosting control plane instead of a large opaque platform.

![StePanel operator workspace preview](docs/assets/operator-workspace-preview.png)

The operator workspace is the strongest overview of StePanel's current value:
resource posture, security checks, deployment state, restore-to-staging, and
managed site operations in one view. It uses deterministic representative data;
it is a product illustration rather than a capture from a live host.

![StePanel developer workspace preview](docs/assets/developer-workspace-preview.png)

The developer workspace shows the application workflow around PHP runtime,
encrypted environment metadata, builds, staging, logs, and workers.

## Why StePanel?

- **Migration-focused:** move cPanel accounts into a controlled Linux hosting environment.
- **Safety-first:** validate archives, reject unsafe entries, stage uploads privately, and expose restore status as a job.
- **Small footprint:** a Go control plane with a limited dependency surface.
- **Operator-friendly:** clear health endpoints, tamper-evident audit events, systemd deployment, and readable documentation.
- **Flexible database layer:** choose MySQL, MariaDB, or PostgreSQL and request an exact repository/AppStream version during installation.
- **Database administration:** optionally install phpMyAdmin for MySQL/MariaDB or phpPgAdmin for PostgreSQL; the dashboard reports service health and links to the matching administrator.

## Current capabilities

| Area | Included today |
| --- | --- |
| Installation | Caddy by default, or Apache/OpenLiteSpeed, PHP, MySQL/MariaDB/PostgreSQL, optional phpMyAdmin/phpPgAdmin, Exim/Dovecot/SpamAssassin/vsftpd, systemd, Debian/Ubuntu and RHEL-family systems |
| Migration | cPanel `.tar.gz` inspection, safe staging, website, SQL, staged mailbox restore, and fail-closed `.htaccess` conversion for Caddy |
| Operations | Dashboard, health endpoint, metrics endpoint, audit log, asynchronous restore jobs |
| Control plane | Durable SQLite state, independently supervised worker, leases/retries/cancellation, dead-letter readiness gate, desired-state reconciliation, and recovery inventory |
| Security | bcrypt credentials, signed sessions, CSRF protection, archive traversal checks, restricted service user |
| Delivery | Dockerfile, ARM64/AMD64 release workflow, checksums, CI, vulnerability scanning |

For cloud inventory/actions, DNS adapters, SSH inventory, WAF capability
constraints, off-site backups, and operational configuration, see the
[integration guide](docs/INTEGRATIONS.md), [installation guide](docs/INSTALLATION.md),
[operations runbook](docs/OPERATIONS.md), and [customer workflows](docs/CUSTOMER_WORKFLOWS.md).

> **Status:** StePanel supports single-host operation with account provisioning features (TOTP MFA, assigned-site limits, scoped access). Multi-tenant production deployment is NOT RECOMMENDED: RBAC-per-customer, audit-log segregation, cross-host session replication, and resource quota enforcement are NOT STARTED. See [PRODUCTION_READINESS.md](docs/PRODUCTION_READINESS.md) for detailed capability status. For single-host deployments, resource profiles, restore-to-staging, and verified restores are available. Always run behind authenticated HTTPS and test restores against a disposable server before using production data.

## Architecture at a glance

```text
Browser / API client
        │ authenticated HTTPS
        ▼
Go control plane ── durable jobs, audit chain, desired state
        │ narrow typed helper calls
        ▼
Root-owned platform helpers ── Caddy/Apache, PHP-FPM, databases, systemd
        │ isolated site identities and cgroup profiles
        ▼
Web applications, databases, verified backups, recoverable releases
```

See the [complete product preview](docs/SCREENSHOTS.md) for the dashboard and
all workspace illustrations. A live lab screenshot is intentionally not
included until it can be captured from a disposable tagged installation with
synthetic data.

Operational key backup and rotation procedures are documented in
[`docs/SECRETS.md`](docs/SECRETS.md).

### Local development

Requirements: Go 1.26+.

```sh
git clone https://github.com/cyberducttape/StePanel.git
cd StePanel
make check
go run .
```

Open <http://localhost:8080>. Local development uses `data/imports` and `data/www`, so root access is not required.

### Container

The container packages the control plane only. It does not run Apache, PHP, or a database server inside the container. Database administrator packages are host integrations and are not installed in the container image.

```sh
docker build -t stepanel:local .
docker run --rm -p 8080:8080 \
  -e STEPANEL_TLS_TERMINATED=1 \
  -e STEPANEL_ADMIN_PASSWORD='use-a-password-manager' \
  -e STEPANEL_SESSION_SECRET='use-at-least-32-random-characters' \
  -e STEPANEL_AUDIT_KEY='use-a-different-32-character-secret' \
  -e STEPANEL_ADMIN_TOTP_SECRET='BASE32_SECRET' \
  -e STEPANEL_ACCOUNT_KEY='another-high-entropy-secret' \
  -e STEPANEL_OFFSITE_TARGET='s3:bucket/stepanel' \
  -e STEPANEL_RCLONE_CONFIG=/run/secrets/rclone.conf \
  -v "$PWD/rclone.conf:/run/secrets/rclone.conf:ro" \
  stepanel:local
```

The image runs in production mode and requires TOTP, an account key, and a
working offsite rclone target. `STEPANEL_TLS_TERMINATED=1` means this container
must be reachable only through a trusted HTTPS reverse proxy; do not expose
the published port directly to an untrusted network. Mount provider
credentials through your secret manager; do not bake them into the image.

### Server installation from a release

Use a tagged release artifact for production installation. The archive
contains the binary, installer, helpers, service files, and web assets needed
by `install.sh`:

```sh
release=v0.7.0
arch=amd64 # use arm64 on aarch64 hosts
curl -fsSLO "https://github.com/cyberducttape/StePanel/releases/download/${release}/stepanel_${release#v}_linux_${arch}.tar.gz"
curl -fsSLO "https://github.com/cyberducttape/StePanel/releases/download/${release}/SHA256SUMS"
grep "stepanel_${release#v}_linux_${arch}.tar.gz" SHA256SUMS | sha256sum -c -
tar -xzf "stepanel_${release#v}_linux_${arch}.tar.gz"
sudo STEPANEL_ADMIN_PASSWORD='use-a-password-manager' \
  STEPANEL_PANEL_HOSTNAME=panel.example.com \
  STEPANEL_DB_ENGINE=mariadb \
  STEPANEL_DB_VERSION=default ./install.sh
```

The installer records the selected database engine/version, creates a restricted `stepanel` service account, writes the requested panel hostname into the selected webserver, and binds the control plane to `127.0.0.1:8090`. Caddy provisions HTTPS automatically; Apache installations must complete TLS termination before signing in.

Nightly/manual installation smoke CI exercises real disposable systemd hosts
for AlmaLinux, Rocky Linux, Ubuntu, and Debian, including package installation,
service restart, synthetic site creation, and selected-webserver validation.

Verify release provenance and the GitHub attestation before installing on a
production host. The checksum authenticates download integrity; the release
page's SBOM and build-provenance attestation provide the corresponding supply
chain evidence. Building from source is a developer/contributor workflow:
see [Local development](#local-development) and keep it separate from the
normal operator installation path.

After installation, inspect the running binary and build provenance with:

```sh
/opt/stepanel/stepanel version
```

Run `stepanel dr-check` on a host to emit a secret-safe control-plane disaster
recovery manifest. It inventories state files, keys, Git trust material,
external rclone dependencies, site data, and regeneration-only helpers without
copying secret values. Use `stepanel backup-control-plane DEST` to create a
verified SQLite backup, and `stepanel restore-control-plane SOURCE --dry-run`
to validate a candidate before the guarded maintenance-window restore:
`stepanel restore-control-plane SOURCE --replace`.

## Quick workflows

- Migrate a cPanel account with the [cpmove guide](docs/CPMOVE_IMPORTS.md).
- Restore a WordPress `.wpress` archive with the [WordPress guide](docs/WPRESS_IMPORTS.md).
- Deploy and roll back a site through [Git releases](docs/GIT_DEPLOYMENTS.md).
- Run the [disposable lab](deploy/lab/README.md) before touching production data.

## Documentation

### Core References (Canonical Sources)

Read these to understand what StePanel is, does, and can do:

- **[docs/FEATURES.md](docs/FEATURES.md)** — Authoritative capability matrix and feature status
- **[docs/SECURITY.md](docs/SECURITY.md)** — Threat model, security boundaries, known limitations  
- **[docs/ROADMAP.md](docs/ROADMAP.md)** — Medium-term direction and planned work

### Getting Started

- **[docs/INSTALLATION.md](docs/INSTALLATION.md)** — Installation, configuration, production setup
- **[docs/OPERATIONS.md](docs/OPERATIONS.md)** — Running and managing StePanel
- **[docs/SECRETS.md](docs/SECRETS.md)** — Backup, rotation, and disaster recovery procedures

### Workflows by Use Case

- **[docs/CPMOVE_IMPORTS.md](docs/CPMOVE_IMPORTS.md)** — Migrate from cPanel
- **[docs/ARCHIVE_IMPORTER.md](docs/ARCHIVE_IMPORTER.md)** — Import sites from other hosting providers
- **[docs/GIT_DEPLOYMENTS.md](docs/GIT_DEPLOYMENTS.md)** — Deploy and roll back via Git
- **[docs/CUSTOMER_WORKFLOWS.md](docs/CUSTOMER_WORKFLOWS.md)** — Multi-tenant operations and workflows

### Architecture & Design

- **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — System design and components
- **[docs/adr/](docs/adr/)** — Architecture Decision Records (immutable design history)
- **[docs/THREAT_MODEL.md](docs/THREAT_MODEL.md)** — Detailed threat analysis and mitigations

### API & Development

- **[docs/API_GUIDE.md](docs/API_GUIDE.md)** — API authentication, permissions, copyable workflows
- **[docs/openapi.yaml](docs/openapi.yaml)** — Machine-readable OpenAPI contract
- **[docs/DEVELOPER_WORKFLOWS.md](docs/DEVELOPER_WORKFLOWS.md)** — Contributor guide and local development

### Evidence & Case Studies

- **[docs/lab-results/2026-09-06-recovery-drills.md](docs/lab-results/2026-09-06-recovery-drills.md)** — Measured recovery RTO/RPO evidence
- **[docs/CASE_STUDY.md](docs/CASE_STUDY.md)** — Real-world backup and recovery example

### Integrations & Operations

- **[docs/INTEGRATIONS.md](docs/INTEGRATIONS.md)** — Cloud provider, DNS, and third-party integrations
- **[docs/RELEASING.md](docs/RELEASING.md)** — Release process and artifact generation
- **[docs/INCIDENT_LAB.md](docs/INCIDENT_LAB.md)** — Incident response procedures and drills

### Other Resources

- **[CHANGELOG.md](CHANGELOG.md)** — Version history and release notes
- **[docs/DOCUMENTATION_GUIDE.md](docs/DOCUMENTATION_GUIDE.md)** — How documentation is organized and maintained
- **[CONTRIBUTING.md](CONTRIBUTING.md)** — Contribution guidelines
- **[observability/README.md](observability/README.md)** — Metrics, logging, and monitoring
- **[Release artifacts](https://github.com/cyberducttape/StePanel/releases)** — Binary releases, checksums, SBOM

## API reference

The [API guide](docs/API_GUIDE.md) explains authentication, permissions,
copyable `curl` workflows, asynchronous jobs, backups, migrations, databases,
and customer scope. Use the versioned [OpenAPI contract](docs/openapi.yaml) as
the machine-readable endpoint, schema, response, and authorization reference.

## Configuration

Configuration is intentionally documented in the [installation guide](docs/INSTALLATION.md)
and [secrets/DR guide](docs/SECRETS.md), rather than duplicated on the landing
page. Those references include the complete environment-variable table,
production validation rules, key backup and rotation procedures.

Invalid numeric limits and unsafe production paths are rejected at startup
instead of silently falling back to defaults. Production state paths must be
absolute so service behavior does not depend on its working directory.

Set `STEPANEL_INSTALL_MAIL=1` during installation to install Exim, Dovecot,
and SpamAssassin.

Set `STEPANEL_INSTALL_FTP=1` to install and enable vsftpd. The panel reports
vsftpd in the service inventory. Local users are chrooted to their site root
and passive ports default to `40100-40200`; configure FTPS and create
least-privilege site users before allowing external access. Plain FTP should
only be used on a trusted management network.

Set `STEPANEL_INSTALL_NODE=1 STEPANEL_NODE_VERSIONS=20.18.0,22.14.0` to install
Node versions through NVM. The panel can select an installed version per site
and generate a validated reverse proxy for a local app backend in the selected
webserver.
Managed apps are supervised by per-site systemd units and can be started,
stopped, or restarted through the authenticated API.

Set `STEPANEL_INSTALL_SECURITY=1` to install ClamAV, inotify-based PHP
monitoring, and recoverable quarantine handling. This is a defense-in-depth
layer, not a guarantee against all malware; keep applications patched and use
least-privilege service accounts.
Mailbox contents are preserved in the private mail root and reported by the
restore job; activation still requires destination domain, mailbox, DNS, TLS,
and credential mapping.

## Roadmap

The next product milestones are first-run setup, verified backups, safer
upgrades, resource quotas, and multi-user roles. See the
[roadmap](docs/ROADMAP.md) for the full plan.

## License

StePanel is released under the [MIT License](LICENSE).
