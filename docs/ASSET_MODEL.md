# StePanel Asset Model

## Overview

StePanel uses an **embedded-first asset model**. All static assets (HTML, CSS, JavaScript) are embedded in the binary at build time. This ensures:

- **Consistency**: UI always matches the binary version
- **Simplicity**: No separate file distribution required
- **Security**: Assets cannot be modified post-deployment

## Asset Source of Truth

**Embedded assets are the source of truth.** See `assets.go`:

```go
//go:embed web/index.html web/static/*
```

This embeds:
- `web/index.html` - Main dashboard
- `web/static/*.css` - Stylesheets (app.css, import.css, workspace.css)
- `web/static/*.js` - Client scripts (nav.js, sites.js, dashboard.js, etc.)

## Installation Behavior

The `install.sh` script does NOT copy web files. The binary serves embedded assets directly from `/` and `/static/`.

```
GET http://localhost:8080/          → serves web/index.html (embedded)
GET http://localhost:8080/static/app.css → serves web/static/app.css (embedded)
```

## For Operators

**Do NOT edit files in `/opt/stepanel/web/`** — they are not used at runtime.

To change the UI:
1. Modify source files in `web/`
2. Rebuild: `go build -o stepanel .`
3. Restart StePanel

Assets cannot be overridden at runtime; this is intentional.

## For Developers

### Adding a New Asset

1. Place the file in `web/` directory:
   ```
   web/static/newfile.js
   ```

2. Update `assets.go` embed pattern if needed:
   ```go
   //go:embed web/index.html web/static/*
   ```

3. Rebuild and test:
   ```bash
   go build -o stepanel .
   ./stepanel
   ```

4. Verify it's accessible:
   ```bash
   curl http://localhost:8080/static/newfile.js
   ```

### Removing an Asset

1. Delete the file from `web/`
2. Rebuild the binary
3. Restart StePanel

No cleanup needed; old files won't be served.

## Future: Runtime Overrides (Optional)

If runtime customization is needed (e.g., custom themes), we could:

1. Keep embedded assets as defaults
2. Optionally load overrides from `$STEPANEL_WEB_OVERRIDE_ROOT`
3. Serve overrides with higher priority than embedded assets

This would maintain the "embedded-first" approach while allowing operator customization.

## Validation

Run the asset validation script to ensure consistency:

```bash
bash scripts/validate-assets.sh
```

This checks:
- All embedded files exist in source
- Embedded pattern includes all necessary assets
- No unnecessary files are shipped
- File sizes are reasonable
