# Plan Enforcement and Account Suspension

This guide describes operator workflows for managing customer plan limits, monitoring usage, and enforcing account suspensions in the shared-hosting beta.

## Overview

StePanel provides three mechanisms for plan enforcement:

1. **Usage Monitoring** — Track customer usage against plan limits
2. **Automatic Warnings** — Alert operators when usage exceeds thresholds
3. **Account Suspension** — Prevent new operations when limits are exceeded

## Hosting Plans

StePanel defines three hosting plans with resource limits:

### Starter Plan
- **Max Sites**: 1
- **Max Databases**: 1
- **CPU**: 100% of 1 core
- **Memory**: 512 MB
- **Disk**: 10 GB
- **Inodes**: 200,000
- **PHP Workers**: 8
- **Cron Tasks**: 128

### Professional Plan
- **Max Sites**: 5
- **Max Databases**: 10
- **CPU**: 200% of cores
- **Memory**: 1 GB
- **Disk**: 50 GB
- **Inodes**: 1,000,000
- **PHP Workers**: 16
- **Cron Tasks**: 256

### Agency Plan
- **Max Sites**: 25
- **Max Databases**: 50
- **CPU**: 400% of cores
- **Memory**: 2 GB
- **Disk**: 200 GB
- **Inodes**: 5,000,000
- **PHP Workers**: 32
- **Cron Tasks**: 512

## Monitoring Account Usage

### Check Plan Status for a Customer

```bash
curl -X GET "https://panel.example.com/api/admin/plan-status?account=customer1" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN"
```

Response shows:

```json
{
  "username": "customer1",
  "plan": "professional",
  "suspended": false,
  "sites_used": 4,
  "site_limit": 5,
  "sites_percent": 80,
  "databases_used": 7,
  "database_limit": 10,
  "databases_percent": 70,
  "warning_threshold_percent": 80,
  "critical_threshold_percent": 95
}
```

### Interpretation

- **sites_percent**: Usage as percentage of site limit
  - 0-79% = Normal
  - 80-94% = Warning threshold
  - 95-100% = Critical
  
- **databases_percent**: Usage as percentage of database limit
  - Same thresholds as sites

**Action required when:**
- Any metric reaches 80% → Consider proactive customer outreach
- Any metric reaches 95% → Operator should review usage
- Customer exceeds limit → Automatic suspension may occur

## Automatic Usage Monitoring

StePanel periodically checks all customer accounts for:

1. **Usage Warnings** — Logged when usage reaches 80% of limit
2. **Critical Warnings** — Logged when usage reaches 95% of limit
3. **Automatic Suspension** — Applied when usage exceeds 100% of limit

Warnings are logged to the audit trail with:
- `Action`: `account.usage.warning` or `account.suspended.auto`
- `Target`: Customer username
- `Details`: Usage breakdown (e.g., "site usage at 80% of limit (4/5)")

Operators should review the audit log daily to identify customers approaching limits:

```bash
grep 'account.usage.warning\|account.suspended' /var/log/stepanel/audit.log
```

## Account Suspension

### Manual Suspension

Suspend a customer account:

```bash
curl -X POST "https://panel.example.com/api/admin/suspend" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  -H "X-CSRF-Token: YOUR_CSRF_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "username": "customer1",
    "reason": "Site limit exceeded (5/5 sites)",
    "permanent": false
  }'
```

**Fields:**
- `username` — Customer account to suspend
- `reason` — Human-readable reason for suspension (required)
- `permanent` — 
  - `false` = Temporary suspension, automatically lifted if usage drops below limit
  - `true` = Permanent suspension, manual unsuspension required

### Automatic Suspension

StePanel automatically suspends accounts when:

1. **Site limit exceeded** — Customer creates more sites than allowed
2. **Database limit exceeded** — Customer creates more databases than allowed

Automatic suspensions are:
- Logged with action `account.suspended.auto`
- Marked with `triggered_by: "system"`
- Temporary (auto-lifted if usage drops below limit)

### Effects of Suspension

When an account is suspended:

1. ✓ Existing sites continue to operate
2. ✓ Existing backups continue to run
3. ✓ Restore-to-staging continues to work
4. ✗ **Cannot create new sites**
5. ✗ **Cannot create new databases**
6. ✗ **Cannot deploy applications**
7. ✗ **Cannot create backups** (except scheduled, which may skip)
8. ✗ **API operations fail** with HTTP 403 Forbidden

Customer receives HTTP 403 responses with message: "Your account is suspended. Contact support."

### Unsuspend an Account

```bash
curl -X POST "https://panel.example.com/api/admin/unsuspend" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  -H "X-CSRF-Token: YOUR_CSRF_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "username": "customer1",
    "reason": "Upgraded to Professional plan"
  }'
```

**Fields:**
- `username` — Customer account to unsuspend
- `reason` — Optional reason for manual unsuspension

## Audit Trail

All account operations are logged to the audit trail:

```bash
# View suspensions
grep 'account.suspended' /var/log/stepanel/audit.log

# View unsuspensions
grep 'account.unsuspended' /var/log/stepanel/audit.log

# View usage warnings
grep 'account.usage.warning' /var/log/stepanel/audit.log
```

Audit format:
```json
{
  "timestamp": "2026-09-19T14:23:45Z",
  "actor": "admin",
  "action": "account.suspended",
  "target": "customer1",
  "details": "Site limit exceeded (5/5 sites)"
}
```

## Operator Runbook

### Daily Usage Review

```bash
# Check for customers approaching limits
grep 'account.usage.warning' /var/log/stepanel/audit.log | tail -20

# Identify customers to contact proactively
grep '80%\|95%' /var/log/stepanel/audit.log | grep usage
```

### Customer Upgrade Workflow

When a customer needs more resources:

1. **Verify current usage**
   ```bash
   curl -X GET "https://panel.example.com/api/admin/plan-status?account=customer1" \
     -H "Authorization: Bearer YOUR_ADMIN_TOKEN"
   ```

2. **Confirm plan upgrade is appropriate**
   - Review their usage patterns
   - Discuss with customer if needed
   - Update billing/CRM

3. **Apply upgrade** (using account management API)
   - Update customer's plan in the database

4. **Unsuspend if suspended**
   ```bash
   curl -X POST "https://panel.example.com/api/admin/unsuspend" \
     -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
     -H "X-CSRF-Token: YOUR_CSRF_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{
       "username": "customer1",
       "reason": "Upgraded to Professional plan"
     }'
   ```

5. **Notify customer**
   - Confirm upgrade is active
   - No action needed on their side

### Troubleshooting Suspension

**Customer reports "account suspended" error:**

1. Check suspension status
   ```bash
   curl -X GET "https://panel.example.com/api/admin/plan-status?account=customer1" \
     -H "Authorization: Bearer YOUR_ADMIN_TOKEN"
   ```

2. Review audit log for why
   ```bash
   grep 'customer1' /var/log/stepanel/audit.log | grep suspended
   ```

3. Determine if automatic or manual
   - `triggered_by: "system"` = Automatic suspension
   - `triggered_by: "admin"` = Manual suspension

4. Take appropriate action
   - If automatic: Upgrade customer's plan or reduce their usage
   - If manual: Unsuspend if reason resolved

## Best Practices

1. **Proactive Communication**
   - Contact customers at 80% usage
   - Offer plan upgrades before suspension
   - Set expectations on limit enforcement

2. **Transparent Limits**
   - Document plan limits in customer onboarding
   - Show usage on customer dashboard
   - Make it easy to see remaining capacity

3. **Graceful Degradation**
   - Don't surprise customers with suspension
   - Warn at 80% and 95%
   - Log all suspension events

4. **Regular Audits**
   - Review usage trends weekly
   - Identify customers at risk of suspension
   - Plan for capacity growth

5. **Clear Escalation Path**
   - Have a process for manual exceptions
   - Document override decisions
   - Keep audit trail complete

## Metrics and Alerts

Monitor these metrics to manage plan enforcement:

- **Suspension count** — Number of suspended accounts
- **High usage accounts** — Customers above 80% usage
- **Limit violations** — New suspensions per day
- **Unsuspension rate** — How quickly customers upgrade to resolve

Set alerts:
- **Alert**: Any account automatic suspension
- **Alert**: More than 5 accounts at 95%+ usage
- **Warning**: More than 10 accounts at 80%+ usage
