#!/bin/bash
# Check for broken internal documentation links

set -e

DOCS_DIR="${1:-.}"
BROKEN_LINKS=0

echo "Checking documentation links in $DOCS_DIR..."

# Find all Markdown files
while IFS= read -r file; do
    # Extract all markdown links: [text](path)
    grep -o '\[.*\](.*\.md)' "$file" | sed 's/.*(\(.*\.md\))/\1/' | sort -u | while read -r link; do
        # Skip external links
        if [[ "$link" =~ ^http ]]; then
            continue
        fi

        # Resolve relative path
        if [[ "$link" = /* ]]; then
            # Absolute path - resolve from docs root
            target_file="$DOCS_DIR/$link"
        else
            # Relative path - resolve from current file's directory
            target_file="$(dirname "$file")/$link"
        fi

        # Remove anchor if present
        target_file="${target_file%#*}"

        # Check if file exists
        if [ ! -f "$target_file" ]; then
            echo "  ✗ $file: broken link to $link (resolved: $target_file)"
            ((BROKEN_LINKS++))
        fi
    done
done < <(find "$DOCS_DIR" -name "*.md" -type f)

if [ $BROKEN_LINKS -eq 0 ]; then
    echo "✓ All documentation links are valid"
    exit 0
else
    echo "✗ Found $BROKEN_LINKS broken documentation link(s)"
    exit 1
fi
