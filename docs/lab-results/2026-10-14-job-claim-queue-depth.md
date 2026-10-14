# Durable job claim queue-depth benchmark — 2026-10-14

| Field | Value |
|---|---|
| Host | Linux x86_64; AMD Ryzen 7 7735U; Go 1.26.7 |
| Initial command | `go test -run '^$' -bench '^BenchmarkClaimNextQueueDepth/queued_100000_stalled_owners_10/existing_indexes$' -benchtime=1x -count=1 .` |
| Initial result | PASS; 324.5 ms/op (one measured claim) |
| Queue shape | 100,000 queued rows across 10 owners with active leases, followed by one eligible owner |
| SQLite plan | Candidate lookup uses `jobs_kind_state_idx`; correlated owner check uses `jobs_owner_state_idx`; SQLite sorts candidates using a temporary B-tree |

The initial run was followed by three measured claims for both index variants
at commit `d553838427c63f122a2b9f7b1027a42f28b7ccca`:

```sh
go test -run '^$' -bench '^BenchmarkClaimNextQueueDepth/queued_100000_stalled_owners_10$' -benchtime=3x -count=1 .
```

The existing indexes measured 234.8 ms/op. Adding
`(kind,state,started_at,id)` measured 241.3 ms/op. The extra index removes the
temporary sort from SQLite's plan but did not improve this measurement. The
correlated eligibility scan across blocked candidates appears to dominate;
the extra index is not justified as a production change by these results.
Fixture setup and re-arming are excluded from the timed region.

An experimental grouped-owner-head selection was also compared against the
current candidate scan, using the same 100k/10-stalled-owner fixture and
three measured selections per strategy:

| Selection strategy | Result | Relevant query-plan behavior |
|---|---:|---|
| Ordered candidate scan | 242.6 ms/op | Uses `jobs_kind_state_idx`; per-candidate owner check uses `jobs_owner_state_idx`; temporary B-tree for final ordering |
| Grouped owner heads | 316.4 ms/op | Scans queued state, materializes owner groups, then sorts candidates; temporary B-trees for grouping and ordering |

This is a selection-only comparison, not a full claim benchmark. The grouped
query preserves the fixture's expected winner, but it is slower here and has
not been promoted to production code. Broader owner distributions and
correctness contract tests would be required before reconsidering it.

This remains a synthetic microbenchmark, not a certified capacity limit. It
does not represent concurrent production writers, representative payload
sizes, or hosted application traffic. A query redesign should use repeated
measurements and retain the owner-ordering and lease correctness guarantees.
