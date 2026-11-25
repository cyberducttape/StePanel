# KVM operation drills with signed webhook alerts — 2026-11-25

**Run:** [GitHub Actions 45525567196](https://github.com/cyberducttape/StePanel/actions/runs/45525567196)  
**Source:** `fe2ecdfa`  
**Guest:** Rocky Linux 9.9, QEMU/KVM, `cache=none`, installed MariaDB and Caddy

All phases passed: boot, install (23 smoke/matrix checks), the six
abrupt-loss scenarios (file restore, database restore, Git deploy,
retention, control plane, interrupted backup), backup/file-restore ENOSPC,
database restore ENOSPC, and operator alerts.

## Operator alerts

The drill (`deploy/lab/operator-alert-smoke.sh`) now covers both channels:

- **SMTP:** a failed job's alert arrived over verified STARTTLS; with the
  SMTP server stopped a second alert stayed undelivered and
  `/api/health/operational` reported 1 failed delivery; after the server
  returned the alert was retried, delivered, and the failure cleared.
- **Webhook:** the HTTPS receiver listened on a public address bound to a
  dummy interface inside the guest, so the production SSRF policy,
  certificate verification and signing ran unchanged and no packet left the
  guest.
  - A failed job's alert arrived with a valid `X-StePanel-Signature`
    (HMAC-SHA256 over the exact body), its event ID and user agent.
  - While the receiver answered HTTP 503, a second alert reached it, was
    refused, and operational health reported 1 failed delivery; once it
    answered 200 the alert was retried and delivered and the count cleared.
  - With the receiver's name mapped to 127.0.0.1, a third alert was blocked
    by the outbound policy and reached no receiver; once the name was public
    again it was delivered, leaving the outbox clean.

## Limits

One pass on one disposable guest. The receiver is local to the guest, not a
separately administered service; repeated outage and large-backlog recovery
are not covered.
