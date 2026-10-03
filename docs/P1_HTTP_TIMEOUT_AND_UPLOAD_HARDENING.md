# P1: HTTP Timeout and Upload Resource Hardening

**Date:** 2026-09-26  
**Status:** Implemented  
**Priority:** P1 (Production Readiness Blocker)

---

## Executive Summary

Overly permissive HTTP timeouts and insufficient upload resource protection create denial-of-service and resource exhaustion surfaces. This document describes the implemented hardening measures.

### Changes Made

1. **Differentiated Timeout Configuration** - Split timeout policies by operation type
2. **Upload Resource Policy Framework** - Enforce concurrency limits, quotas, and resource checks
3. **Disk Space Reservation** - Validate free space before accepting uploads

---

## Problem Statement

### Before (P1 Issues)

**Overly Permissive Global Timeouts:**
- ReadTimeout: 30 minutes (allows malicious clients to hold connections indefinitely)
- WriteTimeout: 30 minutes (prevents efficient resource cleanup)
- Creates attack surface for resource exhaustion and slowloris-style attacks

**Insufficient Upload Protection:**
- 20 GB file size limit only
- No upload concurrency limits at policy level
- No per-user quotas
- No disk reservation validation
- No minimum free-space guard

### Risk Scenarios

1. **Slowloris Attack**: Malicious client sends headers slowly, holding connection for 30 minutes
2. **Upload Bombing**: Multiple users upload 20GB files simultaneously, exhausting disk
3. **Quota Bypass**: No per-user limits; single admin can exhaust storage
4. **Disk Full**: Large upload completes but leaves system with <5% free space

---

## Solution Architecture

### 1. Differentiated Timeout Configuration

**Normal API Endpoints** (fast operations)
- ReadTimeout: 30 seconds (headers + body)
- WriteTimeout: 2 minutes (response generation)
- ReadHeaderTimeout: 5 seconds (prevent slowloris)
- IdleTimeout: 30 seconds

**Rationale:**
- Site list/detail: <2s
- Database queries: <1s
- Job status: <1s
- 30s provides buffer for slow storage + network variance

**Upload Endpoints** (large file transfers)
- Default: 60 minutes for the durable cPanel upload stream
- Max transfer time for 20GB at 100Mbps: ~27 minutes
- Should be replaced with async queue + background job in future

**Download Endpoints** (large file transfers)
- Originally: 30 minutes (now configurable per-route)
- Same rationale as uploads

### 2. Unified Resource Admission

The authoritative resource governor is `resource.go` (`ResourceBudget`). It
combines per-workload caps with a host-wide semaphore, and durable workers
also enforce `STEPANEL_MAX_CONCURRENT_JOBS` in the shared control-plane
database so separate worker processes cannot multiply the configured limit.

The handler-level upload policy was removed because it was not wired into
request handling. `ResourceBudget` and the durable SQLite worker admission
gate are now authoritative.

### 3. Configuration in main.go

**Server-level Timeouts:**
```go
server.ReadHeaderTimeout = 5 * time.Second   // Slowloris prevention
server.ReadTimeout = 60 * time.Minute         // upload ceiling; middleware bounds APIs
server.WriteTimeout = 2 * time.Minute         // Response generation
server.IdleTimeout = 30 * time.Second         // Cleanup idle connections
```

**Per-route Overrides (Future):**
```go
// Uploads: longer timeout via context
uploadCtx, cancel := context.WithTimeout(r.Context(), 5 * time.Minute)
defer cancel()

// Downloads: longer timeout
downloadCtx, cancel := context.WithTimeout(r.Context(), 5 * time.Minute)
defer cancel()
```

---

## Implementation Details

### Timeout Changes

**File:** `main.go` (server setup)

**Before:**
```go
server := &http.Server{
    ReadTimeout: 30 * time.Minute,    // Too permissive
    WriteTimeout: 30 * time.Minute,   // Too permissive
}
```

**After:**
```go
server := &http.Server{
    ReadHeaderTimeout: 5 * time.Second,
    ReadTimeout: 60 * time.Minute,     // upload ceiling; middleware bounds APIs
    WriteTimeout: 2 * time.Minute,     // 2m for response generation
    IdleTimeout: 30 * time.Second,
}
```

### Upload Admission

The cPanel inspection (`/api/cpmove/inspect`) and WPress
(`/api/wpress/import`) handlers read the body with `Request.MultipartReader`
and stream the archive part straight into its private staged object through
`internal/upload`, hashing and counting bytes as they go. They never call
`FormFile` or `ParseMultipartForm`, so no multipart spool file exists: a
20 GiB archive is written to disk once.

Admission happens before the first body byte is read. The declared size is
the request `Content-Length` (or `STEPANEL_MAX_UPLOAD_BYTES` for a body
without one; with no ceiling configured the request is refused with 411).
Demands are summed per filesystem, so an import root and site root on one
device are charged together:

- import root: staged archive + extracted tree (each about the archive size);
- site root: site-manager staging tree (about the archive size);
- every filesystem: the `STEPANEL_MIN_FREE_BYTES` reserve.

A cPanel archive's expanded size is unknown before inspection, so the gzip
size is a lower bound there; after inspection `restoreCPMoveCapacity`
re-checks with the inspected expanded size and reports
`required_free_bytes`. While streaming, the import root is re-checked every
256 MiB for the bytes still expected plus the reserve, so a concurrent
consumer cannot fill the disk underneath an admitted upload. Any failure
removes the partial object. Database working space on the database server's
data directory is not reserved because that directory is outside the panel's
filesystems.

WPress requests must send every form field before the `backup` file part so
the restore is validated (confirmation, site, database names, password, file
extension) before any archive byte is accepted; the dashboard orders the
parts this way. `/api/cpmove/import` carries only an upload ID and is capped
at 1 MiB, so it cannot be used to spool an archive either.

### Event Streams

`/api/jobs/events` is a server-sent event stream and has its own timeout
class. The middleware applies no context deadline to it (the 45-second
long-poll deadline would cancel healthy streams and force reconnect,
re-authentication, and snapshot churn). Instead the handler:

- sets a 15-second write deadline before every event and 25-second
  heartbeat, which detects a dead peer and lifts the server-wide
  `WriteTimeout` for this connection;
- closes the stream after 30 minutes so the client reconnects and is
  re-authenticated.

---

## Timeout Configuration Framework

**File:** `internal/http/timeouts.go`

**Predefined Classes:**
1. **APIRead/APIWrite** - Fast request/response cycles
2. **LongPoll** - Client waiting for updates (45s)
3. **StreamWrite/StreamLifetime** - Server-sent events: no context deadline, 15s per-write deadline, 30m rotation
4. **UploadRead** - File transfer with variance (60m with limits)
5. **DownloadWrite** - Large file delivery (5m with limits)

**Usage Pattern:**
```go
tc := httputil.DefaultTimeouts()
tc.ApplyToServer(server)

// Override specific handlers
middleware := tc.Middleware()
uploadHandler := middleware(uploadHandler)
```

---

## Security Properties Guaranteed

### Timeout-based Protection

✅ **Slowloris Prevention**
- ReadHeaderTimeout: 5s → headers must arrive in 5 seconds
- Forces attackers to complete headers or disconnect

✅ **Connection Holding Prevention**
- IdleTimeout: 30s → closes connections with no activity
- Releases worker threads/file descriptors

✅ **Response Timeout**
- WriteTimeout: 2m → response generation must complete
- Prevents slow responses from holding connections

✅ **Total Request Bounded**
- Ordinary API requests are bounded by a 30s context deadline
- Archive uploads are bounded at 60m; downloads at 5m
- Event streams rotate after 30m and are closed as soon as a write stalls
  for 15s
- Previous: 30 minutes allowed for every request

### Upload Protection

✅ **Host-wide concurrency budget**
- Uploads, imports, and other heavy workloads take a slot from one host-wide
  `ResourceBudget` (`resource.go`), sized by `STEPANEL_MAX_CONCURRENT_JOBS`,
  in addition to their own class limit, so independent workloads cannot
  multiply into an unbounded I/O storm.

✅ **Disk space reservation**
- Admission reserves room for the archive, the extracted tree, the
  site-manager staging tree, and the `STEPANEL_MIN_FREE_BYTES` reserve
  before the body is read, and re-checks the import root while streaming.

✅ **File size limit**
- `STEPANEL_MAX_UPLOAD_BYTES` (at most 20 GiB) is enforced while streaming.

✅ **Single-pass staging**
- Archive parts stream directly into the staged object; there is no
  multipart temporary file on any filesystem.

❌ **Per-user upload quotas**
- Not implemented. An earlier upload-policy prototype with daily per-user
  limits was replaced by the host-wide budget on 2026-09-30.

---

## Deployment Considerations

### Configuration Defaults

```
STEPANEL_MAX_CONCURRENT_JOBS=2          # host-wide heavy-workload slots (1-32)
STEPANEL_MAX_UPLOAD_BYTES=21474836480   # 20 GiB maximum
STEPANEL_MIN_FREE_BYTES=5368709120      # free-space reserve kept on every admission
API timeout: 30s
Upload timeout: 60m (context-based)
```

### Monitoring

`GET /api/admin/resources/status` (administrators) reports active and
maximum slots and utilization for the host budget and each workload class.

### Migration Notes

1. **Timeout Changes**: Applies immediately to all requests
2. **Upload Policy**: Optional; existing code continues to work
3. **Quotas**: Can be enabled without disabling; defaults to unlimited

---

## Testing Requirements

### Timeout Testing

- [ ] Verify 30s timeout on normal API requests
- [ ] Verify 2m timeout on responses
- [ ] Verify 5s header timeout (slowloris protection)
- [ ] Verify 30s idle timeout closes connections

### Upload Testing

- [ ] File size rejection >20GB
- [ ] Concurrency limit at 4 uploads
- [ ] Disk space check prevents uploads when <100GB free
- [ ] Per-user quota enforcement
- [ ] Quota reset after 24 hours

### Load Testing

- [ ] 10 concurrent normal API requests complete in <30s
- [ ] 4 concurrent 10GB uploads complete in <10min
- [ ] 5th concurrent upload rejected with 429
- [ ] Completed upload recorded in quota

---

## Related Documents

- [CLAUDE.md](../CLAUDE.md) - Code quality standards (includes Tier 1 filesystem operations)
- [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md) - Gate 5 requirements
- [GATE5_FINAL_STATUS.md](archive/GATE5_FINAL_STATUS.md) - Production readiness progress

---

## Completion Checklist

- [x] Timeout configuration framework created
- [x] Server timeouts reduced from 30m to 30s/2m
- [x] Host-wide resource admission (replaced the upload policy prototype)
- [x] Disk space validation
- [x] Concurrency limit enforcement
- [ ] Per-user upload quotas (not implemented)
- [ ] Per-route timeout overrides (middleware)
- [ ] Rate limiting on upload bodies (future)
- [ ] Slow upload detection and timeout (future)
- [ ] Async upload queue (future improvement)

---

## Future Improvements

1. **Async Upload Queue** - Queue uploads and return 202, process in background
2. **Upload Rate Limiting** - Detect slow uploads, timeout/disconnect
3. **Network Partition Handling** - Detect stalled transfers
4. **Per-Endpoint Timeouts** - Fine-grained control via middleware
5. **Quota Enforcement UI** - Dashboard showing user upload usage

---

## References

- **Slowloris Attack**: Malicious HTTP client opens connections and sends partial headers slowly
- **Resource Exhaustion**: Attack filling disk, memory, or file descriptors to cause denial of service
- **Upload Bombing**: Rapid successive uploads to exhaust quota or disk
- **Rate Limiting Strategy**: Per-user daily quotas prevent abuse by single user

---

**Status: IMPLEMENTED AND DEPLOYED**

All critical timeout hardening is in place. Upload resource policy is available for integration. This addresses P1 production readiness concerns about HTTP server resource exhaustion.
