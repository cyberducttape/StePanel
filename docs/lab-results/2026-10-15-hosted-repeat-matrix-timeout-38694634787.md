# Hosted repeated recovery matrix hit the job time limit

Run [38694634787](https://github.com/cyberducttape/StePanel/actions/runs/38694634787)
tested source `31a834178e9bff9eaba194c4397ec08630fbd6b1` on 2026-10-15 with
`recovery_matrix_repeats=20`. All six Ubuntu 24.04, AlmaLinux 9, and Rocky
Linux 9 × Apache/Caddy + MariaDB jobs completed installation and two full
recovery-matrix iterations. Each then began iteration 3/20 and was cancelled
by the hosted job's six-hour timeout. The logs end in `context canceled`; no
recovery assertion failure is reported for the completed iterations.

This run contributes two successful iterations per combination, not the
requested 20. The third iteration was partial and is not counted. The
diagnostic artifacts are retained on the Actions run. An earlier 20-repeat
attempt on `1fb3879` also failed at the hosted job boundary and is not current
source evidence.

The workflow previously ran every repetition sequentially inside one job, so
the requested count could exceed GitHub-hosted runner limits. The workflow now
limits each dispatch to one or two iterations and requires unique concurrency
shard IDs for two-iteration runs. See
[`REAL_HOST_FAILURE_MATRIX.md`](../REAL_HOST_FAILURE_MATRIX.md) for the
20–50-iteration shard procedure. The gate remains open until a complete,
same-source cohort passes on all six host combinations.
