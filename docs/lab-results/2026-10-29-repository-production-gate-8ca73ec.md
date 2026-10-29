# Repository production gate — 2026-10-29

**Source:** [`8ca73ec617e08de77d46c3b68975a64232512fd4`](https://github.com/cyberducttape/StePanel/commit/8ca73ec617e08de77d46c3b68975a64232512fd4)
**Code equivalence:** `8ca73ec` changes only Markdown relative to `18672eb`; all Go, shell, workflow, and frontend source files are unchanged from the exact-source checks recorded in [the current-main verification report](./2026-10-29-current-main-checks-18672eb.md).
**Environment:** Go 1.26.10, Linux amd64, kernel 7.0.0-38-generic, AMD Ryzen 7 7735U with Radeon Graphics, 16 logical CPUs, 14 GiB RAM, ext4 on NVMe.
**Command:** `bash scripts/production-gate.sh`

## Results

| Gate | Result | Evidence |
| --- | --- | --- |
| Formatting and `go vet ./...` | PASS | Production-gate run. |
| Full Go tests with atomic coverage | PASS | Root package 54.4%; repository total 57.6%. |
| Package coverage floors | PASS | All 13 configured thresholds met; root broker 68.2%, importer 70.0%, jobs 100.0%. |
| Full Go race suite | PASS | Root package 406.672 seconds; all other packages passed or had no tests. |
| Repository recovery drills | PASS | Partial SQL import cleanup 2s; interrupted transaction recovery 2s; configuration rollback 3s; pending runtime reconciliation 2s. |
| Control-plane benchmarks | PASS | Site mutation samples: 1.03–1.05 ms across 10–500 sites; panel/worker contention 0.92 ms; process contention 0.94 ms; 50 backup/restore jobs 59.3 ms/batch. |
| Mixed HTTP load | PASS | 200 sites, 180 requests, 4 readers and 20 backup jobs; average 4.1 ms, p95 24.0 ms, maximum 36.7 ms. |
| Documentation and references | PASS | All documentation links valid; code references checked across 93 documents. |
| Quota contracts, action pins, assets and shell syntax | PASS | Contract fixtures passed; all workflow actions use full commit SHAs; embedded assets and shell syntax validated. |

These repository-level benchmarks and recovery drills use synthetic workloads
and temporary data. The mixed HTTP run does not include live PHP-FPM or
database traffic. Passing this gate is not a host certification.

## External gates still open

This run did not exercise a real host, physical power loss, installed-host
ENOSPC during database restore/import/publication, the six-host matrix on this
source, a repeated 20–50-run recovery cohort, or a sustained capacity envelope.
The release decision remains **NO-GO**. See the [authoritative production
gates](../V1_PRODUCTION_GATES.md).
