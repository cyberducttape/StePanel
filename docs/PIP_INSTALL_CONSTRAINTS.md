# Pip Install Constraints: Safe Dependency Management

## Problem

Current implementation (v0.7.0):
```bash
# UNSAFE: Runs as root, network access, no signature verification
pip install flask
```

**Risk**: Compromised PyPI package = root compromise of StePanel host

## Solution: Constraint Framework

### 1. Pip Install in Unprivileged Process

Move pip operations from root StePanel process to unprivileged site user:

```bash
# Root StePanel (no pip)
[stepanel process:unprivileged]

# Site user subprocess (isolated)
[step-site-X process:unprivileged]
  └─ pip install --no-index --find-links /opt/stepanel/wheels flask
```

**Benefits**:
- Compromised package ≤ site user compromise
- Cannot access other sites or StePanel state
- Bounded resource usage (site user process limits)

### 2. --no-index Enforcement

No network access during pip install:

```bash
# In stepanel-helper or site subprocess
pip install \
  --no-index \                              # No PyPI access
  --find-links /opt/stepanel/wheels/cache  # Only pre-cached wheels
  --require-hashes \                        # Verify integrity
  flask==2.3.0
```

**Workflow**:
1. Administrator stages wheels to `/opt/stepanel/wheels/cache/`
2. Wheels include hash manifests (pip --require-hashes)
3. Pip install validates against hashes
4. Fails explicitly if package not in cache (no fallback to network)

### 3. Pre-Cached Wheels Directory

Structure:
```
/opt/stepanel/wheels/
├── cache/
│   ├── flask-2.3.0-py3-none-any.whl
│   ├── werkzeug-2.3.0-py3-none-any.whl
│   └── hashes.txt
├── requirements.lock               # Pinned, hashed requirements
└── sync.sh                          # Update cache from S3/mirror
```

### 4. Constraint Files for Runtime

Per-site pip constraints:

```ini
# /opt/stepanel/sites/mysite/pip-constraints.txt
# Site customer can only install from pre-approved list

Flask==2.3.0
Werkzeug==2.3.0
Jinja2==3.1.0
MarkupSafe==2.1.0
click==8.1.0

# NOT ALLOWED (not in whitelist):
# requests==2.28.0  # Not in site requirements
# numpy==1.23.0     # Not in site requirements
```

Usage:
```bash
pip install \
  --no-index \
  --find-links /opt/stepanel/wheels/cache \
  --require-hashes \
  -c /opt/stepanel/sites/mysite/pip-constraints.txt \
  flask
```

## Implementation Phases

### Phase 1: Helper Wrapper (v0.7.0)

Create `stepanel-pip` helper command:

```go
// internal/helper/pip.go
type PipInstallRequest struct {
    Site     string
    Package  string
    Version  string
    // No network access - enforced by Go process
}

type PipInstallResponse struct {
    Status   string
    Installed []string
    Errors   []string
}

// Helper runs as root, but drops privileges to site user before pip
func (h *Helper) PipInstall(req PipInstallRequest) (*PipInstallResponse, error) {
    // 1. Validate package in whitelist
    // 2. Drop to site user
    // 3. Run pip with --no-index in site chroot
    // 4. Verify installation
    // 5. Return status
}
```

### Phase 2: Wheel Cache Management (v0.8+)

```bash
# stepanel wheels sync
# Updates /opt/stepanel/wheels/cache from configured mirror
stepanel wheels sync --mirror https://wheels.internal.example.com/
```

Database schema:
```sql
CREATE TABLE python_wheels (
    id INTEGER PRIMARY KEY,
    package_name TEXT NOT NULL,
    version TEXT NOT NULL,
    filename TEXT NOT NULL,
    hash_sha256 TEXT NOT NULL,
    synced_at TIMESTAMP,
    UNIQUE(package_name, version)
);

CREATE TABLE site_python_requirements (
    id INTEGER PRIMARY KEY,
    site TEXT NOT NULL,
    package TEXT NOT NULL,
    version TEXT NOT NULL,
    constraint_type TEXT,  -- 'allowed', 'pinned'
    approved_by TEXT,
    approved_at TIMESTAMP,
    UNIQUE(site, package)
);
```

### Phase 3: Network Isolation (v0.8+)

Run `stepanel-helper` in network namespace with no internet:

```bash
# At container/VM level, not in Go code
ip netns add stepanel-locked
# stepanel-helper runs in stepanel-locked namespace
# Only filesystem access, no network
```

## Validation Checklist

- [ ] Pip never runs in root StePanel process
- [ ] --no-index enforced (no network access during install)
- [ ] --require-hashes enforced (integrity verification)
- [ ] Per-site constraints enforced (no unwanted packages)
- [ ] Cache sync is explicit (no auto-updates without approval)
- [ ] Failed installs fail loudly (no silent fallbacks)
- [ ] Site user subprocess resource limits applied
- [ ] Audit log all pip installs with hash verification

## Security Boundary

### Trusted Components
- StePanel root process (distribution)
- Wheel cache administrator (manual curation)
- Python interpreter (assumed safe)

### Untrusted Components
- Site customer code (can request any package)
- PyPI mirrors (all network access blocked)
- Third-party packages (constrained to whitelist)

### Attack Scenarios Prevented

| Scenario | Prevention |
|----------|-----------|
| PyPI compromise | --no-index blocks all network |
| Typosquatting | Whitelist prevents similar names |
| Dependency confusion | Private namespace enforcement |
| Supply chain attack | Hashes verify integrity |
| Privilege escalation | Pip runs unprivileged (site user) |
| Cross-site infection | Per-site process isolation |

## Configuration Example

```yaml
# /etc/stepanel/config.yaml
pip:
  enabled: true
  wheels_cache_path: /opt/stepanel/wheels/cache
  require_hashes: true
  network_access: false          # MUST be false
  helper_command: /usr/local/sbin/stepanel-helper
  
sites:
  mysite:
    python_version: "3.11"
    max_pip_packages: 50
    allowed_packages:
      - flask==2.3.0
      - werkzeug==2.3.0
      - jinja2==3.1.0
```

## Migration Path (v0.7.0 → v0.8.0)

v0.7.0:
- [ ] Implement stepanel-pip helper command
- [ ] Remove all `os.system("pip install")` calls from root
- [ ] Implement --no-index enforcement
- [ ] Create initial wheels cache

v0.8.0:
- [ ] Database schema for wheel tracking
- [ ] Web UI for wheel cache management
- [ ] `stepanel wheels` CLI commands
- [ ] Per-site requirement approval workflow

v0.9.0+:
- [ ] Network namespace isolation
- [ ] Automated wheel mirror sync
- [ ] Supply chain attestation verification

## References

- PEP 440 - Version Identification and Dependency Specification
- PEP 508 - Dependency specification for Python Software Packages
- PEP 517 - A build-backend interface for Python source distributions
- Python Packaging Authority: https://packaging.python.org
- OWASP: Dependency Management Security
