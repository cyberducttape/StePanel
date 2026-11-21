# KVM database ENOSPC and operator alert delivery — 2026-11-21

**Run:** [GitHub Actions 44859646620](https://github.com/cyberducttape/StePanel/actions/runs/44859646620)  
**Source:** `7e2a5751`  
**Guest:** Rocky Linux 9.9, QEMU/KVM, `cache=none`, installed MariaDB and Caddy

All phases passed: boot, install (21 smoke/matrix checks), the five
abrupt-loss scenarios (file restore, database restore, Git deploy,
retention, control plane), and two phases that ran on an installed host for
the first time:

- **db-enospc:** the MariaDB data directory sat on a 1,992 MiB loopback
  filesystem with 1,367 MiB free. A declared 1,879 MiB dump was refused at
  admission. With its size undeclared, the load was stopped by the helper's
  free-space watchdog with 1,019 MiB still free. The live database was
  unchanged, no shadow tables remained, and a later restore completed. The
  watchdog stopped the load before the filesystem filled, so this is not a
  true out-of-space write.
- **operator-alerts:** a failed job's alert was emailed over verified
  STARTTLS. With the SMTP server stopped, a second alert stayed undelivered
  and `/api/health/operational` reported 1 failed delivery. After the server
  returned, the alert was retried and delivered and the failure count
  cleared. Webhook delivery was not tested.

One pass on one host. It is not repeated or physical-host evidence.
