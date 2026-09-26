# Encryption Keys: Generation and Security

**Date:** 2026-09-26  
**Importance:** Critical for production deployment

---

## Overview

StePanel uses encryption keys to protect sensitive data at rest:
- Customer TOTP secrets (MFA)
- Site environment variables (passwords, API keys)
- Backup signing keys
- Audit log signing

These keys **MUST be machine-generated random values**, not human-typed passwords.

---

## ⚠️ Critical Rule

**RULE:** Encryption keys must be generated with a cryptographically secure random number generator.

**✅ CORRECT:**
```bash
openssl rand -hex 32
# Output: a7f2e9c3b1d8f4a6e2c9b3d7f1a5e8c2b6d9f2a5e8c1b4d7a0e3f6c9b2e5
```

**❌ WRONG:**
```bash
# These will be rejected at startup in production:
export STEPANEL_ACCOUNT_KEY='my-super-secret-password'
export STEPANEL_ENVIRONMENT_KEY='password123'
export STEPANEL_BACKUP_SIGNING_KEY='backup-key-secret'
```

---

## Why Machine-Generated?

### Password Hashing Is Not Key Derivation

Your configuration might seem to suggest:
```
encryption_key = hash(user_password)
```

This is **insufficient** for encryption keys because:
1. Passwords have low entropy (57-64 bits)
2. Passwords follow human patterns (words, numbers, symbols)
3. Key derivation requires KDF like Argon2id, not SHA-256
4. Even with KDF, you want HIGHER entropy than typical passwords

### Machine-Generated Randomness Requirements

Encryption keys need:
- **256 bits of entropy** (minimum for AES-256)
- **Uniform random distribution** (not patterns or words)
- **Cryptographically secure RNG** (not `Math.random()` or `rand()`)

---

## Key Generation

### Using OpenSSL (Recommended)

```bash
# Generate a 256-bit (32-byte) key in hex format
openssl rand -hex 32

# Output example:
# 7e2a9c3f1b5d8a4e6c9f2b7e1a4d8c5f3b6e9a2c7d0f2e4a7c9b1e3f5a8d
```

### Store Securely

**Option 1: Environment Variables**
```bash
export STEPANEL_ACCOUNT_KEY="$(openssl rand -hex 32)"
export STEPANEL_ENVIRONMENT_KEY="$(openssl rand -hex 32)"
export STEPANEL_BACKUP_SIGNING_KEY="$(openssl rand -hex 32)"
```

**Option 2: Secret Management (Recommended)**
```bash
# Use your organization's secret manager (Vault, AWS Secrets Manager, etc.)
# Then pass to container/process via environment or configuration
export STEPANEL_ACCOUNT_KEY=$(vault kv get -field=value secret/stepanel/account-key)
```

---

## Runtime Validation

Starting in production, StePanel validates encryption keys at startup and rejects:

❌ **Keys that look human-typed:**
- Pure lowercase: `thisismypassword`
- Pure uppercase: `THISISMYPASSWORD`
- Common patterns: containing "password", "secret", "admin", "test", etc.
- Unbalanced character distribution

❌ **Keys that are too short:**
- Less than 32 characters → rejected in production

✅ **Accepted keys:**
- Output from `openssl rand -hex 32`
- Output from `dd if=/dev/urandom bs=32 count=1 | base64`
- Output from your organization's random generator

---

## Configuration Reference

### STEPANEL_ACCOUNT_KEY
- **Purpose:** Encrypt customer TOTP secrets and MFA data
- **Required:** In production (or accept auth without TOTP)
- **Generation:** `openssl rand -hex 32`
- **Example:** `a7f2e9c3b1d8f4a6e2c9b3d7f1a5e8c2b6d9f2a5e8c1b4d7a0e3f6c9b2e5`

### STEPANEL_ENVIRONMENT_KEY
- **Purpose:** Encrypt site environment variables (secrets, passwords, API keys)
- **Required:** In production if using site environment variables
- **Generation:** `openssl rand -hex 32`
- **Example:** `c2b5e8a1d4f7a0c3e6b9d2e5f8a1c4d7e0f2a5b8c1d4e7a0c3f6b9e2d5`

### STEPANEL_BACKUP_SIGNING_KEY
- **Purpose:** Sign backup archives for integrity verification
- **Required:** In production for backups
- **Generation:** `openssl rand -hex 32`
- **Example:** `f1a4d7c0e3f6b9a2c5d8e1f4a7b0c3d6e9f2a5b8c1d4e7a0c3f6b9e2d5`

### STEPANEL_AUDIT_KEY
- **Purpose:** Sign audit log entries for tamper detection
- **Required:** In production
- **Generation:** `openssl rand -hex 32`
- **Storage:** Either `/etc/stepanel-audit.key` or `STEPANEL_AUDIT_KEY` environment variable
- **Example:** `3b6e9a2c5d8e1f4a7b0c3d6e9f2a5b8c1d4e7a0c3f6b9e2d5f8a1b4c7d`

---

## Installation Guide Integration

When installing StePanel in production:

```bash
# Generate all required keys
ACCOUNT_KEY=$(openssl rand -hex 32)
ENVIRONMENT_KEY=$(openssl rand -hex 32)
BACKUP_SIGNING_KEY=$(openssl rand -hex 32)
AUDIT_KEY=$(openssl rand -hex 32)

# Use with installer
sudo ./stepanel-install \
  STEPANEL_ADMIN_PASSWORD='...' \
  STEPANEL_ACCOUNT_KEY="$ACCOUNT_KEY" \
  STEPANEL_ENVIRONMENT_KEY="$ENVIRONMENT_KEY" \
  STEPANEL_BACKUP_SIGNING_KEY="$BACKUP_SIGNING_KEY" \
  STEPANEL_AUDIT_KEY="$AUDIT_KEY" \
  ...
```

---

## Testing Keys

For **development/testing**, weak keys are allowed:

```bash
# Development only - NOT for production
export STEPANEL_ACCOUNT_KEY='dev-key-for-testing-only-not-random'
export STEPANEL_ENVIRONMENT_KEY='test-environment-key-for-dev'
```

Production validation will reject these and log:
```
validation error: STEPANEL_ACCOUNT_KEY appears to be a human-typed password
```

---

## Backup and Recovery

**Critical:** Store encryption keys securely outside the system:
1. Save all generated keys to a password manager
2. Back up keys in your disaster recovery system
3. Never commit keys to version control
4. Never log keys (StePanel will hide them from logs)

**If you lose keys:**
- Encrypted data becomes unrecoverable
- TOTP secrets cannot be accessed
- Environment variables are inaccessible
- Backup signatures cannot be verified

---

## Security Posture

This requirement enforces:
- ✅ High-entropy encryption keys
- ✅ Protection against weak human passwords as keys
- ✅ Machine-generated randomness
- ✅ Consistent key quality across deployments
- ✅ Runtime validation at startup

**Result:** Encrypted data is protected by keys with 256 bits of entropy, not human-typeable passwords with 60-70 bits of entropy.

---

## See Also

- [INSTALLATION.md](INSTALLATION.md) - Production deployment guide
- [OPERATIONS.md](OPERATIONS.md) - Operations and secrets management
- [P1 HTTP Timeout and Upload Hardening](P1_HTTP_TIMEOUT_AND_UPLOAD_HARDENING.md) - Related security hardening
