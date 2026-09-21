# Sudo Privilege Boundary Threat Model (MEDIUM/HIGH)

## Issue: Sudo Grants Are Broad Privilege Bridges

**Current sudo config** (install.sh:687-692):
```sudoers
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-appctl *
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-proxyctl *
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-sitectl *
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-dbctl *
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-vhostctl *
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-gitctl *
```

**What this grants**: The `stepanel` service account can execute ANY arguments to ANY of these helpers as root, without password.

**Actual threat model**:
```
Compromise of stepanel service account
    ↓
Ability to invoke root helpers with arbitrary valid arguments
    ↓
Ability to manipulate ANY site's state
    ↓
Equivalent to root compromise on that machine
```

---

## What This Design DOES Provide

**Strength: Command-Shape Validation**
- Helpers heavily validate their arguments (regex patterns, enumeration, path constraints)
- Syntax like `stepanel-sitectl invalid-action sitename` gets rejected
- Prevents arbitrary command execution via malformed arguments
- Good defense against injection attacks

**Strength: Code Auditing**
- Helpers are small, focused programs
- Easy to audit for unexpected side effects
- Clear interface between daemon and root
- Reduces blast radius compared to `sudo bash`

---

## What This Design DOES NOT Provide

**NOT Strong Privilege Separation**: The helpers do not know about customer authorization boundaries.
```go
// In stepanel-sitectl:
func main() {
    action := os.Args[1]      // "create", "delete", etc.
    siteName := os.Args[2]    // "customer-a.com"
    
    // Helper validates syntax: ✓
    // But it doesn't know: "Is the daemon authorized for customer-a.com?"
    // It just executes: rm -rf /var/www/sites/customer-a.com
}
```

**NOT Tenant Isolation**: A compromised daemon can invoke helpers for ANY site.
```
Compromised daemon:
    stepanel-sitectl delete customer-b.com  ← Can do this even if unauthorized
    stepanel-sitectl delete customer-c.com  ← Can do this too
    stepanel-sitectl create malicious.com   ← Can create arbitrary sites
```

**NOT Independent Root Authorization**: The authorization check happens BEFORE calling sudo, in the daemon.
```
Daemon                          Root Helper
├─ Check: Is user authorized?   (Independent check? No)
├─ Call sudo helper  ──────────→ Helper just validates syntax
└─ (If daemon is compromised,   └─ Doesn't re-verify authorization
   this check is bypassed)
```

---

## Accurate Threat Model Statement

**This design provides:**
1. ✅ Command syntax validation (prevents injection)
2. ✅ Small, auditable helper surface
3. ✅ Better than shell access for root operations

**This design does NOT provide:**
1. ❌ Isolation between customer operations
2. ❌ Independent root-level authorization checks
3. ❌ Defense against compromised service account

**In case of service account compromise:**
- Attacker gets effective root access
- Can manipulate any site
- Can read all customer data
- Can create backdoors
- Equivalent to single-node hosting control plane compromise

---

## Better Models (Not Implemented; For Reference)

### Option 1: Per-Customer Sudo Entries
```sudoers
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-sitectl create customer-a.com *
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-sitectl delete customer-a.com
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-sitectl create customer-b.com *
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-sitectl delete customer-b.com
```
**Pro**: Root enforces authorization  
**Con**: Sudoers file becomes huge; hard to maintain

### Option 2: Sealed Capsules (Privilege Drops)
```bash
# Helper calls setuid() to drop to site-specific user
stepanel-sitectl create customer-a.com
    ↓
setuid(customer-a)  # Helper drops privilege
    ↓
mkdir /var/www/customer-a.com
```
**Pro**: Multi-layer authorization  
**Con**: Complex privilege management; hard to audit

### Option 3: Separate Root Daemons
```
Root daemon for each customer
├─ Customer A daemon (runs as root, but only touches customer-a.com)
├─ Customer B daemon (runs as root, but only touches customer-b.com)
└─ Control plane daemon (stepanel service account, no root)
```
**Pro**: True isolation  
**Con**: Massive operational complexity; scaling nightmare

---

## Current Reality (Honest Assessment)

**This is a single-node hosting control plane** with the standard threat model:

1. **Assumed trust boundary**: Control plane ↔ Customers
2. **Threat scope**: Compromise of control plane ≈ all customer data compromised
3. **This is unavoidable** in a single-node design where root helpers manage all sites
4. **Mitigation focus**: Reduce service account compromise risk
   - Minimize daemon attack surface (code review, fuzzing, sandboxing)
   - Restrict network access to control plane
   - Monitor for privilege escalation attempts
   - Audit all root-level operations

---

## Documentation Requirements

### What Should Be Stated Clearly

1. **Threat Model Document** (needed):
   - "Compromise of the StePanel service account = compromise of all sites"
   - "Root helpers provide command-shape validation, not authorization isolation"
   - "This is acceptable for single-node control plane; not suitable for disaggregated multi-node"

2. **Operational Security Policy** (needed):
   - "Assume the service account's private key/credentials could be compromised"
   - "Design for detecting and responding to privilege escalation"
   - "Implement mandatory privilege escalation alerts"

3. **Security Boundaries** (needed):
   - Which threat vectors are in-scope (outside attackers, malicious customers, inside threats)
   - Which are out-of-scope or mitigated by other controls
   - Recovery procedures if control plane is compromised

### What Should NOT Be Claimed

❌ "Root helpers provide strong privilege separation"  
❌ "Tenant isolation enforced at the kernel level"  
❌ "Compromise of daemon doesn't affect other sites"  
❌ "Sudo design is equivalent to multi-process isolation"

---

## Recommendations

### Immediate (Documentation)
1. ✅ Write honest threat model document
2. ✅ Stop claiming "privilege separation" between daemon and tenants
3. ✅ Document actual isolation boundaries (root helpers, command syntax validation)

### Short-term (Hardening)
1. Monitor sudoers execution (every helper call logs to syslog)
2. Alert on unexpected helper invocations
3. Audit trail of all privileged operations
4. Periodic privilege escalation tests

### Long-term (If Scaling)
1. Consider disaggregated architecture (separate root per customer, or root daemon)
2. Evaluate zero-trust model for control plane
3. Implement cryptographic proof of authorization in helper calls

---

## Related Docs

- `docs/SECURITY.md` - Overall security policy
- `install.sh:687-692` - Current sudo configuration
- `internal/helper/helpers.go` - Helper invocation code
- **MISSING**: `docs/THREAT_MODEL.md` - Should be created

---

## Bottom Line

**The helpers are well-designed, well-audited, and prevent command injection.**

**But don't claim they're privilege separation between customers. They're command validation.**

**Be honest about the threat model: compromise of the service account = hosting control plane compromise.**

**This is acceptable for a single-node platform. Own it. Don't oversell it.**
