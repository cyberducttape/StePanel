# StePanel Troubleshooting Guide

## Startup Issues

### "invalid configuration: production requires..."

**Symptoms:** Service fails to start with configuration error in production mode.

**Causes:** Missing or invalid production settings.

**Solution:**
```bash
# Check production mode
echo "STEPANEL_ENV=${STEPANEL_ENV}"

# Required in production:
# - STEPANEL_ADMIN_TOTP_SECRET (TOTP MFA)
# - STEPANEL_AUDIT_KEY (audit logging)
# - STEPANEL_SESSION_SECRET (session signing)
# - STEPANEL_ENVIRONMENT_KEY (encryption)
# - STEPANEL_BACKUP_SIGNING_KEY (backup verification)
# - STEPANEL_ACCOUNT_KEY (customer encryption)
# - STEPANEL_REQUIRE_OFFSITE_BACKUP=1 (offsite requirement)
# - STEPANEL_OFFSITE_TARGET (offsite destination)

# Generate required secrets (use password manager!)
openssl rand -hex 32  # 32 random bytes
```

---

### "cannot inspect executable: permission denied"

**Symptoms:** Error loading root-owned helpers (stepanel-appctl, stepanel-vhostctl, etc.)

**Cause:** Service account lacks read permissions on helper binaries.

**Solution:**
```bash
# Check permissions (should be root-owned, executable)
ls -la /usr/local/sbin/stepanel-*

# Fix permissions
sudo chown root:root /usr/local/sbin/stepanel-*
sudo chmod 0755 /usr/local/sbin/stepanel-*
```

---

### "acquire process lock: resource temporarily unavailable"

**Symptoms:** StePanel can't start, another instance may be running.

**Cause:** Process lock file exists from previous crash or concurrent run.

**Solution:**
```bash
# Check for running processes
pgrep -af stepanel

# If none running, remove stale lock
rm -f /var/lib/ste-panel/jobs.json.lock
rm -f /var/lib/ste-panel/jobs.json.lock.worker

# Try starting again
sudo systemctl start stepanel
```

---

## Database Issues

### "database helper returned invalid inventory"

**Symptoms:** Dashboard shows "database unavailable" despite MySQL/PostgreSQL running.

**Cause:** Database helper (stepanel-dbctl) output format unexpected.

**Solution:**
```bash
# Test database helper directly
/usr/local/sbin/stepanel-dbctl inventory

# Check database is running
sudo systemctl status mysql  # or mariadb, postgresql

# Check helper has permission to run
ls -la /usr/local/sbin/stepanel-dbctl

# Verify database credentials
echo "SELECT 1" | mysql -u root -p$password
```

---

### "local database lifecycle helper is unavailable"

**Symptoms:** Cannot perform database operations (create, drop, backup).

**Cause:** STEPANEL_DBCTL not set or helper binary missing.

**Solution:**
```bash
# Check if set
echo "STEPANEL_DBCTL=${STEPANEL_DBCTL}"

# Should be: /usr/local/sbin/stepanel-dbctl
# If missing, run installer again:
sudo STEPANEL_ADMIN_PASSWORD=... STEPANEL_PANEL_HOSTNAME=... ./install.sh
```

---

### "Database diagnostics require a healthy local engine..."

**Symptoms:** Dashboard health check shows red, logs show database diagnostics error.

**Cause:** Database server unreachable or not running.

**Solution:**
```bash
# Check if database is running
sudo systemctl status mysql

# Check if accessible
mysql -u root -e "SELECT 1" || \
postgresql -u postgres -c "SELECT 1"

# Check StePanel can connect
/usr/local/sbin/stepanel-dbctl diagnostics

# Restart database
sudo systemctl restart mysql
```

---

## Job/Restore Issues

### "too many long-running jobs or target is already active"

**Symptoms:** Restore/backup fails to start, get "job busy" error.

**Cause:** Hit concurrent job limit or site already has active restore.

**Solution:**
```bash
# Check active jobs
curl http://localhost:8090/api/jobs

# Check how many are running
curl http://localhost:8090/api/jobs | jq '.jobs[] | select(.status=="running")' | wc -l

# Check concurrent job limit
echo "STEPANEL_MAX_CONCURRENT_JOBS=${STEPANEL_MAX_CONCURRENT_JOBS:-2}"

# To increase limit (if you have resources):
export STEPANEL_MAX_CONCURRENT_JOBS=4
sudo systemctl restart stepanel

# Check if specific site is locked
curl http://localhost:8090/api/sites | jq '.sites[] | select(.name=="example.com")'
```

---

### "durable job actor no longer owns the site"

**Symptoms:** Restore job fails mid-operation with authorization error.

**Cause:** Customer account suspended or site reassigned during job.

**Solution:**
```bash
# Check customer account status
curl http://localhost:8090/api/accounts | jq '.accounts[] | select(.username=="customer")'

# If suspended, check why
curl http://localhost:8090/api/accounts/customer | jq '.suspended'

# Check site ownership
curl http://localhost:8090/api/sites | jq '.sites[] | select(.name=="example.com") | .owner'

# Reassign site back to customer if needed
curl -X PATCH http://localhost:8090/api/sites/example.com \
  -H "Content-Type: application/json" \
  -d '{"owner":"customer"}'
```

---

## Backup Issues

### "backup archive verification failed"

**Symptoms:** Backup shows in listing but "verified" is false.

**Cause:** Archive checksum doesn't match, backup corrupted.

**Solution:**
```bash
# Verify backup manually
/opt/stepanel/stepanel verify-backup /var/lib/ste-panel/backups/example.com/2026-09-17-120000

# Check backup filesystem health
df -h /var/lib/ste-panel/backups

# Try recovery if backup is intact
/opt/stepanel/stepanel restore-control-plane /var/backups/backup.db --dry-run

# Delete corrupted backup (if needed to free space)
rm -rf /var/lib/ste-panel/backups/example.com/2026-09-17-120000
```

---

### "backup exceeds the 20 GiB upload limit"

**Symptoms:** Restore fails when importing cpmove backup.

**Cause:** Archive too large.

**Solution:**
```bash
# Check archive size
ls -lh /path/to/archive.tar.gz

# Split large archive (if possible)
tar -czf archive-split-01.tar.gz --exclude='path/to/large/dir' /path/to/archive

# Or increase limit (if you have disk space)
export STEPANEL_MAX_UPLOAD_BYTES=$((30 * 1024 * 1024 * 1024))  # 30GB
sudo systemctl restart stepanel
```

---

## Authentication Issues

### "session cookie invalid or expired"

**Symptoms:** Getting logged out frequently or immediately after login.

**Cause:** Session secret changed, session database corrupted, or clock skew.

**Solution:**
```bash
# Check session state
ls -la /var/lib/ste-panel/sessions.json

# Verify permissions
stat /var/lib/ste-panel/sessions.json | grep Uid

# Check disk space
df -h /var/lib/ste-panel/

# Verify time sync (important for TOTP!)
timedatectl status

# If time is wrong:
sudo timedatectl set-ntp true
sudo systemctl restart stepanel
```

---

### "login rate limit exceeded"

**Symptoms:** Getting "too many attempts" error after failed login.

**Cause:** Exceeded 5 failed login attempts in 15 minutes.

**Solution:**
```bash
# Wait 15 minutes for lock to reset automatically

# Or force reset by restarting service (clears in-memory limiter):
sudo systemctl restart stepanel

# Check login attempt history in audit log
/opt/stepanel/stepanel verify-audit /var/lib/ste-panel/audit.jsonl | \
  grep "login_attempt"
```

---

### "TOTP code invalid or expired"

**Symptoms:** Can't log in even with correct password and TOTP code.

**Cause:** 
- Clock skew between server and TOTP device
- Using old TOTP codes
- Server time incorrect

**Solution:**
```bash
# Verify server time
date
timedatectl status

# Sync time if needed
sudo timedatectl set-ntp true

# TOTP codes are valid for ~30 seconds
# Make sure your device clock is synced

# Check TOTP replay protection
curl http://localhost:8090/readyz | jq '.checks.session_state'

# Reset TOTP if locked out (admin-only)
# Set STEPANEL_ADMIN_TOTP_SECRET="" temporarily to disable MFA
# Then re-enable with new secret
```

---

## API Issues

### "401 Unauthorized"

**Symptoms:** API calls rejected with 401.

**Cause:** Missing or invalid authentication.

**Solution:**
```bash
# Check if session valid (browser auth):
curl -b "session=your-session-cookie" http://localhost:8090/api/sites

# Check if API token valid (for headless API):
curl -H "Authorization: Bearer your-api-token" http://localhost:8090/api/sites

# Generate new API token
curl -X POST http://localhost:8090/api/tokens \
  -H "Content-Type: application/json" \
  -d '{"name":"my-token"}'
```

---

### "403 Forbidden"

**Symptoms:** Valid auth but operation not allowed.

**Cause:** User doesn't have required permissions.

**Solution:**
```bash
# Check user role (admin vs. customer)
curl http://localhost:8090/api/health | jq '.user'

# Check API token scopes (future feature)
curl http://localhost:8090/api/tokens | jq '.tokens[] | select(.name=="my-token")'

# Admin-only operations need admin user
# Customer-only operations need customer account
```

---

### "CSRF token invalid"

**Symptoms:** Form submission fails with CSRF error.

**Cause:** CSRF token missing, expired, or form target mismatch.

**Solution:**
```bash
# Ensure you include CSRF token from form:
<input type="hidden" name="csrf" value="...">

# Token is provided by GET request
# Must match the form method and origin

# Clear cookies and retry if stuck
# Cookies must have SameSite=Strict enforcement
```

---

## Monitoring & Debugging

### Check Service Health

```bash
# Liveness probe (always healthy)
curl http://localhost:8090/livez

# Readiness probe (detailed health)
curl http://localhost:8090/readyz | jq .

# Health endpoint
curl http://localhost:8090/api/health | jq .
```

### View Metrics

```bash
# Prometheus metrics
curl http://localhost:8090/metrics | grep stepanel_

# Custom filter for specific metric
curl http://localhost:8090/metrics | grep "stepanel_restore_jobs"
```

### Review Audit Log

```bash
# Verify audit log integrity
/opt/stepanel/stepanel verify-audit /var/lib/ste-panel/audit.jsonl

# Parse recent events
tail -100 /var/lib/ste-panel/audit.jsonl | jq .

# Filter by event type
grep '"event":"login_success"' /var/lib/ste-panel/audit.jsonl | jq .
```

### Check System Logs

```bash
# StePanel logs
sudo journalctl -u stepanel -n 100 --no-pager

# Worker logs
sudo journalctl -u stepanel-worker -n 100 --no-pager

# Follow logs in real-time
sudo journalctl -u stepanel -f

# Filter by severity
sudo journalctl -u stepanel -p err..alert
```

### Performance Diagnosis

```bash
# Check goroutine count
curl http://localhost:8090/metrics | grep "runtime_goroutines"

# Check memory usage
ps aux | grep stepanel | grep -v grep

# Database query performance
/usr/local/sbin/stepanel-dbctl diagnostics | jq .

# Long-running transactions
curl http://localhost:8090/api/database/diagnostics | jq '.values.long_transactions'
```

---

## Emergency Procedures

### Service Won't Start

```bash
# 1. Check logs for errors
sudo journalctl -u stepanel -n 50

# 2. Verify configuration
/opt/stepanel/stepanel --help 2>&1 | grep -q usage && echo "Binary OK"

# 3. Check permissions
ls -la /opt/stepanel/stepanel
ls -la /var/lib/ste-panel/

# 4. Try manual startup with verbose output
/opt/stepanel/stepanel 2>&1 | head -20

# 5. If still fails, restore from backup
/opt/stepanel/stepanel restore-control-plane /var/backups/latest-backup --replace
```

### Disk Space Exhausted

```bash
# Check usage
df -h /var/lib/ste-panel/

# Find large files
du -sh /var/lib/ste-panel/*

# Clean up old imports (safe to delete)
find /var/lib/ste-panel/imports -mtime +7 -exec rm -rf {} \;

# Clean up old recovery transactions
find /var/www/sites/.stepanel-recovery -mtime +7 -exec rm -rf {} \;

# Monitor metrics
curl http://localhost:8090/metrics | grep "free_bytes"
```

### Lost Admin Password

```bash
# Generate new password hash
/opt/stepanel/stepanel hash-password

# Set in configuration
export STEPANEL_ADMIN_PASSWORD_HASH="bcrypt-hash-here"
sudo systemctl restart stepanel

# Now log in with new password
```

---

## Uploads and Large Imports

### Upload Fails at 5-10 minutes (Large cpmove/WordPress imports)

**Symptoms:** Upload of 10+ GB cpmove or WordPress backup fails with timeout error partway through.

**Cause:** Reverse proxy (Apache, Nginx, Caddy) has shorter timeout than StePanel. StePanel supports 30-minute uploads, but Apache defaults to 300 seconds (5 minutes).

**Solution:**

For Apache (Debian/Ubuntu):
```bash
# Edit /etc/apache2/stepanel-panel/stepanel.conf
# Ensure proxy timeout is set to at least 1800 seconds:

ProxyPass / http://127.0.0.1:8090/ timeout=1800
ProxyPassReverse / http://127.0.0.1:8090/
ProxyTimeout 1800
```

For Apache (RHEL/CentOS):
```bash
# Edit /etc/httpd/stepanel-panel/stepanel.conf
# Same configuration as above
```

For Nginx:
```nginx
# In the upstream or location block:
proxy_read_timeout 1800s;
proxy_connect_timeout 1800s;
proxy_send_timeout 1800s;
```

For Caddy:
```caddy
# In the reverse_proxy block:
reverse_proxy 127.0.0.1:8090 {
    timeout 30m
    flush_interval -1
}
```

Then reload:
```bash
# Apache
sudo systemctl reload apache2  # or httpd

# Nginx
sudo systemctl reload nginx

# Caddy
sudo systemctl reload caddy
```

**Note:** 20 GB at typical bandwidth (50-100 Mbps) requires 2500-5000 seconds, so 30-minute timeout is recommended.

---

## Getting Help

1. **Check this guide** - Most issues are covered above
2. **Review logs** - `journalctl -u stepanel` provides most diagnostic info
3. **Run health checks** - `/readyz` endpoint shows what's failing
4. **Check documentation** - See [OPERATIONS.md](OPERATIONS.md) for runbooks
5. **Report issue** - GitHub: https://github.com/itchyitchy123/StePanel/issues

---

**Last Updated:** September 2026  
**Version:** v0.7.0
