# Upgrading from v0.6.0 to v0.7.0

> **Version:** v0.7.0  
> **Release Date:** September 2026  
> **Compatibility:** v0.6.0 → v0.7.0 upgrade tested

## What's New

### Features
- Shared-hosting beta with per-customer TOTP MFA
- Site environment variable encryption support
- Redis allocation management
- Advanced Git release management
- Python and PHP runtime management
- Malware quarantine and scanning
- Multi-cloud provider integrations (Linode, AWS, OpenStack)

### Bug Fixes
- **Critical:** Fixed silent error ignoring in Git release configuration parsing (see below)

### Performance
- Optimized database diagnostics collection
- Improved job queue efficiency
- Better concurrent job handling

## Breaking Changes

### Configuration Parsing (CRITICAL)
**What changed:** Git release environment variables now properly validate during load, not just during startup checks.

**Affected variables:**
- `STEPANEL_GIT_RELEASE_RETENTION`
- `STEPANEL_GIT_RELEASE_MAX_AGE_HOURS`  
- `STEPANEL_GIT_RELEASE_MAX_BYTES`

**Impact:** 
- Previously: Invalid values silently became 0
- Now: Invalid values are rejected during ValidateConfig()

**Action required:** Verify these environment variables are set to valid integers if you use them.

```bash
# Check current values
echo "STEPANEL_GIT_RELEASE_RETENTION=${STEPANEL_GIT_RELEASE_RETENTION}"
echo "STEPANEL_GIT_RELEASE_MAX_AGE_HOURS=${STEPANEL_GIT_RELEASE_MAX_AGE_HOURS}"
echo "STEPANEL_GIT_RELEASE_MAX_BYTES=${STEPANEL_GIT_RELEASE_MAX_BYTES}"

# Valid examples:
export STEPANEL_GIT_RELEASE_RETENTION=3
export STEPANEL_GIT_RELEASE_MAX_AGE_HOURS=168
export STEPANEL_GIT_RELEASE_MAX_BYTES=5368709120  # 5GB
```

## Upgrade Steps

### 1. Pre-Upgrade Checklist

- [ ] Back up `STEPANEL_CONTROL_PLANE_DB` (control-plane.db)
- [ ] Back up `STEPANEL_JOB_STATE` (jobs.json)
- [ ] Back up site data
- [ ] Verify health check passing: `curl http://localhost:8090/readyz`
- [ ] No active restore/backup jobs running
- [ ] Review these release notes

### 2. On Staging (Strongly Recommended)

```bash
# Extract v0.7.0 release
release=v0.7.0
arch=amd64  # or arm64
curl -fsSLO "https://github.com/itchyitchy123/StePanel/releases/download/${release}/stepanel_${release#v}_linux_${arch}.tar.gz"
curl -fsSLO "https://github.com/itchyitchy123/StePanel/releases/download/${release}/SHA256SUMS"

# Verify checksums
grep "stepanel_${release#v}_linux_${arch}.tar.gz" SHA256SUMS | sha256sum -c -

# Extract
tar -xzf "stepanel_${release#v}_linux_${arch}.tar.gz"

# Run on staging with copy of production data
mkdir -p staging-data/{imports,backups,www,mail,nvm,proxy,vhosts,apps,quarantine}

# Copy backups from production
cp /var/lib/ste-panel/backups staging-data/backups/

# Test startup
STEPANEL_ENV=production \
STEPANEL_LISTEN=127.0.0.1:28090 \
STEPANEL_CONTROL_PLANE_DB=staging-data/control-plane.db \
STEPANEL_JOB_STATE=staging-data/jobs.json \
./stepanel &

# Verify health
sleep 5
curl -s http://127.0.0.1:28090/readyz | jq .

# Test restore workflow (optional but recommended)
# Create test cpmove archive and verify restoration works

# Stop staging instance
pkill -f "stepanel.*28090"
```

### 3. Backup Control Plane State

```bash
# Create snapshot before upgrading
/opt/stepanel/stepanel backup-control-plane /var/backups/stepanel-v0.6.0-backup

# Verify backup
/opt/stepanel/stepanel restore-control-plane /var/backups/stepanel-v0.6.0-backup --dry-run
```

### 4. Update Binary on Production

```bash
# Stop the service
sudo systemctl stop stepanel
sudo systemctl stop stepanel-worker

# Verify stopped
sleep 2
pgrep -f stepanel && echo "ERROR: Process still running" || echo "OK: Stopped"

# Update binary
sudo cp /path/to/new/stepanel /opt/stepanel/stepanel
sudo chown root:root /opt/stepanel/stepanel
sudo chmod 0755 /opt/stepanel/stepanel

# Verify binary
/opt/stepanel/stepanel version
```

### 5. Verify Configuration

```bash
# Check for Git release configuration issues
if [ -z "$STEPANEL_GIT_RELEASE_RETENTION" ]; then
    echo "STEPANEL_GIT_RELEASE_RETENTION: using default (3)"
else
    echo "STEPANEL_GIT_RELEASE_RETENTION: $STEPANEL_GIT_RELEASE_RETENTION"
fi

if [ -z "$STEPANEL_GIT_RELEASE_MAX_AGE_HOURS" ]; then
    echo "STEPANEL_GIT_RELEASE_MAX_AGE_HOURS: using default (168)"
else
    echo "STEPANEL_GIT_RELEASE_MAX_AGE_HOURS: $STEPANEL_GIT_RELEASE_MAX_AGE_HOURS"
fi

# Validate all configuration
/opt/stepanel/stepanel --help 2>&1 | grep -q "usage" || {
    echo "Binary verification failed"
    exit 1
}
```

### 6. Start Services

```bash
# Start control plane
sudo systemctl start stepanel

# Wait for readiness
for i in {1..30}; do
    if curl -sf http://127.0.0.1:8090/readyz > /dev/null; then
        echo "Control plane ready"
        break
    fi
    echo "Waiting... ($i/30)"
    sleep 1
done

# Start worker
sudo systemctl start stepanel-worker

# Verify both running
sudo systemctl status stepanel stepanel-worker
```

### 7. Post-Upgrade Verification

```bash
# Check logs for errors
sudo journalctl -u stepanel -n 50 --no-pager

# Verify health endpoints
curl http://localhost:8080/livez
curl http://localhost:8080/readyz | jq .

# Test basic operations
curl -s http://localhost:8080/api/health | jq .

# List sites (should work)
curl -s -H "Cookie: session=..." http://localhost:8080/api/sites | jq .

# Check metrics
curl http://localhost:8080/metrics | head -20
```

## Rollback Procedure

If you encounter issues, rollback to v0.6.0:

```bash
# Stop services
sudo systemctl stop stepanel stepanel-worker

# Restore control plane state
/opt/stepanel/stepanel restore-control-plane /var/backups/stepanel-v0.6.0-backup --replace

# Revert binary
sudo cp /opt/stepanel/stepanel.v0.6.0 /opt/stepanel/stepanel

# Restart
sudo systemctl start stepanel stepanel-worker

# Verify
curl http://localhost:8080/readyz | jq .
```

## Testing Recommendations

### 1. Create Test Site
```bash
# Create a test site to verify basic operations
curl -X POST http://localhost:8080/api/sites \
  -H "Content-Type: application/json" \
  -d '{"name":"test-upgrade.example.com"}'

# Verify site created
curl http://localhost:8080/api/sites | jq '.sites[] | select(.name=="test-upgrade.example.com")'
```

### 2. Verify Backup Operations
```bash
# Create backup
curl -X POST http://localhost:8080/api/backups \
  -H "Content-Type: application/json" \
  -d '{"site":"test-upgrade.example.com"}'

# List backups
curl http://localhost:8080/api/backups?site=test-upgrade.example.com
```

### 3. Test Restore Workflow
```bash
# If using new shared-hosting features, verify customer account creation
curl -X POST http://localhost:8080/api/accounts \
  -H "Content-Type: application/json" \
  -d '{"username":"testcustomer","password":"temp-password"}'
```

## Known Limitations

- Shared-hosting beta: Still single-host only (no multi-host tenant distribution)
- Customer resource quotas: Enforcement available on supported local engines
- Bandwidth/mail enforcement: Remains operator responsibility

## Support

- **Issues:** Report at https://github.com/itchyitchy123/StePanel/issues
- **Documentation:** https://github.com/itchyitchy123/StePanel/tree/main/docs
- **Security:** Email security@stepanel.dev

## Compatibility Matrix

| Feature | v0.6.0 | v0.7.0 | Notes |
|---------|--------|--------|-------|
| cpmove imports | ✅ | ✅ | Compatible |
| WordPress restores | ✅ | ✅ | Compatible |
| Database management | ✅ | ✅ | Enhanced diagnostics |
| Site backups | ✅ | ✅ | Same format |
| Customer accounts | ❌ | ✅ | New feature |
| Environment variables | ✅ | ✅ | See config parsing fix |

---

**Questions?** See [TROUBLESHOOTING.md](TROUBLESHOOTING.md) or review the [OPERATIONS.md](OPERATIONS.md) runbook.
