# HTTPS certificates

StePanel can optionally install Certbot and request a Let’s Encrypt
certificate for a domain managed by the panel.

## Enable the integration

```sh
sudo STEPANEL_INSTALL_TLS=1 \
  STEPANEL_ADMIN_PASSWORD='use-a-secret-manager' \
  STEPANEL_PANEL_HOSTNAME=panel.example.com ./install.sh
```

The installer adds `certbot` and the Apache plugin, installs the root-owned
`stepanel-certbot` helper, and enables the Certbot renewal timer.

Before issuing a certificate:

- Point the domain’s DNS record at the server.
- Make ports 80 and 443 reachable from the public internet.
- Ensure Apache has a matching virtual host and no conflicting proxy config.
- Use a dedicated hostname; wildcard certificates are not supported here.

Bootstrap the first certificate from the host before browser login:

```sh
sudo stepanel-certbot panel.example.com admin@example.com
```

The authenticated `POST /api/certificates/issue` endpoint accepts a domain and
email address, validates both values, queues a bounded certificate job, invokes
the fixed root-owned helper through a typed root-broker request (without a
separate certbot sudo grant), uses non-interactive terms acceptance and
HTTP-to-HTTPS redirect, and records an audit event. Poll `/api/jobs/<job_id>`
for completion. Only one certificate request for a given domain can run at a
time.

Issuance is deliberately explicit because DNS, firewall, Apache, and rate-limit
failures require operator review. Renewal is handled by the host’s Certbot
timer; monitor its logs and verify Apache after renewals.
