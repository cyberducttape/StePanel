# StePanel service objectives

These objectives are a starting point for a small production installation.
Tune them to the backup volume and hardware profile rather than copying them
blindly.

| Service indicator | Target | Measurement |
| --- | ---: | --- |
| Control-plane availability | 99.9% monthly | `stepanel_up` and HTTP probes |
| Restore acceptance latency | 99% under 2 seconds | HTTP 202 response time |
| Successful restore ratio | 99.5% monthly | completed / (completed + failed) |
| Backup import data loss | 0 bytes | restore verification and audit log |

## Recovery objectives

The default controlled-single-host RPO/RTO contract is defined in
[`DR_OBJECTIVES.md`](DR_OBJECTIVES.md): 24-hour RPO, 60-minute control-plane
RTO, four-hour single-site RTO, and eight-hour bare-host RTO. Those objectives
apply only after the stated offsite backup and destructive restore evidence is
recorded; they are not implied by a locally verified archive.

## Alert policy

- Page when `stepanel_up == 0` for 2 minutes.
- Page when `increase(stepanel_restore_jobs_failed_total[15m]) > 0` during a
  scheduled migration window.
- Warn when `stepanel_restore_jobs_active > 0` for longer than the expected
  restore duration.
- Page when `stepanel_jobs_dead_letter > 0`; investigate the payload and
  operation audit trail before replaying it.
- Warn when `stepanel_jobs_queued` remains above the worker capacity for 10
  minutes.
- Warn when `increase(stepanel_upload_rejections_total{reason="capacity"}[1h]) > 0`;
  migrations are being refused for lack of disk.
- Warn when `increase(stepanel_upload_rejections_total{reason="internal"}[15m]) > 0`;
  staging failed for a server-side reason (see the logged cause).
- Warn when `stepanel_event_streams_rejected_total` increases; dashboards are
  hitting the stream limits.
- Page when backup age or schedule-failure metrics predict an RPO breach.

Every alert should link to an incident ticket and one of the runbooks in
[`INCIDENT_LAB.md`](INCIDENT_LAB.md). Do not put customer names, usernames, or
backup paths into metric labels.
