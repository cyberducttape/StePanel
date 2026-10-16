# StePanel observability

This directory is the seed of **SteObserve**, a focused observability product
for hosting control planes and PHP workloads. It is intentionally deployable
with standard Prometheus and Grafana rather than requiring a proprietary
agent.

The included dashboard combines restore activity with durable job backlog and
age, control-plane database utilization, storage reservations, audit and alert
outbox backlog, dead-letter jobs, and recovery readiness. These are
StePanel-level signals, not a host resource monitor: scrape host CPU, memory,
filesystem free space, and inode metrics from your host monitoring agent and
correlate them with reservation and job activity.

## Quick start

1. Run StePanel with `/metrics` reachable by your Prometheus server.
2. Add [`prometheus.yml`](prometheus.yml) to the Prometheus configuration or
   merge its scrape job into an existing configuration.
3. Import [`grafana-dashboard.json`](grafana-dashboard.json) into Grafana.
4. Load [`alerts.yml`](alerts.yml) through Prometheus `rule_files` and route
   the alerts to the on-call channel. Use [`../docs/SLO.md`](../docs/SLO.md)
   to set service-level targets.

The metrics endpoint is deliberately low-cardinality: account names, backup
paths, and error text are never emitted as labels. Backup health is aggregated
across schedules so customer/site identifiers are not exposed to metrics
consumers.

Protect `/metrics` with network policy, firewall rules, or an authenticated
reverse proxy. If it is exposed publicly, scrape credentials and operational
metadata may be disclosed.
