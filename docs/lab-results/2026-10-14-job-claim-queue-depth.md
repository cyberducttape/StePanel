# Durable job claim queue-depth benchmark — 2026-10-14

| Field | Value |
|---|---|
| Host | Linux x86_64; AMD Ryzen 7 7735U; Go 1.26.7 |
| Command | `go test -run '^$' -bench '^BenchmarkClaimNextQueueDepth/queued_100000_stalled_owners_10/existing_indexes$' -benchtime=1x -count=1 .` |
| Result | PASS; 324.5 ms/op (one measured claim) |
| Queue shape | 100,000 queued rows across 10 owners with active leases, followed by one eligible owner |
| SQLite plan | Candidate lookup uses `jobs_kind_state_idx`; correlated owner check uses `jobs_owner_state_idx`; SQLite sorts candidates using a temporary B-tree |

The benchmark is synthetic and single-iteration. Separate exploratory runs
with an additional `(kind,state,started_at,id)` index removed the temporary
sort from the query plan but did not consistently improve measured latency
(approximately 246–266 ms/op versus approximately 260–324 ms/op without it).
The dominant cost appears to be evaluating the correlated eligibility check
across blocked candidates, so this index alone is not justified as a fix.
Benchmark fixture setup is excluded from the timed region.

This is a queue-depth diagnostic, not a certified capacity limit. It does not
represent concurrent production writers, representative payload sizes, or
hosted application traffic. A query redesign should be guided by repeatable
measurements and retain the owner-ordering and lease correctness guarantees.
