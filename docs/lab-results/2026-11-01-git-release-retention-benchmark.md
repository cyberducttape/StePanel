# Git release retention: walk reduction (2026-11-01)

**Question.** Release retention runs after every Git deployment. It sized
every retained previous release with a full tree walk, including releases the
count or age rule was about to remove, and walked each removed release again
to validate it before deleting it. How much did that cost?

**Change.** Retained releases are ordered newest first and the count and age
rules each remove an oldest suffix, so the byte rule only needs the sizes of
the releases that survive those rules. Releases removed by count or age are no
longer sized; each removed release is walked once (its pre-removal
validation). The kept set is unchanged (`TestPruneReleasesAppliesCountAgeAndBytesTogether`).

**Method.** `go test -run '^$' -bench PruneReleases -benchtime=5x` on a
developer workstation (local NVMe, ext4, warm cache), `retain=3`, a
byte limit that does not bind. Each iteration builds fresh release trees
before the timer starts.

| Benchmark | Before | After |
|-----------|-------:|------:|
| Steady state: 4 previous releases × 2,000 files, 1 pruned | 364 ms | 86 ms |
| Steady state: 4 previous releases × 20,000 files, 1 pruned | 5.17 s | 1.84 s |
| Backlog: 20 previous releases × 5,000 files, 17 pruned | 5.86 s | 3.21 s |

One warm sizing walk of a 20,000-file release costs about 0.1 s and removing
it about 0.5 s (`BenchmarkPruneReleaseWalkVersusRemove`), so deletion, not
walking, now dominates. Absolute times include filesystem writeback of the
freshly created fixtures and vary between runs; the ratio is the result.

**Not done.** Retention still runs synchronously at the end of a deployment
under the site's lock. Moving it to an asynchronous, fenced cleanup job, and
persisting verified release sizes so kept releases need not be re-walked, are
possible next steps if deployments of very large trees show retention in
their latency.
