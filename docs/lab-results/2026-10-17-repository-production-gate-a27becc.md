# Repository production gate — 2026-10-17

**Source:** `a27beccc9b5be33a7e30d57662d027c74e6a6cbe`  
**Toolchain:** Go 1.26.7, linux/amd64  
**Result:** Repository production gate passed.

`bash scripts/production-gate.sh` completed successfully on the exact source
commit above. It passed `go vet ./...`, the complete Go test suite, the full
race suite, repository recovery drills, configured coverage thresholds,
control-plane benchmarks, mixed HTTP load, documentation/link/reference
checks, Grafana dashboard validation, pinned-action checks, embedded asset
validation, and shell syntax checks.

The aggregate statement coverage was 56.2% against the 50% floor; all
configured per-package coverage thresholds passed. The root package reported
52.5% in the ordinary coverage run and 52.8% in the target checker. The root
race suite passed in 475.019 seconds. The synthetic mixed-load test passed
with 200 sites, 180 requests, 20 backup jobs, and a 12.987 ms p95 response
time. These benchmark and synthetic-load measurements are not a certified
production capacity envelope.

The gate does not establish installed-host recovery, physical host power-loss
recovery, all ENOSPC boundaries, live SMTP/webhook outage behavior, or
production capacity. A 20-repeat installed-host recovery cohort was dispatched
against this same source SHA as ten uniquely keyed two-iteration shards:

- [38995993208](https://github.com/cyberducttape/StePanel/actions/runs/38995993208)
- [38995995185](https://github.com/cyberducttape/StePanel/actions/runs/38995995185)
- [38995997076](https://github.com/cyberducttape/StePanel/actions/runs/38995997076)
- [38995999213](https://github.com/cyberducttape/StePanel/actions/runs/38995999213)
- [38996001167](https://github.com/cyberducttape/StePanel/actions/runs/38996001167)
- [38996003249](https://github.com/cyberducttape/StePanel/actions/runs/38996003249)
- [38996005452](https://github.com/cyberducttape/StePanel/actions/runs/38996005452)
- [38996007281](https://github.com/cyberducttape/StePanel/actions/runs/38996007281)
- [38996009083](https://github.com/cyberducttape/StePanel/actions/runs/38996009083)
- [38996011396](https://github.com/cyberducttape/StePanel/actions/runs/38996011396)

At report creation, the cohort was still running/queued and contributes no
completed repetition evidence. The CI, CodeQL, and standard installation smoke
workflows passed on this SHA; the quota-enabled install smoke was still in
progress. See the authoritative [v1 production gates](../V1_PRODUCTION_GATES.md)
for the remaining release blockers.
