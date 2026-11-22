#!/usr/bin/env bash
# Verify release artifacts meet packaging standards
# Run after GoReleaser publishes artifacts

set -Eeuo pipefail
shopt -s nullglob

DIST_DIR="${1:-dist}"
: "${EXPECTED_RELEASE_SHA:?EXPECTED_RELEASE_SHA must identify the tagged source commit}"
[[ "$EXPECTED_RELEASE_SHA" =~ ^[0-9a-f]{40}$ ]] || {
    echo "ERROR: EXPECTED_RELEASE_SHA must be a full commit SHA" >&2
    exit 1
}

if [ ! -d "$DIST_DIR" ]; then
    echo "ERROR: $DIST_DIR not found" >&2
    exit 1
fi

echo "=== Release Artifact Verification ==="
echo

echo "Checking exact-source release evidence..."
evidence_file="$DIST_DIR/RELEASE_EVIDENCE.md"
if [[ ! -f "$evidence_file" ]] || ! grep -Fq "Source commit: [\`$EXPECTED_RELEASE_SHA\`]" "$evidence_file"; then
    echo "✗ FAIL: exact-source RELEASE_EVIDENCE.md missing or for a different commit" >&2
    exit 1
fi
echo "✓ RELEASE_EVIDENCE.md identifies $EXPECTED_RELEASE_SHA"

archives=("$DIST_DIR"/*.tar.gz)
if [ "${#archives[@]}" -eq 0 ]; then
    echo "✗ FAIL: no .tar.gz release archives found" >&2
    exit 1
fi

# Check for .tar.gz files and verify they are actually gzipped
echo "Checking tar.gz compression..."
for archive in "${archives[@]}"; do
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
done

echo
echo "Checking SHA256 checksums..."
if [ -f "$DIST_DIR/SHA256SUMS" ]; then
    if (cd "$DIST_DIR" && sha256sum -c SHA256SUMS --status); then
        echo "✓ All checksums verified"
    else
        echo "✗ FAIL: Checksum verification failed" >&2
        exit 1
    fi
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
    echo "✗ FAIL: SBOM.spdx.json not found" >&2
    exit 1
fi

echo
echo "Checking for required files in archives..."
for archive in "${archives[@]}"; do
    ARCHIVE_NAME=$(basename "$archive")
    echo
    echo "Verifying $ARCHIVE_NAME contents..."

    # Check for required files
    for required_file in LICENSE README.md SECURITY.md; do
        if tar -tzf "$archive" | grep -Eq "(^|/)${required_file}$"; then
            echo "  ✓ $required_file present"
        else
            echo "  ✗ FAIL: $required_file missing" >&2
            exit 1
        fi
    done

    for required_path in \
        docs/INSTALLATION.md \
        docs/V1_PRODUCTION_GATES.md \
        docs/PRODUCTION_READINESS.md \
        docs/lab-results/2026-11-20-six-host-install-smoke-059ba72.md; do
        if tar -tzf "$archive" | grep -Fqx "$required_path"; then
            echo "  ✓ $required_path present"
        else
            echo "  ✗ FAIL: $required_path missing" >&2
            exit 1
        fi
    done

    if tar -tzf "$archive" | grep -Eq '(^|/)deploy/lib/install-postcommit\.sh$'; then
        echo "  ✓ installer post-commit recovery helper present"
    else
        echo "  ✗ FAIL: deploy/lib/install-postcommit.sh missing" >&2
        exit 1
    fi

    if tar -tzf "$archive" | grep -Eq '(^|/)deploy/lib/install-upgrade-retention\.sh$'; then
        echo "  ✓ installer upgrade-rollback configuration retention present"
    else
        echo "  ✗ FAIL: deploy/lib/install-upgrade-retention.sh missing" >&2
        exit 1
    fi

    if tar -tzf "$archive" | grep -Eq '(^|/)deploy/lib/vsftpd-config-validation\.sh$'; then
        echo "  ✓ bounded vsftpd configuration validator present"
    else
        echo "  ✗ FAIL: deploy/lib/vsftpd-config-validation.sh missing" >&2
        exit 1
    fi

    if tar -tzf "$archive" | grep -Fqx 'deploy/lib/install-support-tier.sh'; then
        echo "  ✓ installer support-tier classifier present"
    else
        echo "  ✗ FAIL: deploy/lib/install-support-tier.sh missing" >&2
        exit 1
    fi

    for required_helper in \
        deploy/lib/install-systemd-preflight.sh \
        deploy/lib/install-quota-preflight.sh \
        deploy/lib/install-package-preflight.sh \
        deploy/lib/install-database-version-preflight.sh \
        deploy/lib/install-change-plan.sh \
        deploy/lib/install-env-file.sh; do
        if tar -tzf "$archive" | grep -Fqx "$required_helper"; then
            echo "  ✓ $required_helper present"
        else
            echo "  ✗ FAIL: $required_helper missing" >&2
            exit 1
        fi
    done

    # Verify binary is present
    if tar -tzf "$archive" | grep -Eq '(^|/)stepanel$'; then
        echo "  ✓ stepanel binary present"
    else
        echo "  ✗ FAIL: stepanel binary not found" >&2
        exit 1
    fi
    if tar -tzf "$archive" | grep -Eq '(^|/)stepanel-root$'; then
        echo "  ✓ stepanel-root binary present"
    else
        echo "  ✗ FAIL: stepanel-root binary not found" >&2
        exit 1
    fi
done

echo
echo "=== All packaging checks passed ==="
exit 0
