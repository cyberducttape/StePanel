# Node Application Startup Hardening

**Status:** Recommended approach documented  
**Priority:** Medium (improves robustness and reduces attack surface)  
**Target Component:** `stepanel-appctl` helper (systemd unit generation)

## Current Implementation

Node.js applications are managed by the `stepanel-appctl` helper, which generates systemd service units. The current approach uses NVM version selection at runtime:

```ini
ExecStart=/bin/bash -lc 'nvm use 22.14.0; exec npm start'
```

## Issues with Current Approach

1. **Shell complexity** - Login shell invocation adds attack surface
2. **Runtime resolution** - NVM sourcing happens at service startup, not at unit generation time
3. **Fragility** - NVM initialization depends on shell profile configuration
4. **Debugging difficulty** - Hidden logic makes systemd units harder to audit

## Recommended Approach

Resolve the exact Node.js binary path at unit generation time and write the absolute path directly:

```ini
ExecStart=/opt/stepanel/.nvm/versions/node/v22.14.0/bin/npm start
```

## Implementation Steps

### Phase 1: Unit Generation Enhancement
1. **At unit generation time** (when `/api/apps/deploy` is called):
   - Resolve the requested Node version from NVM installations
   - Verify the exact binary exists at `~stepanel/.nvm/versions/node/vX.Y.Z/bin/npm`
   - Write the absolute path to the generated systemd unit's `ExecStart=`

2. **Update .nvmrc handling**:
   - Keep `.nvmrc` for developer convenience (local NVM usage)
   - Unit generation should NOT depend on .nvmrc being present or parseable

3. **Fallback behavior**:
   - If the requested version is not installed, fail the deployment request with clear error
   - Don't attempt runtime NVM resolution

### Phase 2: Validation & Testing
1. **Systemd unit auditing**:
   - Verify generated units contain absolute paths only
   - No bash, nvm, login shell, or runtime version resolution

2. **Integration testing**:
   - Verify app starts and runs with resolved binary path
   - Test app restart/reload operations
   - Test NVM version updates (old binary still works, new deployment uses new version)

3. **Failure scenario testing**:
   - Attempt deployment with uninstalled Node version → clear error
   - Attempt to remove a Node version that's in-use → identify affected sites

### Phase 3: Documentation
1. **Update NODE_APPS.md** to reflect new resolved-path approach
2. **Update deployment audit logs** to show resolved binary path for troubleshooting
3. **Create troubleshooting guide** for "App fails to start" scenarios

## Benefits

- **Smaller attack surface**: No shell execution at startup
- **More predictable**: What's in the systemd unit is exactly what will execute
- **Easier to audit**: Absolute paths visible in `systemctl status`/`systemd-analyze`
- **Better error messages**: Deployment fails immediately if version not available
- **Clearer troubleshooting**: No hidden shell initialization to debug

## Migration Path

**For existing deployments:**
- Option 1: Recreate units on next app deployment (uses new resolved-path approach)
- Option 2: Force re-generation of all units when helper is updated
- Option 3: Keep working, migrate on next app update

**No breaking changes needed** - existing apps continue working; new deployments use resolved paths.

## Acceptance Criteria

✅ Generated systemd units contain absolute npm/node paths  
✅ No `bash`, `nvm`, or shell-like invocations in ExecStart  
✅ Unit generation fails cleanly if Node version is not installed  
✅ All existing tests pass  
✅ No manual version resolution during app startup  

## Related

- Architecture feedback from operational review: "Less shell means less attack surface and more predictable startup"
- Component: `stepanel-appctl` (helper layer)
- NVM storage: `~stepanel/.nvm/versions/node/vX.Y.Z/`
