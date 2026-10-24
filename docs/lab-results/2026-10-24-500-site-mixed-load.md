# 500-site synthetic mixed HTTP load — 2026-10-24

**Application source:** `1af5fc05e0f3ab39b0da974651ca4b081966a193`  
**Result:** PASS for one synthetic workload run; this is not a certified
production capacity result.

## Environment

| Item | Value |
| --- | --- |
| OS | Ubuntu 26.04.1 LTS (outside the declared supported OS matrix) |
| Kernel / architecture | Linux 7.0.0-38-generic / x86_64 |
| CPU | AMD Ryzen 7 7735U, 8 cores / 16 logical CPUs |
| Memory | 14 GiB installed |
| Storage | ext4 on `/dev/nvme0n1p2`; 469 GiB total, 240 GiB available at measurement |
| Workload scratch | `./stepanel-capacity-run` on the ext4 workspace filesystem; removed after success |
| Binary | `go build -trimpath -o /tmp/stepanel-capacity-1af5fc0 ./cmd/stepanel` |
| Binary SHA-256 | `920eac5a13f8d7964a06ce88c9aac2883809ff0a6331e4e944cb47b65347c8633` |
| Workload duration | Not captured |
| Peak CPU / RSS | Not captured |

The host had nearly all of its 511 MiB swap in use in a post-run sample. The
test did not monitor resource use during execution, isolate the host, or
measure background load. The result must not be compared directly with an
unloaded production host.

## Workload and result

The existing `scripts/e2e-mixed-load-test.sh` seeded 500 small sites (an HTML
page and a 64 KiB asset per site), sent 480 dashboard/API reads from eight
readers across its 12 configured endpoints, and submitted 20 backup jobs for
the in-process worker. All HTTP requests returned 2xx and all backup jobs
completed.

```text
mixed HTTP load passed: sites=500 requests=500 readers=8 backup_jobs=20
avg_seconds=0.0347 p95_seconds=0.296687 max_seconds=0.4513
```

The slowest observed endpoint was `/metrics` at 451 ms; `/api/sites/overview`
reached 221 ms. The configured p95 limit was 500 ms. The exact command was:

```sh
STEPANEL_LOAD_SITES=500 \
STEPANEL_LOAD_WORKERS=8 \
STEPANEL_LOAD_REQUESTS=60 \
STEPANEL_LOAD_WRITES=20 \
STEPANEL_LOAD_MAX_P95_MS=500 \
bash scripts/e2e-mixed-load-test.sh \
  /tmp/stepanel-capacity-1af5fc0 ./stepanel-capacity-run
```

## Limits

This is one run on an unsupported OS with synthetic sites, no live PHP or
database traffic, no sustained schedule load, no restart/recovery phase, and
no measured peak CPU, memory, control-plane database size, or storage growth.
It does not establish the proposed 500-site/2,000-domain/100-account
operating envelope. Repeat on supported OS and storage combinations with
resource monitoring, representative applications, scheduled jobs, concurrent
mutations, and recovery checks before publishing capacity claims.
