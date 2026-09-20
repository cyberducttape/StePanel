# Legacy Token Migration Plan (HIGH)

## Issue: Backward-Compatible God Mode Tokens

**Security problem**: API tokens created before scope enforcement was added have `len(scopes) == 0`, which the code treats as "full access" for backward compatibility.

**Current code** (auth.go:530-539):
```go
// customer token created going forward is required to name at least one
// scope, so the empty-scopes case only ever describes a pre-existing token.
func (a Auth) HasRequiredCustomerScope(r *http.Request, scope string) bool {
    if !a.IsAPITokenRequest(r) {
        return true
    }
    scopes, _ := r.Context().Value(apiTokenScopesKey{}).([]string)
    if len(scopes) == 0 {
        return true  // ← SECURITY ISSUE: Empty scopes = full access
    }
    // ...
}
```

**Impact**:
- Any API token from before scope enforcement was added
- Has **zero scopes** but gets **full access** to that customer's account
- Never expires
- No way to restrict or revoke individual permissions
- "Ancient token means god mode because compatibility"

---

## Solution: Mandatory Migration

### Phase 1: Surface Legacy Tokens (Immediate)
1. Add field to APIToken: `LegacyUnscoped bool`
2. Mark tokens with `len(scopes) == 0` as `LegacyUnscoped: true`
3. Add API endpoint: `GET /api/account/tokens` shows legacy token flag
4. Add security center: "Legacy Tokens" section with warnings
5. Add migration CTA: "Regenerate to enable scope-based access control"

### Phase 2: Require Explicit Migration (30 Days)
1. API still accepts legacy tokens (no breaking change yet)
2. But every use triggers warning in audit log: `token.legacy_unscoped_access`
3. Security center dashboard highlights accounts using legacy tokens
4. Send email: "Your API token needs regeneration by [DATE]"
5. Require explicit confirmation to continue using legacy token

### Phase 3: Revoke Legacy Tokens (60 Days)
1. Hard cutoff: legacy tokens stop working
2. `HasRequiredCustomerScope()` returns `false` for empty scopes
3. Customers forced to regenerate with explicit scopes
4. Audit event: `token.legacy_unscoped_revoked`

---

## Implementation Details

### Phase 1: Token Metadata
Add to database schema:
```sql
ALTER TABLE api_tokens ADD COLUMN legacy_unscoped BOOLEAN DEFAULT false;
UPDATE api_tokens SET legacy_unscoped = true WHERE scopes IS NULL OR scopes = '';
```

### Phase 1: Database Query
When loading token metadata:
```go
type APIToken struct {
    ID                string
    Hash              string
    CreatedAt         time.Time
    ExpiresAt         time.Time
    Scopes            []string
    LegacyUnscoped    bool  // ← New field
}
```

### Phase 1: Security Center UI
Show in customer dashboard:
```
⚠️ LEGACY API TOKEN DETECTED

Your API token "legacy_token_xxxxx" was created before scope-based
access control was available.

Status: FULL ACCESS (all scopes)
Expiration: NEVER
Risk: High

This token has unlimited access to your account. We recommend regenerating
it with specific scopes to limit what it can do.

[Regenerate Token] [Learn More]
```

### Phase 2: Audit Events
Log every use of legacy token:
```json
{
  "actor": "api_token",
  "action": "token.legacy_unscoped_access",
  "token_id": "legacy_token_xxxxx",
  "timestamp": "2026-10-15T14:32:00Z",
  "warning": "Legacy unscoped token used; revocation scheduled for 2026-11-15"
}
```

### Phase 3: Authorization Change
Update auth logic:
```go
func (a Auth) HasRequiredCustomerScope(r *http.Request, scope string) bool {
    if !a.IsAPITokenRequest(r) {
        return true
    }
    scopes, _ := r.Context().Value(apiTokenScopesKey{}).([]string)
    
    // Phase 3: Reject empty scopes instead of allowing
    if len(scopes) == 0 {
        // OLD BEHAVIOR (Phase 1-2): return true
        // NEW BEHAVIOR (Phase 3+): return false
        return false
    }
    
    for _, candidate := range scopes {
        if candidate == scope {
            return true
        }
    }
    return false
}
```

---

## Customer Communication

### Email: Initial Warning (Day 0)
```
Subject: Action Required: Regenerate Your Legacy API Token

Dear [Customer],

We've identified that your account uses an older API token created before
our scope-based access control system was introduced.

Current status:
- Token ID: legacy_token_xxxxx
- Access level: Full account access (all scopes)
- Expiration: Never

This token has unlimited access to your account. We strongly recommend
regenerating it with specific scopes to limit permissions.

Timeline:
- Oct 15: This notification (0 days)
- Nov 1: Legacy token access will show warnings in audit log
- Nov 15: Legacy token will stop working

How to regenerate:
1. Go to Account → API Tokens
2. Click "Regenerate" next to "legacy_token_xxxxx"
3. Select the scopes you actually need
4. Update your applications to use the new token

Questions? Contact support@stepanel.com
```

### Email: Final Notice (Day 30)
```
Subject: URGENT: Your API token will stop working in 2 weeks

Your legacy API token will be revoked on November 15, 2026 at 00:00 UTC.

After that date:
- Requests using this token will be rejected with 401 Unauthorized
- Your integrations will stop working
- You'll need to regenerate a new token

Regenerate now: https://panel.stepanel.com/account/tokens
```

---

## Backward Compatibility Notes

### What Breaks in Phase 3
- Any customer still using legacy token will get 401 Unauthorized
- API integrations will fail
- Webhooks using legacy tokens will fail
- Scheduled jobs using legacy tokens will fail

### How to Avoid Break
- Customers must regenerate token before Nov 15
- They'll select explicit scopes when regenerating
- New token will work like normal scoped token

### Migration Path
```
Old flow: Use legacy token with empty scopes (full access)
New flow: Regenerate token with explicit scopes (e.g., "backup:read", "site:write")
```

---

## References

- `auth.go:530-546` - Current unscoped token handling
- `auth.go` - `APIToken` struct definition
- Security incident: Unscoped tokens provide unlimited access

---

## Recommendation

**Implement Phase 1 + Phase 2 immediately** (30-day cycle):
- Surface the problem (don't hide it)
- Give customers clear migration path
- Log every use for audit trail
- Plan hard cutoff

**Don't leave this debt lingering.** Every day a legacy token exists is a potential compromise vector, and "backward compatibility" isn't an excuse for unlimited god-mode access in a hosting platform.
