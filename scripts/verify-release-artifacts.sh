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
verification_tmp=$(mktemp -d)
trap 'rm -rf "$verification_tmp"' EXIT

# Check for .tar.gz files and verify they are actually gzipped
echo "Checking tar.gz compression..."
for archive in "${archives[@]}"; do
    archive_listing="$verification_tmp/$(basename "$archive").contents"
    # Check file magic number for gzip (1f 8b)
    if file "$archive" | grep -F "gzip compressed" >/dev/null; then
        echo "✓ $archive is properly gzipped"
    else
        echo "✗ FAIL: $archive is NOT gzipped (mislabeled)" >&2
        exit 1
    fi

    # Verify archive can be extracted
    if tar -tzf "$archive" > "$archive_listing" 2>/dev/null; then
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
    archive_listing="$verification_tmp/$ARCHIVE_NAME.contents"
    echo
    echo "Verifying $ARCHIVE_NAME contents..."

    # Check for required files
    for required_file in LICENSE README.md SECURITY.md; do
        if grep -Eq "(^|/)${required_file}$" "$archive_listing"; then
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
        if grep -Fqx "$required_path" "$archive_listing"; then
            echo "  ✓ $required_path present"
        else
            echo "  ✗ FAIL: $required_path missing" >&2
            exit 1
        fi
    done

    if grep -Eq '(^|/)deploy/lib/install-postcommit\.sh$' "$archive_listing"; then
        echo "  ✓ installer post-commit recovery helper present"
    else
        echo "  ✗ FAIL: deploy/lib/install-postcommit.sh missing" >&2
        exit 1
    fi

    if grep -Eq '(^|/)deploy/lib/install-upgrade-retention\.sh$' "$archive_listing"; then
        echo "  ✓ installer upgrade-rollback configuration retention present"
    else
        echo "  ✗ FAIL: deploy/lib/install-upgrade-retention.sh missing" >&2
        exit 1
    fi

    if grep -Eq '(^|/)deploy/lib/vsftpd-config-validation\.sh$' "$archive_listing"; then
        echo "  ✓ bounded vsftpd configuration validator present"
    else
        echo "  ✗ FAIL: deploy/lib/vsftpd-config-validation.sh missing" >&2
        exit 1
    fi

    if grep -Fqx 'deploy/lib/install-support-tier.sh' "$archive_listing"; then
        echo "  ✓ installer support-tier classifier present"
    else
        echo "  ✗ FAIL: deploy/lib/install-support-tier.sh missing" >&2
        exit 1
    fi

    # The packaged guided path must identify its host before prompting for
    # secrets, and include the helper it sources to do so.
    packaged_installer="$verification_tmp/$ARCHIVE_NAME.install.sh"
    packaged_support="$verification_tmp/$ARCHIVE_NAME.install-support-tier.sh"
    tar -xOf "$archive" install.sh > "$packaged_installer"
    tar -xOf "$archive" deploy/lib/install-support-tier.sh > "$packaged_support"
    if ! grep -Fq 'install_guided_host_support()' "$packaged_support"; then
        echo "  ✗ FAIL: packaged support-tier helper lacks guided host detection" >&2
        exit 1
    fi
    source_line=$(grep -nF ". \"\$ROOT_DIR/deploy/lib/install-support-tier.sh\"" "$packaged_installer" | head -1 | cut -d: -f1 || true)
    host_check_line=$(grep -nF 'install_guided_host_support' "$packaged_installer" | head -1 | cut -d: -f1 || true)
    setup_line=$(grep -nF "\"\$ROOT_DIR/stepanel\" setup --output" "$packaged_installer" | head -1 | cut -d: -f1 || true)
    if [[ -z $source_line || -z $host_check_line || -z $setup_line ]] ||
       (( source_line >= host_check_line || host_check_line >= setup_line )); then
        echo "  ✗ FAIL: packaged guided installer does not show host support before setup prompts" >&2
        exit 1
    fi
    echo "  ✓ packaged guided installer reports host support before collecting setup answers"

    for required_helper in \
        deploy/lib/install-systemd-preflight.sh \
        deploy/lib/install-quota-preflight.sh \
        deploy/lib/install-package-preflight.sh \
        deploy/lib/install-database-version-preflight.sh \
        deploy/lib/install-change-plan.sh \
        deploy/lib/install-env-file.sh; do
        if grep -Fqx "$required_helper" "$archive_listing"; then
            echo "  ✓ $required_helper present"
        else
            echo "  ✗ FAIL: $required_helper missing" >&2
            exit 1
        fi
    done

    # Verify binary is present
    if grep -Eq '(^|/)stepanel$' "$archive_listing"; then
        echo "  ✓ stepanel binary present"
    else
        echo "  ✗ FAIL: stepanel binary not found" >&2
        exit 1
    fi
    if grep -Eq '(^|/)stepanel-root$' "$archive_listing"; then
        echo "  ✓ stepanel-root binary present"
    else
        echo "  ✗ FAIL: stepanel-root binary not found" >&2
        exit 1
    fi
done

echo
echo "Checking the packaged setup command runs on $(uname -m)..."
host_arch=$(uname -m)
case $host_arch in
    x86_64) release_arch=amd64 ;;
    aarch64|arm64) release_arch=arm64 ;;
    *) release_arch= ;;
esac
if [[ -n $release_arch ]]; then
    host_archive=
    for archive in "${archives[@]}"; do
        if [[ $archive == *_linux_${release_arch}.tar.gz ]]; then
            host_archive=$archive
            break
        fi
    done
    if [[ -z $host_archive ]]; then
        echo "✗ FAIL: no linux/$release_arch archive available for setup smoke" >&2
        exit 1
    fi
    tar -xOf "$host_archive" stepanel > "$verification_tmp/stepanel"
    chmod 0755 "$verification_tmp/stepanel"
    setup_help=$("$verification_tmp/stepanel" setup --help 2>&1) || {
        echo '✗ FAIL: packaged stepanel setup --help did not run' >&2
        printf '%s\n' "$setup_help" >&2
        exit 1
    }
    if ! grep -Fq 'Usage of setup:' <<<"$setup_help" ||
       ! grep -Fq 'where to write the installer settings' <<<"$setup_help"; then
        echo '✗ FAIL: packaged setup command did not expose its expected options' >&2
        printf '%s\n' "$setup_help" >&2
        exit 1
    fi
    echo '✓ packaged setup command starts and exposes installer settings options'
else
    echo "- skipped native setup execution for unsupported verifier architecture: $host_arch"
fi

echo
echo "=== All packaging checks passed ==="
exit 0
