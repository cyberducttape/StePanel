# StePanel observability

This directory is the seed of **SteObserve**, a focused observability product
for hosting control planes and PHP workloads. It is intentionally deployable
with standard Prometheus and Grafana rather than requiring a proprietary
agent.

`/metrics` exports two kinds of signal:

- **Control-plane health:** durable job backlog and age, dead-letter jobs,
  control-plane database utilization, capacity reservations, audit and alert
  outbox backlog, readiness checks, HTTP error rates, restore and backup
  outcomes, recovery-proof freshness, and database diagnostics.
- **Managed-site and host evidence:** available bytes and inodes for the
  configured site, backup, and database filesystems; per-site systemd-slice
  memory, task, and CPU-quota use; per-site PHP-FPM pool counters; per-site
  scheduled-backup results; and the latest certificate issuance job per
  route domain.

Per-site runtime values come from a background sampler that runs once a
minute with a bounded worker pool, so a scrape never waits on privileged
probes. Each site sample carries its own timestamp, and
`stepanel_site_runtime_inventory_up` reports whether the site inventory was
read completely. CPU-quota utilization is derived from the change in the
slice's cumulative CPU time between two samples and is omitted for unlimited
quotas, counter resets, and stale samples. It is an interval estimate, not
kernel throttling or pressure-stall data. StePanel does not replace a host
agent for whole-host CPU, memory pressure, network, or disk latency.

## Quick start

1. Run StePanel with `/metrics` reachable by your Prometheus server.
2. Add [`prometheus.yml`](prometheus.yml) to the Prometheus configuration or
   merge its scrape job into an existing configuration.
3. Import [`grafana-dashboard.json`](grafana-dashboard.json) into Grafana.
4. Load [`alerts.yml`](alerts.yml) through Prometheus `rule_files` and route
   the alerts to the on-call channel. Use [`../docs/SLO.md`](../docs/SLO.md)
   to set service-level targets.

CI validates dashboard JSON, panel layout, privacy-safe labels, and that every
referenced StePanel metric is emitted by the Go exporters. It also runs
`promtool check rules` on every alert and `promtool test rules` against
[`alerts_test.yml`](alerts_test.yml), which asserts the firing labels and
rendered text of 29 of the 41 alerts. The remaining 12 (recovery-proof,
restore-job, state-error, SQLite-busy, target-down, certificate-evidence,
and database transaction/session alerts) are syntax-checked only.

## Alerts

Every alert description states the observed value, the threshold or sample
window, the metric to inspect next, and a safe operator action. Warning and
critical pairs do not overlap: a near-limit warning stops where its critical
alert starts.

| Area | Alerts |
| --- | --- |
| Availability and readiness | `StePanelTargetDown`, `StePanelReadinessCheckFailed`, `StePanelHTTP5xxRateHigh`, `StePanelHTTP5xxLowTraffic` |
| Durable jobs | `StePanelQueueWaitTooLong`, `StePanelDeadLetterJobs`, `StePanelJobInventoryUnavailable`, `StePanelRestoreJobsStuck`, `StePanelRestoreFailures` |
| Control-plane state | `StePanelDurableStateError`, `StePanelTemporaryStateErrors`, `StePanelSQLiteBusy`, `StePanelControlPlaneDatabaseNearLimit`, `StePanelCapacityLedgerUnavailable` |
| Delivery outboxes | `StePanelAuditOutboxBacklog`, `StePanelNotificationOutboxBacklog` |
| Backups and recovery | `StePanelBackupNeverSucceeded`, `StePanelBackupOverdue`, `StePanelScheduledBackupFailures`, `StePanelApplicationRecoveryProofMissing`, `StePanelApplicationRecoveryProofStale`, `StePanelRecoveryProofInventoryDown` |
| Host filesystems | `StePanelFilesystemBytesLow`, `StePanelFilesystemInodesLow`, `StePanelFilesystemCapacityUnavailable` |
| Managed databases | `StePanelDatabaseDiagnosticsDown`, `StePanelDatabaseLongTransactions`, `StePanelDatabaseSessionsBlocked` |
| Certificates | `StePanelCertificateRenewalFailed`, `StePanelCertificateRenewalEvidenceUnavailable` |
| Per-site runtime | `StePanelSiteFPMSaturated`, `StePanelSiteFPMProbeUnavailable`, `StePanelSiteCgroupMemoryNearLimit`, `StePanelSiteCgroupMemoryLimitReached`, `StePanelSiteCgroupTasksNearLimit`, `StePanelSiteCgroupTasksLimitReached`, `StePanelSiteCPUQuotaNearLimit`, `StePanelSiteCPUQuotaSaturated`, `StePanelSiteResourceProbeUnavailable`, `StePanelSiteRuntimeInventoryUnavailable`, `StePanelSiteRuntimeSamplesStale` |

Severities and thresholds are defined in [`alerts.yml`](alerts.yml). Alert
delivery to a person is not proven by these rules; see the alert-delivery
procedure in [`../docs/OPERATIONS.md`](../docs/OPERATIONS.md).

## Labels and privacy

Labels are bounded. Per-site metrics carry the managed site name (`site`)
and, where relevant, the PHP version of the pool; certificate metrics carry
the route `domain`. Hosting account names, customer identities, backup
paths, file paths, and raw error text are never emitted as labels. Failure
causes appear only as fixed reason codes (for example on
`stepanel_database_diagnostics_up`).

Site names and route domains are operational metadata. Protect `/metrics`
with network policy, firewall rules, or an authenticated reverse proxy, and
treat scrape storage as containing your site inventory. If the endpoint is
exposed publicly, that inventory and other operational metadata are
disclosed.
