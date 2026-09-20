#!/usr/bin/env bash
set -Eeuo pipefail

# Validate that static assets follow the embedded-first model
# Ensures consistency between source, embedding, and documentation

echo "🔍 Validating StePanel asset model..."
echo ""

# Check that assets.go has the correct embed pattern
echo "1️⃣  Checking assets.go embed directive..."
if grep -q '//go:embed web/index.html web/static/\*' assets.go; then
    echo "   ✅ Embed pattern correct"
else
    echo "   ❌ Embed pattern incorrect or missing"
    echo "   Expected: //go:embed web/index.html web/static/*"
    exit 1
fi

# Verify all embedded files exist
echo "2️⃣  Verifying embedded files exist..."
required_files=(
    "web/index.html"
    "web/static/app.css"
    "web/static/import.css"
    "web/static/workspace.css"
    "web/static/nav.js"
    "web/static/dashboard.js"
    "web/static/sites.js"
    "web/static/cpmove.js"
    "web/static/deploy.js"
    "web/static/certificates.js"
    "web/static/wpress.js"
    "web/static/database.js"
    "web/static/htaccess.js"
)

missing=0
for file in "${required_files[@]}"; do
    if [[ -f "$file" ]]; then
        size=$(stat -f%z "$file" 2>/dev/null || stat -c%s "$file" 2>/dev/null || echo "unknown")
        echo "   ✅ $file ($size bytes)"
    else
        echo "   ❌ $file (MISSING)"
        missing=1
    fi
done

if [[ $missing -ne 0 ]]; then
    exit 1
fi

# Check that no files are copied in install.sh
echo ""
echo "3️⃣  Checking install.sh doesn't copy web files..."
if grep -q "cp.*web/static\|cp.*web/index.html" install.sh 2>/dev/null; then
    echo "   ⚠️  install.sh copies web files (should not - using embedded assets)"
    echo "   Consider removing these copy commands:"
    grep "cp.*web" install.sh || true
else
    echo "   ✅ install.sh correctly doesn't copy web files"
fi

# Verify index.html references correct asset paths
echo ""
echo "4️⃣  Checking index.html asset references..."
if grep -q '/static/.*?v=' web/index.html; then
    echo "   ✅ Asset paths include version parameter"
else
    echo "   ⚠️  Asset paths may not include version parameter"
fi

# Check that ASSET_MODEL documentation exists
echo ""
echo "5️⃣  Checking documentation..."
if [[ -f "docs/ASSET_MODEL.md" ]]; then
    echo "   ✅ Asset model documented in docs/ASSET_MODEL.md"
else
    echo "   ⚠️  Asset model documentation missing"
fi

# Verify asset sizes are reasonable
echo ""
echo "6️⃣  Checking asset sizes..."
total_size=0
for file in web/static/*.{css,js} web/index.html; do
    if [[ -f "$file" ]]; then
        size=$(stat -f%z "$file" 2>/dev/null || stat -c%s "$file" 2>/dev/null || echo 0)
        total_size=$((total_size + size))

        # Warn if individual files are suspiciously large
        if [[ $size -gt 500000 ]]; then
            echo "   ⚠️  $file is large ($size bytes) - consider splitting"
        fi
    fi
done

total_kb=$((total_size / 1024))
echo "   Total embedded assets: $total_kb KB"

if [[ $total_kb -gt 1000 ]]; then
    echo "   ⚠️  Embedded assets exceed 1MB - consider optimization"
fi

echo ""
echo "✅ Asset validation complete!"
echo ""
echo "Asset Model Summary:"
echo "  • All static assets embedded in binary"
echo "  • Embedded-first: served directly, not from files"
echo "  • No runtime file copying needed"
echo "  • UI always matches binary version"
echo "  • Override model optional for future customization"
