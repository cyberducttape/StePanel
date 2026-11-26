# Control-plane database access model

StePanel keeps its control plane in one SQLite database in WAL mode
(`STEPANEL_CONTROL_PLANE_DB`). SQLite stays: it keeps installation and
maintenance simple. This document records how the database is accessed, what
an audit of that access found, and the order in which access will change. It
exists because the obvious fix for contention, raising the connection limit,
changes transaction behavior and is unsafe without the steps below.

## Current model

- Each process (panel, worker) opens one `*sql.DB` with
  `SetMaxOpenConns(1)` (`openControlPlaneDBWithMaxBytes`, `controlplane.go`).
  Across the two processes WAL lets one process read while the other writes,
  but inside a process every query waits for the single connection.
- Writes that read-then-write take the connection with `BEGIN IMMEDIATE`
  (`beginImmediate`, `sqlite_immediate.go`, 9 call sites); 28 sites use
  ordinary transactions. Code inside a transaction assumes it holds the only
  connection.
- Per-connection settings are in the DSN, so every connection the pool opens
  gets them: `busy_timeout(5000)`, `journal_mode(WAL)`, `foreign_keys(ON)` and,
  since 2026-11-25, `max_page_count` (the size limit).
- The database file and its `-wal`, `-shm` and `-journal` sidecars are created
  or tightened to `0600` before SQLite opens them
  (`prepareControlPlaneDatabaseFile`).
- Persistence failures are typed: only `errControlPlaneRevisionConflict` is
  merged and retried; `errControlPlaneStorageExhausted` and
  `errControlPlaneStorageUnavailable` are returned at once and reported by the
  `control_plane_storage` readiness check.

## Audit findings (2026-11-25)

| Finding | Status |
| --- | --- |
| `max_page_count` was applied once to the pooled connection, so a connection replaced after a driver error lost the size limit | Fixed: the limit is in the DSN (`TestControlPlaneSizeLimitSurvivesConnectionReplacement`) |
| No visibility into connection waits | Fixed: `stepanel_control_plane_db_connection_waits_total`, `..._wait_seconds_total`, `..._connections_in_use`, `..._connections_max` |
| 178 query and exec calls carry no context, so no deadline; 64 do | Request paths done (phase 2): session validation and revocation (`session.ReadTimeout` 2 s, `session.WriteTimeout` 10 s), job status reads, listings and the submission idempotency lookup (`durableJobReadTimeout` 5 s), and state-blob writes (accounts, site access and the other bound stores; `controlPlaneStateWriteTimeout` 10 s) now have deadlines; worker, lease and maintenance paths do not yet |
| Transactions rely on being the only connection; with more connections a stray `db.Query` inside a transaction would read outside it instead of deadlocking | Open: must be ruled out before a read pool (phase 3) |
| The panel and worker each hold a connection; WAL already gives cross-process read concurrency | By design |

## Planned changes, in order

1. **Instrumentation (done).** Watch the wait metrics on real hosts before
   changing the pool. The capacity profile reports them under load.
2. **Deadlines on request paths (done).** Request-path queries take a
   context with a short deadline, so a slow maintenance operation fails a
   request quickly instead of stalling it: the session registry, the job
   store's status reads, listings and idempotency lookup, and state-blob
   writes (which carry account management). Each has a test that holds the
   single connection and requires the call to fail at its deadline. The
   dashboard's database reads are its job history, which uses those bounded
   listings; its site overviews come from the stale-while-revalidate cache.
   Writes keep a deadline longer than SQLite's 5-second busy timeout.
   Worker, lease and maintenance queries are not request paths and keep
   waiting for the connection.
3. **Bounded read concurrency, serialized writes.** Open a second `*sql.DB`
   per process for read-only request paths with `_pragma=query_only(1)` and a
   small connection limit, and keep the writer at one connection. Move read
   sites to it one at a time, each with a test. Prerequisites: every
   per-connection setting in the DSN (now true), no reliance on temporary
   tables or other connection state, and no read inside a write transaction
   that expects to see the transaction's own uncommitted changes.
4. **No new whole-object state blobs.** The `state_blobs` stores (whole JSON
   payloads with compare-and-swap and three-way merge, `controlplane.go`) are
   a transitional layer whose cost grows with the number of objects. New
   features must use relational tables. Existing blobs move to tables
   gradually, frequently updated and security-sensitive ones first: resource
   profiles, environment secrets, site access, then the rest. **Resource
   profiles are done** (migration 33, `resource_profiles_table.go`): one row
   per site with a revision. `ResourceStore` keeps its map and lock, and a
   write sends only the rows that changed, each guarded by its revision, so
   writes to different sites no longer conflict; a write over a row another
   process changed reloads the table and is retried by the caller. **Site
   access is done** (migration 34, `site_access_table.go`): `site_access`
   rows with their SSH keys in `site_access_keys`. Both use the shared
   `keyedRowTable` (`keyed_rows.go`), which the remaining map-shaped stores
   can adopt the same way.
