# OpenAPI Specification Maintenance Roadmap

**Status:** Out of sync (34 missing routes)  
**Test Coverage:** Automated validation implemented  
**Priority:** P2 (Quality/Documentation)

---

## Current State

### Missing Routes (34 total)

These routes are **registered in code** but **not documented in openapi.yaml**:

**Account Management (5)**
- `/api/account/security` - Security center for customer accounts
- `/api/account/tokens/` - Customer API token management
- `/api/accounts/` - Account listing and creation
- `/api/admin/tokens/` - Administrator token management
- `/api/admin/suspend`, `/api/admin/unsuspend` - Account suspension

**Archive Import (4)** ← New in v0.7.0
- `/api/admin/archive/inspect` - Archive inspection endpoint
- `/api/admin/archive/inspect/status` - Inspection job status
- `/api/admin/archive/import` - Archive import request
- `/api/admin/archive/import/status` - Import job status

**Site Management (11)**
- `/api/sites/` - Site listing
- `/api/sites/overview/` - Site overview
- `/api/sites/access/` - Site access control
- `/api/sites/environment/` - Environment variables
- `/api/sites/git-key/` - Git deployment keys
- `/api/sites/git-webhook/` - Git webhook handling
- `/api/sites/logs/` - Site logs
- `/api/sites/php/` - PHP configuration
- `/api/sites/redis/` - Redis allocation
- `/api/sites/resources/` - Resource management
- `/api/sites/usage/` - Usage statistics

**Database & Tools (6)**
- `/api/databases/` - Database management
- `/api/database/sessions/` - Database session management
- `/api/tasks/` - Scheduled tasks
- `/api/jobs/` - Job queue management
- `/api/workers/` - Worker management
- `/api/wordpress/` - WordPress-specific operations

**Development & Deployment (5)**
- `/api/apps/` - Application management
- `/api/composer/` - PHP Composer integration
- `/api/proxy/` - Proxy configuration
- `/api/node/tooling` - Node.js runtime management
- `/api/python/` and `/api/python/deploy` - Python runtime

**Other (3)**
- `/api/admin/migration-doctor` - Migration analysis tool
- `/api/admin/migration-doctor/status` - Migration status
- `/api/admin/plan-status` - Plan enforcement status

### Stale Routes (Documentation Only)

These routes are **documented in openapi.yaml** but **don't match actual code patterns**:

**Pattern Mismatches:**
- Documented: `/api/doctor` 
- Actual: `/api/admin/migration-doctor` (namespace moved)

Many other routes use path parameters that don't exactly match the actual registration patterns.

---

## Why This Matters

### Current Impact
1. **Client confusion** - Developers can't trust the API documentation
2. **Discovery issues** - New endpoints not discoverable from spec
3. **Tooling problems** - Code generators and validators fail
4. **Support burden** - Users ask "is this endpoint real?"

### Downstream Issues
- **No code generation** - Can't auto-generate client SDKs
- **No schema validation** - Can't validate request/response formats
- **No API gating** - Can't use spec for access control
- **Poor developer experience** - Spec becomes unreliable

---

## Solution: Fix the OpenAPI Spec

### Phase 1: Update Spec with Missing Routes (2-3 hours)

Add documentation for all 34 missing routes. Each needs:
- Path and method (GET, POST, DELETE, etc.)
- Summary and description
- Parameters (path, query, body)
- Response schemas (200, 400, 403, 404, 500)
- Security requirements (admin vs. customer)
- Examples if applicable

**High-priority group (archive import + auth):**
```yaml
/api/admin/archive/inspect:
  post:
    summary: Inspect archive for migration requirements
    description: >
      Analyzes an archive without importing it. Returns site type, 
      PHP version, database requirements, and any issues.
    security:
      - sessionCookie: []
    requestBody:
      content:
        application/json:
          schema:
            type: object
            required: [url, config_path]
            properties:
              url:
                type: string
                description: Archive URL (S3, HTTPS)
              config_path:
                type: string
                description: Path to config file within archive
    responses:
      '200':
        description: Archive inspection results
      '400':
        description: Invalid archive or URL
      '403':
        description: Not administrator
```

**Scope:** Document all 34 routes with similar quality

### Phase 2: Align Existing Routes (1-2 hours)

Fix route patterns that don't match code:

- `/api/doctor` → `/api/admin/migration-doctor`
- Parameter patterns: `/api/{name}` vs `/api/{site}` consistency
- Method consistency: PUT vs PATCH for updates

### Phase 3: Add Response Schemas (3-5 hours)

Define proper JSON schemas for:
- Request bodies
- Response bodies (for each status code)
- Common models (Site, Database, Task, etc.)

**Example:**
```yaml
components:
  schemas:
    Site:
      type: object
      properties:
        name:
          type: string
        status:
          type: string
          enum: [initializing, ready, suspended]
        created_at:
          type: string
          format: date-time
        php_version:
          type: string
```

### Phase 4: Add Examples (1-2 hours)

Real-world example requests and responses for each endpoint:

```yaml
examples:
  InspectArchiveSuccess:
    value:
      status: done
      inspection:
        archive_type: tar.gz
        config_type: wordpress
        requirements:
          php_version: "8.0+"
          database_type: mysql
```

---

## Implementation Plan

### Recommended Approach

**Option A: Manual Update (Current)** - 6-12 hours
- Edit openapi.yaml directly
- Add routes, schemas, examples
- Verify with contract test suite
- **Pros:** Full control, learn the API deeply
- **Cons:** Time-consuming, error-prone

**Option B: Auto-Generate (Future)** - 8+ hours one-time
- Add code annotations to route handlers
- Build spec generator
- Regenerate spec from code
- **Pros:** Always in sync, less maintenance
- **Cons:** Requires tooling setup

**Recommendation for v0.7.0:** Hybrid approach
1. Manually add 34 missing routes (quick)
2. Add security/response schemas for critical paths
3. Leave detailed schema definitions for v0.8.0 auto-generation

---

## Testing & Validation

### Already Implemented
- `TestOpenAPIContractSync` - Catches missing/stale routes
- `TestOpenAPIVersionSync` - Ensures version matches
- `TestOpenAPIStructure` - Validates YAML format

### To Add
- `TestOpenAPISchemas` - Validate response schema completeness
- `TestOpenAPIExamples` - Verify examples are valid
- `TestOpenAPISecurityConsistency` - Check auth requirements match code

### CI Integration
```yaml
# Add to CI pipeline
- name: Validate OpenAPI spec
  run: go test -run TestOpenAPI ./...
  
- name: Fail if spec is out of sync
  run: |
    if go test -run TestOpenAPIContractSync ./... 2>&1 | grep -q "Missing from"; then
      echo "OpenAPI spec is out of sync - see test output"
      exit 1
    fi
```

---

## Success Criteria

- [ ] All 34 routes documented in OpenAPI spec
- [ ] No stale routes (spec matches code)
- [ ] All routes have summary, description, and security
- [ ] All responses have schema definitions
- [ ] Version in spec matches code
- [ ] TestOpenAPIContractSync passes
- [ ] CI fails if spec drifts

---

## Long-term Vision (v0.8.0+)

### Auto-Generation from Code
Instead of manually maintaining spec, generate it from code:

```go
// Route handler with spec annotation
// +openapi:route post /api/admin/archive/inspect
// +openapi:summary Inspect archive for migration requirements
// +openapi:security admin
// +openapi:response 200 ArchiveInspection
// +openapi:response 400 ErrorResponse
func (a *App) inspectArchive(w http.ResponseWriter, r *http.Request) {
    // implementation
}
```

**Benefits:**
- Spec always matches code (no manual sync)
- Changes to route automatically update spec
- Easier onboarding for new developers
- Can generate multiple formats (OpenAPI 3.0, 3.1, Swagger)

---

## Effort & Timeline

| Phase | Effort | Timeline | Blocker? |
|-------|--------|----------|----------|
| Phase 1 (Add 34 routes) | 2-3h | v0.7.0 | No, nice-to-have |
| Phase 2 (Fix patterns) | 1-2h | v0.7.0 | No, cleanup |
| Phase 3 (Response schemas) | 3-5h | v0.8.0 | No, quality |
| Phase 4 (Examples) | 1-2h | v0.8.0 | No, polish |
| **Auto-generation** | 8-12h | v0.8.0+ | No, future |

---

## Next Steps

1. **Immediate (this session):**
   - ✅ Implement contract validation tests
   - ✅ Identify 34 missing routes
   - 📋 Create this roadmap

2. **Next session (v0.7.0 release):**
   - Add all 34 routes to OpenAPI spec
   - Fix route pattern mismatches
   - Make TestOpenAPIContractSync pass

3. **v0.8.0 planning:**
   - Add comprehensive response schemas
   - Add real-world examples
   - Plan auto-generation tooling

---

## Related Documents

- [docs/DOCUMENTATION_GUIDE.md](./DOCUMENTATION_GUIDE.md) - Documentation structure
- [openapi.yaml](./openapi.yaml) - Current spec (34 routes missing)
- [openapi_contract_test.go](../openapi_contract_test.go) - Contract validation tests

