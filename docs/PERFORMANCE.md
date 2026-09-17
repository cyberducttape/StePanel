# StePanel Performance Tuning Guide

## Performance Baselines

### Expected Performance

| Metric | Baseline | Threshold | Notes |
|--------|----------|-----------|-------|
| API Latency P50 | 50ms | < 100ms | Cached responses |
| API Latency P95 | 200ms | < 500ms | Most operations |
| API Latency P99 | 1s | < 2s | Heavy operations |
| Restore Throughput | 50-100 MB/s | - | Disk-dependent |
| Backup Throughput | 50-100 MB/s | - | Disk-dependent |
| Active Sessions | 100-1000 | - | Per 100GB disk |
| Concurrent Restores | 2-10 | - | Set via STEPANEL_MAX_CONCURRENT_JOBS |
| Error Rate | < 0.1% | < 0.5% | Monitor closely |
| Memory Usage | 100-500 MB | < 2 GB | Process + Go runtime |

---

## Configuration Tuning

### Concurrency Settings

```bash
# Default: 2 concurrent jobs (conservative)
# Suitable for: Single host, limited disk I/O

export STEPANEL_MAX_CONCURRENT_JOBS=2

# For larger servers (8+ cores, SSD):
export STEPANEL_MAX_CONCURRENT_JOBS=4

# For high-throughput (16+ cores, fast disks):
export STEPANEL_MAX_CONCURRENT_JOBS=8

# WARNING: Each job uses disk + CPU + memory
# Monitor resource usage when increasing
```

### Upload Limits

```bash
# Default: 20 GB per upload
export STEPANEL_MAX_UPLOAD_BYTES=$((20 * 1024 * 1024 * 1024))

# For large archives:
export STEPANEL_MAX_UPLOAD_BYTES=$((50 * 1024 * 1024 * 1024))  # 50GB

# WARNING: Increases staging disk usage
# Ensure STEPANEL_IMPORT_ROOT has adequate space
```

### Archive Entry Limits

```bash
# Default: 1 million entries max
export STEPANEL_MAX_ARCHIVE_ENTRIES=1000000

# For complex sites (many small files):
export STEPANEL_MAX_ARCHIVE_ENTRIES=5000000

# This controls memory usage during tar parsing
# Larger values = more memory needed
```

### Retention Settings

```bash
# Default: 168 hours (7 days) for staging retention
export STEPANEL_STAGE_RETENTION_HOURS=168

# For high-volume operations:
export STEPANEL_STAGE_RETENTION_HOURS=72  # 3 days

# For development/testing:
export STEPANEL_STAGE_RETENTION_HOURS=24  # 1 day
```

---

## Disk Optimization

### Filesystem Selection

**Best:** SSD with journaling
```bash
# ext4 with noatime (reduces metadata writes)
/dev/sda1 /var/lib/ste-panel ext4 defaults,noatime 0 2
```

**Good:** HDD with journaling  
```bash
# ext4 or xfs
/dev/sdb1 /var/lib/ste-panel ext4 defaults,noatime 0 2
```

**Avoid:** NFS, SMB, or other network filesystems (metadata overhead)

### Disk Layout

**Recommended:** Separate mount points

```bash
/var/lib/ste-panel/          # Control plane data
/var/lib/ste-panel/imports   # Upload staging (temp, high I/O)
/var/lib/ste-panel/backups   # Backup archives (large, sequential)
/var/www/sites               # Site data (frequent access)
```

**Setup:**
```bash
# Check usage
du -sh /var/lib/ste-panel/*
du -sh /var/www/sites/*

# Monitor growth
df -h /var/lib/ste-panel
df -h /var/www

# Set up alerts
df | awk '{if(NR>1 && $5+0 > 80) print "WARNING: " $6 " at " $5}'
```

### Minimum Free Space

```bash
# Default: 1 GB minimum free
export STEPANEL_MIN_FREE_BYTES=$((1 * 1024 * 1024 * 1024))

# For high-volume: 10 GB minimum
export STEPANEL_MIN_FREE_BYTES=$((10 * 1024 * 1024 * 1024))

# StePanel refuses operations when below threshold
# Prevents "disk full" corruption
```

### Cleanup Strategies

**Manual cleanup:**
```bash
# Remove old import staging (safe)
find /var/lib/ste-panel/imports -mtime +7 -exec rm -rf {} \;

# Remove old recovery transactions (safe)
find /var/www/sites/.stepanel-recovery -mtime +7 -exec rm -rf {} \;

# Archive old backups to external storage
rsync -av --remove-source-files /var/lib/ste-panel/backups/ s3://archive-bucket/
```

**Automated cleanup:**
```bash
# Add to crontab
0 2 * * * find /var/lib/ste-panel/imports -mtime +7 -exec rm -rf {} \;
0 3 * * 0 find /var/www/sites/.stepanel-recovery -mtime +30 -exec rm -rf {} \;
```

---

## Database Performance

### Connection Settings

**MySQL/MariaDB:**
```ini
# /etc/mysql/conf.d/stepanel.cnf
[mysqld]
max_connections = 100
max_user_connections = 50
connection_time_zone = UTC
```

**PostgreSQL:**
```ini
# /etc/postgresql/14/main/postgresql.conf
max_connections = 100
shared_buffers = 256MB
effective_cache_size = 1GB
```

### Query Performance

```bash
# Check slow queries
mysql -e "SELECT * FROM mysql.slow_log;" 

# Enable query log temporarily
/usr/local/sbin/stepanel-dbctl enable-query-log

# Check diagnostics
curl http://localhost:8090/api/database/diagnostics | jq '.values'
```

### Backup Performance

```bash
# Database dump optimization
# Use single-transaction for consistency (no locks)
export DUMP_OPTIONS="--single-transaction --quick --lock-tables=false"

# For large databases, increase timeout:
export LONG_QUERY_TIME=60

# Monitor dump progress
tail -f /var/log/mysql/slow.log
```

---

## Application Performance

### Memory Management

```bash
# Check memory usage
ps aux | grep stepanel | grep -v grep | awk '{print $6}'

# Monitor over time
watch -n 5 'ps aux | grep stepanel | grep -v grep | awk "{print \$6}"'

# If memory grows unbounded, may indicate:
# - Memory leak in Go code
# - Unbounded cache growth
# - Session table growth
```

### Goroutine Monitoring

```bash
# Check goroutine count
curl http://localhost:8090/metrics | grep runtime_goroutines

# Healthy baseline: 50-100 goroutines
# Growing baseline: May indicate goroutine leak
# Per-request: ~5-10 goroutines per active request
```

### HTTP Performance

```bash
# Request latency histogram
curl http://localhost:8090/metrics | grep stepanel_http_request_duration

# Error rate
curl http://localhost:8090/metrics | grep stepanel_http_errors_total

# Monitor dashboard
curl http://localhost:8090/api/health | jq '.request_rate'
```

---

## Backup Strategy

### Scheduling

**Recommended pattern:**
```bash
# Spread backups to avoid thundering herd
# Stagger by time of day based on site count

# 10 sites: Run every 6 hours
0 0,6,12,18 * * * /usr/local/bin/stepanel backup-site example.com

# 100 sites: Run every 24 hours, spread across day
# 0 1 * * * # Site 1
# 5 1 * * * # Site 2
# 10 1 * * * # Site 3
```

**Configuration:**
```bash
export STEPANEL_STAGE_RETENTION_HOURS=168  # 7-day staging retention
export STEPANEL_GIT_RELEASE_RETENTION=3    # Keep 3 Git releases
export STEPANEL_GIT_RELEASE_MAX_AGE_HOURS=168  # For 7 days
```

### Verification

```bash
# Automatic verification during backup
/opt/stepanel/stepanel verify-backup /var/lib/ste-panel/backups/example.com/2026-09-17-120000

# Test restore to staging
curl -X POST http://localhost:8090/api/backups/2026-09-17-120000/restore \
  -H "Content-Type: application/json" \
  -d '{"target":"staging"}'
```

---

## Monitoring & Profiling

### Real-Time Monitoring

```bash
# CPU usage
top -p $(pgrep -f stepanel | tr '\n' ',')

# Memory usage
smem -p stepanel

# Disk I/O
iostat -x 1

# Network I/O
iftop -i eth0
```

### Profiling

```bash
# Enable pprof (development only)
# Add to main.go after starting HTTP server:
# import _ "net/http/pprof"

# CPU profile (30 seconds)
curl http://localhost:6060/debug/pprof/profile?seconds=30 > cpu.prof
go tool pprof -http=:8081 cpu.prof

# Heap profile
curl http://localhost:6060/debug/pprof/heap > heap.prof
go tool pprof -http=:8081 heap.prof

# Goroutine profile
curl http://localhost:6060/debug/pprof/goroutine > goroutine.prof
go tool pprof goroutine.prof
```

### Long-Running Diagnostics

```bash
# Monitor over 1 hour
while true; do
  echo "$(date) - $(curl -s http://localhost:8090/metrics | grep stepanel_http_requests_total | awk '{print $2}')"
  sleep 60
done

# Track memory growth
ps aux | grep stepanel | grep -v grep | awk '{print $6, $4, $3}' >> /tmp/memory.log

# Analyze trends
awk '{print $1, $2}' /tmp/memory.log | tail -60
```

---

## Optimization Checklist

- [ ] Enable SSD for /var/lib/ste-panel
- [ ] Mount with `noatime` flag
- [ ] Set STEPANEL_MAX_CONCURRENT_JOBS to match CPU cores / 2
- [ ] Monitor disk space (set alerts at 80%)
- [ ] Schedule backups outside peak hours
- [ ] Test restore workflows monthly
- [ ] Monitor error rates (target < 0.1%)
- [ ] Review slow query log weekly
- [ ] Archive old backups monthly
- [ ] Update statistics for database query planner
- [ ] Monitor memory for unbounded growth
- [ ] Track goroutine count for leaks
- [ ] Measure API latency baseline

---

## Troubleshooting Performance

### Slow Restores

```bash
# Check disk I/O
iostat -x 1 | grep -E 'Device|sda'

# If r/s or w/s very high:
# - Database too slow: check connections
# - Disk too slow: check SSD health
# - Network (if NFS): add local cache

# Check job status
curl http://localhost:8090/api/jobs | jq '.jobs[] | select(.status=="running")'

# Increase concurrent jobs if under-utilizing disk
```

### High Memory Usage

```bash
# Check what's consuming memory
pmap -x $(pgrep -f stepanel) | tail -20

# If Go runtime large: Run garbage collection
# (No direct GC trigger in Go, but can restart service)

# If sessions growing: Check session cleanup
curl http://localhost:8090/api/health | jq '.sessions'

# If caches growing: Review application code
```

### Slow API Responses

```bash
# Check if database slow
/usr/local/sbin/stepanel-dbctl diagnostics

# Check if disk I/O bottleneck
iostat -x 1

# Check if goroutine exhaustion
curl http://localhost:8090/metrics | grep runtime_goroutines

# Restart if suspect memory bloat
sudo systemctl restart stepanel
```

---

## SLA Targets

```
Availability:      99.9% (43 minutes/month)
Restore Latency:   < 30 minutes (P95)
Backup Latency:    < 30 minutes (P95)
API Latency:       < 500ms (P95)
Error Rate:        < 0.1%
```

---

**Last Updated:** September 2026  
**Version:** v0.7.0
