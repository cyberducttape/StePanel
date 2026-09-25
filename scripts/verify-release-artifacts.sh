#!/bin/bash
# Verify release artifacts meet packaging standards
# Run after GoReleaser publishes artifacts

set -e

DIST_DIR="${1:-dist}"

if [ ! -d "$DIST_DIR" ]; then
    echo "ERROR: $DIST_DIR not found" >&2
    exit 1
fi

echo "=== Release Artifact Verification ==="
echo

# Check for .tar.gz files and verify they are actually gzipped
echo "Checking tar.gz compression..."
for archive in "$DIST_DIR"/*.tar.gz; do
    if [ -f "$archive" ]; then
        # Check file magic number for gzip (1f 8b)
        if file "$archive" | grep -q "gzip compressed"; then
            echo "✓ $archive is properly gzipped"
        else
            echo "✗ FAIL: $archive is NOT gzipped (mislabeled)" >&2
            exit 1
        fi

        # Verify archive can be extracted
        if tar -tzf "$archive" > /dev/null 2>&1; then
            echo "✓ $archive can be extracted successfully"
        else
            echo "✗ FAIL: $archive cannot be extracted" >&2
            exit 1
        fi
    fi
done

echo
echo "Checking SHA256 checksums..."
if [ -f "$DIST_DIR/SHA256SUMS" ]; then
    cd "$DIST_DIR"
    if sha256sum -c SHA256SUMS --status; then
        echo "✓ All checksums verified"
    else
        echo "✗ FAIL: Checksum verification failed" >&2
        cd - > /dev/null
        exit 1
    fi
    cd - > /dev/null
else
    echo "✗ FAIL: SHA256SUMS not found" >&2
    exit 1
fi

echo
echo "Checking SBOM..."
if [ -f "$DIST_DIR/SBOM.spdx.json" ]; then
    # Verify SBOM is valid JSON
    if python3 -m json.tool "$DIST_DIR/SBOM.spdx.json" > /dev/null 2>&1; then
        echo "✓ SBOM.spdx.json is valid JSON"
    else
        echo "✗ FAIL: SBOM.spdx.json is not valid JSON" >&2
        exit 1
    fi
else
    echo "✗ WARNING: SBOM.spdx.json not found (expected in release)" >&2
fi

echo
echo "Checking for required files in archives..."
for archive in "$DIST_DIR"/*.tar.gz; do
    if [ -f "$archive" ]; then
        ARCHIVE_NAME=$(basename "$archive")
        echo
        echo "Verifying $ARCHIVE_NAME contents..."

        # Check for required files
        for required_file in LICENSE README.md SECURITY.md; do
            if tar -tzf "$archive" | grep -q "$required_file"; then
                echo "  ✓ $required_file present"
            else
                echo "  ✗ WARNING: $required_file missing" >&2
            fi
        done

        # Verify binary is present
        if tar -tzf "$archive" | grep -q "stepanel$"; then
            echo "  ✓ stepanel binary present"
        else
            echo "  ✗ FAIL: stepanel binary not found" >&2
            exit 1
        fi
    fi
done

echo
echo "=== All packaging checks passed ==="
exit 0
