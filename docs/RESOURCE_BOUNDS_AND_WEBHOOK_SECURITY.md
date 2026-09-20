# Resource Bounds, Webhook Replay, and Build Reproducibility (MEDIUM)

## Issue 1: Resource Ceilings Are Absurdly High

**Problem**: Resource validation bounds don't reflect shared hosting reality.

**Current code** (stepanel-appctl:249-251):
```bash
CPU:       up to 6400%
Memory:    up to 1,048,576 MB (1 TB)
TasksMax:  up to 100,000
```

**Why this matters**: These are *validation bounds* (syntactically acceptable), not *hosting defaults*. On shared hosting, one misbehaving site should never be allowed to request 1TB of RAM or spawn 100,000 tasks.

**Risk**: Validation bounds become operational defaults when customers request the max. One customer misconfiguration knocks the entire hosting node offline.

---

## Solution: Separate Validation from Hosting Policy

### Distinction
```go
// Validation bounds (what the API accepts syntactically)
const (
    MaxValidCPU        = 10000    // Reject >10000% as obviously wrong input
    MaxValidMemory     = 1 << 40  // Reject >1TB as obviously wrong input
    MaxValidTasksMax   = 1000000  // Reject >1M as obviously wrong input
)

// Hosting policy defaults (what customers actually get on shared hosting)
const (
    DefaultCPU         = 200      // 2 full cores
    DefaultMemory      = 512 * MB // 512 MB
    DefaultTasksMax    = 100      // 100 concurrent tasks
    MaxPerSiteMemory   = 4096 * MB // 4 GB max per site
    MaxPerSiteCPU      = 400      // 4 full cores max per site
)
```

### Implementation
1. **Validation layer**: Accept up to MaxValid* bounds
2. **Hosting policy layer**: 
   - Apply defaults (DefaultCPU, DefaultMemory, DefaultTasksMax)
   - Apply caps (MaxPerSiteCPU, MaxPerSiteMemory)
   - Reject values between default and cap as invalid
3. **Customer override**: Allow plan-based overrides (via plan tier)

### Example Flow
```
Customer requests: CPU=1000, Memory=10GB

Validation:
├─ CPU 1000 < MaxValidCPU 10000 ✓
├─ Memory 10GB < MaxValidMemory 1TB ✓
└─ Passed validation

Hosting policy:
├─ MaxPerSiteCPU = 400 (from plan)
├─ Requested CPU 1000 > MaxPerSiteCPU 400 ✗
└─ Rejected: "CPU limited to 400% on your plan"
```

---

## Issue 2: Git Webhook Replay Protection Missing

**Problem**: HMAC signature proves the secret holder signed it, but doesn't prove it's a new request.

**Current code** (git_deploy.go):
```go
func verifyWebhookSignature(r *http.Request, secret string) bool {
    signature := r.Header.Get("X-Hub-Signature-256")
    // Verifies signature matches secret + body
    // But doesn't verify timestamp or delivery ID
}
```

**What HMAC proves**: Somebody with the secret generated this request body.

**What HMAC doesn't prove**: This is a new request (not a replay).

**Threat**: Attacker captures a valid webhook delivery and replays it N times:
1. Webhook: "Deploy commit abc123 to production"
2. Attacker replays webhook
3. Commit abc123 gets deployed again (unwanted)
4. Attacker replays again
5. And again...

---

## Solution: Timestamp + Delivery ID Replay Protection

### Implementation

**Add to webhook signature verification**:
```go
func verifyWebhookSignature(r *http.Request, secret string) (bool, string, error) {
    // 1. Verify HMAC signature
    signature := r.Header.Get("X-Hub-Signature-256")
    if !verifyHMAC(signature, secret, r.Body) {
        return false, "", errors.New("invalid signature")
    }
    
    // 2. Verify timestamp is recent (< 5 minutes old)
    timestamp := r.Header.Get("X-Hub-Delivery-Timestamp")
    ts, _ := time.Parse(time.RFC3339, timestamp)
    if time.Since(ts) > 5*time.Minute {
        return false, "", errors.New("timestamp too old (stale replay)")
    }
    
    // 3. Verify delivery ID hasn't been seen before
    deliveryID := r.Header.Get("X-Hub-Delivery-ID")
    if replayCache.Contains(deliveryID) {
        return false, "", errors.New("duplicate delivery (replay detected)")
    }
    replayCache.Store(deliveryID, time.Now())
    
    return true, deliveryID, nil
}
```

**Replay cache management**:
```go
type ReplayCache struct {
    mu      sync.RWMutex
    cache   map[string]time.Time
    maxAge  time.Duration
}

func (rc *ReplayCache) Contains(deliveryID string) bool {
    rc.mu.RLock()
    defer rc.mu.RUnlock()
    
    storedAt, exists := rc.cache[deliveryID]
    if !exists {
        return false
    }
    
    // Clean up old entries
    if time.Since(storedAt) > rc.maxAge {
        delete(rc.cache, deliveryID)
        return false
    }
    
    return true
}

func (rc *ReplayCache) Store(deliveryID string, at time.Time) {
    rc.mu.Lock()
    defer rc.mu.Unlock()
    rc.cache[deliveryID] = at
    
    // Bounded cache size (prune if >10K entries)
    if len(rc.cache) > 10000 {
        // Remove oldest entries
        for id, ts := range rc.cache {
            if time.Since(ts) > rc.maxAge {
                delete(rc.cache, id)
            }
        }
    }
}
```

**Expected webhook headers** (GitHub example):
- `X-Hub-Signature-256`: `sha256=abc123...`
- `X-Hub-Delivery-ID`: UUID (unique per delivery)
- `X-Hub-Delivery-Timestamp`: RFC3339 timestamp

---

## Issue 3: Git Clone Errors Leak Raw Command Output

**Problem**: stderr from `git clone` is returned directly to API clients.

**Current code** (git_deploy.go:174-176):
```go
http.Error(w,
    "Git checkout failed: "+strings.TrimSpace(string(cloneOutput)),
    http.StatusBadGateway)
```

**What leaks**:
- Filesystem paths (`/var/www/sites/customer-name/repo`)
- Host details (kernel version, container names)
- Internal configuration (private registry URLs, SSH keys in error messages)
- Repository structure (subdirectories, hidden branches)
- Transport details (SSH timeouts, network errors)

**Risk**: Attacker learns infrastructure details from error messages.

---

## Solution: Sanitized Error Messages + Protected Logs

### API Response (Customer-Facing)
```go
func handleGitClone(w http.ResponseWriter, r *http.Request) {
    // Attempt clone...
    cmd := exec.CommandContext(ctx, "git", "clone", ...)
    output, err := cmd.CombinedOutput()
    
    if err != nil {
        // Log FULL output to protected operational logs
        logger.Error("git clone failed",
            "error", err,
            "output", string(output),
            "site", siteName,
            "repo", repoURL)
        
        // Return SANITIZED error to customer
        http.Error(w,
            "Git clone failed. Please check your repository URL and credentials.",
            http.StatusBadGateway)
        return
    }
}
```

### What Customer Sees
```
HTTP/502 Bad Gateway
Git clone failed. Please check your repository URL and credentials.
```

### What Operational Logs Contain
```json
{
  "level": "error",
  "message": "git clone failed",
  "site": "customer-a.com",
  "repo": "git@github.com:customer-a/private-repo.git",
  "error": "exit status 128",
  "output": "fatal: could not read Username for 'https://github.com': terminal prompts disabled"
}
```

### Error Categorization
```go
type GitErrorCategory string

const (
    // Customer errors (safe to expose)
    InvalidRepository GitErrorCategory = "invalid_repo"    // "bad repo URL"
    InvalidCredentials GitErrorCategory = "bad_credentials" // "check your SSH key"
    
    // System errors (sanitize completely)
    NetworkError GitErrorCategory = "network_error"        // "temporary failure"
    FileSystemError GitErrorCategory = "fs_error"           // "internal error"
    PermissionError GitErrorCategory = "permission_error"   // "access denied"
)

func sanitizeGitError(output string) (msg string, category GitErrorCategory) {
    if strings.Contains(output, "not a git repository") {
        return "Invalid repository URL", InvalidRepository
    }
    if strings.Contains(output, "Permission denied") {
        return "Check your SSH credentials", InvalidCredentials
    }
    if strings.Contains(output, "timed out") {
        return "Repository temporarily unavailable", NetworkError
    }
    return "Git operation failed", NetworkError
}
```

---

## Issue 4: Docker Builds Not Reproducible

**Problem**: Dockerfile pins base image digest but not package versions, creating false sense of reproducibility.

**Current Dockerfile**:
```dockerfile
FROM debian:12@sha256:abc123...

RUN apt-get update && \
    apt-get upgrade -y && \
    apt-get install -y ca-certificates curl mariadb-client rclone
```

**Why this doesn't work**:
- Base image digest is pinned ✓
- But `apt-get upgrade -y` upgrades all packages to latest
- So exact package set depends on Debian mirror state on build day
- Same Dockerfile produces different images on different days
- Contradicts "provenance" and "immutable references" claims

**Risk**: Security audits trust SBOMs that don't match actual deployed images.

---

## Solution: Full Package Pinning

### Option 1: apt-get with pinned versions (Simple)
```dockerfile
FROM debian:12@sha256:abc123...

RUN apt-get update && \
    apt-get install -y \
        ca-certificates=20240110 \
        curl=7.88.1-14+0~deb12u1 \
        mariadb-client=1:10.11.7-1 \
        rclone=1.64.2-1
```

**Pro**: Simple, explicit versions  
**Con**: Manual maintenance when updating

### Option 2: Lock file (Better)
```dockerfile
FROM debian:12@sha256:abc123...

COPY apt-pinning.txt /tmp/
RUN apt-get update && \
    apt-get install -y $(cat /tmp/apt-pinning.txt)
```

Where `apt-pinning.txt` is generated and committed:
```
ca-certificates=20240110
curl=7.88.1-14+0~deb12u1
mariadb-client=1:10.11.7-1
rclone=1.64.2-1
```

**Pro**: Versions tracked in git, easy to diff updates  
**Con**: Requires lock file generation tooling

### Option 3: Full Debian snapshot (Strongest)
```dockerfile
FROM debian:12@sha256:abc123...

RUN echo "deb [snapshot=20240101T000000Z] http://snapshot.debian.org/debian bullseye main" \
    > /etc/apt/sources.list && \
    apt-get update && \
    apt-get install -y ca-certificates curl mariadb-client rclone
```

**Pro**: Guarantees exact packages from specific date  
**Con**: Requires Debian snapshot infrastructure access

---

## Implementation Guidance

### Priority Order
1. **Webhook replay protection** (2-3 days) — Prevents deployment loops
2. **Git error sanitization** (1-2 days) — Prevents information leakage
3. **Resource bounds separation** (3-5 days) — Prevents resource exhaustion
4. **Docker reproducibility** (3-5 days) — Aligns with provenance claims

### Quick Wins
- Error sanitization: Replace direct stderr with categorized messages
- Replay cache: Add timestamp + delivery ID check to webhook handler
- Resource bounds: Document hosting policy separately from validation bounds

### Longer-term
- Implement per-plan resource caps
- Generate and track package lock files
- Audit all external process error handling

---

## Related Issues

- `git_deploy.go:174-176` - Raw error output
- `stepanel-appctl:249-251` - Resource validation bounds
- `Dockerfile` - Package pinning
- `docs/ROOT_HELPER_MODERNIZATION.md` - Overall helper hardening
